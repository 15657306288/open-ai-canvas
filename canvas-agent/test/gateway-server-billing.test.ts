import { test } from "node:test";
import assert from "node:assert/strict";
import { runBilledCall, type BilledCallDeps } from "../src/bridge/billing-lifecycle.js";
import type { AccountProvider, AuthOutcome, Principal, ReserveOutcome, TerminalOutcome } from "../src/bridge/account-provider.js";

/** 记录调用序列的假账户，便于断言 reserve→call→settle/refund 顺序与幂等键一致性。 */
class FakeAccount implements AccountProvider {
    kind = "local" as const;
    seq: string[] = [];
    idemAt: { reserve?: string; settle?: string; refund?: string } = {};
    reserveResult: ReserveOutcome = { ok: true, orderId: "ord_1" };
    settleResult: TerminalOutcome = { ok: true };
    refundResult: TerminalOutcome = { ok: true };

    async authenticateByKey(): Promise<AuthOutcome> { return { ok: false, reason: "not_found", status: 401 }; }
    async authenticateClient(): Promise<AuthOutcome> { return { ok: false, reason: "bad_secret", status: 401 }; }
    async resolveSubject(): Promise<Principal | undefined> { return undefined; }
    async reserve(_s: string, _a: number, idem: string): Promise<ReserveOutcome> {
        this.seq.push("reserve"); this.idemAt.reserve = idem; return this.reserveResult;
    }
    async settle(_o: string, idem: string): Promise<TerminalOutcome> {
        this.seq.push("settle"); this.idemAt.settle = idem; return this.settleResult;
    }
    async refund(_o: string, idem: string): Promise<TerminalOutcome> {
        this.seq.push("refund"); this.idemAt.refund = idem; return this.refundResult;
    }
    async recordCall(): Promise<void> { this.seq.push("record"); }
}

function depsFor(acct: FakeAccount, callTool: BilledCallDeps["callTool"]) {
    const logs: Record<string, unknown>[] = [];
    const deps: BilledCallDeps = {
        account: acct,
        callTool: async (name, args) => { acct.seq.push("call"); return callTool(name, args); },
        priceOf: () => 10_000,
        log: (e) => logs.push(e),
    };
    return { deps, logs };
}

const keyAuth = { type: "key" as const, keyId: "k_1", keyName: "甲" };

test("[lifecycle] 成功：reserve→call→settle，幂等键贯穿，成功后才记账", async () => {
    const acct = new FakeAccount();
    const { deps, logs } = depsFor(acct, async () => "RESULT");
    const r = await runBilledCall("canvas_get_context", {}, keyAuth, deps);

    assert.deepEqual(acct.seq, ["reserve", "call", "settle", "record"]);
    assert.equal(r.isError, undefined);
    assert.equal(r.content[0].text, "RESULT");
    assert.equal(acct.idemAt.reserve, acct.idemAt.settle, "reserve/settle 必须同一幂等键");
    assert.equal(logs.at(-1)!.ok, true);
    assert.equal(logs.at(-1)!.microcredits, 10_000);
});

test("[lifecycle] 工具失败：reserve→call→refund，返回 isError", async () => {
    const acct = new FakeAccount();
    const { deps } = depsFor(acct, async () => { throw new Error("boom"); });
    const r = await runBilledCall("video_generation", {}, keyAuth, deps);

    assert.deepEqual(acct.seq, ["reserve", "call", "refund", "record"]);
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /boom/);
    assert.equal(acct.idemAt.reserve, acct.idemAt.refund);
});

test("[lifecycle] reserve 被拒：绝不执行工具，直接 isError", async () => {
    const acct = new FakeAccount();
    acct.reserveResult = { ok: false, code: "insufficient_balance", message: "积分不足" };
    let called = false;
    const { deps, logs } = depsFor(acct, async () => { called = true; return "X"; });
    const r = await runBilledCall("canvas_get_context", {}, keyAuth, deps);

    assert.equal(called, false, "reserve 失败不得调用工具");
    assert.deepEqual(acct.seq, ["reserve"]);
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /积分不足|充值/);
    assert.equal(logs.at(-1)!.ok, false);
});

test("[lifecycle] settle 失败：工具结果保留但标记 isError，且不误退款", async () => {
    const acct = new FakeAccount();
    acct.settleResult = { ok: false, message: "settle down" };
    const { deps } = depsFor(acct, async () => "DONE");
    const r = await runBilledCall("canvas_get_context", {}, keyAuth, deps);

    assert.ok(acct.seq.includes("settle") && !acct.seq.includes("refund"), "结算失败不应退款");
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /DONE/);
    assert.match(r.content[0].text, /结算失败/);
});

test("[lifecycle] master 内部调用不计费：无 reserve/settle，直接执行", async () => {
    const acct = new FakeAccount();
    const { deps, logs } = depsFor(acct, async () => "INTERNAL");
    const r = await runBilledCall("channel_list", {}, { type: "master" }, deps);

    assert.deepEqual(acct.seq, ["call"]);
    assert.equal(r.isError, undefined);
    assert.equal(r.content[0].text, "INTERNAL");
    assert.equal(logs.at(-1)!.microcredits, undefined, "master 调用不记金额");
});

test("[lifecycle] remote 模式：连接器不本地定价，reserve 传 0，金额以后端返回为准", async () => {
    class RemoteFake extends FakeAccount {
        override kind = "remote" as const;
        override reserveResult: ReserveOutcome = { ok: true, orderId: "ord_backend", microcredits: 20_000 };
        reservedAmount: number | undefined;
        override async reserve(s: string, amount: number, idem: string): Promise<ReserveOutcome> {
            this.reservedAmount = amount;
            return super.reserve(s, amount, idem);
        }
    }
    const acct = new RemoteFake();
    const { deps, logs } = depsFor(acct, async () => "BACKEND_PRICED");
    deps.priceOf = () => 999_999; // 本地定价应被 remote 模式忽略
    const r = await runBilledCall("canvas_get_context", {}, keyAuth, deps);

    assert.equal(acct.reservedAmount, 0, "remote reserve 必须传 0（后端定价）");
    assert.deepEqual(acct.seq, ["reserve", "call", "settle", "record"]);
    assert.equal(r.content[0].text, "BACKEND_PRICED");
    assert.equal(logs.at(-1)!.microcredits, 20_000, "日志金额应为后端返回的实际冻结金额");
});
