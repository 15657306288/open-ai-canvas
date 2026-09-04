# 影策通用 Agent 连接器（Universal Agent Connector）执行方案 P0–P2 · v1.1

> 分支基线：`feat/universal-agent-connector` 切自 upstream/main `7a1f2db`（v1.2.5 之后）
> 主战场：`canvas-agent/`，辅以 `web/`、`plugins/`、`backend/`
> 一句话目标：**让影策画布能力与渠道模型能力，以"一份内核 + 多协议门面"被市面上绝大多数 Agent（Codex、Claude Code、Cursor、Cline、WorkBuddy、豆包/火山方舟、Hermes 及任意支持 MCP/OpenAPI 的智能体）稳定、安全、可插拔地调用，且渠道模型目录能自动保持更新。**
>
> v1.1 决策（评审 Q1–Q5 已拍板）：
> - Q1 `/mcp` **默认开启**；Q3 远程采用**主动外连 bridge**；Q5 **允许外部 Agent 读取画布媒体内容**（受控）。
> - Q2 新增 **P0-A 连接稳定性专项（最高优先）**，含代码级根因与修复。
> - Q4 新增主线：**渠道/模型作为连接器工具 + 目录自更新（保持更新）**。
>
> 抗上游冲突原则（二次开发长期跟上游）：新增文件优先、老文件只做单点挂载并打 `// [connector]` 标记、核心修复用包装/子类化而非重写函数体、锁 MCP SDK 版本、每次 merge upstream 后跑 §11 契约测试。

---

## 0. 目标与非目标

### 0.1 目标
1. 一份内核、多门面：工具定义/执行只写一份，对 stdio MCP、Streamable HTTP MCP、OpenAPI 同时生效。
2. **连接稳定**：消除"每 10 分钟掉一次、瞬断即丢画布、外部调用偶发失败"，达到长时间无人值守可靠。
3. 覆盖本地命令行 Agent（stdio）、本地 GUI/工作台 Agent（HTTP MCP）、远程/云端 Agent（bridge）。
4. 安全最小权限：scope 分级、写操作确认、默认仅本机、远程显式开启、全链路审计。
5. **渠道/模型也成为连接器工具**：外部 Agent 能列出并调用 a6api/红鸟/artbox 等渠道模型，且**渠道模型增删改后连接器自动感知、无需改代码或重启 Agent**。
6. **外部 Agent 可读取画布媒体内容**（图片/视频/音频），以受控临时凭证或 MCP 媒体块返回。
7. 新增一个 Agent 平台零代码：只产出该平台接入配置（自动生成）。
8. 不破坏现有 Codex 插件、Dreamina CLI、Comfy Bridge、人像排查与已建渠道。

### 0.2 非目标
- 不重写前端画布、不改 40 个画布工具业务语义。
- 不做 A2A 对等协作，仅在 P2 预留 Agent Card。
- 不做 SaaS 多租户；远程只到单机/单团队可被云端 Agent 安全访问。
- 不替换后端 Provider 模型插件体系（模型入向），本方案是能力出向，二者正交。

---

## 1. 现状基线（canvas-agent，实现前需以当前分支代码再核对一次）

| 主题 | 现状 | 位置 |
|---|---|---|
| 运行时 | Node≥18、ESM、Express 5、MCP SDK `^1.30.0`、zod | `canvas-agent/package.json` |
| 入口 | `mcp`=stdio MCP；默认=常驻 Runtime（强制 127.0.0.1:17371） | `src/index.ts` |
| 工具内核 | 40 工具（24 canvas_* + 10 project_*，另 dreamina MCP），名称/zod/描述集中 | `src/schemas.ts`、`tools.ts` |
| 执行口 | `CanvasSession.callTool`；只读工具由 Runtime 用最近 state 直接算；写/生成经 SSE 下发网页、网页 `/canvas/result` 回传 | `canvas-session.ts` |
| stdio 门面 | 仅 `StdioServerTransport`；每次调用裸 `fetch /api/tools`（**无 keepalive/重试**） | `mcp-server.ts` |
| Runtime 模块 | `createLocalRuntimeApp(modules[])`；模块 id 白名单仅三个 | `local-runtime.ts` |
| 鉴权双通道 | 带 token 走 masterToken；不带 token 走签名会话（4 个 proof 头） | `local-runtime-security.ts` |
| 签名会话 | **固定 TTL 10 分钟、不滑动续期**；时钟窗 30s；nonce 一次性、上限 2048 | `local-runtime-session.ts` |
| SSE | 15s ping；同 clientId 重连替换；**close 时若 state 属于该 client 立即清空 canvasState**；工具 30s 超时 | `canvas-session.ts` |
| 配置 | `~/.infinite-canvas/canvas-agent.json`（url 必须 loopback、token、trustedOrigins、registrations） | `config.ts` |
| 入向 Agent | 已能拉起 Codex、Claude | `agents.ts` |
| 出向壳 | Codex 插件 `.mcp.json = npx ... mcp` + 5 skill | `plugins/yingce/` |

> 注：v1.2.5 已合入大量 agent/分镜/外观改动，开工 P0 前先 `git diff 7a1f2db -- canvas-agent` 复核上述行号与逻辑是否仍一致。

---

## 2. 目标架构

```
外部 Agent（Codex/Claude/Cursor/Cline/WorkBuddy/豆包/Hermes…）
   │ MCP stdio            │ MCP Streamable HTTP        │ OpenAPI      │ 云端经 Bridge
┌──▼────────┐   ┌─────────▼─────────┐   ┌──────────────▼──────┐  ┌──────────────────┐
│stdio MCP  │   │ /mcp（本机，默认开）│   │ /openapi /invoke    │  │主动外连 bridge(P2)│
└──┬────────┘   └─────────┬─────────┘   └──────────────┬──────┘  └────────┬─────────┘
   └──────────┬───────────┴───────────────┬───────────┘                  │
      统一鉴权·scope 过滤·审计·会话/画布路由·连接保活与重试
┌──────────────▼──────────────────────────▼──────────────────────────────▼──────────┐
│ 工具内核 ToolKernel（唯一实现）                                                      │
│  画布工具(40) · 媒体读取工具 · 渠道/模型工具(目录自更新) · 直连生成工具(P2)             │
└──────────────┬───────────────────────────────────────────────────┬────────────────┘
       画布执行链（SSE/state/result，加固保活）            渠道执行链（直连后端 API，P1只读 P2生成）
```

---

## 3. 评审决策记录（已拍板，设计约束）

| 编号 | 决策 | 落地方式 |
|---|---|---|
| Q1 | HTTP MCP **默认开启** | Runtime 启动即挂 `/mcp`（loopback+token），配置可关 |
| Q2 | masterToken/连接**不稳定，必须优化** | **P0-A 稳定性专项**，先于/并行 HTTP MCP |
| Q3 | 远程走**主动外连 bridge**（本地不开入站端口） | P2，Runtime 主动连云端、云端投送请求回来 |
| Q4 | a6api/红鸟等**渠道开成连接器工具，且保持更新** | P1 只读目录、P2 直连生成 + 目录变更推送，见 §7 |
| Q5 | **允许外部 Agent 读画布媒体内容** | P1，新增受控媒体读取，见 §8，独立 scope |

---

## 4. P0-A 连接稳定性专项（最高优先）

> 目标：连续 24h，网页 SSE 与外部调用零非预期掉线；休眠唤醒/网络抖动/多标签切换不丢画布；外部连接器单次抖动可自愈。

### 4.1 根因 → 修复对照（代码级）
**S1 签名会话 10 分钟硬过期、不续期（主因）**
- 滑动续期：`verifyRequest` 通过且距到期 < 1/2 TTL 时重置 `expiresAtMs` 与定时器（活跃不过期）；设 12h 绝对上限强制重握手。
- TTL 可配置（默认 30min，最小 10min）；SSE hello/ping 下发 `sessionExpiresAt`，面板显示倒计时、到期前主动 re-exchange。

**S2 SSE 瞬断即清空画布状态**
- 断线宽限（grace，默认 8s）：close 后不立即清 state，同 clientId 重连且 stateHash 一致即恢复，pending 不立即 reject。
- state 归属从仅 clientId 改为 runtimeSessionId+clientId，防旧标签晚到 close 误清新标签；grace 超时再清并返回可重试的 `canvas_disconnected`。

**S3 masterToken 外部通道脆弱**
- 单实例化：lockfile（pid/port/token 指纹）；端口被占优先复用在跑实例而非静默换端口换 token；token 仅缺失时生成一次并持久。
- 统一 `agentFetch`：keep-alive、连接复用、超时；**只读幂等**对 RST/502/503 指数退避重试≤2，**写不自动重试**；token 不一致报 `runtime_identity_mismatch` 并经 `/runtime/info` 暴露 runtimeInstanceId。

**S4 时钟窗/nonce 容错**
- 时钟窗可配（默认 60s）；`request_stale` 自动重新握手一次再重试；nonce 改 LRU 滑动清理而非到 2048 直接 429。

**S5 可观测连接健康**
- `/health` 增 hasCanvas、clients、最老会话剩余 TTL、近 5 分钟重连次数、pending、runtimeInstanceId；前端四态（已连接/重连中/宽限中/离线）+ 倒计时；结构化连接日志。

### 4.2 P0-A 改动清单（尽量新增类、老文件单点接线）
- [ ] `local-runtime-session.ts`：滑动续期、TTL/时钟窗可配、nonce LRU（优先抽独立 renewal/TTL 工具，少改 verifyRequest 函数体）
- [ ] `canvas-session.ts`：新增 `grace-tracker.ts`，老类只加两行接线；state 归属加 runtimeSessionId
- [ ] 新增 `runtime-lock.ts`：单实例/实例复用/token 稳定化（配合 `local-runtime-host.ts`、`config.ts`）
- [ ] 新增 `agent-fetch.ts`：keepalive+只读重试+超时+identity 比对；`mcp-server.ts` 只替换调用
- [ ] `/health` 健康字段；前端面板四态与倒计时
- [ ] 测试：续期、grace 重连、lockfile 抢占、fetch 重试幂等、时钟自愈

---

## 5. P0-B Streamable HTTP MCP（覆盖大多数 Agent）

### 5.1 退出标准
常驻 Runtime 提供 `POST /mcp`（默认开、本机）；MCP Inspector 与至少一个真实 Agent（WorkBuddy 或 Cursor）完成 initialize/tools.list(40 工具)/只读 call 拉到真实画布；stdio 与网页零回归；无画布返回 `no_canvas_connected` 而非挂起。

### 5.2 任务分解（文件级，新增为主）
- **内核抽取（新增）** `src/tool-kernel.ts`：`buildToolRegistry()`（由 schemas 派生 name/schema/description/scope/kind）、`listToolsForScopes`、`parseToolInput`、`toolRequiredScope`；`mcp-server.ts` 改共用，行为不变。
- **HTTP MCP（新增）** `src/modules/mcp-http.ts`：descriptor 复用 `canvas-agent` module id（绕开白名单），`POST /mcp` 用 **stateless** `StreamableHTTPServerTransport({sessionIdGenerator:undefined})`，每请求新建 McpServer+transport、注入 scope/canvasId、闭包直接持有同一 `CanvasSession.callTool`（不再 loopback 一跳）；GET/DELETE 返回 405；兼容全局 `express.raw` 的 Buffer body 与 SSE/JSON 响应协商。
- **挂载（单点）** `http-server.ts` 加一行注册，与 canvas-agent-http **共享同一 CanvasSession**；`local-runtime.ts` 模块白名单/ CORS 单点放开 `/mcp`（POST/GET/DELETE，允许并暴露 `authorization,mcp-session-id,content-type`），打 `// [connector]`。
- **鉴权**：`/mcp` 同时接受 `x-canvas-agent-token` 与 `Authorization: Bearer`（P0 等价 masterToken，P1 换多凭证）；loopback/Host 锁不变。
- **开关/CLI**：`config.mcpHttp={enabled:true,path:'/mcp'}`（Q1 默认开）；`mcp --http` 退化为"确保 Runtime 在跑并打印接入串"；启动不回显 token。
- **前端**：新增独立组件 `agent-connector-panel.tsx`，在老面板单点挂一行 `<AgentConnectorPanel/>`（老文件上游在改，避免大面积冲突）。

### 5.3 验收与工作量
initialize 协议正确；tools/list=40 且与 schemas 一致；无画布快速失败；坏凭证 401、坏 Host 421；Inspector 全通；`npm test` 全绿且 stdio/网页回归。P0-A 约 3–4d，P0-B 约 2–3d，**P0 合计 5–7 人日**（先合 P0-A 再做 P0-B）。

---

## 6. P1 —— 权限分级、多画布、多平台零代码、媒体读取、渠道目录只读

### 6.1 连接器凭证与 scope
- `~/.infinite-canvas/connectors.json`：`{id,label,platform,tokenHash,scopes[],canvasId?,allowMedia,allowAutoApprove,createdAt,lastUsedAt,revoked}`，明文 token 仅创建时返回一次；Bearer 查连接器表，masterToken 仅作本机最高权限兼容；管理接口走签名会话保护。
- tools/list 与 tools/call 双重 scope 校验，越权 `forbidden_scope`。

**Scope 体系（v1.1 增补媒体与渠道）**
| Scope | 工具范围 |
|---|---|
| `canvas:read` | 全部 get/find/validate/export/selection；project_get_context/list_units |
| `canvas:media:read` | **新增**：读取节点/资源媒体内容（§8），默认不授予 |
| `canvas:write` | apply_ops/create/update/move/resize/delete/connect/select/viewport |
| `canvas:generate` | create_*_flow/generate_*/run_generation/project_start/register_output（隐含 write+read） |
| `project:write` | project 资产/镜头写操作 |
| `channel:read` | **新增**：列渠道/逻辑模型/规格价格/目录版本（§7） |
| `channel:generate` | **P2**：经影策渠道直接发起生成（隐含 channel:read） |
- generate⊇write⊇read；媒体、渠道为独立正交 scope，需显式授予。

### 6.2 多画布路由
`CanvasSession` 升级为 `Map<canvasId, CanvasBridge>`，保留默认活跃画布兜底；凭证可绑 canvasId，请求可用 `X-Canvas-Id`（须授权内）；不在线 `canvas_not_connected`，多画布未指定 `canvas_ambiguous`。

### 6.3 平台配置生成器
`src/connect-templates/`：codex/workbuddy/doubao/claude/cursor/cline/generic-mcp/openapi 模板；CLI `canvas-agent connect <platform> [--scope ...] [--canvas ...] [--media] [--json]`；前端可视化生成；产出 `docs/connect/` 五套接入文档。

### 6.4 写操作确认
复用网页确认流，确认卡标注来源连接器；`allowAutoApprove` 仅对低风险写开放，删除/批量/生成始终需确认；超时 `confirmation_timeout`。

### 6.5 渠道/模型只读工具（Q4 第一步，详见 §7）
`channel_list / channel_list_models / model_list_logical / model_get_capability / channel_catalog_version`，数据实时来自后端。

### 6.6 画布媒体读取（Q5，详见 §8）
新增 `canvas_get_media`，独立 scope `canvas:media:read`，返回 MCP 图片块或短 TTL 签名引用，限大小、不落盘。

### 6.7 验收与工作量
只读连接器看不到写/媒体/渠道工具；两连接器绑不同画布并发不串话；吊销即时生效；`connect workbuddy` 可直接导入；媒体无 scope 403、有 scope 取到图；后台新增模型后下一次 list 立即看到。约 **7–9 人日**。

---

## 7. 渠道/模型连接器工具与"目录自更新"（Q4 专章）

### 7.1 原则：易变的是"数据"，不是"工具定义"
模型频繁增删改价；若每模型一个工具，加模型就要改工具集、重启所有 Agent。故**工具集稳定（list/get/generate 通用工具，入参带 model），模型目录是工具返回的动态数据**，加模型=数据变化，协议零改动。

### 7.2 工具定义
| 工具 | 期 | 作用 |
|---|---|---|
| `channel_list` | P1 | 列渠道（id/名称/协议/启用/模型计数） |
| `channel_list_models` | P1 | 列渠道模型：key、capability、protocol、计费、价格、enabled、规格 |
| `model_list_logical` | P1 | 列逻辑模型家族及可用线路数（外部选模型主入口） |
| `model_get_capability` | P1 | 取模型输入约束（比例/分辨率/时长/质量并集） |
| `channel_catalog_version` | P1 | 目录版本（updatedAt/hash/计数）供比对刷新 |
| `channel_generate` | P2 | 经影策渠道直接发起文/图/视/音生成，返回 taskId |
| `channel_get_task` | P2 | 查询生成任务状态与结果 |

### 7.3 "保持更新"三层机制
1. **拉取即最新（P1）**：HTTP stateless 每次 tools/call 实时查后端；stdio 侧加 5–10s 短缓存而非进程内常驻。
2. **版本探测（P1）**：`channel_catalog_version` 返回 `{version,updatedAt,hash,counts}`，长任务前后比对决定是否重拉。
3. **变更推送（P2）**：后端模型增删改/改价时 bump 目录版本；Runtime 经 SSE/bridge 收 `channel.catalog_changed`，向已连接 Agent 发 MCP `notifications/tools/list_changed` 自动重拉；stateless 靠 1/2 层。
4. tools/list 声明 `listChanged`；工具 schema 稳定，**不需要重新生成任何平台配置**。

### 7.4 数据来源与边界
Runtime→Backend 走内部只读投影端点（带内部令牌），避免直连 SQLite 耦合；`channel_generate` 复用后端既有 provider 执行/任务/计费体系；渠道密钥绝不经连接器返回。

### 7.5 验收
后台新增/停用/改价：P1 下次 list/version 立即反映；P2 已连接 Agent 收到 list_changed 自动刷新；`channel_generate` 借红鸟出图并轮询拿到结果；全程不重启、不改平台配置。

---

## 8. 画布媒体读取（Q5 专章）

### 8.1 能力与安全
- `canvas_get_media{nodeId|resourceId, maxBytes?, mode?:"block"|"url"}`，需 `canvas:media:read`（默认不授，`--media` 显式开）。
- block：图片返回 MCP `image` content（base64）；音视频过大转受控引用。url：短 TTL（默认 5min）、单次/限次、绑定 connectorId 的签名 URL。
- 限大小（图片默认 8MB，音视频给 URL）；仅授权画布；不落地、不缓存、不进日志；全审计。

### 8.2 获取链路
画布 state 默认不含媒体本体（`compactNode` 剔除 url/dataUrl）。已持久化到后端/对象存储的，Runtime 用内部令牌换短期地址中转（优先，不依赖网页）；仅在网页内存的，经 SSE 发 `media_fetch`、网页回 base64（带超时），网页离线返回 `canvas_not_connected`；中转流式返回。

### 8.3 验收
无 scope→403；有权限图片以 image block 返回；URL 5 分钟后失效、二次使用被拒；20MB 视频自动降级签名 URL；日志可追溯、磁盘无残留。

---

## 9. P2 —— OpenAPI 兜底、主动外连 Bridge、直连生成与变更推送

### 9.1 OpenAPI 门面（GPT Actions/Coze/Dify/Hermes）
新增 `src/modules/openapi-http.ts`：`GET /openapi.json`（ToolRegistry 自动生成 OpenAPI 3.1，zod→JSON Schema，按 scope 过滤）；`POST /invoke/{tool}` 统一响应；本机调试页；`connect openapi` 产出导入指引。

### 9.2 主动外连 Bridge（Q3 选定，本地零入站端口）
本地 Runtime 作为客户端主动外连云端 Relay（WSS 长连 + 出站令牌/可选 mTLS）；Relay 把云端 Agent 请求投送回 Runtime、结果原路返回；免公网 IP/端口映射、NAT 友好；断线指数退避重连，多 Runtime 按 connectorId 路由；Relay 不存数据只转发（端到端加密优先），仍走 scope/审计/写确认，Relay 侧限流、令牌可吊销、可选只读；复用 `canvas-agent/native/comfy-bridge` 已验证的主动轮询/回传/心跳/重启续领经验。交付 `canvas-agent bridge --relay <url> --token <t> [--read-only]` + Relay 最小参考实现与部署文档。

### 9.3 渠道直连生成与目录变更推送
落地 §7.3 的 `channel_generate/channel_get_task` 与 `notifications/tools/list_changed`；后端补目录版本自增与变更事件。

### 9.4 可观测与 A2A 预留
结构化 JSON 日志 + `/metrics`（计数/时延/错误率/在线画布/活跃连接器/bridge 状态）；`GET /.well-known/agent.json` Agent Card。

### 9.5 验收与工作量
OpenAPI 导入 Postman/GPT Actions 可调；bridge 本地不开端口、云端 Agent 完成只读与一次确认写；远程无 token 401、超频 429；渠道生成与 list_changed 打通。约 **8–11 人日**。

---

## 10. 横切设计
- **统一错误码**：unauthorized/forbidden_scope/no_canvas_connected/canvas_not_connected/canvas_disconnected(可重试)/canvas_ambiguous/invalid_params/confirmation_required/confirmation_timeout/rejected_by_user/runtime_identity_mismatch/request_stale(自动握手)/tool_failed/upstream_timeout/media_too_large/catalog_stale。MCP→JSON-RPC error，OpenAPI→HTTP 状态码。
- **超时/并发/幂等**：复用 canvas-tool-timeouts；只读幂等可重试、写不重试；同画布写串行、只读并发；grace 期 pending 可等重连。
- **兼容版本**：工具只增不删、废弃先 deprecated；server 名 `yingce-canvas`；`/runtime/status` 暴露 mcp-http/openapi/bridge 状态；三门面共用 kernel。
- **发布**：随 `@ddcat666/open-ai-canvas-agent` 发版；前端"Agent 接入"向导（启动→选平台→选权限含媒体/渠道→生成配置，不手填 token）。

---

## 11. 测试策略（同时作为"上游契约回归"）
- 单测：kernel scope 过滤、schema 派生、续期/grace/nonce LRU、锁竞争、fetch 重试幂等、OpenAPI 生成、错误码映射。
- 协议集成：进程内 MCP Client（Streamable HTTP）跑 initialize/list/call；OpenAPI 用 supertest。
- 稳定性：24h soak、断网/休眠/多标签/重启 Runtime 自愈、并发与重连压测。
- E2E：脚本化假浏览器 SSE+state/result，跑通"Agent→Runtime→网页→回传"与媒体读取、渠道只读。
- 真实平台矩阵：Inspector/Codex/Claude/Cursor/WorkBuddy/豆包每轮回归只读 + 一次确认写。
- **每次 merge upstream/main 后必须全量跑本套测试**，作为对插件协议/MCP SDK/runtime module 三大契约的回归门禁。全部纳入 `canvas-agent/test/`，CI 跑 `npm test`。

---

## 12. 里程碑
| 阶段 | 交付 | 人日 | 里程碑结果 |
|---|---|---|---|
| **P0-A 稳定性** | 会话续期、SSE grace、单实例/token 稳定、fetch 加固、健康面板 | 3–4 | 24h 不掉线，抖动/休眠/重启自愈 |
| **P0-B HTTP MCP** | tool-kernel、`/mcp` stateless、鉴权 CORS、Inspector+真实 Agent | 2–3 | WorkBuddy/豆包/Cursor 类可连（单画布、masterToken） |
| **P1** | 凭证+scope、多画布、配置生成器、写确认、**媒体读取、渠道只读目录** | 7–9 | 主流 Agent 零代码接入，权限可控，渠道目录实时 |
| **P2** | OpenAPI、**主动外连 bridge**、**渠道直连生成+变更推送**、metrics、Agent Card | 8–11 | 覆盖 REST 长尾与云端 Agent，模型目录自动更新 |
| 合计 | — | **20–27 人日** | 通用连接器成型且长期稳定 |

建议 P0-A 先合入并观察 3–5 天真实连接日志，再推进 P0-B/P1。

---

## 13. 风险登记
| 风险 | 缓解 |
|---|---|
| 滑动续期变"永不过期" | 12h 绝对上限强制重握手；空闲超 TTL 仍过期 |
| grace 掩盖真实离线/pending 堆积 | grace 时限 + pending 上限，超时明确失败 |
| 残留 lockfile 误判 | lock 带 pid/心跳与活性探测，死锁可安全接管 |
| 只读重试误用于写 | 重试白名单只含只读幂等工具 |
| 媒体外泄 | 独立 scope、短 TTL 签名 URL、限大小、不落地、全审计 |
| 渠道目录缓存不一致 | 极短缓存 + 版本号 + P2 推送三重保障 |
| bridge 中继攻击面 | 端到端加密、Relay 不存数据、可只读、令牌可吊销、限流 |
| 上游更新与本分支冲突 | 新增文件优先、单点挂载打 `[connector]`、核心修复包装化、merge 后跑 §11 |
| Express5/Streamable transport 兼容 | P0-B 首日 Inspector 打通最小闭环 |
| 三门面行为漂移 | 单一 kernel 派生，门面内禁止私加工具 |

---

## 14. P0 开工 Checklist（合并 A/B）
**A. 稳定性**
- [ ] session 滑动续期 + TTL/时钟窗可配 + nonce LRU
- [ ] SSE 断线 grace（新增 grace-tracker）、state 归属加 runtimeSessionId、pending 可重试
- [ ] runtime-lock 单实例/token 稳定化，identity mismatch 自检
- [ ] agent-fetch：keepalive + 只读重试 + 超时
- [ ] /health 健康字段 + 前端四态/倒计时面板
- [ ] 稳定性单测与 soak

**B. HTTP MCP**
- [ ] tool-kernel 抽取，stdio 改用 kernel（行为不变）
- [ ] modules/mcp-http.ts（Streamable HTTP, stateless, /mcp，默认开）
- [ ] http-server 挂载并共享 CanvasSession；CORS/鉴权（Bearer+token）
- [ ] config/CLI 开关；无画布快速失败 no_canvas_connected
- [ ] Inspector + 一个真实 Agent 联调；stdio/网页回归；docs/connect/mcp-http.md

## 15. L 分层落地状态（L1/L2/L3，Mac 当服务器）

> 目标：不止 Codex，任何 MCP 客户端（豆包/WorkBuddy/Hermes 等）都能调用影策画布。三层递进，均已实现并验证。

### L1 本机（Codex 直连 /mcp）
- `/mcp` Streamable HTTP 门面（50 工具：canvas 32 / project 10 / channel 5 / model 2 / dreamina 1），Runtime 常驻 `127.0.0.1:17371`。
- `~/.codex/config.toml` `[mcp_servers.yingce]` 走 `url = http://127.0.0.1:17371/mcp` + `x-canvas-agent-token` 头。

### L2 局域网（其他电脑可连本机 Runtime）
- `CANVAS_HOST=0.0.0.0` 对外监听；`CANVAS_AUTHORITY` 逗号分隔权威 Host（`exactAuthorityGuard` 多 authority，未声明 Host 仍 421）。
- `/mcp` 配 token 即强制 Bearer 校验（`Authorization: Bearer` 或 `x-canvas-agent-token`，无 token 401）。
- 已验证：局域网 `http://192.168.31.244:17371/mcp` + token 通 / 401 / 421。commit `9683fba`。

### L3 公网 / 网站用户（经云端 broker 网关调用本机画布）
- **架构**：外部 Agent → MCP 网关(`:17801/mcp`, Bearer) → Broker(`:17800`, 队列转发) ← 主动外连 bridge ← 本地 Runtime(`:17371`)。
  本机 Runtime 零入站端口，只主动外连 Broker（register/heartbeat/poll/result）。
- **新增入口**：
  - `canvas-agent/src/bridge/broker-server.ts`：Broker 独立 server（`node dist/bridge/broker-server.js`，默认 `0.0.0.0:17800`）。
  - `canvas-agent/src/bridge/gateway-server.ts`：MCP 网关（`node dist/bridge/gateway-server.js`，默认 `0.0.0.0:17801`），启动时连 Schema Runtime 拉工具定义，`tools/call` 经 Broker `/api/canvas-bridge/request` 转发并轮询 `/request/:id`。
- **`/api/tools` 补 channel 分支**：渠道工具（channel_* / model_*）走本地 ctx，画布工具走 `session.callTool` —— 补齐 bridge 转发对全部 50 工具的覆盖。
- **一键启动**：`启动影策L3服务器.command`（幂等：broker → Runtime+bridge → 网关，自动取局域网 IP）。
- **验证**：外部 MCP 客户端 → 网关 `channel_list` 返回 3 渠道 121 模型；`canvas_get_context` 无画布错误透传（isError）；无 token 401；局域网 IP 访问网关正常。
- **外部 Agent 接入配置**：`http://<网关IP>:17801/mcp` + Bearer `<gateway token>`；生产环境必须使用强随机值，仓库不保存真实凭据。

### 安全与运维备注
- Broker 的 bridge 注册与远程 Agent API 使用两套独立凭据：`CANVAS_BROKER_REGISTRATION_TOKEN` 仅用于首次注册/换证，`CANVAS_BROKER_AGENT_TOKEN` 用于 `request`/`bridges`/`request/:id` 与状态接口；两者均应配置强随机值。
- `启动影策L3服务器.command` 不保存真实凭据；集中配置位于本机 `~/.infinite-canvas/l3.env`，不得提交仓库。
- 未配置 token 仅适用于 loopback 开发；Broker/Gateway 对外监听时必须配置对应 token。

### 配置固化（launchd 托管 · 2026-09-04）
- **集中配置** `~/.infinite-canvas/l3.env`（端口/token/bridgeId/目录），模板 `deploy/l3.env.example`；改 token 后 `l3-manage.sh restart` 生效。
- **开机自启 + 崩溃自动重启**：3 个 LaunchAgent（`~/Library/LaunchAgents/com.yingce.canvas-{broker,runtime,gateway}.plist`，RunAtLoad + KeepAlive）。
- **wrapper 脚本** `~/.infinite-canvas/l3-run-{broker,runtime,gateway}.sh`：绝对路径 node（`~/.local/bin/node`）、不依赖 launchd PATH、动态局域网 IP。
- **管理命令** `~/.infinite-canvas/l3-manage.sh {start|stop|restart|status|logs}`；`启动影策L3服务器.command` 双击查看状态 / 传参管理。
- **稳定性修复**（本次固化新增）：
  - `gateway-server.ts`：拉 Schema 加重试（10 次/2s），launchd 同时拉起时等待 Runtime 就绪，避免启动即崩。
  - `bridge/client.ts`：poll 失败自动**重新 register**，Broker 重启后 bridge 无需手动干预自动恢复接入。
- **验证**：launchd 三件套上线；杀 broker 后 12s 自动拉起（KeepAlive）+ bridge 自动重注册；外部 MCP 客户端全链路 `channel_list` 3 渠道 121 模型。

### 公网暴露安全加固（2026-09-04）
- **背景**：broker 远程侧端点（`request`/`bridges`/`request/:id`）原无鉴权，公网暴露=无鉴权远程执行。
- **加固**：broker 新增 `CANVAS_BROKER_AGENT_TOKEN`（兼容旧 `CANVAS_BROKER_GATEWAY_TOKEN`）和 `CANVAS_BROKER_REGISTRATION_TOKEN`；配置后远程 Agent API 与 bridge 注册分别使用独立 `Authorization: Bearer`，401 拒绝。网关调 broker 时携带 Agent token。
- **队列保护**：队列满时只清理已完成结果，不丢弃 `pending`/`running` 请求；无法安全入队时返回 409。
- **token 强随机化**：本机 `~/.infinite-canvas/l3.env` 使用独立的 bridge、registration、broker-agent、gateway 四类凭据；wrapper 只负责注入，不把真实值写入仓库。
- **验证**：无 token/错 token → 401；对 token → 全通；bridge token 轮换、队列满保护与 L3 全链路测试通过。

### 正式公网落地（Cloudflare Tunnel · 2026-09-04）
- **最终入口**：`https://yingce.cc.cd/mcp`（外部任意 Agent 经公网直连画布），Bearer `<gateway token>`。
- **拓扑**：公网 → Cloudflare 边缘 → 本机 `cloudflared`（Named Tunnel `yingce-canvas`）→ 本地 ingress → `127.0.0.1:17801`（MCP 网关）→ Broker → Runtime（零入站端口主动外连）；`yingce.cc.cd` 其余路径仍指向影策 web（`127.0.0.1:3100`）。
- **为什么不用 Cloudflare 远程隧道 path 路由**：在承载 `yingce.cc.cd` 的远程隧道（newapi）里加 `/mcp` 规则后，实测仍被同域通配规则 `* → 3100` 抢占（405 nginx），等待+重启 cloudflared 均不生效 → 该同域多路径匹配不可靠。
- **改为本地 config 隧道**：`~/.cloudflared/yingce-canvas.yml` 本地 ingress 从上到下首个匹配、确定生效：`yingce.cc.cd /mcp → 17801`、`yingce.cc.cd → 3100`、兜底 404。隧道 `cloudflared tunnel --config <yml> run yingce-canvas`（注意 `--config/--no-autoupdate` 须在 `run` 之前）。
- **DNS**：`yingce.cc.cd` 记录（CNAME 隧道类型）目标切到 `62493ea4-fc1e-4f14-b1df-bfb4067da444.cfargotunnel.com`，代理保持开（橙色云朵）。
- **托管**：launchd `com.yingce.canvas-tunnel`（wrapper `~/.infinite-canvas/l3-run-tunnel.sh`）；四件套（broker/runtime/gateway/tunnel）全部 RunAtLoad+KeepAlive。
- **验证**：公网 `POST https://yingce.cc.cd/mcp` → initialize 200（serverInfo canvas-gateway 0.1.0）、tools/list 50 工具、channel_list 3 渠道 121 模型；无/错 token 401；web `GET /` 200 不受影响。
- **外部 Agent 接入**：MCP 配置 `url = "https://yingce.cc.cd/mcp"` + `http_headers { Authorization = "Bearer <gateway token>" }`（token 见本机 `~/.infinite-canvas/l3.env` 的 `CANVAS_L3_GATEWAY_TOKEN`）。

### P1 商业化 Key 网关（2026-09-04）
- **目标**：把单一共享内部 token 升级为多租户客户 API Key 体系，支持颁发/吊销/配额/计量，为计费打底。
- **新增**：
  - `canvas-agent/src/bridge/gateway-keys.ts`：KeyStore（SHA-256 哈希存储，明文只在颁发时打印一次）+ 管理 CLI。
  - `canvas-agent/src/bridge/gateway-server.ts`：认证升级为 master token（内部）或客户 Key（`Authorization: Bearer ak_…` 或 `X-Api-Key`）。
- **安全**：磁盘只存哈希；Key 泄露可单独吊销（`revoke`），不影响其他客户；默认 `~/.infinite-canvas/gateway-keys.json`（0600）。
- **配额**：每 Key 日调用上限（`quota.dailyCalls`，0=不限），满额后该 Key 所有请求 429（`-32029`）。
- **计量**：每次工具调用追加写 JSONL（`~/.infinite-canvas/gateway-usage.jsonl`），含 keyId/keyName/tool/ok/ms/日期——P2 计费数据源。
- **热重载**：KeyStore 按文件 mtime 自动热重载，CLI 颁发/吊销后网关**无需重启**即时生效。
- **CLI**（`node dist/bridge/gateway-keys.js`）：`add --name <客户> [--quota <日上限>]` / `list` / `revoke|enable|reset <id|name>` / `usage <id|name>`。
- **验证**：公网客户 Key 全链路（50 工具 / 3 渠道 121 模型）；无凭据/错误 Key 401；配额满额 429；用量 JSONL 落盘；6 个单测全绿（含热重载测试）。

### P2 商业化计量计费（2026-09-04）
- **目标**：在 P1 Key 基础上实现"计量 + 定价 + 账单 + 余额扣费"商业闭环。
- **新增**：
  - `canvas-agent/src/bridge/gateway-billing.ts`：定价表 + 用量聚合 + 账单 CLI（`bill`/`report`/`pricing`）。
  - `gateway-keys.ts`：Key 增加 `balance`（CNY 元）+ `topup`（充值）/`deduct`（扣费，不允许负余额）。
  - `gateway-server.ts`：余额预检（不足返回业务 402）+ 调用后扣费 + `cost` 写入用量 JSONL。
- **计费模型**：按工具调用次数（per-call）计费；定价表 `~/.infinite-canvas/gateway-pricing.json` 支持**精确名**、**前缀通配**（`canvas_*`）、**默认单价**三级命中；改价热重载（mtime），无需重启。
- **规则**：失败调用不计费（`ok=false` → cost 0）；余额不足调用前拦截（402 Payment Required）；未配置余额的 Key 不受影响（仅日配额）。
- **CLI**：`gateway-billing bill [--date D] [--key K]` / `report` / `pricing`；`gateway-keys topup <id|name> <金额>` / `balance <id|name>`。
- **验证**：公网客户 Key 成功调用扣费（余额 10→9.98）、失败调用 0 计费、零余额 402 拦截、账单/报表正确；10 个单测全绿（定价三级命中、聚合计费、余额扣费/不足拦截）。

### P3 标准接入：OAuth2 client credentials 统一网关（2026-09-04）
- **目标**：让企业客户/主流 agent 用行业标准方式接入（client_id/client_secret 换短期 access_token），替代直接裸传长期 Key。
- **新增**：
  - `gateway-keys.ts`：`--oauth` 颁发时生成 `client_secret`（`cs_` 前缀，哈希存储）+ `verifyClientSecret(client_id=key.id, secret)`。
  - `gateway-server.ts`：`POST /auth/token` OAuth2 token 端点（`grant_type=client_credentials`，支持 JSON body 或 `Authorization: Basic`），签发短期 access_token（`at_` 前缀，默认 3600s，内存存储）；Bearer 校验优先级 master → **access_token** → 客户 Key；无效/过期 token 401。
- **标准**：token 响应符合 RFC 6749（`access_token` / `token_type: Bearer` / `expires_in`）；错误码 `invalid_client` / `invalid_request` / `unsupported_grant_type`。
- **隧道**：`yingce-canvas` 隧道新增 `yingce.cc.cd /auth/token → 17801` 路由（`~/.cloudflared/yingce-canvas.yml`）。
- **验证**：公网 client_credentials 换 token（200）→ access_token 调 MCP（200）→ 无效 token 401 → 错误 secret 401 invalid_client → Basic auth 换 token 200；12 个单测全绿（含 client_secret 哈希/停用拒绝/非 oauth 拒绝）。
- **后续统一网关建议**：规模商用后可在网关前叠加 Kong/KrakenD 等 API 网关做限流、监控、多环境路由；OAuth2 token 端点已按标准实现，可平滑对接授权服务器（如 Keycloak/Auth0）升级为授权码流。
