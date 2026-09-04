import { useEffect, useState } from "react";

import { getLocalRuntimeSessionClient } from "@/stores/use-local-runtime-store";

// [connector] P0-A-5 前端四态健康面板（P1）
// 后端 /health 返回四态：healthy / reconnecting / degraded / offline
//  - healthy      有活跃画布连接
//  - reconnecting SSE 瞬断，正在等重连（断线宽限中）
//  - degraded     有画布状态但无活跃连接（宽限已过，画布待重新连接）
//  - offline      从未连接过画布（Runtime 已起但画布未就绪）

export type CanvasRuntimeHealthStatus = "healthy" | "reconnecting" | "degraded" | "offline";

export const CANVAS_RUNTIME_HEALTH_META: Record<
    CanvasRuntimeHealthStatus,
    { label: string; color: string; detail: string }
> = {
    healthy: { label: "连接正常", color: "#16a34a", detail: "本机运行时已连接，画布状态实时同步" },
    reconnecting: { label: "正在重连", color: "#d97706", detail: "事件流瞬断，正在等画布重连（宽限期内）" },
    degraded: { label: "画布待重连", color: "#ea580c", detail: "运行时在线，但画布未建立活跃连接，请刷新画布重连" },
    offline: { label: "画布未连接", color: "#6b7280", detail: "运行时尚未连接到任何画布，打开画布后自动建立连接" },
};

const HEALTH_POLL_MS = 5_000;
const HEALTH_TIMEOUT_MS = 4_000;

function isHealthStatus(value: unknown): value is CanvasRuntimeHealthStatus {
    return value === "healthy" || value === "reconnecting" || value === "degraded" || value === "offline";
}

export async function fetchCanvasRuntimeHealth(signal?: AbortSignal): Promise<CanvasRuntimeHealthStatus> {
    const response = await getLocalRuntimeSessionClient().request("/health", { method: "GET", signal });
    if (!response.ok) return "offline";
    const body = (await response.json().catch(() => null)) as { status?: unknown } | null;
    if (!body || !isHealthStatus(body.status)) return "offline";
    return body.status;
}

/** 轮询后端 /health 四态；enabled=false 时回退 offline。返回 null 表示未启用（不展示）。 */
export function useCanvasRuntimeHealth(enabled: boolean): CanvasRuntimeHealthStatus | null {
    const [status, setStatus] = useState<CanvasRuntimeHealthStatus | null>(null);
    useEffect(() => {
        if (!enabled) {
            setStatus(null);
            return;
        }
        let cancelled = false;
        let timer: number | undefined;
        const poll = async () => {
            if (cancelled) return;
            try {
                const control = new AbortController();
                const timeout = window.setTimeout(() => control.abort(), HEALTH_TIMEOUT_MS);
                const next = await fetchCanvasRuntimeHealth(control.signal);
                window.clearTimeout(timeout);
                if (!cancelled) setStatus(next);
            } catch {
                if (!cancelled) setStatus("offline");
            }
            if (!cancelled) timer = window.setTimeout(poll, HEALTH_POLL_MS);
        };
        void poll();
        return () => {
            cancelled = true;
            if (timer !== undefined) window.clearTimeout(timer);
        };
    }, [enabled]);
    return status;
}
