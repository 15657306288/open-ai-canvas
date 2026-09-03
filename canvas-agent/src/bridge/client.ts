// [connector] P0-B-3 远程主动外连 bridge —— Client（本地 Runtime 侧）
//
// 本地 Runtime 主动外连 Broker（零入站端口）：
//   - register 注册（bridgeId/token/endpoint/capabilities）
//   - 循环长轮询 poll 拉取请求
//   - 收到请求 → 转发给本地 Runtime /api/tools（masterToken 鉴权）→ result 回传 Broker
//   - 30s 心跳上报在线状态与能力
//
// 复用 P0-A-4 agent-fetch（keepalive + 超时），网络稳定性与主链路一致。

import { agentFetch } from "../agent-fetch.js";

export interface CanvasBridgeClientOptions {
    /** Broker 地址（http/https），例如 https://broker.example.com */
    server: string;
    /** 本 bridge 唯一标识（例如本机 hostname + runtime 端口） */
    bridgeId: string;
    /** 连接 Broker 的凭据（Bearer token），由 Broker 侧分配 */
    token: string;
    /** 远程 Agent 可见的本地 Runtime 地址（用于能力上报，不对外开放入站） */
    endpoint: string;
    /** 本地 Runtime 的 masterToken（调用 /api/tools 用） */
    runtimeToken: string;
    /** 轮询等待秒数（长轮询），默认 25 */
    pollSeconds?: number;
    /** 心跳间隔秒数，默认 30 */
    heartbeatSeconds?: number;
    /** 能力上报（如已加载的模型目录版本、工具列表），可空 */
    capabilities?: Record<string, unknown>;
    /** 队列空时两次 poll 之间的最小间隔 ms，默认 200 */
    pollGapMs?: number;
}

export interface CanvasBridgeClient {
    /** 已启动标志 */
    readonly started: boolean;
    /** 启动（register + 心跳 + 轮询循环），返回后持续后台运行 */
    start: () => Promise<void>;
    /** 停止（清理定时器与循环） */
    stop: () => void;
    /** 累计处理成功/失败计数（供监控） */
    stats: () => { processed: number; failed: number };
}

export function createCanvasBridgeClient(options: CanvasBridgeClientOptions): CanvasBridgeClient {
    const server = options.server.replace(/\/+$/, "");
    const pollSeconds = options.pollSeconds ?? 25;
    const heartbeatSeconds = options.heartbeatSeconds ?? 30;
    const pollGapMs = options.pollGapMs ?? 200;

    let started = false;
    let stopped = false;
    let heartbeatTimer: ReturnType<typeof setInterval> | undefined;
    let pollLoopTimer: ReturnType<typeof setTimeout> | undefined;
    let processed = 0;
    let failed = 0;

    async function bridgeRequest<T = unknown>(path: string, init: RequestInit = {}, body?: unknown): Promise<{ code: number; data: T; msg?: string }> {
        const res = await agentFetch(`${server}${path}`, {
            ...init,
            headers: {
                authorization: `Bearer ${options.token}`,
                "content-type": "application/json",
                ...(init.headers ?? {}),
            },
            ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
            timeoutMs: 40_000,
        });
        const envelope = (await res.json()) as { code: number; data: T; msg?: string };
        if (envelope.code !== 0) {
            throw new Error(envelope.msg || `Bridge HTTP ${res.status}`);
        }
        return envelope;
    }

    async function sendHeartbeat() {
        try {
            await bridgeRequest("/api/canvas-bridge/heartbeat", { method: "POST" }, {
                bridgeId: options.bridgeId,
                capabilities: options.capabilities,
            });
        } catch {
        // 心跳失败不致命，下轮重试；poll 本身也会刷新 lastSeenAt
        }
    }

    async function executeRequest(requestId: string, name: string, input: Record<string, unknown>) {
        // 转发到本地 Runtime /api/tools（masterToken 鉴权）
        const res = await agentFetch(`${options.endpoint}/api/tools`, {
            method: "POST",
            headers: { "content-type": "application/json", "x-canvas-agent-token": options.runtimeToken },
            body: JSON.stringify({ name, input }),
            timeoutMs: 120_000,
        });
        const body = (await res.json()) as { ok?: boolean; result?: unknown; error?: string };
        if (!body.ok) throw new Error(body.error || "Runtime tool call failed");
        return body.result;
    }

    async function submitResult(bridgeId: string, requestId: string, status: "succeeded" | "failed", payload: { result?: unknown; error?: string }) {
        await bridgeRequest("/api/canvas-bridge/result", { method: "POST" }, { bridgeId, requestId, status, ...payload });
    }

    async function pollOnce(): Promise<void> {
        const envelope = await bridgeRequest<{ request: { requestId: string; name: string; input: Record<string, unknown> } | null }>(
            `/api/canvas-bridge/poll?bridgeId=${encodeURIComponent(options.bridgeId)}&wait=${pollSeconds}`,
            { method: "GET" },
        );
        const request = envelope.data.request;
        if (!request) return; // 无请求，短暂间隔后再轮询
        try {
            const result = await executeRequest(request.requestId, request.name, request.input);
            await submitResult(options.bridgeId, request.requestId, "succeeded", { result });
            processed++;
        } catch (error) {
            failed++;
            try {
                await submitResult(options.bridgeId, request.requestId, "failed", {
                    error: error instanceof Error ? error.message : "bridge execute failed",
                });
            } catch {
            // 结果回传失败时尽力而为；Broker 队列会保留该请求以便人工排查
            }
        }
    }

    async function pollLoop() {
        while (!stopped) {
            try {
                await pollOnce();
            } catch {
            // poll 网络异常：休眠后重试（桥连重试机制）
            }
            if (!stopped) {
                await new Promise((resolve) => {
                    pollLoopTimer = setTimeout(resolve, pollGapMs);
                });
            }
        }
    }

    return {
        get started() {
            return started;
        },
        async start() {
            if (started) return;
            started = true;
            stopped = false;
            // 注册（携带 endpoint + capabilities）
            await bridgeRequest("/api/canvas-bridge/register", { method: "POST" }, {
                bridgeId: options.bridgeId,
                token: options.token,
                endpoint: options.endpoint,
                capabilities: options.capabilities,
            });
            await sendHeartbeat();
            heartbeatTimer = setInterval(() => void sendHeartbeat(), heartbeatSeconds * 1000);
            void pollLoop();
        },
        stop() {
            stopped = true;
            started = false;
            if (heartbeatTimer) clearInterval(heartbeatTimer);
            if (pollLoopTimer) clearTimeout(pollLoopTimer);
            heartbeatTimer = undefined;
            pollLoopTimer = undefined;
        },
        stats() {
            return { processed, failed };
        },
    };
}
