import { expect, test } from "bun:test";
import { ApprovedToolExecution, type ApprovedToolExecutionState } from "../src/services/approved-tool-execution";
import { creationRuns, type CreationRun, type CreationSubmission } from "../src/services/api/creation-runs";
import { createModelChannel, defaultConfig } from "../src/stores/use-config-store";
import type { GenerationTask } from "../src/services/api/task-center";

const identity = { kind: "art-critique", nodeId: "node", sourceFingerprint: "image", model: "model" };
const config = { ...defaultConfig, model: "system::model", channels: [createModelChannel({ id: "system", scope: "system", models: ["model"], interfaceType: "chat-completion" })] };
const request = (stage: string) => ({ config, prompt: stage, messages: [{ role: "user" as const, content: "review" }], tools: [], toolChoice: "auto" as const, metadata: { source: "art-critique", stage } });
const quote = (id: string, stage: string, taskId?: string): CreationSubmission => ({ id, runId: "run", itemKey: `stage:${stage}`, requestHash: stage, taskId, quote: { model: "model", billingMode: "fixed_request", quantity: 1, amountMicrocredits: 1_000_000, estimated: false, expiresAt: "2099-01-01", quoteHash: id } });

function harness(submissions: CreationSubmission[] = [], state = identity) {
    let run: CreationRun = { id: "run", userId: "user", revision: 1, executionEpoch: 0, executionOwner: "", status: "idle", state, createdAt: "", updatedAt: "" };
    let view: ApprovedToolExecutionState = { busy: false };
    const listeners: Array<{ matches: (view: ApprovedToolExecutionState) => boolean; resolve: (view: ApprovedToolExecutionState) => void }> = [];
    const approvals: string[][] = [], executions: string[] = [], waits: string[] = [];
    let creates = 0, refreshes = 0;
    const api = { ...creationRuns,
        create: async () => { creates++; return { run, submissions }; },
        get: async () => ({ run, submissions }),
        claim: async (_id: string, input: { owner: string }) => (run = { ...run, executionEpoch: run.executionEpoch + 1, executionOwner: input.owner }),
        release: async () => ({ released: true }),
        prepare: async (_id: string, input: { itemKey: string }) => {
            const existing = submissions.find((item) => item.itemKey === input.itemKey);
            if (existing) return existing;
            const created = quote(`quote-${submissions.length}`, input.itemKey.replace("stage:", "")); submissions.push(created); return created;
        },
        approve: async (_id: string, input: { submissionIds: string[] }) => {
            approvals.push(input.submissionIds);
            submissions = submissions.map((item) => input.submissionIds.includes(item.id) ? { ...item, approvedAt: "2026-01-01" } : item);
            return { submissions: submissions.filter((item) => input.submissionIds.includes(item.id)) };
        },
        refreshQuote: async (_id: string, input: { submissionId: string }) => {
            refreshes++;
            const item = submissions.find((entry) => entry.id === input.submissionId)!;
            item.revokedAt = "2026-01-01";
            const fresh = { ...item, id: `fresh-${refreshes}`, itemKey: `requote:${item.id}`, revokedAt: undefined, approvedAt: undefined, quote: { ...item.quote, expiresAt: "2099-01-01" } };
            submissions.push(fresh); return fresh;
        },
        execute: async (_id: string, input: { submissionId: string }) => {
            const item = submissions.find((entry) => entry.id === input.submissionId)!;
            expect(Boolean(item.taskId || item.approvedAt)).toBe(true);
            executions.push(input.submissionId);
            return { id: item.taskId || `task-${item.id}` } as GenerationTask;
        },
    } as typeof creationRuns;
    const controller = new AbortController();
    const execution = new ApprovedToolExecution({ clientKey: "test", ...(submissions.length ? { runId: "run" } : {}), identity, signal: controller.signal, onRun: () => {}, onState: (next) => {
        view = next;
        for (const listener of [...listeners]) if (listener.matches(view)) { listeners.splice(listeners.indexOf(listener), 1); listener.resolve(view); }
    } }, api, async (id) => { waits.push(id); return { id, status: "succeeded", resultJson: JSON.stringify({ text: "report" }) } as GenerationTask; });
    return { execution, controller, api, approvals, executions, waits, counters: () => ({ creates, refreshes }), view: () => view,
        until: (matches: (view: ApprovedToolExecutionState) => boolean) => matches(view) ? Promise.resolve(view) : new Promise<ApprovedToolExecutionState>((resolve) => listeners.push({ matches, resolve })),
    };
}

test("用户只批准费用卡已展示的项目，后来加入的阶段继续等待", async () => {
    const h = harness();
    try {
        const first = h.execution.execute(request("first"));
        const shown = await h.until((view) => view.quote?.items.length === 1);
        const second = h.execution.execute(request("second"));
        await h.until((view) => view.quote?.items.length === 2);
        expect(h.executions).toEqual([]);
        await h.execution.approve(shown.quote!.items.map((item) => item.id));
        expect((await first).content).toBe("report");
        expect(h.executions).toHaveLength(1);
        expect(h.view().quote?.items).toHaveLength(1);
        await h.execution.approve(h.view().quote!.items.map((item) => item.id)); await second;
        expect(h.approvals).toEqual([["quote-0"], ["quote-1"]]);
    } finally { h.execution.dispose(); }
});

test("恢复已提交阶段沿用原任务，即使原报价过期也不新增批准", async () => {
    const item = quote("existing", "stage", "original-task"); item.quote.expiresAt = "2000-01-01";
    const h = harness([item]);
    try {
        await h.execution.execute(request("stage"));
        expect(h.waits).toEqual(["original-task"]); expect(h.approvals).toEqual([]);
        expect(h.counters()).toEqual({ creates: 0, refreshes: 0 });
    } finally { h.execution.dispose(); }
});

test("等待费用确认时取消，不提交模型；迟到按钮调用不抛未处理异常", async () => {
    const h = harness();
    try {
        const result = h.execution.execute(request("stage")).catch((error: Error) => error);
        await h.until((view) => Boolean(view.quote)); h.controller.abort();
        expect((await result as Error).name).toBe("AbortError");
        await h.execution.approve(["quote-0"]); await h.execution.refresh();
        expect(h.executions).toEqual([]); expect(h.approvals).toEqual([]);
    } finally { h.execution.dispose(); }
});

test("已批准但未提交的过期报价必须刷新并重新确认", async () => {
    const item = { ...quote("expired", "stage"), approvedAt: "2000-01-01" }; item.quote.expiresAt = "2000-01-01";
    const h = harness([item]);
    try {
        const result = h.execution.execute(request("stage"));
        await h.until((view) => Boolean(view.quote));
        expect(h.executions).toEqual([]); expect(h.counters().refreshes).toBe(1);
        await h.execution.approve(["expired"]); expect(h.executions).toEqual([]);
        await h.execution.approve(h.view().quote!.items.map((entry) => entry.id)); await result;
        expect(h.executions).toEqual(["fresh-1"]);
    } finally { h.execution.dispose(); }
});

test("刷新后的报价可重新打开恢复，找回未撤销的替代条目", async () => {
    const old = { ...quote("old", "stage"), revokedAt: "2026-01-01" };
    const fresh = { ...quote("new", "stage", "original-task"), itemKey: "requote:old" };
    const h = harness([old, fresh]);
    try { await h.execution.execute(request("stage")); expect(h.executions).toEqual(["new"]); expect(h.waits).toEqual(["original-task"]); }
    finally { h.execution.dispose(); }
});

test("图片或模型身份改变时拒绝恢复", async () => {
    const h = harness([quote("old", "stage")], { ...identity, model: "different" });
    try { await expect(h.execution.execute(request("stage"))).rejects.toThrow("已变化"); expect(h.executions).toEqual([]); }
    finally { h.execution.dispose(); }
});

test("并行阶段报价失败会停止其他等待中的阶段，不永久挂起", async () => {
    const h = harness();
    try {
        const first = h.execution.execute(request("first")).catch((error: Error) => error);
        await h.until((view) => Boolean(view.quote));
        h.api.prepare = async () => { throw new Error("报价服务不可用"); };
        await expect(h.execution.execute(request("second"))).rejects.toThrow("报价服务不可用");
        expect((await first as Error).name).toBe("AbortError"); expect(h.executions).toEqual([]);
    } finally { h.execution.dispose(); }
});

test("刷新费用卡后旧按钮不能批准新报价", async () => {
    const h = harness();
    try {
        const result = h.execution.execute(request("analyze_art_scene"));
        const shown = await h.until((view) => Boolean(view.quote));
        await h.execution.refresh();
        expect(h.view().quote?.items[0]?.label).toBe("理解画面");
        await h.execution.approve(shown.quote!.items.map((item) => item.id));
        expect(h.executions).toEqual([]); expect(h.view().error).toContain("报价已变化");
        await h.execution.approve(h.view().quote!.items.map((item) => item.id)); await result;
        expect(h.approvals).toEqual([["fresh-1"]]);
    } finally { h.execution.dispose(); }
});
