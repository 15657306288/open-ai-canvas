// [connector] 标准 MCP OAuth 2.1 授权服务器（P3+）
//
// 让 Codex / Cursor / Claude 等标准 MCP 客户端通过 `mcp login` 一键授权，
// 而不是手工复制 Bearer Key。实现：
//   - RFC 9728  Protected Resource Metadata（/.well-known/oauth-protected-resource）
//   - RFC 8414  Authorization Server Metadata（/.well-known/oauth-authorization-server）
//   - RFC 7591  动态客户端注册（POST /register，公共客户端 + PKCE）
//   - OAuth 2.1 授权码 + PKCE（GET/POST /authorize）
//   - token 端点（POST /token：authorization_code / refresh_token / client_credentials）
//
// 授权身份：授权页让用户粘贴一个已颁发的客户 API Key（ak_…，P1 KeyStore），
// OAuth access_token 最终绑定到该 Key，从而复用 P1 配额与 P2 计费。
//
// 存储：动态注册的 client 与 refresh_token 持久化（重启不丢登录态）；
// authorization code 与 access_token 仅内存（短时效，可用 refresh 续期）。

import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import type { IncomingMessage, ServerResponse } from "node:http";
import type { AccountProvider } from "./account-provider.js";

const SCOPE = "mcp:tools";
const ACCESS_TTL_MS = Number(process.env.CANVAS_OAUTH_ACCESS_TTL ?? 3600_000); // 1h
const REFRESH_TTL_MS = Number(process.env.CANVAS_OAUTH_REFRESH_TTL ?? 30 * 24 * 3600_000); // 30d
const CODE_TTL_MS = 120_000; // 2min

interface RegisteredClient {
    client_id: string;
    client_name?: string;
    redirect_uris: string[];
    grant_types: string[];
    response_types: string[];
    token_endpoint_auth_method: string;
    scope?: string;
    created_at: string;
}

interface RefreshRecord { clientId: string; keyId: string; scope: string; exp: number; }
interface CodeRecord { clientId: string; redirectUri: string; codeChallenge: string; keyId: string; scope: string; exp: number; }
interface AccessRecord { keyId: string; clientId: string; scope: string; exp: number; }

interface PersistShape {
    clients: Record<string, RegisteredClient>;
    refresh: Record<string, RefreshRecord>;
}

function randomToken(prefix: string, bytes: number): string {
    return `${prefix}${crypto.randomBytes(bytes).toString("hex")}`;
}

function pkceS256(verifier: string): string {
    return crypto.createHash("sha256").update(verifier).digest("base64url");
}

export class OAuthManager {
    private readonly accounts: AccountProvider;
    readonly publicBaseUrl: string;
    private readonly storeFile: string;
    private clients = new Map<string, RegisteredClient>();
    private refreshTokens = new Map<string, RefreshRecord>();
    private readonly codes = new Map<string, CodeRecord>();
    private readonly accessTokens = new Map<string, AccessRecord>();

    constructor(opts: { accounts: AccountProvider; publicBaseUrl: string; storeFile: string }) {
        this.accounts = opts.accounts;
        this.publicBaseUrl = opts.publicBaseUrl.replace(/\/+$/, "");
        this.storeFile = opts.storeFile;
        this.load();
    }

    // ---------- 持久化 ----------
    private load(): void {
        try {
            const raw = JSON.parse(fs.readFileSync(this.storeFile, "utf8")) as PersistShape;
            this.clients = new Map(Object.entries(raw.clients ?? {}));
            const now = Date.now();
            this.refreshTokens = new Map(
                Object.entries(raw.refresh ?? {}).filter(([, r]) => r.exp > now),
            );
        } catch {
            this.clients = new Map();
            this.refreshTokens = new Map();
        }
    }

    private persist(): void {
        try {
            fs.mkdirSync(path.dirname(this.storeFile), { recursive: true });
            const data: PersistShape = {
                clients: Object.fromEntries(this.clients),
                refresh: Object.fromEntries(this.refreshTokens),
            };
            const tmp = `${this.storeFile}.tmp`;
            fs.writeFileSync(tmp, JSON.stringify(data, null, 2), { mode: 0o600 });
            fs.renameSync(tmp, this.storeFile);
        } catch {
            // 持久化失败不阻断内存流程
        }
    }

    // ---------- metadata ----------
    private asMetadata() {
        const b = this.publicBaseUrl;
        return {
            resource: `${b}/mcp`,
            authorization_servers: [b],
            scopes_supported: [SCOPE],
            bearer_methods_supported: ["header"],
            resource_documentation: `${b}/`,
        };
    }

    private serverMetadata() {
        const b = this.publicBaseUrl;
        return {
            issuer: b,
            authorization_endpoint: `${b}/authorize`,
            token_endpoint: `${b}/token`,
            registration_endpoint: `${b}/register`,
            jwks_uri: undefined as string | undefined,
            response_types_supported: ["code"],
            grant_types_supported: ["authorization_code", "refresh_token", "client_credentials"],
            code_challenge_methods_supported: ["S256"],
            scopes_supported: [SCOPE],
            token_endpoint_auth_methods_supported: ["none", "client_secret_post"],
        };
    }

    /** access_token 校验（供网关 authenticate 使用） */
    verifyAccessToken(token: string): { keyId: string } | undefined {
        const rec = this.accessTokens.get(token);
        if (!rec) return undefined;
        if (Date.now() > rec.exp) { this.accessTokens.delete(token); return undefined; }
        return { keyId: rec.keyId };
    }

    private issueAccess(clientId: string, keyId: string, scope: string) {
        const token = randomToken("at_", 24);
        this.accessTokens.set(token, { clientId, keyId, scope, exp: Date.now() + ACCESS_TTL_MS });
        return { access_token: token, expires_in: Math.floor(ACCESS_TTL_MS / 1000), scope };
    }

    // ---------- 主路由：返回 true 表示已接管响应 ----------
    async handle(req: IncomingMessage, res: ServerResponse): Promise<boolean> {
        const url = new URL(req.url ?? "/", "http://localhost");
        const p = url.pathname;
        const method = (req.method ?? "GET").toUpperCase();

        if (p.endsWith("/.well-known/oauth-protected-resource") || p === "/.well-known/oauth-protected-resource") {
            this.json(res, 200, this.asMetadata()); return true;
        }
        if (p === "/.well-known/oauth-authorization-server") {
            this.json(res, 200, this.serverMetadata()); return true;
        }
        if (p === "/register" && method === "POST") return this.handleRegister(req, res);
        if (p === "/authorize") return method === "POST" ? this.handleAuthorizeSubmit(req, res) : this.handleAuthorizePage(req, res);
        if ((p === "/token" || p === "/auth/token") && method === "POST") return this.handleToken(req, res);
        return false;
    }

    private json(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}): void {
        res.statusCode = status;
        res.setHeader("content-type", "application/json; charset=utf-8");
        for (const [k, v] of Object.entries(headers)) res.setHeader(k, v);
        res.end(JSON.stringify(body));
    }

    // ---------- RFC 7591 动态注册 ----------
    private async handleRegister(req: IncomingMessage, res: ServerResponse): Promise<boolean> {
        const body = (await readBody(req)) as Record<string, unknown>;
        const clientId = randomToken("ocli_", 12);
        const client: RegisteredClient = {
            client_id: clientId,
            client_name: String(body.client_name ?? "MCP Client"),
            redirect_uris: Array.isArray(body.redirect_uris) ? (body.redirect_uris as string[]) : [],
            grant_types: Array.isArray(body.grant_types) ? (body.grant_types as string[]) : ["authorization_code", "refresh_token"],
            response_types: ["code"],
            token_endpoint_auth_method: String(body.token_endpoint_auth_method ?? "none"),
            scope: SCOPE,
            created_at: new Date().toISOString(),
        };
        this.clients.set(clientId, client);
        this.persist();
        this.json(res, 201, {
            client_id: clientId,
            client_id_issued_at: Math.floor(Date.now() / 1000),
            client_name: client.client_name,
            redirect_uris: client.redirect_uris,
            grant_types: client.grant_types,
            response_types: client.response_types,
            token_endpoint_auth_method: client.token_endpoint_auth_method,
            scope: SCOPE,
        });
        return true;
    }

    // ---------- /authorize GET：返回授权确认页 ----------
    private async handleAuthorizePage(req: IncomingMessage, res: ServerResponse): Promise<boolean> {
        const url = new URL(req.url ?? "/", "http://localhost");
        const q = url.searchParams;
        const clientId = q.get("client_id") ?? "";
        const client = this.clients.get(clientId);
        const redirectUri = q.get("redirect_uri") ?? "";
        const state = q.get("state") ?? "";
        const codeChallenge = q.get("code_challenge") ?? "";
        const codeChallengeMethod = q.get("code_challenge_method") ?? "S256";
        const scope = q.get("scope") ?? SCOPE;

        const fail = (desc: string) => {
            res.statusCode = 400;
            res.setHeader("content-type", "text/html; charset=utf-8");
            res.end(renderPage("授权请求无效", `<p style="color:#b91c1c">${escapeHtml(desc)}</p>`));
        };
        if (!client) { fail("未知的 client_id，请在客户端重新发起登录。"); return true; }
        if (!redirectUri || !client.redirect_uris.includes(redirectUri)) { fail("redirect_uri 与注册信息不匹配。"); return true; }
        if (!codeChallenge || codeChallengeMethod !== "S256") { fail("必须使用 PKCE（S256）。"); return true; }

        // 把原始授权参数回传，提交时原样带回
        const params = new URLSearchParams({
            client_id: clientId, redirect_uri: redirectUri, state,
            code_challenge: codeChallenge, code_challenge_method: codeChallengeMethod, scope,
        });
        const body = `
          <p>应用 <b>${escapeHtml(client.client_name ?? clientId)}</b> 请求访问你的影策画布（权限：${escapeHtml(scope)}）。</p>
          <p style="font-size:13px;color:#6b7280">粘贴一个已颁发的客户 API Key（ak_…）作为本次授权身份；授权后客户端无需保存该 Key。</p>
          <form method="post" action="/authorize" style="margin-top:12px">
            <input type="hidden" name="params" value='${escapeHtml(params.toString())}'>
            <input name="api_key" placeholder="ak_xxxxxxxx" autocomplete="off" required
              style="width:100%;padding:10px;border:1px solid #d1d5db;border-radius:8px;font-family:monospace;box-sizing:border-box">
            <div style="display:flex;gap:10px;margin-top:14px">
              <button type="submit" style="flex:1;padding:10px;border:0;border-radius:8px;background:#2563eb;color:#fff;font-size:14px;cursor:pointer">同意授权</button>
            </div>
          </form>`;
        res.statusCode = 200;
        res.setHeader("content-type", "text/html; charset=utf-8");
        res.end(renderPage("影策画布 · 授权", body));
        return true;
    }

    // ---------- /authorize POST：校验 Key，发 code，302 回客户端 ----------
    private async handleAuthorizeSubmit(req: IncomingMessage, res: ServerResponse): Promise<boolean> {
        const form = await readForm(req);
        const params = new URLSearchParams(String(form.params ?? ""));
        const clientId = params.get("client_id") ?? "";
        const redirectUri = params.get("redirect_uri") ?? "";
        const state = params.get("state") ?? "";
        const codeChallenge = params.get("code_challenge") ?? "";
        const scope = params.get("scope") ?? SCOPE;
        const apiKey = String(form.api_key ?? "").trim();
        const client = this.clients.get(clientId);

        const redirectBack = (extra: Record<string, string>) => {
            if (!client || !redirectUri) {
                res.statusCode = 400; res.setHeader("content-type", "text/plain; charset=utf-8");
                res.end("invalid authorize request"); return;
            }
            const u = new URL(redirectUri);
            for (const [k, v] of Object.entries(extra)) u.searchParams.set(k, v);
            if (state) u.searchParams.set("state", state);
            res.statusCode = 302; res.setHeader("location", u.toString()); res.end();
        };

        if (!client || !client.redirect_uris.includes(redirectUri)) {
            res.statusCode = 400; res.setHeader("content-type", "text/plain; charset=utf-8");
            res.end("invalid client or redirect_uri"); return true;
        }
        const v = await this.accounts.authenticateByKey(apiKey);
        if (!v.ok) {
            const u = new URL("/authorize", "http://localhost");
            params.forEach((val, key) => u.searchParams.set(key, val));
            res.statusCode = 302;
            res.setHeader("location", `${u.pathname}?${u.searchParams.toString()}&error=${encodeURIComponent("Key 无效或已停用，请重试")}`);
            res.end(); return true;
        }
        const subjectId = v.principal.subjectId;
        const code = randomToken("oc_", 24);
        this.codes.set(code, {
            clientId, redirectUri, codeChallenge, keyId: subjectId, scope, exp: Date.now() + CODE_TTL_MS,
        });
        redirectBack({ code });
        return true;
    }

    // ---------- /token ----------
    private async handleToken(req: IncomingMessage, res: ServerResponse): Promise<boolean> {
        const ctype = req.headers["content-type"] ?? "";
        const body = ctype.includes("application/json")
            ? (await readBody(req)) as Record<string, unknown>
            : (await readForm(req)) as Record<string, unknown>;
        const grant = String(body.grant_type ?? "");

        if (grant === "authorization_code") {
            const code = String(body.code ?? "");
            const rec = this.codes.get(code);
            this.codes.delete(code); // 一次性
            if (!rec || Date.now() > rec.exp) return this.tokenError(res, "invalid_grant", "code 无效或已过期"), true;
            if (rec.clientId !== String(body.client_id ?? "")) return this.tokenError(res, "invalid_client", "client_id 不匹配"), true;
            if (rec.redirectUri !== String(body.redirect_uri ?? "")) return this.tokenError(res, "invalid_grant", "redirect_uri 不匹配"), true;
            const verifier = String(body.code_verifier ?? "");
            if (!verifier || pkceS256(verifier) !== rec.codeChallenge) return this.tokenError(res, "invalid_grant", "PKCE 校验失败"), true;

            const access = this.issueAccess(rec.clientId, rec.keyId, rec.scope);
            const refresh = randomToken("rt_", 32);
            this.refreshTokens.set(refresh, { clientId: rec.clientId, keyId: rec.keyId, scope: rec.scope, exp: Date.now() + REFRESH_TTL_MS });
            this.persist();
            this.json(res, 200, { token_type: "Bearer", ...access, refresh_token: refresh });
            return true;
        }

        if (grant === "refresh_token") {
            const rt = String(body.refresh_token ?? "");
            const rec = this.refreshTokens.get(rt);
            if (!rec || Date.now() > rec.exp) return this.tokenError(res, "invalid_grant", "refresh_token 无效或已过期"), true;
            // 旋转 refresh token
            this.refreshTokens.delete(rt);
            const newRt = randomToken("rt_", 32);
            this.refreshTokens.set(newRt, { ...rec, exp: Date.now() + REFRESH_TTL_MS });
            const access = this.issueAccess(rec.clientId, rec.keyId, rec.scope);
            this.persist();
            this.json(res, 200, { token_type: "Bearer", ...access, refresh_token: newRt });
            return true;
        }

        if (grant === "client_credentials") {
            // P3 兼容：client_id=key.id，client_secret=cs_…（哈希校验）
            const cid = String(body.client_id ?? "");
            const secret = String(body.client_secret ?? "");
            const cv = await this.accounts.authenticateClient(cid, secret);
            if (!cv.ok) return this.tokenError(res, "invalid_client", "invalid client credentials", 401), true;
            const subjectId = cv.principal.subjectId;
            const access = this.issueAccess(cid, subjectId, SCOPE);
            this.json(res, 200, { token_type: "Bearer", ...access });
            return true;
        }

        return this.tokenError(res, "unsupported_grant_type", `不支持的 grant_type: ${grant}`, 400), true;
    }

    private tokenError(res: ServerResponse, error: string, description: string, status = 400): void {
        this.json(res, status, { error, error_description: description });
    }
}

// ---------- 工具 ----------
async function readBody(req: IncomingMessage): Promise<unknown> {
    const chunks: Buffer[] = [];
    let size = 0;
    for await (const c of req) {
        const b = Buffer.isBuffer(c) ? c : Buffer.from(c);
        size += b.length;
        if (size > 2 * 1024 * 1024) break;
        chunks.push(b);
    }
    const raw = Buffer.concat(chunks).toString("utf8").trim();
    if (!raw) return {};
    try { return JSON.parse(raw); } catch { return {}; }
}

async function readForm(req: IncomingMessage): Promise<Record<string, string>> {
    const chunks: Buffer[] = [];
    for await (const c of req) chunks.push(Buffer.isBuffer(c) ? c : Buffer.from(c));
    const raw = Buffer.concat(chunks).toString("utf8");
    return Object.fromEntries(new URLSearchParams(raw));
}

function escapeHtml(s: string): string {
    return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c] as string);
}

function renderPage(title: string, inner: string): string {
    return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>${escapeHtml(title)}</title>
<style>
 body{margin:0;background:#f4f3ee;font-family:-apple-system,'PingFang SC',sans-serif;color:#1a1b1c;display:flex;min-height:100vh;align-items:center;justify-content:center;padding:20px;box-sizing:border-box}
 .card{background:#fff;border:1px solid rgba(0,0,0,.08);border-radius:16px;padding:24px;max-width:440px;width:100%;box-sizing:border-box}
 h1{font-size:18px;margin:0 0 12px}
</style></head><body><div class="card"><h1>${escapeHtml(title)}</h1>${inner}</div></body></html>`;
}
