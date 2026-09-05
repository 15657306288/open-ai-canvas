import { Button, Spin } from "antd";
import { ImageIcon, Play, Video, WandSparkles, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import {
    approveAgentConfirmation,
    listPendingAgentConfirmations,
    rejectAgentConfirmation,
    type AgentConfirmation,
} from "@/services/api/agent-confirmations";

/** 工具名 → 中文展示名（外部智能体经网关调用生成类工具前的用户确认）。 */
const toolLabels: Record<string, string> = {
    canvas_generate_text: "生成文本",
    canvas_generate_image: "生成图片",
    canvas_generate_video: "生成视频",
    canvas_generate_audio: "生成音频",
    canvas_run_generation: "批量生成",
};

function toolLabel(tool: string): string {
    return toolLabels[tool] ?? tool;
}

/** 精确十进制积分：microcredits 是整数，1 积分 = 1,000,000 microcredits；不取整、不丢小数。 */
export function formatCredits(microcredits: number): string {
    if (!Number.isFinite(microcredits) || microcredits < 0) return "0";
    const whole = Math.floor(microcredits / 1_000_000);
    const frac = String(microcredits % 1_000_000).padStart(6, "0").replace(/0+$/, "");
    return frac ? `${whole}.${frac}` : String(whole);
}

/**
 * 外部 Agent 生成确认浮层：外部智能体（MCP/API key）调用画布生成工具时，
 * 网关已冻结费用并挂起等待；用户在这里批准或拒绝。批准才真正生成并扣费，拒绝立即退款。
 */
export function CanvasExternalConfirmations() {
    const [items, setItems] = useState<AgentConfirmation[]>([]);
    const [busy, setBusy] = useState<string | null>(null);
    const [enabled, setEnabled] = useState(true);
    const inFlight = useRef(false);

    const refresh = useCallback(async () => {
        if (inFlight.current) return;
        inFlight.current = true;
        try {
            const list = await listPendingAgentConfirmations();
            setItems(list.items ?? []);
        } catch (error) {
            // 401 等鉴权失败：停止轮询（画布页未登录时不需要打扰用户）
            const status = (error as { status?: number })?.status;
            if (status === 401 || status === 403) setEnabled(false);
        } finally {
            inFlight.current = false;
        }
    }, []);

    useEffect(() => {
        if (!enabled) return;
        void refresh();
        const timer = setInterval(() => void refresh(), 3000);
        return () => clearInterval(timer);
    }, [enabled, refresh]);

    const act = useCallback(
        async (id: string, kind: "approve" | "reject") => {
            setBusy(id);
            try {
                if (kind === "approve") await approveAgentConfirmation(id);
                else await rejectAgentConfirmation(id);
                setItems((prev) => prev.filter((it) => it.id !== id));
            } catch {
                // 操作失败保留卡片，等待下轮刷新反映真实状态
            } finally {
                setBusy(null);
            }
        },
        [],
    );

    if (!enabled || items.length === 0) return null;

    return (
        <div className="pointer-events-none fixed bottom-4 right-4 z-[var(--z-toast)] flex w-80 flex-col gap-2">
            {items.map((item) => (
                <div
                    key={item.id}
                    className="pointer-events-auto rounded-xl border border-zinc-200 bg-white p-4 shadow-2xl dark:border-zinc-700 dark:bg-zinc-900"
                >
                    <div className="mb-1 flex items-center justify-between gap-2">
                        <div className="flex items-center gap-1.5 text-sm font-semibold">
                            {item.tool === "canvas_generate_video" ? <Video className="h-4 w-4" /> : item.tool === "canvas_generate_image" ? <ImageIcon className="h-4 w-4" /> : <WandSparkles className="h-4 w-4" />}
                            {toolLabel(item.tool)}
                        </div>
                        <span className="rounded bg-amber-100 px-1.5 py-0.5 text-[11px] font-medium text-amber-700 dark:bg-amber-900/40 dark:text-amber-300">待确认</span>
                    </div>
                    <div className="mb-2 text-xs text-zinc-500 dark:text-zinc-400">
                        {item.modelKey ? `模型 ${item.modelKey} · ` : ""}预计消耗 <span className="font-medium text-zinc-700 dark:text-zinc-200">{formatCredits(item.amountMicrocredits)} 积分</span>
                    </div>
                    {item.promptSummary ? <div className="mb-3 line-clamp-2 text-xs text-zinc-600 dark:text-zinc-300">{item.promptSummary}</div> : null}
                    <div className="flex gap-2">
                        <Button size="small" type="primary" icon={<Play className="h-3 w-3" />} loading={busy === item.id} onClick={() => void act(item.id, "approve")}>
                            批准生成
                        </Button>
                        <Button size="small" icon={<X className="h-3 w-3" />} loading={busy === item.id} onClick={() => void act(item.id, "reject")}>
                            拒绝
                        </Button>
                        <Spin spinning={false} className="ml-auto" size="small" />
                    </div>
                </div>
            ))}
        </div>
    );
}
