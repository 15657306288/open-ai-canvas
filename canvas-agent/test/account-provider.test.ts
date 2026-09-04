import { test } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import crypto from "node:crypto";
import { KeyStore } from "../src/bridge/gateway-keys.js";
import { LocalAccountProvider, createAccountProvider } from "../src/bridge/account-provider.js";

function tmpStore() {
    return path.join(os.tmpdir(), `acct-${crypto.randomBytes(5).toString("hex")}.json`);
}

test("[account] LocalAccountProvider 行为与 KeyStore 等价（认证/配额/预检/扣费/计数）", async () => {
    const file = tmpStore();
    const ks = new KeyStore(file);
    const created = ks.createKey({ name: "客户甲", dailyCalls: 1, balance: 1 });
    const plain = created.key;
    const acct = new LocalAccountProvider(ks);

    // 正确 key 认证成功
    let a = await acct.authenticateByKey(plain);
    assert.equal(a.ok, true);
    if (a.ok) assert.equal(a.principal.subjectId, created.record.id);

    // 错误 key → 401
    a = await acct.authenticateByKey("ak_nope");
    assert.equal(a.ok, false);
    if (!a.ok) assert.equal(a.status, 401);

    // 预检：余额 1，本次 0.5 允许；本次 2 拒绝并回传余额
    assert.deepEqual(await acct.preCheck(created.record.id, 0.5), { allow: true });
    const pc = await acct.preCheck(created.record.id, 2);
    assert.equal(pc.allow, false);
    if (!pc.allow) { assert.equal(pc.balance, 1); assert.equal(pc.need, 2); }

    // 扣费 0.5 → 余额 0.5
    const ch = await acct.charge(created.record.id, 0.5);
    assert.equal(ch.ok, true); assert.equal(ch.balance, 0.5);

    // 记一次调用后达到日配额 1，再次认证 → 429
    await acct.recordCall(created.record.id, "channel_list");
    a = await acct.authenticateByKey(plain);
    assert.equal(a.ok, false);
    if (!a.ok) assert.equal(a.status, 429);

    // resolveSubject：停用后为 undefined
    assert.ok(await acct.resolveSubject(created.record.id));
    ks.revoke(created.record.id);
    assert.equal(await acct.resolveSubject(created.record.id), undefined);

    fs.rmSync(file, { force: true });
});

test("[account] 工厂默认返回 local 实现", () => {
    const p = createAccountProvider(new KeyStore(tmpStore()));
    assert.equal(p.kind, "local");
});

test("[account] remote 缺 baseUrl 时明确报错（fail-fast）", () => {
    const old = process.env.CANVAS_ACCOUNT_PROVIDER;
    process.env.CANVAS_ACCOUNT_PROVIDER = "remote";
    delete process.env.CANVAS_ACCOUNT_BASE_URL;
    assert.throws(() => createAccountProvider(), /CANVAS_ACCOUNT_BASE_URL/);
    if (old === undefined) delete process.env.CANVAS_ACCOUNT_PROVIDER; else process.env.CANVAS_ACCOUNT_PROVIDER = old;
});
