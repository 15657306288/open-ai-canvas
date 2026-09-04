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

## 3. 当前进度：P0 全部完成 ✅（A 稳定性 + B 协议门面 + bridge + 渠道工具化）；P1 媒体读取（Q5）完成 ✅；P2 完成 ✅

三个分支已全部推上 fork 做云备份，git 全链路（SSH 推送 + HTTPS 代理拉上游）已长期稳定。**P0-A 五块、P0-B-1/2 协议门面、P0-B-3 主动外连 bridge、P0-B-4 渠道工具化、P1 媒体读取（Q5）、P2（metrics + Agent Card + bridge 限流 + OpenAPI 深度联调）全部完成**，全量 372 测试零失败。剩余仅 P1 尾项（前端四态面板、红鸟/artbox 视频直连联调待平台/充值）与仓库治理待办。

P0-A 已提交（`feat(connector):` 前缀，全在 `feat/universal-agent-connector`，已推 fork）：
1. ✅ `5243a65` 会话滑动续期 + TTL/时钟窗可配 + nonce LRU（`local-runtime-session.ts`：TTL 30min/绝对 12h/时钟窗 60s/nonce 2048 LRU，对外 expiresAt 不变、协议零破坏）
2. ✅ `ef2ee9d` SSE 断线宽限（新增 `src/grace-tracker.ts`，`canvas-session.ts` 接线：close 进 8s 宽限、同 clientId 重连恢复、stateOwner 归属 clientId+runtimeSessionId）
3. ✅ `a50a03d` 单实例锁（新增 `src/runtime-lock.ts`：lockfile+pid 检测，`local-runtime-host.ts` 接线，防多实例端口漂移；锁文件记录权威 endpoint/token）
4. ✅ `59fee95` agent-fetch（新增 `src/agent-fetch.ts`：keepalive+只读重试≤2+超时，`mcp-server.ts` 转发路径已接线）
5. ✅ `0a09795` /health 四态（`local-runtime.ts` 聚合 status：healthy/reconnecting/degraded/offline）
6. ⏳ 24h soak 脚本与前端四态面板：后端 /health 字段已就绪；前端面板与 soak 脚本未做（可入 P1，见 §4）

P1 媒体读取（Q5）已提交（同分支，已推 fork）：
- ✅ `canvas_get_media` 工具（`src/schemas.ts`）：`{nodeId, mode?: "block"|"url", maxBytes?}`；block 返回 base64（默认上限 8MB），url 返回短 TTL（默认 5min）单次签名链接
- ✅ 新增 `src/media-access.ts`（`CanvasMediaAccess`：dataUrl/http 解码、block 超限提示、签名 token 内存表、单次消费、审计回调）；不落地/不缓存/不进日志
- ✅ 新 scope `canvas:media:read`（`local-runtime-contract.ts`）不入 `LOCAL_RUNTIME_DEFAULT_SCOPES`（默认不授）；严格 HTTP 路径 `POST /api/media/get`（canvas-agent-http.ts）需该 scope，否则 403
- ✅ 签名 URL 消费端点 `GET /api/media/:token`（canvas-agent-http.ts + local-runtime.ts `public` 路由：令牌即鉴权、TTL+单次、404 无效/过期）；`compactNode` 外部投影不含媒体本体，读取经签名/受控链路
- ✅ MCP / OpenAPI 门面自动获得该工具（走本地信任）；`CanvasSession` 新增 `getMedia/consumeMediaToken/loadNodeMedia`（canvas-session.ts）
- 测试：`test/media-access.test.ts`（5）+ `test/media-route.test.ts`（2：无画布报错/block 返回/签名 URL 单次消费/错误 token 拒绝）+ 更新 `canvas-agent-module.test.ts` 路由 scope 断言；全量 364 tests / 0 fail（7 cancelled 为 dreamina-task-reconciler 既有）

新增测试文件：`test/runtime-lock.test.ts`、`test/agent-fetch.test.ts`、`test/local-runtime-health.test.ts`、`test/mcp-http-server.test.ts`、`test/openapi-server.test.ts`、`test/media-access.test.ts`、`test/media-route.test.ts`；全量 `cd canvas-agent && npx tsx --test test/*.test.ts` = 364 tests / 0 fail（7 cancelled 为 dreamina-task-reconciler 既有状态，非本次引入）。

P0-B 已提交：
1. ✅ `19a045e` MCP Streamable HTTP 门面（`src/mcp-http-server.ts`，`/mcp` 默认开 Q1，SDK 端到端测试通过）
2. ✅ `80d31b4` OpenAPI 兜底门面（`src/openapi-server.ts`，`/openapi.json` + `/tools/:name`）
3. ✅ `6052f7e` 远程主动外连 bridge（`src/bridge/{broker,client}.ts`，Q3：本地零入站端口，长轮询+心跳+Bearer 鉴权，broker 自托管零依赖，host 经 options/env 启用）
4. ✅ `...` Q4 渠道工具化（`src/channel-{catalog,tools,generate}.ts` + `examples/channel-catalog.example.json`，7 工具、目录自更新三层机制）
4. ⏳ Q4 渠道工具化 + 目录自更新（a6api/artbox/红鸟 → 连接器工具 + 三层更新机制）

---

## 4. 跨设备 / 待办清单

- [x] ~~开代理后推 fork~~ **已完成**（SSH + 代理均已配置，三个分支已推上 fork）。
- [x] ~~Windows 5 个渠道插件回补~~ **已完成**（在 Mac 按上游声明式 Provider 协议 `yingce.plugin/v2` 重建：`plugin-packages/src-{a6api-chat,artbox-video,hongniao-video,hongniao-image,hongniao-image-res}/` 源码目录 + `{id}.yingce-plugin` zip 产物，从上游 newapi-chat / newapi-video-generations-v1 / openai-images 模板派生，baseUrl 分别指向 a6api.com / artbox.top / open.hongniaoai.com，提交 `c3bd8fa`）。红鸟视频/图片端点仍未平台确认，插件 create path 暂按 /v1/video|images/generations 与渠道目录一致，待平台确认后统一回填。
- [ ] 旧分支 `feat/decimal-price-and-model-picker-improvements` 确认是否保留/合并/删除。
- [ ] 仓库根 7 个未跟踪运维文件（channel-config-backup/、hongniao-models.json、update-yingce.sh、更新影策.command、本机部署说明.md、红鸟模型价格清单.csv/.md）决定是纳入文档、加入 .gitignore 还是维持现状（当前保持原样）。
- [x] ~~P0-A 24h soak 脚本~~ **已完成**（`canvas-agent/scripts/health-soak.ts`：周期性 GET /health 统计四态分布 + 状态跳变 + offline 检测，退出码判定达标；用法 `npx tsx scripts/health-soak.ts [endpoint] [durationSec] [intervalSec]`）。
- [x] ~~前端四态面板~~ **已完成**（`web/src/lib/canvas/canvas-runtime-health.ts`：/health 轮询 hook + 四态元数据；`canvas-local-agent-panel.tsx` 顶部加四态徽章 healthy/reconnecting/degraded/offline + tooltip 说明；web tsc 新增文件零错误）。
- [x] ~~P1：媒体读取（Q5）~~ **已完成**（`canvas_get_media` + scope `canvas:media:read` + 短 TTL 单次签名 URL，block base64 上限 8MB；MCP/OpenAPI 自动获得，测试 7 个全过）。
- [~] **P1：渠道直连联调（部分完成）**：
  - ✅ **a6api 文本直连端到端通过**（`channel_generate` text → 真实返回内容；同时修复 baseUrl 约定：目录 baseUrl 为 OpenAI 兼容根不含 /v1，text/image 拼接 /v1/chat|images/generations，video 走 videoUrl；one-api 风格业务码 HTTP 200+code≠200 检测，防误判 running；task_id 嵌套提取增强）。
  - ✅ **artbox 视频端点确认** `/v1/video/generations`（存在），但**用户余额 ¥0 无法实际生成**（需充值后再联调）。
  - ⏳ **红鸟视频端点待平台确认**：`/api/v1/models` 可读（26 模型全量），但视频提交端点（`/v1/video/generations`、`/v1/tasks`、`/v1/generations` 等 20+ 候选）全部 404；红鸟为"AI 创作系统"工作台，公开 REST 生成端点需从红鸟工作台/官方文档确认后回填 videoUrl/taskUrl。
  - 本机目录已回补全量：**3 渠道 / 121 模型**（a6api 87 + artbox 8 + 红鸟 26），密钥隔离验证通过（channel 列表视图无 apiKey）。红鸟 26 为接口当前真实状态（22 视频 + 4 图片，含 pricing + tasks 参数）。
- [x] ~~P2：OpenAPI 直连生成深度联调、`/.well-known/agent.json` Agent Card、metrics~~ **已完成**（commit `7298c5d`，见 §3）：
  - ✅ **metrics**：新增 `src/metrics.ts` registry（计数/时延/状态 gauge，JSON + Prometheus 双格式）；`canvas-session.callTool` 挂钩 `tools.called/errors` 与时延；canvas-agent module 注入单例 + 媒体审计计数；local-runtime 挂 `GET /metrics`（`?format=prometheus`）。
  - ✅ **Agent Card**：`GET /.well-known/agent.json`（A2A 预留 + 平台发现：name/capabilities/protocol/endpoints/security/tools），随 OpenAPI 门面挂载；openapi-server 支持注入渠道 ctx（可测试）。
  - ✅ **bridge 429 限流**：broker 按 bridge 限流（默认 60 次/60s，超限 `code 42901`）。
  - ✅ **OpenAPI 深度联调**：端到端冒烟通过（`/openapi.json` 50 paths 含渠道+画布+health；`channel_list` 3 渠道 121 模型密钥隔离；`model_list_logical` 4 逻辑模型；红鸟 26 模型；`/metrics`；`agent.json`）。
  - 测试：metrics 3 + openapi-deep 3 + bridge 429 1 = **+7，全量 372 tests / 0 fail / 7 cancelled（既有）**。
- [x] ~~Windows 五个渠道插件源码回补 Mac~~ **已完成**（见上方"Windows 5 个渠道插件回补"条目，Mac 按协议重建，源码 + zip 均已入 git）。

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
方案 v1.1 与交接报告已落 `feat/universal-agent-connector` 分支，上游已追平、在途工作已安全固化、**分支已推上 fork 云备份、SSH+代理长期稳定**；**P0 全部完成 + P1 媒体读取（Q5）+ soak 脚本完成、渠道直连联调部分完成 + P2 全部完成**：A 稳定性五块（`5243a65`→`0a09795`）、B-1 MCP HTTP（`19a045e`）、B-2 OpenAPI（`80d31b4`）、B-3 主动外连 bridge（`6052f7e`）、B-4 渠道工具化（`f28685b`）、P1 媒体读取（`c2c5524`）、P1 渠道直连联调修复（`8b82415`：a6api 文本端到端通过 + baseUrl 约定/业务码/task_id 提取 + soak 脚本）、P2 metrics+Agent Card+限流+OpenAPI 深度联调（`7298c5d`），全量 372 测试 0 失败；本机 `~/.infinite-canvas/channel-catalog.json` 三渠道真实密钥 + 121 模型；剩余仅 P1 尾项（前端四态面板、红鸟/artbox 视频联调待平台/充值）与仓库治理待办。
