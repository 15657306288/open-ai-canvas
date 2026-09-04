import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import type { IncomingMessage, ServerResponse } from "node:http";
import { OAuthManager } from "../src/bridge/gateway-oauth.js";
import type { AccountProvider, AuthOutcome, Principal } from "../src/bridge/account-provider.js";

// 最小假 AccountProvider：OAuth 流程只用到 authenticateByKey / authenticateClient
function fakeAccount(): AccountProvider {
    const principal: Principal = { subjectId: "k_test1", displayName: "测试客户", enabled: true };
    const ok: AuthOutcome = { ok: true, principal };
    return {
        kind: "local",
        async authenticateByKey(plain: string): Promise<AuthOutcome> {
            return plain === "ak_good" ? ok : { ok: false, reason: "not_found", status: 401 };
        },
        async authenticateClient(_c: string, secret: string): Promise<AuthOutcome> {
            return secret === "cs_good" ? ok : { ok: false, reason: "bad_secret", status: 401 };
        },
        async resolveSubject(): Promise<Principal | undefined> { return principal; },
        async reserve(): Promise<{ ok: true; orderId: string }> { return { ok: true, orderId: "lo_test" }; },
        async settle(): Promise<{ ok: true }> { return { ok: true }; },
        async refund(): Promise<{ ok: true }> { return { ok: true }; },
        async recordCall(): Promise<void> {},
    };
}

function pkce() {
    const verifier = crypto.randomBytes(32).toString("base64url");
    const challenge = crypto.createHash("sha256").update(verifier).digest("base64url");
    return { verifier, challenge };
}

async function startServer() {
    const storeFile = path.join(os.tmpdir(), `oauth-test-${crypto.randomBytes(6).toString("hex")}.json`);
    const oauth = new OAuthManager({ accounts: fakeAccount(), publicBaseUrl: "http://127.0.0.1:0", storeFile });
    const server = http.createServer((req: IncomingMessage, res: ServerResponse) => { void oauth.handle(req, res); });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const port = (server.address() as { port: number }).port;
    const base = `http://127.0.0.1:${port}`;
    return { base, oauth, storeFile, close: () => new Promise<void>((r) => server.close(() => r())) };
}

test("[oauth] discovery metadata（RFC9728/8414）", async () => {
    const s = await startServer();
    try {
        let r = await fetch(`${s.base}/.well-known/oauth-protected-resource`);
        assert.equal(r.status, 200);
        const prm = await r.json() as Record<string, unknown>;
        assert.equal(prm.resource, "http://127.0.0.1:0/mcp");
        r = await fetch(`${s.base}/.well-known/oauth-authorization-server`);
        const asm = await r.json() as Record<string, unknown>;
        assert.deepEqual(asm.code_challenge_methods_supported, ["S256"]);
        assert.ok((asm.grant_types_supported as string[]).includes("authorization_code"));
    } finally { await s.close(); fs.rmSync(s.storeFile, { force: true }); }
});

test("[oauth] 动态注册 → PKCE 授权码 → 换 token → refresh 旋转", async () => {
    const s = await startServer();
    try {
        // register
        let r = await fetch(`${s.base}/register`, {
            method: "POST", headers: { "content-type": "application/json" },
            body: JSON.stringify({ client_name: "T", redirect_uris: ["http://localhost/cb"], token_endpoint_auth_method: "none" }),
        });
        assert.equal(r.status, 201);
        const clientId = (await r.json() as { client_id: string }).client_id;

        const { verifier, challenge } = pkce();
        const params = new URLSearchParams({
            client_id: clientId, redirect_uri: "http://localhost/cb", state: "st",
            code_challenge: challenge, code_challenge_method: "S256", scope: "mcp:tools",
        });

        // GET 授权页
        r = await fetch(`${s.base}/authorize?${new URLSearchParams({ response_type: "code", ...Object.fromEntries(params) })}`, { redirect: "manual" });
        assert.equal(r.status, 200);
        assert.ok((await r.text()).includes("同意授权"));

        // 错误 key：302 但无 code
        r = await fetch(`${s.base}/authorize`, { method: "POST", redirect: "manual",
            headers: { "content-type": "application/x-www-form-urlencoded" },
            body: new URLSearchParams({ params: params.toString(), api_key: "ak_bad" }) });
        let loc = r.headers.get("location")!;
        assert.equal(r.status, 302); assert.ok(!loc.includes("code=")); assert.ok(loc.includes("error="));

        // 正确 key：302 带 code
        r = await fetch(`${s.base}/authorize`, { method: "POST", redirect: "manual",
            headers: { "content-type": "application/x-www-form-urlencoded" },
            body: new URLSearchParams({ params: params.toString(), api_key: "ak_good" }) });
        loc = r.headers.get("location")!;
        const code = new URL(loc, "http://localhost").searchParams.get("code")!;
        assert.ok(code);

        // 错误 verifier 拒绝
        r = await fetch(`${s.base}/token`, { method: "POST", headers: { "content-type": "application/json" },
            body: JSON.stringify({ grant_type: "authorization_code", code: "x", redirect_uri: "http://localhost/cb", client_id: clientId, code_verifier: "nope" }) });
        assert.equal(r.status, 400);

        // 正确换 token
        r = await fetch(`${s.base}/token`, { method: "POST", headers: { "content-type": "application/json" },
            body: JSON.stringify({ grant_type: "authorization_code", code, redirect_uri: "http://localhost/cb", client_id: clientId, code_verifier: verifier }) });
        assert.equal(r.status, 200);
        const tok = await r.json() as { access_token: string; refresh_token: string; expires_in: number };
        assert.ok(tok.access_token.startsWith("at_")); assert.ok(tok.refresh_token.startsWith("rt_"));
        assert.equal(tok.expires_in, 3600);

        // access_token 可被校验，绑定 keyId
        const rec = s.oauth.verifyAccessToken(tok.access_token);
        assert.equal(rec?.keyId, "k_test1");

        // code 一次性：重用拒绝
        r = await fetch(`${s.base}/token`, { method: "POST", headers: { "content-type": "application/json" },
            body: JSON.stringify({ grant_type: "authorization_code", code, redirect_uri: "http://localhost/cb", client_id: clientId, code_verifier: verifier }) });
        assert.equal(r.status, 400);

        // refresh 旋转：新 rt，旧 rt 失效
        r = await fetch(`${s.base}/token`, { method: "POST", headers: { "content-type": "application/json" },
            body: JSON.stringify({ grant_type: "refresh_token", refresh_token: tok.refresh_token }) });
        assert.equal(r.status, 200);
        const tok2 = await r.json() as { refresh_token: string };
        assert.notEqual(tok2.refresh_token, tok.refresh_token);
        r = await fetch(`${s.base}/token`, { method: "POST", headers: { "content-type": "application/json" },
            body: JSON.stringify({ grant_type: "refresh_token", refresh_token: tok.refresh_token }) });
        assert.equal(r.status, 400);
    } finally { await s.close(); fs.rmSync(s.storeFile, { force: true }); }
});

test("[oauth] client_credentials 兼容（P3）", async () => {
    const s = await startServer();
    try {
        const okR = await fetch(`${s.base}/token`, { method: "POST", headers: { "content-type": "application/json" },
            body: JSON.stringify({ grant_type: "client_credentials", client_id: "k_test1", client_secret: "cs_good" }) });
        assert.equal(okR.status, 200);
        assert.ok((await okR.json() as { access_token: string }).access_token.startsWith("at_"));
        const bad = await fetch(`${s.base}/token`, { method: "POST", headers: { "content-type": "application/json" },
            body: JSON.stringify({ grant_type: "client_credentials", client_id: "k_test1", client_secret: "wrong" }) });
        assert.equal(bad.status, 401);
    } finally { await s.close(); fs.rmSync(s.storeFile, { force: true }); }
});
