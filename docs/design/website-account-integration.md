# 影策商业化：MCP 网关接入网站账户/积分/支付体系 · 落地方案

> 目标：客户在**网站**注册、充值（积分/余额），在自己的 Codex 等 Agent 里装影策插件、`mcp login`
> 用**网站账号**授权，之后每次 MCP 工具调用实时扣其**网站积分**；运营方不再手工发 `ak_`、不再维护本地账本。
>
> 关键结论：**账户、积分、充值、支付、对账上游后端已经全部建好，不需要重造钱包。** 我们要做的是
> 让 Node 侧 MCP 网关通过一组「内部接口」复用 Go 后端的账户与计费能力；任务 2 落地的
> `AccountProvider` 抽象（`canvas-agent/src/bridge/account-provider.ts`）就是为此预留的开关。

---

## 1. 现状盘点（backend = Go，Gin + GORM + SQLite/Postgres，默认监听 `:8080`）

### 1.1 已具备、直接复用（不重写）

| 能力 | 已有模型 / Service / 接口 | 说明 |
|---|---|---|
| 用户与登录 | `model.User` / `AuthSession` / `UserIdentity` / `EmailVerificationCode`；`service/auth.go`（Register/Login/CurrentUser）；`/api/auth/register`、`/login`、`/session`、Linux.do OAuth | 用户名密码、邮箱验证码、第三方登录、会话 cookie（`SessionCookieName`） |
| 积分账户 | `model.CreditAccount`（可用/冻结微积分 + 乐观锁 `Version`） | 每用户一个钱包 |
| 积分流水 | `model.CreditLedgerEntry`（类型、增量、变更后余额、幂等 `ReferenceKey`、关联订单/支付/兑换码、模型/渠道/场景维度） | 不可变明细账，账单数据源 |
| 两阶段计费 | `service/finance.go`：`ReserveProxyBilling(WithBody)`（冻结，积分不足报 `ErrInsufficientCredits`，幂等）、`SettleBilling`、`RefundBilling`、`MarkBillingRunning/Uncertain`；`model.BillingOrder`（reserved→settled/refunded） | 正是 MCP 调用「预检冻结→成功结算/失败退款」所需 |
| 兑换码 | `RedeemBatch` / `RedeemCode`；`/api/wallet/redeem`、签到 `/wallet/checkin` | 冷启动/赠送可直接用 |
| 充值商品 | `model.TopupProduct`（金额分 `AmountFen` → 积分 `CreditsMicrocredits`，服务端定价快照，客户端不能自报金额） | |
| 支付下单/回调 | `model.PaymentOrder`（状态机 created/pending/closing/closed/credited）、`PaymentNotification`（验签收件箱 + 异步入账 + 重试）；`/api/payments/orders`、`/checkout`、`/notify/:providerId/:configId`、`/return/:providerId` | |
| 支付渠道插件 | `payment-plugins/`、`cmd/payment-alipay`、`cmd/payment-wechat`、`official-payment-*` 插件；`PaymentProviderConfig`（不可变 + 版本化，密钥轮换不影响老回调验签） | 支付宝/微信已具备插件化能力 |
| 对账 | `PaymentReconciliationRun/Item`（每日对账：matched/recovered/金额或单号不符/入账失败） | |
| 定价 | `model.ModelPricing`（输入/输出/缓存 token、按请求/按媒体/按视频秒多维定价）；`credit_policy.go`（注册赠送、签到、倍率） | |
| 用户钱包查询 | `GET /api/wallet`（余额 + 分页流水） | 前端控制台可直接用 |

### 1.2 记账单位（必须统一）

- 后端以 **微积分 Microcredits** 整数记账：`CreditScale = 1_000_000`，即 1 积分 = 10^6 微积分，杜绝浮点误差。
- 人民币以 **分 AmountFen** 记账；充值商品决定「花多少分 → 得多少微积分」。
- Node 网关当前 P2 定价表 `gateway-pricing.json` 以 **人民币元** 计（default 0.01、video_generation 1.5）。
  接入后需统一到「积分（微积分）」为唯一扣费单位（见 §4）。

### 1.3 目前缺的（本方案要新增，量不大）

1. **后端给网关用的内部接口组** `/api/internal/...`（服务令牌 `X-Internal-Token` 校验，不经用户登录态）。
2. **开发者密钥模型 `DeveloperApiKey`**：网站用户自助生成的长期密钥（`ak_` 的网站化归宿），用于 CI / `client_credentials` 等非交互场景；交互式 `mcp login` 不需要它。
3. **OAuth 授权页对接网站登录态**：`/authorize` 从「粘贴 ak_」升级为「网站登录 + 授权确认 + 授权可撤销」。
4. **工具级定价到积分的映射**：MCP 的 50 个工具（canvas_*/channel_*/…）需要一条「工具 → 积分单价」规则（可复用 ModelPricing 思路，或网关侧定价表改为积分）。

---

## 2. 目标架构与数据流

```
客户 Codex
  │  ① codex mcp login yingce → 浏览器打开 /authorize
  ▼
Cloudflare 隧道 yingce.cc.cd
  │
  ├─ /authorize（未登录先跳网站登录）──── 网站账号会话(cookie) ──→ Go 后端 :8080（用户/同意）
  │        同意后发 OAuth 授权码 → token（access 绑定 userId）
  │
  └─ /mcp（每次工具调用，Bearer access_token）
          │
          ▼
   Node MCP 网关 :17801（gateway-server，AccountProvider=remote）
          │  ② 校验 token → userId
          │  ③ preCheck/reserve：X-Internal-Token 调 Go 内部接口冻结积分
          │  ④ 执行画布/渠道工具（broker→runtime）
          │  ⑤ 成功 settle（冻结转实扣） / 失败 refund（解冻退回）
          ▼
   Go 后端 :8080（CreditAccount / BillingOrder / CreditLedgerEntry，全部复用）
          ▲
          │ 充值：/api/payments/orders → 支付宝/微信插件 → 回调验签 → 异步入账积分
   客户在网站控制台（/api/wallet、充值商品、流水、账单、授权管理）
```

要点：**网关不持有任何余额真相**，只做协议、鉴权编排与计量；钱和账都在 Go 后端，单一真相源。

---

## 3. 内部接口契约（Go 后端新增，供 RemoteAccountProvider 调用）

全部挂在 `/api/internal`，中间件统一校验 `X-Internal-Token == CANVAS_ACCOUNT_ADMIN_TOKEN`（仅内网/隧道可达，长随机串，不入日志）。与任务 2 `RemoteAccountProvider` 已写好的调用路径一一对齐：

| RemoteAccountProvider 方法 | 内部接口 | 后端复用 | 返回 |
|---|---|---|---|
| `authenticateByKey(ak)` | `POST /internal/auth/api-key` {apiKey} | 新 `DeveloperApiKey`（哈希比对→userId→User 启用/配额） | 200 `{principal:{subjectId,displayName,enabled,balanceCredits}}` / 401 / 429 |
| `authenticateClient(cid,secret)` | `POST /internal/auth/client` | 同上（机密客户端） | 同上 |
| `resolveSubject(userId)` | `GET /internal/accounts/:userId` | User + CreditAccount | principal（含可用积分） |
| `preCheck` + 冻结 | `POST /internal/accounts/:userId/reserve` {tool, scene, reqId, quantity, estimatedMicrocredits} | `ReserveProxyBillingWithBody`（幂等 reqId，积分不足→402） | `{orderId, status:reserved}` / 402 |
| `charge`（成功结算） | `POST /internal/billing/:orderId/settle` {reqId, actualMicrocredits?} | `SettleBilling` | `{ok, balanceCredits}` |
| 失败退款 | `POST /internal/billing/:orderId/refund` {reason} | `RefundBilling` | 204 |
| `recordCall`（统计） | `POST /internal/accounts/:userId/record` | 写 usage/日志（可选） | 204 |

> 任务 2 的 `RemoteAccountProvider` 当前骨架里 preCheck/charge 是两次调用；落地时建议把
> preCheck 升级为 **reserve（返回 orderId）**、charge 改为 **settle by orderId**，以复用后端两阶段计费、
> 避免并发超额。这需要给 `AccountProvider` 接口加一个 `reserve→settle/refund` 的可选阶段（向后兼容：
> local 实现内部直接走余额，行为不变）。

---

## 4. 计费与定价映射

1. **唯一记账单位 = 微积分**。网关侧定价表 `gateway-pricing.json` 增加 `credits`（或改为后端统一下发）：
   - 例：`canvas_get_context = 1 积分`、`video_generation = 150 积分`，内部乘 `CreditScale`。
2. **一次 MCP 工具调用 = 一条 BillingOrder**：
   - `scene = "mcp:<tool>"`，`channelID/model/capability` 对 channel_* 工具填真实渠道模型，canvas_* 填工具名；
   - `idempotencyKey = 网关生成的 reqId`（重试不重复扣费，对应 `CreditLedgerEntry.ReferenceKey` 唯一索引）。
3. **定价快照不可变**：命中的单价/倍率随订单固化（后端 BillingOrder 已有 PriceTier/快照字段），事后改价不影响在途订单。
4. **渠道模型类工具（channel_generate/video）**：优先用后端 `ModelPricing` 的按秒/按媒体/按 token 规则结算；
   纯画布操作类工具用「按次固定积分」。
5. **失败不扣**：工具报错走 refund（与现在 P2「成功才扣费」语义一致，但用冻结/解冻实现，并发更安全）。

---

## 5. OAuth 授权页对接网站登录（替代粘贴 ak_）

- `mcp login` 打开 `https://yingce.cc.cd/authorize?client_id=...&PKCE...`（标准参数不变）。
- `/authorize` 处理顺序：
  1. 网关用内部接口携带请求头里的网站会话 cookie 问后端「当前登录用户是谁」；未登录 → 302 到网站登录页，登录后回跳 `/authorize` 且保留全部 OAuth 参数；
  2. 已登录 → 渲染「应用 Codex 申请访问你的影策账户（scope: mcp:tools）」确认页（页面由网站前端风格承载）；
  3. 用户点同意 → 网关内部记录一条 **授权同意（consent）** → 发授权码 → token 绑定 `userId`。
- **授权管理/撤销**：网站控制台列出「已授权应用」，可撤销；撤销后删除 refresh_token、access 到期不再续。
  （可新增轻量表 `OAuthConsent(userId, clientId, scopes, grantedAt, revokedAt)`，或复用 `OAuthState` 扩展。）
- `DeveloperApiKey`（ak_）退居为**可选**：只服务于无人值守的 CI / `client_credentials`，由用户在控制台自助生成、可吊销，同样绑定 userId、走同一套积分。

---

## 6. 分阶段落地

### P0 — 打通「网站积分」最小闭环（先跑通，不追求页面美观）
- [ ] 后端新增 `/api/internal` 中间件（X-Internal-Token）+ auth/api-key 之外的账户/计费接口（reserve/settle/refund/resolve）。
- [ ] `AccountProvider` 增加 reserve→settle/refund 阶段；`RemoteAccountProvider` 对齐内部接口与微积分单位。
- [ ] 网关定价表改为积分；`CANVAS_ACCOUNT_PROVIDER=remote` + BASE_URL/ADMIN_TOKEN 接入。
- [ ] 选一个**已存在的网站用户**，用其 userId 端到端：MCP 调用 → 冻结 → 结算，`/api/wallet` 看到流水与余额变化；失败路径退款正确。
- [ ] 验收：并发调用不会扣成负数（乐观锁/冻结生效）；重复 reqId 不重复扣费；切回 `local` 内测链路仍可用。
- 备注：P0 阶段 `/authorize` 可先用「内部接口用 userId 换发」或开发者 key 过渡，不阻塞计费闭环验证。

### P1 — 标准用户体验（对外可售卖）
- [ ] `/authorize` 接网站登录态 + 授权确认页 + consent 记录与撤销。
- [ ] `DeveloperApiKey` 模型 + 控制台自助生成/吊销（client_credentials/CI）。
- [ ] 客户控制台：钱包余额、积分流水、账单（复用 `/api/wallet`、BillingOrder 查询），充值入口接支付。
- [ ] 配置 TopupProduct 充值档位；上线支付宝/微信支付插件、跑通「下单→支付→回调验签→积分到账」。
- [ ] 验收：全新用户自助完成 注册→登录→mcp 授权→（充值）→调用扣积分→控制台看账单，全程无需运营手工操作。

### P2 — 商业化加固
- [ ] 支付对账定时任务（ReconciliationRun）、关单/补单、异常告警。
- [ ] 工具/模型定价后台（含倍率、活动价、新用户赠送/签到策略 credit_policy）。
- [ ] 速率与风控：按用户/工具限流、异常消耗预警、封禁；发票/套餐订阅（如需要）。
- [ ] 可观测：网关侧用量与后端 BillingOrder/Ledger 每日对账，差异告警；多副本下内部接口鉴权与网络加固（mTLS 或限隧道可达）。
- [ ] 灰度：保留 `CANVAS_ACCOUNT_PROVIDER=local|remote` 开关，内测客户走 local、商用客户走 remote，平滑迁移后下线本地人民币账本。

---

## 7. 关键决策与风险

- **单一真相源**：余额/积分只认 Go 后端；网关不缓存余额、不落地账本（本地 KeyStore 仅内测）。
- **幂等**：所有扣费以 reqId/orderId 幂等，网络重试绝不重复扣费（后端唯一索引已保障）。
- **fail-closed**：RemoteAccountProvider 认证失败/账户服务不可用时拒绝调用（任务 2 已如此实现），但**预检抖动不阻断、由结算兜底**，避免误伤。
- **单位统一风险**：人民币元/分与微积分混用最易出账错，P0 必须先定死换算并加单测。
- **隧道与内部接口安全**：`/api/internal` 只接受正确 X-Internal-Token，且建议仅经隧道/内网暴露，不对公网匿名开放；Cloudflare 规则需避免把 internal 路由暴露为匿名可访问。
- **上游合并友好**：所有新增走独立文件（internal handler/新模型、canvas-agent 的 account-provider），不改上游既有计费主流程，降低与 ddcat-ai 上游冲突的面。
