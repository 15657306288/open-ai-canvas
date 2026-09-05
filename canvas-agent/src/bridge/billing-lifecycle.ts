/**
 * billing-lifecycle —— 单次 MCP 工具调用的两阶段计费生命周期（纯逻辑，可单测）。
 *
 * 从 gateway-server 抽出，避免测试导入 HTTP 服务器模块时触发拉取 Runtime schema 等副作用。
 * 流程：reserve（失败不执行工具）→ [生成类工具：用户确认门] → callTool → 成功 settle / 异常 refund；
 * master 内部调用不计费。
 *
 * 外部用户确认门（remote 定价 + API key 调用生成类工具时启用）：
 *   reserve 冻结后不立即执行，先创建后端确认请求，网关挂起轮询；
 *   用户批准（网站/画布点确认）→ 真正执行工具并结算；
 *   用户拒绝 / 确认超时 → 退款并返回失败，绝不未确认就生成。
 * local 内测模式没有后端确认通道，保持原逻辑（不启用确认门）。
 *
 * 金额一律为正整数 microcredits（微积分）；任何 reserve/settle 失败都 fail-closed，不伪装成功。
 */
import crypto from "node:crypto";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { today } from "./gateway-keys.js";
import type { AccountProvider, ConfirmationOutcome } from "./account-provider.js";

/** 计费视角的认证主体（gateway-server 的 GwAuth 结构兼容，直接传入即可）。 */
export interface BilledAuth {
    type: "master" | "key";
    keyId?: string;
    keyName?: string;
}

export interface BilledCallDeps {
    account: AccountProvider;
    callTool: (name: string, args: Record<string, unknown>) => Promise<unknown>;
    priceOf: (tool: string) => number;
    log: (entry: Record<string, unknown>) => void;
}

/** 单次工具调用结果，直接对齐 MCP CallToolResult（content + 可选 isError）。 */
export type BilledToolResult = CallToolResult;

/** 生成类工具：外部调用前必须经用户确认（与后端 IsGenerationTool 保持一致）。 */
const generationTools = new Set([
    "canvas_generate_text",
    "canvas_generate_image",
    "canvas_generate_video",
    "canvas_generate_audio",
    "canvas_run_generation",
]);

export function isGenerationTool(name: string): boolean {
    return generationTools.has(name);
}

/** 等待用户确认的超时（毫秒），可被环境变量覆盖（CANVAS_CONFIRM_TIMEOUT_MS）。 */
export function confirmTimeoutMs(): number {
    const raw = Number(process.env.CANVAS_CONFIRM_TIMEOUT_MS);
    return Number.isFinite(raw) && raw >= 2000 && raw <= 3_600_000 ? Math.round(raw) : 300_000;
}

/** 从工具参数提取可安全展示的 prompt 摘要（前 200 字符，用于用户确认卡片）。 */
function promptSummaryOf(args: Record<string, unknown>): string | undefined {
    const raw = args?.prompt ?? args?.text;
    if (typeof raw !== "string") return undefined;
    const trimmed = raw.trim();
    return trimmed ? trimmed.slice(0, 200) : undefined;
}

/**
 * 确认门：创建后端确认请求并挂起轮询，直到 approved/rejected/expired 或超时。
 * 返回 true=已批准可执行；false=用户拒绝/超时/确认服务不可用（调用方必须退款）。
 */
async function awaitUserConfirmation(
    account: AccountProvider,
    subjectId: string,
    orderId: string,
    tool: string,
    modelKey: string | undefined,
    amountMicrocredits: number,
    args: Record<string, unknown>,
    idempotencyKey: string,
): Promise<{ ok: boolean; reason: string }> {
    const created = await account.createConfirmation({
        userId: subjectId,
        orderId,
        tool,
        modelKey,
        amountMicrocredits,
        promptSummary: promptSummaryOf(args),
        idempotencyKey,
    });
    if (!created.ok || !created.id) {
        return { ok: false, reason: `确认请求创建失败：${created.message ?? "确认服务不可用"}` };
    }
    const deadline = Date.now() + confirmTimeoutMs();
    for (;;) {
        const status = await account.confirmationStatus(created.id);
        if (status.ok && status.status) {
            if (status.status === "approved") return { ok: true, reason: "" };
            if (status.status === "rejected") return { ok: false, reason: "用户拒绝了本次生成" };
            if (status.status === "expired") return { ok: false, reason: "确认已超时，未生成，费用已退还" };
        }
        if (Date.now() >= deadline) return { ok: false, reason: "等待用户确认超时，未生成，费用已退还" };
        await new Promise((resolve) => setTimeout(resolve, 2000));
    }
}

export async function runBilledCall(
    name: string,
    args: Record<string, unknown>,
    auth: BilledAuth,
    deps: BilledCallDeps,
): Promise<BilledToolResult> {
    const started = Date.now();
    // 定价来源：remote 模式下后端 reserve 按工具定价（连接器不参与定价，amount=0 表示后端定价）；
    // 画布真实选择的模型从工具参数 model 中提取，随 reserve 传给后端按模型定价。
    // local 内测模式仍由网关本地 pricing 表决定占位价。
    const remotePriced = deps.account.kind === "remote";
    const amountMicro = remotePriced ? 0 : deps.priceOf(name);
    const modelKey = typeof args?.model === "string" && args.model.trim() ? args.model.trim() : undefined;
    const subjectId = auth.type === "key" && auth.keyId ? auth.keyId : undefined;
    const keyName = auth.type === "key" ? (auth.keyName ?? "") : "master";
    const logKey = subjectId ?? "master";
    const stamp = () => ({ ts: new Date().toISOString(), date: today() });

    // ① 执行前 reserve（冻结/预扣），失败直接拒绝，不触碰工具
    let orderId: string | undefined;
    let settledMicro: number | undefined;
    const idemKey = subjectId ? crypto.randomUUID() : "";
    if (subjectId) {
        const rv = await deps.account.reserve(subjectId, amountMicro, idemKey, name, modelKey);
        if (!rv.ok || !rv.orderId) {
            deps.log({ ...stamp(), keyId: logKey, keyName, tool: name, model: modelKey, ok: false, microcredits: amountMicro, error: `reserve_${rv.code ?? "failed"}`, ms: Date.now() - started });
            const hint = rv.code === "insufficient_balance"
                ? "402 Payment Required: 积分不足，请先在网站充值"
                : `计费预检失败：${rv.message ?? "暂时不可用"}`;
            return { content: [{ type: "text", text: `[canvas-bridge] ${hint}` }], isError: true };
        }
        orderId = rv.orderId;
        // remote 由后端返回实际冻结金额（后端定价）；local 用本地价。
        settledMicro = rv.microcredits ?? amountMicro;
    }

    // ①.5 确认门：外部（API key）+ remote 定价 + 生成类工具 → 必须先经用户批准。
    // reserve 已冻结，但绝不未经确认就执行生成；拒绝/超时/服务不可用一律退款。
    if (subjectId && remotePriced && orderId && isGenerationTool(name)) {
        const confirmation = await awaitUserConfirmation(deps.account, subjectId, orderId, name, modelKey, settledMicro ?? 0, args, idemKey);
        if (!confirmation.ok) {
            const rf = await deps.account.refund(orderId, idemKey, confirmation.reason);
            if (!rf.ok) console.error(`[canvas-gateway] 确认未通过 refund 失败 order=${orderId}: ${rf.message ?? ""}`);
            deps.log({ ...stamp(), keyId: logKey, keyName, tool: name, model: modelKey, ok: false, microcredits: settledMicro, error: `confirmation_${confirmation.reason}`, ms: Date.now() - started });
            if (subjectId) await deps.account.recordCall(subjectId, name);
            return { content: [{ type: "text", text: `[canvas-bridge] ${confirmation.reason}` }], isError: true };
        }
        deps.log({ ...stamp(), keyId: logKey, keyName, tool: name, model: modelKey, ok: "approved", microcredits: settledMicro, ms: Date.now() - started });
    }

    try {
        // ② 执行工具
        const result = await deps.callTool(name, args);
        // ③ 成功 → settle；结算失败不能伪装成完整成功（保留工具结果并标记计费异常，不误退款）
        if (subjectId && orderId) {
            const sv = await deps.account.settle(orderId, idemKey);
            if (!sv.ok) {
                deps.log({ ...stamp(), keyId: logKey, keyName, tool: name, ok: true, microcredits: settledMicro, error: `settle_failed:${sv.message ?? ""}`, ms: Date.now() - started });
                await deps.account.recordCall(subjectId, name);
                const text = typeof result === "string" ? result : JSON.stringify(result);
                return {
                    content: [{ type: "text", text: `${text}\n\n[canvas-bridge] 警告：工具已执行但计费结算失败（${sv.message ?? "未知"}），该笔冻结将进入对账，请联系运营。` }],
                    isError: true,
                };
            }
        }
        // 终态完成后才写成功用量（账单数据源）
        deps.log({ ...stamp(), keyId: logKey, keyName, tool: name, ok: true, microcredits: subjectId ? settledMicro : undefined, ms: Date.now() - started });
        if (subjectId) await deps.account.recordCall(subjectId, name);
        const text = typeof result === "string" ? result : JSON.stringify(result);
        return { content: [{ type: "text", text }] };
    } catch (error) {
        // ④ 工具异常 → refund（尽力而为，结果入日志）
        const msg = error instanceof Error ? error.message : String(error);
        if (subjectId && orderId) {
            const rf = await deps.account.refund(orderId, idemKey, msg);
            if (!rf.ok) console.error(`[canvas-gateway] refund 失败 order=${orderId}: ${rf.message ?? ""}`);
        }
        deps.log({ ...stamp(), keyId: logKey, keyName, tool: name, ok: false, error: msg, ms: Date.now() - started });
        if (subjectId) await deps.account.recordCall(subjectId, name);
        return { content: [{ type: "text", text: `[canvas-bridge] ${msg}` }], isError: true };
    }
}
