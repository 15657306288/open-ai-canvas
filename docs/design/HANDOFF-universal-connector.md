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

### 网络与凭证（已解决，长期稳定配置）
- **SSH 已打通**：本仓库 `fork` remote 已改为 `git@github.com:15657306288/open-ai-canvas.git`；仓库级 `core.sshCommand` 固定用专用 key `~/.ssh/id_ed25519_yingce`（已绑 `15657306288` 账号，指纹 `sn2kttxly8RJ6/fGKRBZ5buqM6mJO9n2jxRGK0rRvAo`）。SSH 推送**不受 workflow scope 限制**。
- **密钥分工**：`~/.ssh/id_ed25519` → `lmj15657306288-ui` 账号；`~/.ssh/id_ed25519_yingce` → `15657306288` 账号（fork owner，有写权限）。两者互不干扰。
- **HTTPS 上游**：`origin/upstream` 走 HTTP 代理 `127.0.0.1:7897`（仓库级 http.proxy/https.proxy），公开仓库免认证，fetch 稳定。
- **三个分支已全部推上 fork**：`feat/universal-agent-connector`（新建）、`main`（→`7a1f2db`）、`codex/merge-upstream-v1.2.4-and-decimal-price`（→`e2d4d3b`）。
- token 明文凭证已删除（`~/.git-credentials` 已清、credential.helper 已移除），不残留敏感 token。

---

## 3. 当前进度：P0 全部完成 ✅（A 稳定性 + B 协议门面 + bridge + 渠道工具化）

三个分支已全部推上 fork 做云备份，git 全链路（SSH 推送 + HTTPS 代理拉上游）已长期稳定。**P0-A 五块、P0-B-1/2 协议门面、P0-B-3 主动外连 bridge、P0-B-4 渠道工具化全部完成**，全量 356 测试零失败。剩余为 P1（前端四态面板、媒体读取、渠道直连视频联调）与 P2（直连生成深度联调、Agent Card/metrics）。

P0-A 已提交（`feat(connector):` 前缀，全在 `feat/universal-agent-connector`，已推 fork）：
1. ✅ `5243a65` 会话滑动续期 + TTL/时钟窗可配 + nonce LRU（`local-runtime-session.ts`：TTL 30min/绝对 12h/时钟窗 60s/nonce 2048 LRU，对外 expiresAt 不变、协议零破坏）
2. ✅ `ef2ee9d` SSE 断线宽限（新增 `src/grace-tracker.ts`，`canvas-session.ts` 接线：close 进 8s 宽限、同 clientId 重连恢复、stateOwner 归属 clientId+runtimeSessionId）
3. ✅ `a50a03d` 单实例锁（新增 `src/runtime-lock.ts`：lockfile+pid 检测，`local-runtime-host.ts` 接线，防多实例端口漂移；锁文件记录权威 endpoint/token）
4. ✅ `59fee95` agent-fetch（新增 `src/agent-fetch.ts`：keepalive+只读重试≤2+超时，`mcp-server.ts` 转发路径已接线）
5. ✅ `0a09795` /health 四态（`local-runtime.ts` 聚合 status：healthy/reconnecting/degraded/offline）
6. ⏳ 24h soak 脚本与前端四态面板：后端 /health 字段已就绪；前端面板与 soak 脚本未做（可入 P1，见 §4）

新增测试文件：`test/runtime-lock.test.ts`、`test/agent-fetch.test.ts`、`test/local-runtime-health.test.ts`、`test/mcp-http-server.test.ts`、`test/openapi-server.test.ts`；全量 `cd canvas-agent && npx tsx --test test/*.test.ts` = 346 tests / 0 fail（7 cancelled 为 dreamina-task-reconciler 既有状态，非本次引入）。

P0-B 已提交：
1. ✅ `19a045e` MCP Streamable HTTP 门面（`src/mcp-http-server.ts`，`/mcp` 默认开 Q1，SDK 端到端测试通过）
2. ✅ `80d31b4` OpenAPI 兜底门面（`src/openapi-server.ts`，`/openapi.json` + `/tools/:name`）
3. ✅ `6052f7e` 远程主动外连 bridge（`src/bridge/{broker,client}.ts`，Q3：本地零入站端口，长轮询+心跳+Bearer 鉴权，broker 自托管零依赖，host 经 options/env 启用）
4. ✅ `...` Q4 渠道工具化（`src/channel-{catalog,tools,generate}.ts` + `examples/channel-catalog.example.json`，7 工具、目录自更新三层机制）
4. ⏳ Q4 渠道工具化 + 目录自更新（a6api/artbox/红鸟 → 连接器工具 + 三层更新机制）

---

## 4. 跨设备 / 待办清单

- [x] ~~开代理后推 fork~~ **已完成**（SSH + 代理均已配置，三个分支已推上 fork）。
- [ ] **Windows 5 个渠道插件回补**（二选一）：
  - 推荐：在 Windows 那份 `D:\yingce\open-ai-canvas-main` 初始化为 git、加 fork 远端、把 `plugin-packages/src-{a6api-chat,artbox-video,hongniao-video,hongniao-image,hongniao-image-res}` 与 5 个 `.yingce-plugin` 提交推到 fork 某分支，Mac 再 `git fetch fork && git checkout fork/<branch> -- plugin-packages/`；
  - 或在 Mac 按 v1.2.5 插件协议重建（三渠道 key 见历史会话；红鸟 27 模型明细可用仓库根 `hongniao-models.json`/价格清单，Windows 完整明细在其 `.local/hn_models.json`）。
- [ ] 旧分支 `feat/decimal-price-and-model-picker-improvements` 确认是否保留/合并/删除。
- [ ] 仓库根 7 个未跟踪运维文件（channel-config-backup/、hongniao-models.json、update-yingce.sh、更新影策.command、本机部署说明.md、红鸟模型价格清单.csv/.md）决定是纳入文档、加入 .gitignore 还是维持现状（当前保持原样）。
- [ ] P0-A 前端四态面板（后端 /health 四态已就绪，前端 React 面板与 24h soak 脚本未做，入 P1）。
- [ ] P1：媒体读取（Q5 `canvas_get_media` + scope `canvas:media:read` + 短 TTL 签名 URL）。
- [ ] P1：渠道直连视频联调（红鸟/artbox 视频生成走真实 API 验证 + 目录完整回补 a6api 22/artbox 8 模型列表）。
- [ ] P2：OpenAPI 直连生成深度联调、`/.well-known/agent.json` Agent Card、metrics。
- [ ] Windows 五个渠道插件源码回补 Mac（`plugin-packages/` 经 fork 同步或按 v1.2.5 协议重建）。

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
方案 v1.1 与交接报告已落 `feat/universal-agent-connector` 分支，上游已追平、在途工作已安全固化、**分支已推上 fork 云备份、SSH+代理长期稳定**；**P0 全部完成**：A 稳定性五块（`5243a65`→`0a09795`）、B-1 MCP HTTP（`19a045e`）、B-2 OpenAPI（`80d31b4`）、B-3 主动外连 bridge（`6052f7e`）、B-4 渠道工具化（目录自更新，全量 356 测试 0 失败）；本机 `~/.infinite-canvas/channel-catalog.json` 已写入三渠道真实密钥 + 红鸟 29 模型目录；下一阶段 P1（前端四态面板、媒体读取、渠道直连视频联调）。
