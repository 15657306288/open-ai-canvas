import { nanoid } from "nanoid";
import { creationRuns, type CreationRun, type CreationSubmission } from "./api/creation-runs";
import { prepareBackendToolGenerationTask, parseBackendGenerationResult, runBackendToolGenerationTask } from "./api/generation-task";
import { waitForGenerationTask } from "./api/task-center";
import type { CreativeQuote } from "@/lib/creation/creative-agent-contract";
import { getActiveUserScope } from "@/lib/user-scope";
import { logicalModelIDForConfig, resolveModelRequestConfig } from "@/stores/use-config-store";

type Request = Parameters<typeof runBackendToolGenerationTask>[0];
type Pending = { submission: CreationSubmission; externalBilling: boolean; resolve: () => void; reject: (error: Error) => void };
export type ApprovedToolExecutionState = { quote?: CreativeQuote; busy: boolean; error?: string };

// Only this boundary approves and executes quoted tasks. Pipeline code never grants approval.
export class ApprovedToolExecution {
    private run?: CreationRun;
    private owner = `plugin:${nanoid()}`;
    private scope = getActiveUserScope();
    private pending = new Map<string, Pending>();
    private heartbeat?: ReturnType<typeof setInterval>;
    private disposed = false;
    private busy = false;
    private error?: string;
    private stopReason?: string;
    private ready?: Promise<void>;
    private controller = new AbortController();

    constructor(private options: {
        clientKey: string; runId?: string; identity: Record<string, string>;
        signal: AbortSignal; onRun: (id: string) => void;
        onState: (state: ApprovedToolExecutionState) => void;
    }, private api = creationRuns, private waitTask = waitForGenerationTask) {
        options.signal.addEventListener("abort", this.abort, { once: true });
    }

    private assertLive() {
        if (this.disposed || this.options.signal.aborted || this.scope !== getActiveUserScope() || this.controller.signal.aborted) throw new DOMException(this.stopReason || "分析已停止，请重新打开后继续", "AbortError");
    }
    private guard() { return { owner: this.owner, executionEpoch: this.run!.executionEpoch }; }
    private initialize() {
        return this.ready ||= (async () => {
            this.assertLive();
            const detail = this.options.runId ? await this.api.get(this.options.runId, this.controller.signal) : await this.api.create({ clientKey: this.options.clientKey, state: this.options.identity }, this.controller.signal);
            this.assertLive();
            if (Object.entries(this.options.identity).some(([key, value]) => detail.run.state[key] !== value)) throw new Error("原分析的图片、节点或模型已变化，请开始新的分析");
            this.run = await this.api.claim(detail.run.id, { expectedEpoch: detail.run.executionEpoch, owner: this.owner }, this.controller.signal);
            this.assertLive();
            this.options.onRun(this.run.id);
            this.heartbeat = setInterval(() => {
                if (this.disposed) return;
                void (async () => { this.assertLive(); await this.api.heartbeat(this.run!.id, this.guard(), this.controller.signal); })().catch(() => this.abort(new Error("分析连接已中断，请重新打开后继续")));
            }, 15_000);
        })();
    }

    execute = async (request: Request) => {
        // A control/quote failure must also unblock sibling stages waiting for approval.
        // Model task failures below keep the pipeline's existing partial-result handling.
        const task = await this.submit(request).catch((error: unknown) => {
            this.abort(error);
            throw new DOMException(error instanceof Error ? error.message : "分析提交失败，请重新打开后继续", "AbortError");
        });
        if (this.scope === getActiveUserScope()) request.onTaskCreated?.(task);
        this.assertLive();
        const completed = await this.waitTask(task.id, { initialTask: task, signal: this.controller.signal, onTextDelta: request.onDelta });
        this.assertLive();
        const result = parseBackendGenerationResult(completed);
        return { content: result.text || "", toolCalls: result.toolCalls || [], ...(result.reasoning ? { reasoning: result.reasoning } : {}) };
    };

    private async submit(request: Request) {
        await this.initialize();
        this.assertLive();
        const stage = request.metadata?.stage;
        if (!stage) throw new Error("分析阶段缺少稳定标识");
        let submission = await this.api.prepare(this.run!.id, { ...this.guard(), itemKey: `stage:${stage}`, request: prepareBackendToolGenerationTask(request) }, this.controller.signal);
        this.assertLive();
        if (submission.revokedAt) {
            const detail = await this.api.get(this.run!.id, this.controller.signal);
            submission = detail.submissions.find((item) => item.requestHash === submission.requestHash && !item.revokedAt) || submission;
            if (submission.revokedAt) throw new Error("原报价已失效，无法恢复此阶段");
        }
        if (!submission.taskId && Date.parse(submission.quote.expiresAt) <= Date.now()) submission = await this.api.refreshQuote(this.run!.id, { ...this.guard(), submissionId: submission.id }, this.controller.signal);
        this.assertLive();
        if (!submission.taskId && !submission.approvedAt) {
            await new Promise<void>((resolve, reject) => {
                const externalBilling = !logicalModelIDForConfig(request.config) && !resolveModelRequestConfig(request.config, request.config.model).channelId;
                this.pending.set(stage, { submission, externalBilling, resolve, reject });
                this.emit();
            });
            submission = this.pending.get(stage)!.submission;
            this.pending.delete(stage);
            this.emit();
        }
        this.assertLive();
        return this.api.execute(this.run!.id, { ...this.guard(), submissionId: submission.id }, this.controller.signal);
    }

    async approve(submissionIds: string[]) {
        if (this.busy || this.disposed || this.controller.signal.aborted) return;
        const ids = new Set(submissionIds);
        const batch = [...this.pending.values()].filter((item) => ids.has(item.submission.id) && !item.submission.approvedAt);
        if (batch.length !== ids.size) { this.error = "报价已变化，请核对当前费用卡后确认"; this.emit(); return; }
        if (!batch.length) return;
        this.busy = true; this.error = undefined; this.emit();
        try {
            this.assertLive();
            const result = await this.api.approve(this.run!.id, { ...this.guard(), submissionIds: batch.map((item) => item.submission.id) }, this.controller.signal);
            this.assertLive();
            if (batch.some((item) => !result.submissions.some((candidate) => candidate.id === item.submission.id && candidate.approvedAt))) {
                const error = new Error("费用确认回执不完整，请继续上次分析以读取实际状态");
                this.abort(error);
                throw error;
            }
            for (const item of batch) {
                const approved = result.submissions.find((candidate) => candidate.id === item.submission.id && candidate.approvedAt);
                if (!approved) throw new Error("费用确认结果不完整，请重新核对");
                item.submission = approved;
            }
            batch.forEach((item) => item.resolve());
        } catch (error) { this.error = error instanceof Error ? error.message : "费用确认失败"; }
        finally { this.busy = false; this.emit(); }
    }

    async refresh() {
        if (this.busy || this.disposed || this.controller.signal.aborted) return;
        this.busy = true; this.error = undefined; this.emit();
        try {
            this.assertLive();
            for (const item of [...this.pending.values()]) {
                if (item.submission.approvedAt) continue;
                item.submission = await this.api.refreshQuote(this.run!.id, { ...this.guard(), submissionId: item.submission.id }, this.controller.signal);
                this.assertLive();
            }
        } catch (error) { this.error = error instanceof Error ? error.message : "报价刷新失败"; }
        finally { this.busy = false; this.emit(); }
    }

    private emit() {
        if (this.disposed || this.scope !== getActiveUserScope()) return;
        const items = [...this.pending.entries()].filter(([, item]) => !item.submission.approvedAt).map(([stage, item]) => ({ ...item.submission, stage }));
        const externalBilling = [...this.pending.values()].some((item) => !item.submission.approvedAt && item.externalBilling);
        const estimated = items.some((item) => item.quote.estimated);
        this.options.onState({ busy: this.busy, error: this.error, quote: items.length ? {
            id: items.map((item) => item.id).join(":"), title: "确认本批分析费用",
            items: items.map((item) => ({ id: item.id, label: stageLabel(item.stage), model: item.quote.model, quantity: item.quote.quantity, specification: `${item.quote.estimated ? "预计" : ""} ${item.quote.amountMicrocredits / 1_000_000} 积分` })),
            amountLabel: `${externalBilling ? "平台 " : ""}${estimated ? "预计 " : ""}${items.reduce((sum, item) => sum + item.quote.amountMicrocredits, 0) / 1_000_000} 积分${externalBilling ? " + 外部渠道费用" : ""}`,
            externalBilling,
            basis: "仅批准本批列出的模型调用；按用量计费时按实际结算，估算不是费用上限。后续阶段根据分析结果另行报价。",
            expiresAt: items.map((item) => item.quote.expiresAt).sort()[0],
        } : undefined });
    }

    private abort = (reason?: unknown) => {
        if (!this.controller.signal.aborted && reason instanceof Error) this.stopReason = reason.message;
        this.controller.abort();
        if (this.heartbeat) clearInterval(this.heartbeat);
        for (const item of this.pending.values()) item.reject(new DOMException(this.stopReason || "分析已停止", "AbortError"));
    };
    dispose() {
        this.disposed = true; this.abort();
        if (this.heartbeat) clearInterval(this.heartbeat);
        this.options.signal.removeEventListener("abort", this.abort);
        if (this.run && this.scope === getActiveUserScope()) void this.api.release(this.run.id, this.guard()).catch(() => { /* Lease expiry releases an offline owner. */ });
    }
}

function stageLabel(key: string) {
    return ({ analyze_art_scene: "理解画面", review_art_composition: "分析构图", review_art_color: "分析色彩", review_art_lighting: "分析光线", review_art_structure: "分析结构", aggregate_art_critique: "整理问题", ground_art_critique_issues: "定位问题", verify_art_critique: "复核结论", generate_art_edit_prompts: "编写修改建议" } as Record<string, string>)[key.replace(/^stage:/, "")] || "分析阶段";
}
