/**
 * AccountProvider —— 网关的「账户来源」抽象层（P0 商业化积分闭环）。
 *
 * 网关（gateway-server / gateway-oauth）只依赖本接口：
 *   - 认证：authenticateByKey / authenticateClient / resolveSubject（P0 仍由本地 KeyStore 承担）；
 *   - 计费：两阶段 reserve →（执行工具）→ settle / refund，金额一律为正整数 microcredits（微积分）。
 *
 * 账户真相源可平滑切换：
 *   - local（默认，内测）：本地 JSON KeyStore（gateway-keys.ts），reserve 即预扣、refund 加回；
 *   - remote（商业化）：认证仍走本地 KeyStore（P1 才换成网站账号 OAuth），计费经 Go /api/internal
 *     复用网站钱包（冻结→结算/退款），网络/协议错误一律 fail-closed，绝不默认放行。
 *
 * 切换：CANVAS_ACCOUNT_PROVIDER=local|remote（默认 local）。
 * remote 需要：
 *   CANVAS_ACCOUNT_BASE_URL           网站后端内部接口基址，如 https://yingce.cc.cd/api
 *   CANVAS_INTERNAL_SERVICE_TOKEN     网关↔后端共享服务令牌（X-Internal-Token，与 Go 端同名）
 */
import crypto from "node:crypto";
import { KeyStore, type ApiKey } from "./gateway-keys.js";

// ---------------- 领域类型 ----------------

/** 认证后的调用主体。本地 subjectId=keyId；远程计费时 subjectId 即网站 userId。 */
export interface Principal {
    subjectId: string;
    displayName: string;
    enabled: boolean;
    /** 账户余额（整数 microcredits）；undefined 表示不启用余额控制（仅按配额）。 */
    balance?: number;
}

export type AuthDenyReason = "not_found" | "disabled" | "quota" | "bad_secret";

export type AuthOutcome =
    | { ok: true; principal: Principal }
    | { ok: false; reason: AuthDenyReason; status: 401 | 429 };

/** reserve 失败分类：余额不足 / 被服务端拒绝 / 服务不可用（网络、超时、协议错误）。 */
export type ReserveDenyCode = "insufficient_balance" | "rejected" | "unavailable";

export interface ReserveOutcome {
    ok: boolean;
    /** 计费订单号；settle/refund 时回传。local 以 lo_ 开头，remote 由网站后端生成。 */
    orderId?: string;
    /** 实际冻结金额（正整数 microcredits）。remote 由后端定价返回；local 为传入金额。 */
    microcredits?: number;
    code?: ReserveDenyCode;
    /** 可安全展示给调用方的简短原因，不含内部细节。 */
    message?: string;
    httpStatus?: number;
}

export interface TerminalOutcome {
    ok: boolean;
    message?: string;
}

/** 网关需要账户体系提供的全部能力（统一异步）。 */
export interface AccountProvider {
    readonly kind: "local" | "remote";
    authenticateByKey(plainKey: string): Promise<AuthOutcome>;
    authenticateClient(clientId: string, clientSecret: string): Promise<AuthOutcome>;
    resolveSubject(subjectId: string): Promise<Principal | undefined>;
    /** 调用工具前冻结/预扣 amountMicrocredits（正整数）；同一 idempotencyKey 贯穿本次调用。 */
    reserve(subjectId: string, amountMicrocredits: number, idempotencyKey: string, tool: string): Promise<ReserveOutcome>;
    /** 工具成功后结算（幂等）。 */
    settle(orderId: string, idempotencyKey: string): Promise<TerminalOutcome>;
    /** 工具失败后退款/释放冻结（幂等）。 */
    refund(orderId: string, idempotencyKey: string, error: string): Promise<TerminalOutcome>;
    recordCall(subjectId: string, toolName: string): Promise<void>;
}

function toPrincipal(k: ApiKey): Principal {
    return { subjectId: k.id, displayName: k.name, enabled: k.enabled, balance: k.balance };
}

/** reason → HTTP 状态（仅 quota=429，其余 401，与历史行为一致）。 */
function denyStatus(reason: AuthDenyReason): 401 | 429 {
    return reason === "quota" ? 429 : 401;
}

/** 金额必须是正整数 microcredits；浮点/零/负数/NaN 一律拒绝。 */
function validMicrocredits(amount: number): boolean {
    return typeof amount === "number" && Number.isInteger(amount) && amount > 0;
}

// ---------------- 本地实现（包装现有 KeyStore，reserve 预扣 / refund 加回） ----------------

interface LocalReservation {
    orderId: string;
    subjectId: string;
    amount: number;
    tool: string;
    idempotencyKey: string;
    state: "reserved" | "settled" | "refunded";
}

export class LocalAccountProvider implements AccountProvider {
    readonly kind = "local" as const;
    /** orderId → 预留记录 */
    private readonly orders = new Map<string, LocalReservation>();
    /** idempotencyKey → orderId，保证重复 reserve 返回同一订单 */
    private readonly idemIndex = new Map<string, string>();

    constructor(private readonly keyStore: KeyStore) {}

    async authenticateByKey(plainKey: string): Promise<AuthOutcome> {
        const v = this.keyStore.verify(plainKey);
        if (v.ok && v.key) return { ok: true, principal: toPrincipal(v.key) };
        const reason = v.reason ?? "not_found";
        return { ok: false, reason, status: denyStatus(reason) };
    }

    async authenticateClient(clientId: string, clientSecret: string): Promise<AuthOutcome> {
        const v = this.keyStore.verifyClientSecret(clientId, clientSecret);
        if (v.ok && v.key) return { ok: true, principal: toPrincipal(v.key) };
        const reason: AuthDenyReason = v.reason === "bad_secret" ? "bad_secret" : (v.reason ?? "not_found");
        return { ok: false, reason, status: 401 };
    }

    async resolveSubject(subjectId: string): Promise<Principal | undefined> {
        const k = this.keyStore.get(subjectId);
        return k && k.enabled ? toPrincipal(k) : undefined;
    }

    async reserve(subjectId: string, amount: number, idempotencyKey: string, tool: string): Promise<ReserveOutcome> {
        if (!subjectId || !idempotencyKey || !tool) return { ok: false, code: "rejected", message: "计费参数不完整" };
        if (!validMicrocredits(amount)) return { ok: false, code: "rejected", message: "amountMicrocredits 必须是正整数" };

        // 幂等：同一 idempotencyKey 直接回原订单
        const existedId = this.idemIndex.get(idempotencyKey);
        if (existedId) {
            const ex = this.orders.get(existedId);
            if (ex && ex.subjectId === subjectId && ex.amount === amount && ex.tool === tool) {
                return { ok: true, orderId: ex.orderId };
            }
            return { ok: false, code: "rejected", message: "幂等键已用于其他计费请求" };
        }

        const k = this.keyStore.get(subjectId);
        if (!k || !k.enabled) return { ok: false, code: "rejected", message: "账户不可用" };
        if (k.balance !== undefined && k.balance < amount) {
            return { ok: false, code: "insufficient_balance", message: "余额不足", httpStatus: 402 };
        }
        // reserve 即预扣（本地钱包无独立冻结列，预扣=冻结，settle 无需再动账）
        const d = this.keyStore.deduct(subjectId, amount);
        if (!d.ok) return { ok: false, code: "insufficient_balance", message: "余额不足", httpStatus: 402 };

        const orderId = `lo_${crypto.randomBytes(8).toString("hex")}`;
        this.orders.set(orderId, { orderId, subjectId, amount, tool, idempotencyKey, state: "reserved" });
        this.idemIndex.set(idempotencyKey, orderId);
        return { ok: true, orderId };
    }

    async settle(orderId: string, idempotencyKey: string): Promise<TerminalOutcome> {
        const o = this.orders.get(orderId);
        if (!o || o.idempotencyKey !== idempotencyKey) return { ok: false, message: "计费订单不存在或幂等键不匹配" };
        if (o.state === "settled") return { ok: true }; // 幂等
        if (o.state === "refunded") return { ok: false, message: "订单已退款，不能结算" };
        o.state = "settled"; // 钱已在 reserve 预扣，这里仅落终态
        return { ok: true };
    }

    async refund(orderId: string, idempotencyKey: string, _error: string): Promise<TerminalOutcome> {
        const o = this.orders.get(orderId);
        if (!o || o.idempotencyKey !== idempotencyKey) return { ok: false, message: "计费订单不存在或幂等键不匹配" };
        if (o.state === "refunded") return { ok: true }; // 幂等
        if (o.state === "settled") return { ok: false, message: "订单已结算，不能退款" };
        this.keyStore.topup(o.subjectId, o.amount); // 退回预扣
        o.state = "refunded";
        return { ok: true };
    }

    async recordCall(subjectId: string, toolName: string): Promise<void> {
        this.keyStore.recordUsage(subjectId, toolName);
    }
}

// ---------------- 远程实现（认证委托本地 KeyStore；计费走 Go /api/internal） ----------------

export interface RemoteAccountOptions {
    /** 网站后端 /api 基址（内部接口将拼接 /internal/accounts/...），例如 https://yingce.cc.cd/api */
    baseUrl: string;
    /** 网关↔网站后端内部服务令牌（对外不可见），经 X-Internal-Token 传递。 */
    serviceToken: string;
    /** P0 认证仍使用的本地 KeyStore。 */
    keyStore: KeyStore;
    timeoutMs?: number;
}

interface InternalEnvelope<T> {
    code: number;
    data: T | null;
    msg: string;
}

/**
 * Go 端内部计费契约（backend/internal/handler/internal_finance.go，统一 {code,data,msg}）：
 *   GET  /internal/accounts/:userId
 *   POST /internal/accounts/:userId/reservations           {amountMicrocredits,tool,scene,idempotencyKey}
 *   POST /internal/accounts/:userId/reservations/:oid/settle {idempotencyKey}
 *   POST /internal/accounts/:userId/reservations/:oid/refund {idempotencyKey,error}
 * 全部请求需 X-Internal-Token；HTTP 402=余额不足，409=幂等/状态冲突，其余非 2xx 视为失败。
 */
export class RemoteAccountProvider implements AccountProvider {
    readonly kind = "remote" as const;
    private readonly local: LocalAccountProvider;
    private readonly base: string;
    private readonly serviceToken: string;
    private readonly timeoutMs: number;
    /** orderId → 计费主体（网站 userId），供 settle/refund 拼归属路径，保持对外签名不含 userId。 */
    private readonly orderSubject = new Map<string, string>();

    constructor(opts: RemoteAccountOptions) {
        this.local = new LocalAccountProvider(opts.keyStore);
        // 兼容传入 .../api 或 .../api/internal：统一收敛到 /api，再拼 /internal。
        this.base = opts.baseUrl.replace(/\/+$/, "").replace(/\/internal$/, "");
        this.serviceToken = opts.serviceToken;
        this.timeoutMs = opts.timeoutMs ?? 5000;
    }

    // 认证在 P0 仍由本地 KeyStore 承担（网站账号 OAuth 是 P1）。
    authenticateByKey(plainKey: string): Promise<AuthOutcome> { return this.local.authenticateByKey(plainKey); }
    authenticateClient(clientId: string, clientSecret: string): Promise<AuthOutcome> {
        return this.local.authenticateClient(clientId, clientSecret);
    }
    resolveSubject(subjectId: string): Promise<Principal | undefined> { return this.local.resolveSubject(subjectId); }

    /** 发起一次内部请求并解析 envelope；任何网络/超时/协议异常都抛出，由调用方 fail-closed。 */
    private async request<T>(path: string, body: unknown, method = "POST"): Promise<{ status: number; env: InternalEnvelope<T> }> {
        const ctrl = new AbortController();
        const timer = setTimeout(() => ctrl.abort(), this.timeoutMs);
        try {
            const r = await fetch(`${this.base}/internal${path}`, {
                method,
                headers: { "content-type": "application/json", "x-internal-token": this.serviceToken },
                body: body === undefined ? undefined : JSON.stringify(body),
                signal: ctrl.signal,
            });
            const env = (await r.json().catch(() => null)) as InternalEnvelope<T> | null;
            if (!env || typeof env.code !== "number") throw new Error("internal envelope invalid");
            return { status: r.status, env };
        } finally {
            clearTimeout(timer);
        }
    }

    async reserve(subjectId: string, amount: number, idempotencyKey: string, tool: string): Promise<ReserveOutcome> {
        if (!subjectId || !idempotencyKey || !tool) return { ok: false, code: "rejected", message: "计费参数不完整" };
        // 金额语义：remote 下 amount=0 表示由后端按工具定价（连接器不参与定价）；正数仍兼容显式金额。
        if (!Number.isInteger(amount) || amount < 0) return { ok: false, code: "rejected", message: "amountMicrocredits 必须是非负整数" };
        try {
            const r = await this.request<{ orderId?: string; status?: string; amountMicrocredits?: number }>(
                `/accounts/${encodeURIComponent(subjectId)}/reservations`,
                { ...(amount > 0 ? { amountMicrocredits: amount } : {}), tool, scene: "mcp", idempotencyKey },
            );
            if (r.status === 200 && r.env.code === 0 && r.env.data?.orderId) {
                this.orderSubject.set(r.env.data.orderId, subjectId);
                return { ok: true, orderId: r.env.data.orderId, microcredits: r.env.data.amountMicrocredits };
            }
            if (r.status === 402) return { ok: false, code: "insufficient_balance", message: r.env.msg || "积分不足", httpStatus: 402 };
            return { ok: false, code: "rejected", message: r.env.msg || "计费被拒绝", httpStatus: r.status };
        } catch (e) {
            console.error(`[account-remote] reserve 不可用（fail-closed）: ${e instanceof Error ? e.message : String(e)}`);
            return { ok: false, code: "unavailable", message: "计费服务暂时不可用" };
        }
    }

    async settle(orderId: string, idempotencyKey: string): Promise<TerminalOutcome> {
        return this.terminal(orderId, idempotencyKey, {}, "settle");
    }

    async refund(orderId: string, idempotencyKey: string, error: string): Promise<TerminalOutcome> {
        return this.terminal(orderId, idempotencyKey, { error: error.slice(0, 500) }, "refund");
    }

    private async terminal(orderId: string, idempotencyKey: string, extra: Record<string, unknown>, kind: "settle" | "refund"): Promise<TerminalOutcome> {
        if (!orderId || !idempotencyKey) return { ok: false, message: "计费参数不完整" };
        const subjectId = this.orderSubject.get(orderId);
        if (!subjectId) return { ok: false, message: "计费订单归属缺失" };
        try {
            const r = await this.request<unknown>(
                `/accounts/${encodeURIComponent(subjectId)}/reservations/${encodeURIComponent(orderId)}/${kind}`,
                { idempotencyKey, ...extra },
            );
            if (r.status === 200 && r.env.code === 0) return { ok: true };
            return { ok: false, message: r.env.msg || `${kind} 失败` };
        } catch (e) {
            console.error(`[account-remote] ${kind} 失败（fail-closed）: ${e instanceof Error ? e.message : String(e)}`);
            return { ok: false, message: `${kind} 服务不可用` };
        }
    }

    async recordCall(subjectId: string, toolName: string): Promise<void> {
        // P0 计数由 reserve/settle 的账单承担；远程计数抖动不阻断主流程。
        try {
            await this.local.recordCall(subjectId, toolName);
        } catch { /* ignore */ }
    }
}

// ---------------- 工厂 ----------------

export function createAccountProvider(existing?: KeyStore): AccountProvider {
    const kind = (process.env.CANVAS_ACCOUNT_PROVIDER ?? "local").toLowerCase();
    if (kind === "remote") {
        const baseUrl = process.env.CANVAS_ACCOUNT_BASE_URL;
        const serviceToken = process.env.CANVAS_INTERNAL_SERVICE_TOKEN ?? "";
        if (!baseUrl) throw new Error("CANVAS_ACCOUNT_PROVIDER=remote 时必须配置 CANVAS_ACCOUNT_BASE_URL（网站后端 /api 基址）");
        if (!serviceToken) throw new Error("CANVAS_ACCOUNT_PROVIDER=remote 时必须配置 CANVAS_INTERNAL_SERVICE_TOKEN（内部服务令牌）");
        const keyStore = existing ?? new KeyStore();
        return new RemoteAccountProvider({ baseUrl, serviceToken, keyStore });
    }
    return new LocalAccountProvider(existing ?? new KeyStore());
}
