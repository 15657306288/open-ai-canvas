# 通用 Agent 连接器 · 交接报告（HANDOFF）

> 生成时间：2026-09-03（macOS）
> 仓库：`/Users/linmengjiang/open-ai-canvas`
> 配套方案：同目录 `agent-connector-roadmap.md`（v1.1，施工蓝图）
> 本报告用途：跨会话/跨设备无缝接手，记录本次 git 治理结果、当前状态、下一步与待办。

---

## 1. 本次完成了什么（按顺序四步）

### ① 固化在途改动
- 原工作区有 32 个未提交改动（codex "agent 融合"线：canvas-agent 前端面板/状态、agent-fallback、skill 版本控制、storyboard 语义与测试）。
- 已在分支 `codex/merge-upstream-v1.2.4-and-decimal-price` 固化为 WIP 提交 **`aa67bf9`**。
- 撤回方式：`git reset --soft aa67bf9~1`（改动回到暂存区，不丢内容）。
- 仓库级 git 身份已设：`yingce-dev <dev@yingce.local>`（未改全局配置）。

### ② 追平上游（零冲突）
- `git fetch upstream --tags`。
- 本地 `main` 由落后 48 个提交 **fast-forward 到 upstream/main `7a1f2db`**（现与上游 0/0）。
- codex 在途分支合并 upstream/main，产生合并提交 **`e2d4d3b`**，**无冲突**；合并后领先上游 9、落后 0。

### ③ Windows 成果同步（部分）
- **roadmap v1.1 已在 Mac 重建**（见④，已提交）。
- **Windows（9/3）做的 5 个完整渠道插件源码 Mac 上不存在**：`a6api-chat / artbox-video / hongniao-video / hongniao-image / hongniao-image-res`（`.yingce-plugin` 与 `plugin-packages/src-*`）。已排查外挂盘 `/Volumes/5T/yingce`（仅 8/30 早期 outputs）、`~/open-ai-canvas-backups`（仅 8/31 红鸟 patch 与 DB 备份），均无。
- Mac 上的相关存量：`custom-plugins/` 有 **9/2 早期** hongniao-image/hongniao-video/grsai-image/agnes-* 插件；仓库根有用户运维资料 `hongniao-models.json`、`红鸟模型价格清单.csv/.md`、`channel-config-backup/`（Q4 渠道工具化可作参考数据，**保持未跟踪、未提交**）。

### ④ 切出连接器特性分支
- 从最新干净的 `main`(`7a1f2db`) 切出 **`feat/universal-agent-connector`**（不夹带 codex 在途工作，未来合并最清晰）。
- roadmap v1.1 提交为 **`7cbcccd`**。
- 分支提交链：`7cbcccd(docs 方案) → 7a1f2db(上游最新)`。

---

## 2. 当前仓库全景

### Remote
| 名称 | 地址 | 用途 |
|---|---|---|
| origin | https://github.com/ddcat-ai/open-ai-canvas.git | 官方（同 upstream） |
| upstream | https://github.com/ddcat-ai/open-ai-canvas.git | 跟上游用 |
| fork | https://github.com/15657306288/open-ai-canvas.git | **你自己的 fork，备份/跨设备同步用** |

### 分支
| 分支 | 位置 | 说明 |
|---|---|---|
| `main` | `7a1f2db`，= upstream/main | 干净基线 |
| `feat/universal-agent-connector` ★当前 | `7cbcccd` | **连接器开发分支，P0 在这里做** |
| `codex/merge-upstream-v1.2.4-and-decimal-price` | `e2d4d3b` | agent 融合在途线（已固化+合上游） |
| `feat/decimal-price-and-model-picker-improvements` | `0790bbc` | 另一条旧特性分支，未处理 |

### 网络（重要）
- 当前**直连 github.com 443 不通**（curl 返回 000，git push fork 报 curl 28 超时），未配置 http(s).proxy；之前 fetch 成功应为网络瞬通/缓存。
- **推 fork 前需先开代理**，然后：
  ```bash
  # 示例：按本机代理端口调整
  git -C /Users/linmengjiang/open-ai-canvas config http.proxy http://127.0.0.1:7890
  git -C /Users/linmengjiang/open-ai-canvas push -u fork feat/universal-agent-connector
  git -C /Users/linmengjiang/open-ai-canvas push fork main codex/merge-upstream-v1.2.4-and-decimal-price
  ```
- 本地提交均已落盘、安全，push 仅为云备份。

---

## 3. 下一步：开工 P0-A（连接稳定性专项）

当前停在"方案已就绪、尚未写代码"。**P0-A 先做，P0-B 紧随**（见 roadmap §4/§5，合计 5–7 人日）。

动手前必做（上游 v1.2.5 改动多，行号可能漂移）：
1. `cd /Users/linmengjiang/open-ai-canvas && git checkout feat/universal-agent-connector`
2. 重新核对 canvas-agent 现状：
   - `canvas-agent/src/local-runtime-session.ts`（TTL=10min/时钟窗 30s/nonce 2048 常量在文件头）
   - `canvas-agent/src/canvas-session.ts`（SSE 15s ping、close 即清 canvasState、30s 工具超时）
   - `canvas-agent/src/local-runtime-security.ts`（legacy masterToken / signed 双通道）
   - `canvas-agent/src/mcp-server.ts`（stdio 裸 fetch）
   - 用 `git diff 7a1f2db -- canvas-agent | head` 确认相对基线差异
3. 在 `canvas-agent/node_modules/@modelcontextprotocol/sdk` 读真实类型，确认 `StreamableHTTPServerTransport` 构造参数（勿凭记忆）。

P0-A 顺序（抗冲突：新增类优先、老文件单点接线、打 `// [connector]`）：
1. 会话滑动续期 + TTL/时钟窗可配 + nonce LRU（`local-runtime-session.ts`，抽 renewal 工具，12h 绝对上限）
2. 新增 `src/grace-tracker.ts`，`canvas-session.ts` 加两行接线实现 8s 断线宽限；state 归属加 runtimeSessionId
3. 新增 `src/runtime-lock.ts`：lockfile 单实例 / token 稳定化 / runtimeInstanceId
4. 新增 `src/agent-fetch.ts`：keepalive + 只读幂等重试≤2 + 超时；写不重试
5. `/health` 健康字段 + 前端连接四态/倒计时面板
6. 单测 + 24h soak 脚本，`cd canvas-agent && npm test`

每完成一块就 commit 到本分支，commit message 前缀 `feat(connector):` / `fix(connector):`。

---

## 4. 跨设备 / 待办清单

- [ ] **开代理后推 fork**（命令见 §2），把 main、连接器分支、codex 分支都推上去做云备份。
- [ ] **Windows 5 个渠道插件回补**（二选一）：
  - 推荐：在 Windows 那份 `D:\yingce\open-ai-canvas-main` 初始化为 git、加 fork 远端、把 `plugin-packages/src-{a6api-chat,artbox-video,hongniao-video,hongniao-image,hongniao-image-res}` 与 5 个 `.yingce-plugin` 提交推到 fork 某分支，Mac 再 `git fetch fork && git checkout fork/<branch> -- plugin-packages/`；
  - 或在 Mac 按 v1.2.5 插件协议重建（三渠道 key 见历史会话；红鸟 27 模型明细可用仓库根 `hongniao-models.json`/价格清单，Windows 完整明细在其 `.local/hn_models.json`）。
- [ ] 旧分支 `feat/decimal-price-and-model-picker-improvements` 确认是否保留/合并/删除。
- [ ] 仓库根 7 个未跟踪运维文件（channel-config-backup/、hongniao-models.json、update-yingce.sh、更新影策.command、本机部署说明.md、红鸟模型价格清单.csv/.md）决定是纳入文档、加入 .gitignore 还是维持现状（当前保持原样）。
- [ ] P0-A 完成后观察 3–5 天真实连接日志再推进 P0-B/P1。

---

## 5. 关键事实速查

- 主战场 `canvas-agent/`：ESM、Node≥18、Express 5、MCP SDK `^1.30.0`、zod 3.25.76；入口 `src/index.ts`（`mcp`=stdio，默认=Runtime 127.0.0.1:17371）；配置 `~/.infinite-canvas/canvas-agent.json`。
- 40 个画布工具定义集中在 `src/schemas.ts`；执行内核 `CanvasSession.callTool`（`canvas-session.ts`）；Runtime 模块白名单在 `local-runtime.ts`（新增 module id 会被拒，P0 复用 `canvas-agent` id 挂路由）。
- 稳定性四大根因：会话 10min 硬过期不续期 / SSE 瞬断清 state / masterToken 裸 fetch 无重试且多实例端口漂移 / 时钟窗 30s+nonce 2048 硬顶。
- 五个评审决策：Q1 `/mcp` 默认开；Q2 稳定性专项；Q3 远程=主动外连 bridge（本地零入站端口，参考 `canvas-agent/native/comfy-bridge`）；Q4 渠道工具化且目录自更新（工具稳定、模型目录当数据 + list_changed）；Q5 受控媒体读取（scope `canvas:media:read`，短 TTL 签名 URL / MCP image block）。
- 三渠道（历史会话，密钥勿入库）：CHANNEL_000001 a6api 文本、CHANNEL_000002 artbox 视频、CHANNEL_000003 红鸟（视频+图片 27 模型）；逻辑模型 a6api-chat/artbox-video/hongniao-video/hongniao-image。
- 本地服务端口（Windows 环境，Mac 未在本次启动）：前端 3000、后端 8080、canvas-agent runtime 17371。

---

## 6. 一句话现状
方案 v1.1 已落 `feat/universal-agent-connector` 分支（commit `7cbcccd`），上游已追平、在途工作已安全固化、工作区无待提交源码；**下一步即在该分支开工 P0-A 稳定性专项**；唯一外部阻塞是推 fork 需要代理，以及 Windows 五个渠道插件需经 fork 回补。
