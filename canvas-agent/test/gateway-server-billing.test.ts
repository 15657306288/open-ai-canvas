import { test } from "node:test";
import assert from "node:assert/strict";
import { runBilledCall, type BilledCallDeps } from "../src/bridge/billing-lifecycle.js";
import type { AccountProvider, AuthOutcome, ConfirmationOutcome, ConfirmationRequest, Principal, ReserveOutcome, TerminalOutcome } from "../src/bridge/account-provider.js";

/** 记录调用序列的假账户，便于断言 reserve→call→settle/refund 顺序与幂等键一致性。 */
class FakeAccount implements AccountProvider {
    kind = "local" as const;
    seq: string[] = [];
    idemAt: { reserve?: string; settle?: string; refund?: string } = {};
    reserveResult: ReserveOutcome = { ok: true, orderId: "ord_1" };
    settleResult: TerminalOutcome = { ok: true };
    refundResult: TerminalOutcome = { ok: true };
    /** 确认门：创建请求记录 + 轮询状态队列（默认 approved，兼容既有 remote 测试）。 */
    confirmCreated: ConfirmationRequest[] = [];
    confirmStatusQueue: NonNullable<ConfirmationOutcome["status"]>[] = ["approved"];

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
    async createConfirmation(req: ConfirmationRequest): Promise<ConfirmationOutcome> {
        this.confirmCreated.push(req); return { ok: true, id: "conf_1", status: "pending" };
    }
    async confirmationStatus(): Promise<ConfirmationOutcome> {
        const next = this.confirmStatusQueue.shift() ?? "approved";
        return { ok: true, id: "conf_1", status: next };
    }
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

test("[lifecycle] remote 模式：画布真实选择的模型（args.model）随 reserve 传给后端定价", async () => {
    class RemoteFake extends FakeAccount {
        override kind = "remote" as const;
        override reserveResult: ReserveOutcome = { ok: true, orderId: "ord_model", microcredits: 10_000 };
        reservedModel: string | undefined;
        override async reserve(s: string, amount: number, idem: string, _tool: string, modelKey?: string): Promise<ReserveOutcome> {
            this.reservedModel = modelKey;
            return super.reserve(s, amount, idem);
        }
    }
    const acct = new RemoteFake();
    const { deps } = depsFor(acct, async () => "VIDEO_OK");
    const r = await runBilledCall(
        "canvas_generate_video",
        { model: "agnes-video-2.5-flash", prompt: "test", seconds: "5" },
        keyAuth,
        deps,
    );

    assert.equal(acct.reservedModel, "agnes-video-2.5-flash", "reserve 必须携带画布选择的模型");
    assert.deepEqual(acct.seq, ["reserve", "call", "settle", "record"]);
    assert.equal(r.content[0].text, "VIDEO_OK");
});

test("[确认门] remote + 生成工具 + 用户批准：确认请求携带后端定价金额/模型/摘要，批准后才执行", async () => {
    class RemoteFake extends FakeAccount {
        override kind = "remote" as const;
        override reserveResult: ReserveOutcome = { ok: true, orderId: "ord_confirm", microcredits: 20_000 };
    }
    const acct = new RemoteFake();
    const { deps, logs } = depsFor(acct, async () => "GEN_OK");
    const r = await runBilledCall(
        "canvas_generate_image",
        { model: "nano-banana-2", prompt: "一只猫" },
        keyAuth,
        deps,
    );

    assert.equal(acct.confirmCreated.length, 1, "生成前必须创建确认请求");
    const req = acct.confirmCreated[0];
    assert.equal(req.orderId, "ord_confirm");
    assert.equal(req.amountMicrocredits, 20_000, "确认金额必须以后端定价为准");
    assert.equal(req.modelKey, "nano-banana-2");
    assert.equal(req.promptSummary, "一只猫");
    assert.deepEqual(acct.seq, ["reserve", "call", "settle", "record"], "批准后顺序不变");
    assert.equal(r.isError, undefined);
    assert.equal(r.content[0].text, "GEN_OK");
    assert.equal(logs.some((e) => e.ok === "approved"), true, "日志应记录已批准");
});

test("[确认门] remote + 生成工具 + 用户拒绝：绝不执行工具，退款并返回失败", async () => {
    class RemoteFake extends FakeAccount {
        override kind = "remote" as const;
        override reserveResult: ReserveOutcome = { ok: true, orderId: "ord_reject", microcredits: 20_000 };
    }
    const acct = new RemoteFake();
    acct.confirmStatusQueue = ["rejected"];
    let called = false;
    const { deps } = depsFor(acct, async () => { called = true; return "X"; });
    const r = await runBilledCall("canvas_generate_video", { model: "agnes-video-2.5", prompt: "x" }, keyAuth, deps);

    assert.equal(called, false, "用户拒绝后不得执行生成");
    assert.deepEqual(acct.seq, ["reserve", "refund", "record"]);
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /拒绝/);
    assert.equal(acct.idemAt.reserve, acct.idemAt.refund, "退款必须用同一幂等键");
});

test("[确认门] remote + 生成工具 + 确认超时：退款并返回失败", async () => {
    class RemoteFake extends FakeAccount {
        override kind = "remote" as const;
        override reserveResult: ReserveOutcome = { ok: true, orderId: "ord_timeout", microcredits: 20_000 };
    }
    const acct = new RemoteFake();
    acct.confirmStatusQueue = ["pending", "pending", "pending"];
    let called = false;
    const { deps } = depsFor(acct, async () => { called = true; return "X"; });
    const before = process.env.CANVAS_CONFIRM_TIMEOUT_MS;
    process.env.CANVAS_CONFIRM_TIMEOUT_MS = "2000";
    try {
        const r = await runBilledCall("canvas_run_generation", { model: "x", prompt: "y" }, keyAuth, deps);
        assert.equal(called, false, "确认超时不得执行生成");
        assert.deepEqual(acct.seq, ["reserve", "refund", "record"]);
        assert.equal(r.isError, true);
        assert.match(r.content[0].text, /超时/);
    } finally {
        if (before === undefined) delete process.env.CANVAS_CONFIRM_TIMEOUT_MS; else process.env.CANVAS_CONFIRM_TIMEOUT_MS = before;
    }
});

test("[确认门] local 内测模式：不启用确认门，保持原计费流程", async () => {
    const acct = new FakeAccount(); // kind = local
    const { deps } = depsFor(acct, async () => "LOCAL_OK");
    const r = await runBilledCall("canvas_generate_image", { model: "m", prompt: "p" }, keyAuth, deps);

    assert.equal(acct.confirmCreated.length, 0, "local 模式不创建确认请求");
    assert.deepEqual(acct.seq, ["reserve", "call", "settle", "record"]);
    assert.equal(r.content[0].text, "LOCAL_OK");
});

test("[确认门] remote + 非生成工具：不创建确认，直接执行", async () => {
    class RemoteFake extends FakeAccount {
        override kind = "remote" as const;
        override reserveResult: ReserveOutcome = { ok: true, orderId: "ord_ctx", microcredits: 5_000 };
    }
    const acct = new RemoteFake();
    const { deps } = depsFor(acct, async () => "CTX_OK");
    const r = await runBilledCall("canvas_get_context", {}, keyAuth, deps);

    assert.equal(acct.confirmCreated.length, 0);
    assert.deepEqual(acct.seq, ["reserve", "call", "settle", "record"]);
    assert.equal(r.content[0].text, "CTX_OK");
});

test("[确认门] 确认请求创建失败：fail-closed，退款且不执行工具", async () => {
    class RemoteFake extends FakeAccount {
        override kind = "remote" as const;
        override reserveResult: ReserveOutcome = { ok: true, orderId: "ord_fail", microcredits: 20_000 };
        override async createConfirmation(): Promise<ConfirmationOutcome> {
            return { ok: false, message: "确认服务不可用" };
        }
    }
    const acct = new RemoteFake();
    let called = false;
    const { deps } = depsFor(acct, async () => { called = true; return "X"; });
    const r = await runBilledCall("canvas_generate_image", { prompt: "p" }, keyAuth, deps);

    assert.equal(called, false);
    assert.deepEqual(acct.seq, ["reserve", "refund", "record"]);
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /确认/);
});
