import { test } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import crypto from "node:crypto";
import http from "node:http";
import { KeyStore } from "../src/bridge/gateway-keys.js";
import { LocalAccountProvider, RemoteAccountProvider, createAccountProvider } from "../src/bridge/account-provider.js";

function tmpStore() {
    return path.join(os.tmpdir(), `acct-${crypto.randomBytes(5).toString("hex")}.json`);
}

test("[account] Local 认证/配额/计数与停用解析", async () => {
    const file = tmpStore();
    const ks = new KeyStore(file);
    const created = ks.createKey({ name: "客户甲", dailyCalls: 1, balance: 1_000_000 });
    const plain = created.key;
    const acct = new LocalAccountProvider(ks);

    let a = await acct.authenticateByKey(plain);
    assert.equal(a.ok, true);
    if (a.ok) assert.equal(a.principal.subjectId, created.record.id);

    a = await acct.authenticateByKey("ak_nope");
    assert.equal(a.ok, false);
    if (!a.ok) assert.equal(a.status, 401);

    await acct.recordCall(created.record.id, "channel_list");
    a = await acct.authenticateByKey(plain); // 达日配额 1
    assert.equal(a.ok, false);
    if (!a.ok) assert.equal(a.status, 429);

    assert.ok(await acct.resolveSubject(created.record.id));
    ks.revoke(created.record.id);
    assert.equal(await acct.resolveSubject(created.record.id), undefined);
    fs.rmSync(file, { force: true });
});

test("[account] Local reserve→settle 预扣并幂等", async () => {
    const ks = new KeyStore(tmpStore());
    const created = ks.createKey({ name: "乙", balance: 1_000_000 });
    const id = created.record.id;
    const acct = new LocalAccountProvider(ks);

    const r1 = await acct.reserve(id, 10_000, "idem-1", "canvas_get_context");
    assert.equal(r1.ok, true);
    assert.ok(r1.orderId);
    assert.equal(ks.get(id)?.balance, 990_000); // reserve 即预扣

    // 同幂等键同参数 → 同一订单，不重复扣
    const r1again = await acct.reserve(id, 10_000, "idem-1", "canvas_get_context");
    assert.equal(r1again.orderId, r1.orderId);
    assert.equal(ks.get(id)?.balance, 990_000);

    assert.equal((await acct.settle(r1.orderId!, "idem-1")).ok, true);
    assert.equal((await acct.settle(r1.orderId!, "idem-1")).ok, true); // 幂等
    // settled 后不可退款
    assert.equal((await acct.refund(r1.orderId!, "idem-1", "x")).ok, false);
});

test("[account] Local reserve→refund 退回预扣，且状态转换受限", async () => {
    const ks = new KeyStore(tmpStore());
    const id = ks.createKey({ name: "丙", balance: 1_000_000 }).record.id;
    const acct = new LocalAccountProvider(ks);

    const r = await acct.reserve(id, 30_000, "idem-r", "video_generation");
    assert.equal(ks.get(id)?.balance, 970_000);
    assert.equal((await acct.refund(r.orderId!, "idem-r", "failed")).ok, true);
    assert.equal(ks.get(id)?.balance, 1_000_000); // 全额退回
    assert.equal((await acct.refund(r.orderId!, "idem-r", "again")).ok, true); // 幂等
    assert.equal((await acct.settle(r.orderId!, "idem-r")).ok, false); // refunded 后不可结算
});

test("[account] Local 余额不足/非正整数/幂等冲突全部拒绝", async () => {
    const ks = new KeyStore(tmpStore());
    const id = ks.createKey({ name: "丁", balance: 5_000 }).record.id;
    const acct = new LocalAccountProvider(ks);

    const poor = await acct.reserve(id, 9_999_999, "k1", "t");
    assert.equal(poor.ok, false);
    assert.equal(poor.code, "insufficient_balance");

    for (const bad of [0, -1, 1.5, NaN]) {
        const rr = await acct.reserve(id, bad as number, "k" + bad, "t");
        assert.equal(rr.ok, false, `amount=${bad} 应拒绝`);
    }

    await acct.reserve(id, 1_000, "idem-x", "tool_a");
    const conflict = await acct.reserve(id, 2_000, "idem-x", "tool_a"); // 同键不同金额
    assert.equal(conflict.ok, false);
});

function startInternalServer(handler: (req: http.IncomingMessage, body: string, res: http.ServerResponse) => void): Promise<{ base: string; close: () => void }> {
    return new Promise((resolve) => {
        const server = http.createServer((req, res) => {
            let raw = "";
            req.on("data", (c) => (raw += c));
            req.on("end", () => handler(req, raw, res));
        });
        server.listen(0, "127.0.0.1", () => {
            const addr = server.address();
            const port = typeof addr === "object" && addr ? addr.port : 0;
            resolve({ base: `http://127.0.0.1:${port}/api`, close: () => server.close() });
        });
    });
}

test("[account] Remote 走 /internal envelope：reserve→settle/refund 成功", async () => {
    const seen: string[] = [];
    const srv = await startInternalServer((req, body, res) => {
        seen.push(`${req.method} ${req.url}`);
        assert.equal(req.headers["x-internal-token"], "svc-token");
        res.writeHead(200, { "content-type": "application/json" });
        if (req.url?.endsWith("/settle")) return res.end(JSON.stringify({ code: 0, data: { status: "settled" }, msg: "ok" }));
        if (req.url?.endsWith("/refund")) return res.end(JSON.stringify({ code: 0, data: { status: "refunded" }, msg: "ok" }));
        const parsed = JSON.parse(body);
        assert.equal(parsed.amountMicrocredits, 10_000);
        res.end(JSON.stringify({ code: 0, data: { orderId: "ord_1", status: "reserved", amountMicrocredits: 10_000, idempotencyKey: parsed.idempotencyKey }, msg: "ok" }));
    });
    const ks = new KeyStore(tmpStore());
    const acct = new RemoteAccountProvider({ baseUrl: srv.base, serviceToken: "svc-token", keyStore: ks });

    const r = await acct.reserve("user-1", 10_000, "idem", "canvas_get_context");
    assert.equal(r.ok, true);
    assert.equal(r.orderId, "ord_1");
    assert.equal((await acct.settle("ord_1", "idem")).ok, true);
    const r2 = await acct.reserve("user-1", 10_000, "idem2", "t");
    assert.equal((await acct.refund(r2.orderId!, "idem2", "boom")).ok, true);
    assert.ok(seen.some((l) => l.includes("/internal/accounts/user-1/reservations")));
    srv.close();
});

test("[account] Remote reserve amount=0 走后端定价：请求不含金额、返回实际冻结金额", async () => {
    let requestBody: any = null;
    const srv = await startInternalServer((_req, body, res) => {
        requestBody = JSON.parse(body);
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ code: 0, data: { orderId: "ord_p", status: "reserved", amountMicrocredits: 20_000, idempotencyKey: requestBody.idempotencyKey }, msg: "ok" }));
    });
    const acct = new RemoteAccountProvider({ baseUrl: srv.base, serviceToken: "svc-token", keyStore: new KeyStore(tmpStore()) });

    const r = await acct.reserve("user-1", 0, "idem-p", "canvas_get_context");
    assert.equal(r.ok, true);
    assert.equal(r.orderId, "ord_p");
    assert.equal(r.microcredits, 20_000); // 后端定价的实际冻结金额回传
    assert.equal("amountMicrocredits" in requestBody, false); // 连接器不传金额
    assert.equal(requestBody.tool, "canvas_get_context");
    srv.close();
});

test("[account] Remote 402/错误 envelope/网络不可达 全部 fail-closed", async () => {
    // 402 与非法 envelope
    const srv = await startInternalServer((req, _body, res) => {
        if (req.url?.includes("poor")) {
            res.writeHead(402); return res.end(JSON.stringify({ code: 402, data: null, msg: "积分不足" }));
        }
        res.writeHead(200); res.end(JSON.stringify({ code: 500, data: null, msg: "bad" })); // code 非 0
    });
    const acct = new RemoteAccountProvider({ baseUrl: srv.base, serviceToken: "t", keyStore: new KeyStore(tmpStore()) });

    const poor = await acct.reserve("poor", 100, "k", "t");
    assert.equal(poor.ok, false);
    assert.equal(poor.code, "insufficient_balance");
    const bad = await acct.reserve("user", 100, "k2", "t");
    assert.equal(bad.ok, false);
    assert.equal(bad.code, "rejected");
    srv.close();

    // 网络不可达 → unavailable，绝不放行
    const dead = new RemoteAccountProvider({ baseUrl: "http://127.0.0.1:1/api", serviceToken: "t", keyStore: new KeyStore(tmpStore()), timeoutMs: 300 });
    const u = await dead.reserve("user", 100, "k3", "t");
    assert.equal(u.ok, false);
    assert.equal(u.code, "unavailable");
});

test("[account] 工厂：默认 local；remote 缺配置 fail-fast", () => {
    assert.equal(createAccountProvider(new KeyStore(tmpStore())).kind, "local");

    const oldKind = process.env.CANVAS_ACCOUNT_PROVIDER;
    const oldBase = process.env.CANVAS_ACCOUNT_BASE_URL;
    const oldTok = process.env.CANVAS_INTERNAL_SERVICE_TOKEN;
    process.env.CANVAS_ACCOUNT_PROVIDER = "remote";
    delete process.env.CANVAS_ACCOUNT_BASE_URL;
    delete process.env.CANVAS_INTERNAL_SERVICE_TOKEN;
    assert.throws(() => createAccountProvider(), /CANVAS_ACCOUNT_BASE_URL/);
    process.env.CANVAS_ACCOUNT_BASE_URL = "http://x/api";
    assert.throws(() => createAccountProvider(), /CANVAS_INTERNAL_SERVICE_TOKEN/);
    process.env.CANVAS_INTERNAL_SERVICE_TOKEN = "tok";
    assert.equal(createAccountProvider(new KeyStore(tmpStore())).kind, "remote");
    if (oldKind === undefined) delete process.env.CANVAS_ACCOUNT_PROVIDER; else process.env.CANVAS_ACCOUNT_PROVIDER = oldKind;
    if (oldBase === undefined) delete process.env.CANVAS_ACCOUNT_BASE_URL; else process.env.CANVAS_ACCOUNT_BASE_URL = oldBase;
    if (oldTok === undefined) delete process.env.CANVAS_INTERNAL_SERVICE_TOKEN; else process.env.CANVAS_INTERNAL_SERVICE_TOKEN = oldTok;
});
