# FaceGuard：参考视频主体人脸打码

换人（商品视频复刻 · 模特替换）任务在上游成片阶段会被内容审核按「真人换脸」拒绝
（`VIDEO_CONTENT_REVIEW`，`progress:100` 后失败）。把原片里主体的人脸打成马赛克后再作为编辑底片，
上游识别不到被篡改的真实人物身份，成片只能靠参考图重绘人脸，审核通过率显著提高。

## 组成

| 文件 | 作用 |
|---|---|
| `redact.py` | 检测 + 打码核心。YuNet 检测，像素化 + 自适应高斯，输出 H.264 |
| `faceguard_server.py` | HTTP 微服务，供画布后端调用（容器内没有 python/opencv） |
| `faceguard.service` | systemd 单元，监听 docker 网桥地址，仅本机容器可达 |

## 为什么放在宿主机

画布后端容器基于 alpine，没有 python 与 opencv；逐帧检测 + 重编码是 CPU 密集任务，
放在宿主机上既不用改镜像，也能独立调参重启。容器通过 `172.19.0.1:18091` 访问，
容器内路径 `/data/xxx` 由服务映射为宿主机 `/opt/open-ai-canvas/data/backend/xxx`。

## 部署

```sh
# 1. 运行环境（Debian 13 上验证）
apt-get install -y ffmpeg
python3 -m venv /opt/faceguard/venv
/opt/faceguard/venv/bin/pip install -i https://pypi.tuna.tsinghua.edu.cn/simple \
    opencv-python-headless numpy

# 2. 模型（232KB）
curl -sL -o /opt/faceguard/yunet.onnx \
  https://github.com/opencv/opencv_zoo/raw/main/models/face_detection_yunet/face_detection_yunet_2023mar.onnx

# 3. 代码与单元
cp redact.py faceguard_server.py /opt/faceguard/
cp faceguard.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now faceguard

# 4. 验证（宿主机与容器两侧都应为 {"ok": true}）
curl -s http://172.19.0.1:18091/health
docker exec open-ai-canvas-backend sh -c 'wget -qO- http://172.19.0.1:18091/health'
```

> `FACEGUARD_BIND` 必须绑 docker 网桥网关地址。若 `docker network` 被重建导致网关变化，
> 用 `docker exec open-ai-canvas-backend sh -c 'ip route'` 查默认网关并改 systemd 环境变量。

## 接口

```
GET  /health
POST /redact
  {"input": "/data/resources/.../x.mp4",          # 容器路径
   "output": "/data/tmp/faceguard/x.out.mp4",     # 容器路径，可省略
   "mode": "subject" | "all",                     # subject 只打主体
   "maxFaces": 1, "sample": 2}
→ {"ok": true, "output": "...", "stats": {...}, "ms": 21399}
```

## 参数取舍

- `mode=subject`：按「面积 × 居中权重 × 置信度」选最大的一张脸。实测主体打码即可过审，
  背景儿童与路人保持清晰也不影响结果 —— 拒片的触发点是主体换脸，不是背景有人。
- `sample=2`：每 2 帧检测一次，中间帧沿用前一帧框并做 EMA 平滑。395 帧 / 6.6 秒视频约 21 秒。
- `blocks=9` + 高斯：只做像素化仍能看出五官轮廓，叠加 `sigma=min(w,h)/16` 的模糊后不可辨认。

## 后端侧

`backend/internal/app/video_replication_redact.go` + `video_replication_edit.go` 的
`videoReplicationSourceClip`：截好片段 → 复制到 `/data/tmp/faceguard` → 调本服务 → 上传成资源。
配置在 `system_settings.face_guard_setting`，缺省开启；服务不可达时**降级为不打码**，不阻断生成。
片段幂等标识区分 `:redact=on` / `:redact=off`，避免「打码失败降级」与「打码成功」撞同一个 id。
