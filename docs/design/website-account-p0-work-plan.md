# Yingce P0：网站积分闭环工作方案

> 状态：Ready for implementation  
> 日期：2026-09-04  
> 仓库：`ddcat-ai/open-ai-canvas`  
> 工作分支：`feat/yingce-p0-agent`  
> 上位设计：[`website-account-integration.md`](./website-account-integration.md)

## 0. 给所有智能体的执行规则

1. 本文件是本轮 P0 的执行契约；上位设计文档只提供背景。发现代码与契约冲突时，先停止写入并报告证据，不自行发明第二套协议。
2. 只在 `feat/yingce-p0-agent` 工作树修改；不要触碰 `/Users/linmengjiang/open-ai-canvas` 主 checkout 的用户改动。
3. 每个任务只修改自己的 **Write scope**；不得顺手重构相邻代码、开发者密钥、OAuth、支付或对账。
4. 不新增钱包、余额表、积分流水或依赖；复用现有 `CreditAccount`、`BillingOrder`、`CreditLedgerEntry` 和既有两阶段状态机。
5. 金额字段只能使用正整数 `microcredits`；不得把人民币元浮点数发送到 Go 内部接口。
6. 日志、测试输出、文档和提交中不得出现真实 token、API key、cookie 或 session；示例只用 `<service-token>` 等占位符。
7. 每个任务完成时必须留下：改动文件、验证命令及结果、未解决风险、下一任务入口。未通过验证不得声称完成。
8. 本轮默认不 commit/push；由集成智能体统一检查后再决定是否发布。

## 1. 目标与完成定义

### 目标

让 Yingce MCP 网关在 `CANVAS_ACCOUNT_PROVIDER=remote` 时，通过 Go 的 `/api/internal` 复用网站钱包，完成：

```text
reserve → 执行 MCP 工具 → settle
                         ↘ refund（工具失败）
```

### P0 完成定义

- Go 暴露并保护以下内部接口：账户查询、冻结、结算、退款。
- 内部接口只接受有效服务令牌；令牌缺失或错误时 fail-closed。
- reserve 对活跃用户、正整数金额、余额、同幂等键和并发扣费正确。
- settle/refund 校验用户与订单归属，并支持重复请求安全返回。
- Node Remote provider 只调用新接口，解析 `{code,data,msg}` envelope。
- MCP 工具执行前必须成功 reserve；工具成功后 settle，失败后 refund。
- reserve/结算/退款网络错误或协议错误不能默认为放行或成功。
- 本地 provider 行为保持兼容；已有 Go、Node 测试全部通过。

## 2. 冻结的 P0 内部接口契约

### 2.1 认证与公共响应

请求头：

```http
X-Internal-Token: <service-token>
```

Go 与 Node 使用同一个环境变量名：

```text
CANVAS_INTERNAL_SERVICE_TOKEN
```

服务端未配置 token 时，内部接口不可用；不得把空字符串视为有效 token。成功响应统一为：

```json
{"code":0,"data":{},"msg":"ok"}
```

失败响应使用非零 `code`、`data:null` 和不泄露内部细节的 `msg`。HTTP 状态映射：

| 情况 | HTTP |
|---|---:|
| 缺失/错误服务令牌 | 401 |
| JSON、路径或金额参数非法 | 400 |
| 用户/订单不存在或不属于该用户 | 404 |
| 余额不足 | 402 |
| 幂等参数冲突或非法状态转换 | 409 |
| 未预期的服务错误 | 500 |

### 2.2 路由

内部接口基址包含 `/api/internal`：

```text
GET  /accounts/:userId
POST /accounts/:userId/reservations
POST /accounts/:userId/reservations/:orderId/settle
POST /accounts/:userId/reservations/:orderId/refund
```

### 2.3 请求与最小响应字段

reserve：

```json
{
  "amountMicrocredits": 10000,
  "tool": "canvas_get_context",
  "scene": "mcp",
  "idempotencyKey": "stable-request-key"
}
```

- `amountMicrocredits` 必须为 JSON 整数且 `> 0`，服务端用 `int64` 校验。
- `tool`、`scene`、`idempotencyKey` 必须非空且有合理长度上限。
- `billingMode` 固定为 `fixed_request`，`capability` 固定为 `mcp`，由服务端写入订单，不信任客户端自报。

账户查询 `data` 至少包含：

```json
{
  "userId": "user-id",
  "availableMicrocredits": 1000000,
  "reservedMicrocredits": 0,
  "version": 3
}
```

reserve `data` 至少包含：

```json
{
  "orderId": "order-id",
  "status": "reserved",
  "amountMicrocredits": 10000,
  "idempotencyKey": "stable-request-key"
}
```

settle 请求：

```json
{"idempotencyKey":"stable-request-key"}
```

refund 请求：

```json
{
  "idempotencyKey": "stable-request-key",
  "error": "tool execution failed"
}
```

`idempotencyKey` 同一调用贯穿 reserve、settle、refund；settle 时作为既有结算服务需要的稳定 provider request id。成功返回订单终态；重复提交同一终态请求必须返回原终态，不重复写账。

### 2.4 幂等与归属规则

- reserve 先按 `(userId, idempotencyKey)` 查询已有订单：
  - 金额、tool、scene、能力、计费模式完全一致：返回原订单；
  - 任一不可变参数不同：409；
  - 不能只依赖数据库唯一键异常来实现幂等。
- 只允许 `UserStatusActive` 用户冻结；未知、禁用用户均拒绝。
- settle/refund 必须确认 URL 的 `userId` 与订单用户一致。
- 已 `settled` 的 settle、已 `refunded` 的 refund 可幂等返回；settled 后 refund、refunded 后 settle、未知状态转换返回 409。
- 不能因为远程查询/结算失败而把请求当作成功；调用方必须 fail-closed。

## 3. 任务包与写入边界

### A｜Go 钱包领域与幂等

**职责**：把现有 finance service/repository 适配为内部 fixed-request 契约，不改变既有公开钱包行为。

**Write scope**：

- `backend/internal/model/models_finance.go`（仅必要字段/JSON 适配）
- `backend/internal/repository/finance.go`
- `backend/internal/repository/finance_internal_test.go`（新增）
- `backend/internal/service/finance.go`
- `backend/internal/service/finance_internal_test.go`（新增）

**必须完成**：

- 活跃用户检查与正整数 `microcredits` 校验。
- reserve 幂等回读、参数冲突、余额不足、并发不超扣。
- settle/refund 复用现有状态机、不可变流水和幂等 reference key。
- 订单字段固定为 `microcredits`，不得引入人民币浮点。

**验收**：

```bash
cd backend && go test ./internal/repository ./internal/service
```

输出交接：列出 service/repository 新增方法及它们对应的 handler 调用方式；不要修改 HTTP 路由。

### B｜Go 内部 HTTP 边界

**依赖**：A 的 service/repository 契约。

**Write scope**：

- `backend/internal/handler/finance.go`
- `backend/internal/handler/internal_finance.go`（新增，若更小可合并到 finance.go）
- `backend/internal/handler/internal_finance_test.go`（新增）
- `backend/internal/handler/response.go`（仅在统一 envelope 必需时）
- `backend/cmd/server/main.go`（仅在现有配置注入确有必要时）

**必须完成**：

- 在 `/api` 下注册 `/internal` 子组，形成第 2 节的四条路径。
- `X-Internal-Token` 中间件 fail-closed；比较时使用安全的固定时间比较方式。
- 严格解码 JSON、拒绝零/负数/浮点金额和过长字段。
- 统一 `{code,data,msg}`，不把上游数据库/令牌内容写入响应或日志。
- 覆盖认证、参数、用户/订单归属、幂等冲突、状态码映射。

**验收**：

```bash
cd backend && go test ./internal/handler
```

### C｜Node RemoteAccountProvider

**依赖**：B 的 HTTP 契约；不修改 MCP 主流程。

**Write scope**：

- `canvas-agent/src/bridge/account-provider.ts`
- `canvas-agent/test/account-provider.test.ts`
- 必要时同步 `canvas-agent/src/bridge/gateway-oauth.ts` 中的 fake provider 适配（只改接口调用，不改 OAuth 行为）

**必须完成**：

- `AccountProvider` 公共生命周期改为：

```ts
reserve(subjectId, amountMicrocredits, idempotencyKey, tool)
settle(orderId, idempotencyKey)
refund(orderId, idempotencyKey, error)
recordCall(subjectId, toolName)
```

- Remote 只调用 `/accounts/:userId` 与 reservations 路由；解析 Go envelope。
- 所有金额为 `number` 但必须是正整数 microcredits；远程 JSON 不出现 `cost`/人民币元。
- HTTP 非 2xx、协议错误、超时、服务不可用均返回明确失败结果；不得 `allow: true`。
- 重试沿用调用方传入的稳定幂等键；删除 `Date.now()+Math.random()` 作为扣费幂等方案。
- token 只放请求头，不进入错误日志。
- 工厂使用 `CANVAS_ACCOUNT_PROVIDER=remote` 与 `CANVAS_INTERNAL_SERVICE_TOKEN`；缺配置时直接报配置错误。

**验收**：

```bash
cd canvas-agent && npm test -- --test-name-pattern='account-provider|oauth'
```

### D｜Node MCP 计费生命周期

**依赖**：C 的新接口；负责主流程。

**Write scope**：

- `canvas-agent/src/bridge/gateway-server.ts`
- `canvas-agent/test/gateway-server-billing.test.ts`（新增）

**必须完成**：

- 每次 MCP 调用生成一个稳定幂等键，并将同一个键传给 reserve/settle/refund。
- reserve 失败、协议错误或网络错误时，不执行工具。
- 工具成功后 settle；工具失败后 refund。
- settle/refund 失败时返回错误，不能伪装成完整成功。
- 仅在终态处理完成后写 usage log；`recordCall` 不阻断主要记账结果。
- 兼容 key 与 OAuth subject 的现有认证映射，不扩展 P1 授权功能。

**验收**：

- fake provider 断言调用顺序为 reserve→call→settle 或 reserve→call→refund。
- reserve 拒绝时断言 bridge 未被调用。
- settle/refund 失败时响应为 `isError: true`。

### E｜Node 定价与用量单位

**依赖**：C 的方法签名；可与 D 并行，但不得修改 D 的文件。

**Write scope**：

- `canvas-agent/src/bridge/gateway-billing.ts`
- `canvas-agent/test/gateway-billing.test.ts`
- `canvas-agent/gateway-pricing.json`（若该文件存在且为当前默认配置）
- 与上述文件直接相关的配置/README 注释（只改 P0 环境变量和单位）

**必须完成**：

- `perCall` 改为 `perCallMicrocredits`。
- `Pricing`、`priceFor`、`aggregate`、`UsageEntry.cost` 和 CLI 输出统一 microcredits。
- 默认配置用整数；避免浮点四舍五入。
- 账单展示若保留人类可读格式，必须是展示层换算，不得把展示金额传给 Remote provider。

**验收**：

```bash
cd canvas-agent && npm test -- --test-name-pattern='gateway-billing'
```

### F｜集成与验证

**负责人**：当前协调/集成智能体；其他任务包完成前不抢改业务文件。

**Write scope**：仅修复由 A-E 直接造成的孤立引用/类型错误；不得扩大功能。

**必须完成**：

- 合并/检查 A-E 的接口命名和字段命名。
- 检查 `gateway-oauth.ts`、README、环境变量注释及所有旧 `preCheck`/`charge` 引用。
- 运行完整验证：

```bash
cd backend && go test ./...
cd ../canvas-agent && npm test
npm run build
cd .. && git diff --check
```

- 做最小人工端到端 smoke：一个已存在的活跃用户完成 reserve→settle；工具失败完成 refund；重复幂等键不重复扣费；local 模式回归。

## 4. 依赖图与交接顺序

```text
A Go domain ──→ B Go HTTP ──→ C Remote provider ──→ D MCP lifecycle
                                      └──────────────→ F integration
E pricing ──────────────────────────────────────────→ D/F（只通过公共类型衔接）
```

推荐提交/交接顺序（即使未来并行，也按此顺序合入）：

1. A：领域与 repository/service 测试绿。
2. B：HTTP 契约与 handler 测试绿。
3. C：Remote provider 测试绿。
4. E：pricing/microcredits 测试绿。
5. D：生命周期测试绿。
6. F：全量测试、diff 和手工 smoke 绿。

## 5. 明确不做（留给 P1/P2）

- `DeveloperApiKey` 表、自助密钥和 client credentials 产品化。
- 网站登录态 `/authorize`、consent、授权撤销。
- 充值商品、支付宝/微信支付、回调、对账、补单。
- 定价后台、50 个工具的商业化定价管理、限流风控。
- 删除 local provider 或本地账本；P0 只保留切换开关。

## 6. 串行交接记录

每个任务包完成后，在本地交接记录中写清：

```text
任务包：A/B/C/D/E/F
状态：done | blocked
改动文件：绝对路径列表
验证：命令 + 结果
契约偏差：none 或具体字段/状态码
残余风险：一条
下一步：下一个任务包及阻塞条件
```

本轮由当前执行者按 A → B → C → E → D → F 顺序串行推进；不创建额外控制面，不重复扫描已确认的文件。
