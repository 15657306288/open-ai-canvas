/**
 * billing-lifecycle —— 单次 MCP 工具调用的两阶段计费生命周期（纯逻辑，可单测）。
 *
 * 从 gateway-server 抽出，避免测试导入 HTTP 服务器模块时触发拉取 Runtime schema 等副作用。
 * 流程：reserve（失败不执行工具）→ callTool → 成功 settle / 异常 refund；master 内部调用不计费。
 * 金额一律为正整数 microcredits（微积分）；任何 reserve/settle 失败都 fail-closed，不伪装成功。
 */
import crypto from "node:crypto";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { today } from "./gateway-keys.js";
import type { AccountProvider } from "./account-provider.js";

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
