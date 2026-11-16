# 影策画布 · 云端状态基线

这份目录把生产环境里**只存在于数据库和服务器上的改动**固化成可以进 git、可以 diff、可以回放的文件。
目的是：上游（`ddcat-ai/open-ai-canvas`）发大版本时，我们不是靠记忆手工重配，而是拿着这份基线在本地合并。

快照时间见 `meta.json`，当前对应 schema **104**、镜像 `v1.8.5-yc104-visionfix1`。

---

## 目录

| 路径 | 内容 |
|---|---|
| `json/channel_websites.json` | 站点分组（渠道归属哪个网站） |
| `json/model_channels.json` | 32 个上游渠道：`base_url`、协议、启用状态、密钥状态 |
| `json/channel_models.json` | 432 个上架模型：`display_name`、`model_key`、能力、计费方式、排序 |
| `json/channel_model_price_tiers.json` | 322 条价格档（按分辨率/时长/线路选择器） |
| `json/logical_models.json` | 逻辑模型目录（当前为空，前台由 `channel_models` 聚合） |
| `json/system_settings.json` | 运行时策略、视频复刻设置、电商 Agent 编排、功能开关 |
| `sql/channels-data.sql.gz` | 覆盖式恢复脚本（`TRUNCATE` + `INSERT`，含 6 张表） |
| `config/Caddyfile` | 反向代理配置（含 sslip.io 直连镜像站点） |
| `config/canvas-bridge-18080.service` | `172.23.0.1:18080 → 127.0.0.1:5173` 中转守护 |
| `config/plugin-registry-slim.json` | 插件注册表摘要（id / source / sha） |

**凭据一律不入库**：`model_channels.api_key` 整列清空（保留 `api_key_was_set` 标记），
`system_settings` 里的 `enc:v1:` 密文替换为 `REDACTED`。恢复后必须重新填 Key。

---

## 用法

```bash
# 从生产拉一份新快照，覆盖本目录（git diff 就是这次的配置变更）
./scripts/pull.sh

# 把快照灌进某个库（会先 TRUNCATE，自动备份到 /root/pre-apply-*.sql.gz）
./scripts/apply.sh --yes
```

`pull.sh` 默认 `root@100.73.133.67:11890`，可用 `CANVAS_SSH_HOST` / `CANVAS_SSH_PORT` 覆盖。

---

## 上游大更新时的合并流程

1. **先看迁移编号，这是最大的坑。** 我们的谱系和上游原生**不同**：93 是我们自研的商品视频，
   94–99 才是上游 v1.8.4 的 93–98（连 checksum 字符串里的版本号当年也一起改过）。
   100 `site_traffic_events`、101 `agent_mcp_connections`、102 `creation_conversation_history`、
   103 商品视频新版、104 微信登录 都是自增的。
   上游若新增 105+，必须**顺延到 105+**，绝不能让他们用 100–104。改完同步 `database.CurrentSchemaVersion`。
2. 合并上游代码 → `GOOS=linux go build ./cmd/server` → 前端 `npx vite build --outDir dist-test`
   → 按 `docker/` 里的三行 Dockerfile 构建镜像（详见项目记忆「构建」一节）。
3. **镜像一换，容器内 `/app/plugin-packages` 会被基础镜像里的旧插件覆盖。**
   换完镜像必须重新装一次我们的插件包（见下节），否则 `generate_audio` 的 400 立刻复发。
4. 用 `apply.sh` 把渠道/模型/价格回灌，或在后台逐项核对 `git diff deploy/yingce-state/json/`。
5. `docker exec open-ai-canvas-redis redis-cli INCR canvas:logical-model-route-catalog:version` 刷新目录。
6. 后台为每个渠道重新填写 API Key（快照里是空的）。

---

## 必须保住的定制项（合并时逐条核对）

**渠道 / 密钥共用关系**

- `CHANNEL_000022` YuxiBox 视频 / `CHANNEL_000023` yuxibox-官转 / `CHANNEL_000024` yuxibox-视频h3
  → 同一 yuxibox 账号，共用一本 Key（`000023`/`000024` 曾被漏填，报「缺少 Base URL、API Key 或模型名」）。
- `CHANNEL_000021` 影策官方中转-GPT图片 的 Key 同时适用于 `CHANNEL_000017` 特惠分组
  （同 `api.yingce.tv` 账号，开放 `sd2.0-c2/c3/c4`、`sd2.5-c1..c4`、`gemini-4-argon-high`）。
- `CHANNEL_000013` 大橙（显示名「特价渠道」）、`CHANNEL_000020` 山海、`CHANNEL_000025` 千问。
- `channel_models.enabled` 与 `model_channels.enabled` 是两层，**任一层关闭都不会出现在画布**。

**模型命名与价格**

- 视频模型 `display_name` 全局唯一，格式 `模型 版本 <性质> [规格]`，性质词限：
  低价 / 官转 / 特惠 / 拼车 / 按次 / 按秒 / 按条 / 满血 / 渠道简称。
  `channel_label` = `<性质> · <规格> · <渠道>`。
- **每个上架模型必须有 `channel_model_price_tiers` 记录**，否则后台任何一次编辑都会把价格字段清零成「未配置」。
- 新增视频模型时 `capability_config_json.video` 必须含 `operations` + `defaultOperation`，
  否则校验拦下，现象是「库里有、前台选不到」。

**关键设置**

- `video_replication_setting.vision` = `MODEL_000533`（`antigravity/gemini-4-hydrogen`，`magpie-chat` 协议）。
  同一个 `model_key` 下若还有 `gemini-multimodal-text` 的旧记录，`ChannelModelByKey` 无排序会命中旧记录，
  导致走「直传 video_url」分支被 Magpie 拒（400 `its model list doesn't take video`）。旧记录必须保持软删。
- 该模型 `text.references.maxVideos` 必须为 **0**（走抽帧，12 帧上限），`maxImages` 16。
  抽帧模式仍会附带 `input_audio`，实测音频正确透传给上游。
- `ecommerce_agent_orchestration_v1.vision.model.channelModelId` = `MODEL_000533`，
  且该配置 `version=1`、**`enabled` 必须为 false**（代码把它当历史标志位，true 会报「配置版本无效」）。
- `runtime_policy`：`resourceUploadMB=512`、`dailyUploadMB=4096`（默认 50 会导致 >50MB 分片初始化被拒）。
- `oss.enabled=false`（MinIO/NAS 走 Cloudflare 必超时，已弃用）。

**插件**

- `newapi-video-generations-v1`：`create.body.generate_audio` 必须是
  `{"$if":{"condition":{"$ref":"request.generateAudio"},"then":true,"else":null}}`
  —— 只有 true 才下发，false 整个字段省略。yuxibox 见到 `generate_audio:false` 直接 400
  「暂不支持自选声音开关」。
  该修复当前只打在运行容器 + `plugin_registry.json` 上，**未进镜像**；换镜像后要重装。
  另注意 `plugin_registry.json` 里这个插件的 `source` 会被启动扫描改回 `official`，
  功能不受影响（`packagePath`/`manifest` 仍是打过补丁的那份），但别被这个字段误导。

**服务器侧（不在本仓库代码里）**

- `config/Caddyfile`：`yingce.39-109-63-227.sslip.io` / `upload.39-109-63-227.sslip.io`
  两站点整站反代 `172.23.0.1:18080`，`flush_interval -1`，只对静态资源做 encoding。
- `config/canvas-bridge-18080.service`：`socat` 中转，`Restart=always`（原来是无守护裸进程）。
- 目标路径 `/opt/commerce-brain/app/deploy/Caddyfile`、`/etc/systemd/system/canvas-bridge-18080.service`。
