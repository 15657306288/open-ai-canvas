/**
 * AccountProvider —— 网关的「账户来源」抽象层（商业化铺路）。
 *
 * 网关（gateway-server / gateway-oauth）只依赖本接口，不直接耦合本地 KeyStore。
 * 账户真相源可平滑切换：
 *   - local（默认，内测）：本地 JSON KeyStore（gateway-keys.ts），余额/配额在本机文件；
 *   - remote（商业化）：网站后端账户/钱包服务，客户在网站注册充值，网关经内部 HTTP 实时鉴权/查余额/扣费。
 *
 * 切换：环境变量 CANVAS_ACCOUNT_PROVIDER=local|remote（默认 local）。
 * remote 还需 CANVAS_ACCOUNT_BASE_URL（内部接口基址）与 CANVAS_ACCOUNT_ADMIN_TOKEN（内部服务令牌）。
 * 所有方法统一异步，便于从本地文件实现平滑切到远程 HTTP 实现，调用方无感知。
 */
import { KeyStore, type ApiKey } from "./gateway-keys.js";

// ---------------- 领域类型 ----------------

/** 认证后的调用主体。本地 subjectId=keyId；远程 subjectId=网站 userId/accountId。 */
export interface Principal {
    subjectId: string;
    displayName: string;
    enabled: boolean;
    /** 账户余额（CNY）；undefined 表示不启用余额控制（仅按配额）。 */
    balance?: number;
}

export type AuthDenyReason = "not_found" | "disabled" | "quota" | "bad_secret";

export type AuthOutcome =
    | { ok: true; principal: Principal }
    | { ok: false; reason: AuthDenyReason; status: 401 | 429 };

export type PreCheckOutcome =
    | { allow: true }
    | { allow: false; code: "insufficient_balance"; balance: number; need: number };

export interface ChargeOutcome {
    ok: boolean;
    /** 扣费后余额；未启用余额控制时为 undefined。 */
    balance?: number;
}

/** 网关需要账户体系提供的全部能力（统一异步）。 */
export interface AccountProvider {
    readonly kind: "local" | "remote";
    authenticateByKey(plainKey: string): Promise<AuthOutcome>;
    authenticateClient(clientId: string, clientSecret: string): Promise<AuthOutcome>;
    resolveSubject(subjectId: string): Promise<Principal | undefined>;
    preCheck(subjectId: string, cost: number): Promise<PreCheckOutcome>;
    charge(subjectId: string, cost: number): Promise<ChargeOutcome>;
    recordCall(subjectId: string, toolName: string): Promise<void>;
}

function toPrincipal(k: ApiKey): Principal {
    return { subjectId: k.id, displayName: k.name, enabled: k.enabled, balance: k.balance };
}

/** reason → HTTP 状态（仅 quota=429，其余 401，与历史行为一致）。 */
function denyStatus(reason: AuthDenyReason): 401 | 429 {
    return reason === "quota" ? 429 : 401;
}

// ---------------- 本地实现（包装现有 KeyStore，不改其内部） ----------------

export class LocalAccountProvider implements AccountProvider {
    readonly kind = "local" as const;
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

    async preCheck(subjectId: string, cost: number): Promise<PreCheckOutcome> {
        const k = this.keyStore.get(subjectId);
        if (!k || k.balance === undefined || k.balance >= cost) return { allow: true };
        return { allow: false, code: "insufficient_balance", balance: k.balance, need: cost };
    }

    async charge(subjectId: string, cost: number): Promise<ChargeOutcome> {
        return this.keyStore.deduct(subjectId, cost);
    }

    async recordCall(subjectId: string, toolName: string): Promise<void> {
        this.keyStore.recordUsage(subjectId, toolName);
    }
}

// ---------------- 远程实现（网站后端账户/钱包服务，契约骨架） ----------------

export interface RemoteAccountOptions {
    /** 网站后端内部接口基址，例如 https://yingce.cc.cd/api/internal */
    baseUrl: string;
    /** 网关↔网站后端内部服务令牌（对外不可见），经 X-Internal-Token 传递。 */
    adminToken: string;
    timeoutMs?: number;
}

/**
 * 网站后端需实现的内部 HTTP 契约（任务3 落地；JSON；必须校验 X-Internal-Token）：
 *   POST /auth/api-key           {apiKey}           → 200 {principal} / 401|429 {reason}
 *   POST /auth/client            {clientId,secret}  → 200 {principal} / 401 {reason}
 *   GET  /accounts/:subjectId                       → 200 Principal / 404
 *   POST /accounts/:id/precheck  {cost}             → {allow:true} | {allow:false,code,balance,need}
 *   POST /accounts/:id/charge    {cost,tool,reqId}  → {ok,balance}（钱包原子扣费，按 reqId 幂等）
 *   POST /accounts/:id/record    {tool,ok}          → 204（配额/统计计数）
 */
export class RemoteAccountProvider implements AccountProvider {
    readonly kind = "remote" as const;
    private readonly base: string;
    private readonly adminToken: string;
    private readonly timeoutMs: number;

    constructor(opts: RemoteAccountOptions) {
        this.base = opts.baseUrl.replace(/\/+$/, "");
        this.adminToken = opts.adminToken;
        this.timeoutMs = opts.timeoutMs ?? 5000;
    }

    private async call<T>(path: string, init?: { method?: string; body?: unknown }): Promise<{ status: number; json: T }> {
        const ctrl = new AbortController();
        const timer = setTimeout(() => ctrl.abort(), this.timeoutMs);
        try {
            const r = await fetch(`${this.base}${path}`, {
                method: init?.method ?? "GET",
                headers: { "content-type": "application/json", "x-internal-token": this.adminToken },
                body: init?.body !== undefined ? JSON.stringify(init.body) : undefined,
                signal: ctrl.signal,
            });
            const json = (await r.json().catch(() => ({}))) as T;
            return { status: r.status, json };
        } finally {
            clearTimeout(timer);
        }
    }

    private async auth(path: string, body: unknown): Promise<AuthOutcome> {
        try {
            const r = await this.call<{ principal?: Principal; reason?: AuthDenyReason }>(path, { method: "POST", body });
            if (r.status === 200 && r.json.principal) return { ok: true, principal: r.json.principal };
            const reason = r.json.reason ?? "not_found";
            return { ok: false, reason, status: denyStatus(reason) };
        } catch (e) {
            // 账户服务不可用时 fail-closed，避免未授权放行
            console.error(`[account-remote] 认证不可用（fail-closed）: ${e instanceof Error ? e.message : String(e)}`);
            return { ok: false, reason: "not_found", status: 401 };
        }
    }

    authenticateByKey(plainKey: string): Promise<AuthOutcome> { return this.auth("/auth/api-key", { apiKey: plainKey }); }
    authenticateClient(clientId: string, clientSecret: string): Promise<AuthOutcome> {
        return this.auth("/auth/client", { clientId, secret: clientSecret });
    }

    async resolveSubject(subjectId: string): Promise<Principal | undefined> {
        try {
            const r = await this.call<Principal>(`/accounts/${encodeURIComponent(subjectId)}`);
            return r.status === 200 ? r.json : undefined;
        } catch { return undefined; }
    }

    async preCheck(subjectId: string, cost: number): Promise<PreCheckOutcome> {
        try {
            const r = await this.call<PreCheckOutcome>(`/accounts/${encodeURIComponent(subjectId)}/precheck`, { method: "POST", body: { cost } });
            return r.status === 200 && r.json ? r.json : { allow: true };
        } catch { return { allow: true }; } // 预检服务抖动时不阻断，交由 charge 兜底
    }

    async charge(subjectId: string, cost: number): Promise<ChargeOutcome> {
        const reqId = `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
        try {
            const r = await this.call<ChargeOutcome>(`/accounts/${encodeURIComponent(subjectId)}/charge`, { method: "POST", body: { cost, reqId } });
            return r.status === 200 && r.json ? r.json : { ok: false };
        } catch (e) {
            console.error(`[account-remote] 扣费失败: ${e instanceof Error ? e.message : String(e)}`);
            return { ok: false };
        }
    }

    async recordCall(subjectId: string, toolName: string): Promise<void> {
        try {
            await this.call(`/accounts/${encodeURIComponent(subjectId)}/record`, { method: "POST", body: { tool: toolName } });
        } catch { /* 计数失败不阻断主流程 */ }
    }
}

// ---------------- 工厂 ----------------

export function createAccountProvider(existing?: KeyStore): AccountProvider {
    const kind = (process.env.CANVAS_ACCOUNT_PROVIDER ?? "local").toLowerCase();
    if (kind === "remote") {
        const baseUrl = process.env.CANVAS_ACCOUNT_BASE_URL;
        const adminToken = process.env.CANVAS_ACCOUNT_ADMIN_TOKEN ?? "";
        if (!baseUrl) throw new Error("CANVAS_ACCOUNT_PROVIDER=remote 时必须配置 CANVAS_ACCOUNT_BASE_URL（网站后端内部接口基址）");
        return new RemoteAccountProvider({ baseUrl, adminToken });
    }
    return new LocalAccountProvider(existing ?? new KeyStore());
}
