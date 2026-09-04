// [connector] P0-B-3 远程主动外连 bridge —— Broker（中继服务）端
//
// 设计目标（Q3 拍板：远程走主动外连 bridge，本地零入站端口）：
//   本地 Runtime 的 BridgeClient 主动外连本 Broker（长轮询拉取请求），
//   远程 Agent 通过 Broker 的 HTTP API 提交"工具调用"，Broker 将其排入
//   对应 bridge 的队列，本地 Client 轮询到后转发给 Runtime /api/tools，
//   结果经 Broker 回传给远程 Agent。
//
// 协议与 comfy-bridge（canvas-agent/native/comfy-bridge）保持同构：
//   - 信封统一为 { code: 0, data } 成功 / { code: <非0>, msg } 失败
//   - client 侧请求带 Authorization: Bearer <token>
//   - poll 为长轮询（wait 秒），队列空时挂起等待
//
// Broker 是自托管中继，用原生 node:http 实现（零依赖、可独立部署）。

import type { IncomingMessage, ServerResponse } from "node:http";

export interface CanvasBridgeRequest {
    /** 全局唯一请求 id */
    requestId: string;
    /** 远程 Agent 要调用的画布工具名（对应 schemas.ts 的 toolNames） */
    name: string;
    /** 工具入参 */
    input: Record<string, unknown>;
}

export interface CanvasBridgePendingRequest extends CanvasBridgeRequest {
    status: "pending" | "running" | "done";
    result?: unknown;
    error?: string;
    createdAtMs: number;
}

export interface CanvasBridgeRecord {
    bridgeId: string;
    token: string;
    endpoint: string;
    capabilities?: Record<string, unknown>;
    lastSeenAt: number;
    queue: CanvasBridgePendingRequest[];
    /** [connector] P2 §9.5 限流：远程 Agent 提交请求的时间戳窗口 */
    requestTimestamps: number[];
}

export interface CanvasBridgeBrokerOptions {
    /** 队列中请求的最大保留数（防止结果无人消费时无限堆积） */
    maxPendingPerBridge?: number;
    /** 长轮询最大等待秒数 */
    maxWaitSeconds?: number;
    /** 心跳超时判定秒数（超过视为离线） */
    offlineAfterSeconds?: number;
    /** [connector] P2 §9.5 每 bridge 的远程提交限流（默认 60 次/60s，超限返回 429） */
    rateLimit?: { maxRequests: number; windowMs: number };
}

export interface CanvasBridgeBroker {
    /** 注入到 http server 的请求处理函数 */
    handle: (req: IncomingMessage, res: ServerResponse) => void;
    /** 当前在线 bridge 列表（供管理/展示） */
    listBridges: () => CanvasBridgeRecord[];
    /** 销毁（释放定时器） */
    close: () => void;
    /** 供测试：直接入队一个请求并返回 requestId */
    enqueueForTest: (bridgeId: string, request: Omit<CanvasBridgeRequest, "requestId">) => string | undefined;
}

export function createCanvasBridgeBroker(options: CanvasBridgeBrokerOptions = {}): CanvasBridgeBroker {
    const maxPending = options.maxPendingPerBridge ?? 200;
    const maxWaitMs = (options.maxWaitSeconds ?? 25) * 1000;
    const offlineAfterMs = (options.offlineAfterSeconds ?? 90) * 1000;
    const rateLimit = options.rateLimit ?? { maxRequests: 60, windowMs: 60_000 };

    const bridges = new Map<string, CanvasBridgeRecord>();
    /** bridgeId -> 正在长轮询等待的 resolver 列表 */
    const waiters = new Map<string, Array<() => void>>();

    const now = () => Date.now();

    function getOrCreateBridge(bridgeId: string, token: string, endpoint: string): CanvasBridgeRecord {
        let record = bridges.get(bridgeId);
        if (!record) {
            record = { bridgeId, token, endpoint, lastSeenAt: now(), queue: [], requestTimestamps: [] };
            bridges.set(bridgeId, record);
        }
        // 更新 token/endpoint（允许重注册刷新）
        record.token = token;
        record.endpoint = endpoint;
        record.lastSeenAt = now();
        return record;
    }

    // [connector] P2 §9.5 限流：窗口内请求数超上限返回 true（应拒绝并回 429）
    function rateLimited(bridgeId: string): boolean {
        const record = bridges.get(bridgeId);
        if (!record) return true;
        const windowStart = now() - rateLimit.windowMs;
        const recent = record.requestTimestamps.filter((t) => t > windowStart);
        if (recent.length >= rateLimit.maxRequests) {
            record.requestTimestamps = recent;
            return true;
        }
        recent.push(now());
        record.requestTimestamps = recent;
        return false;
    }

    function notifyWaiters(bridgeId: string) {
        const list = waiters.get(bridgeId);
        if (list) {
            waiters.delete(bridgeId);
            for (const resolve of list) resolve();
        }
    }

    function enqueue(bridgeId: string, request: Omit<CanvasBridgeRequest, "requestId">): string | undefined {
        const record = bridges.get(bridgeId);
        if (!record) return undefined;
        if (record.queue.length >= maxPending) {
            // 队满：丢弃最旧的一条，保证新请求可入队
            record.queue.shift();
        }
        const pending: CanvasBridgePendingRequest = {
            requestId: `${bridgeId}-${now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
            name: request.name,
            input: request.input ?? {},
            status: "pending",
            createdAtMs: now(),
        };
        record.queue.push(pending);
        notifyWaiters(bridgeId);
        return pending.requestId;
    }

    function authorize(req: IncomingMessage, record: CanvasBridgeRecord): boolean {
        const auth = req.headers.authorization ?? "";
        const expected = `Bearer ${record.token}`;
        // 常量时间比较，避免时序侧信道（token 是连接凭据）
        if (auth.length !== expected.length) return false;
        let diff = 0;
        for (let i = 0; i < auth.length; i++) diff |= auth.charCodeAt(i) ^ expected.charCodeAt(i);
        return diff === 0;
    }

    async function readBody(req: IncomingMessage): Promise<unknown> {
        const chunks: Buffer[] = [];
        for await (const chunk of req) chunks.push(chunk as Buffer);
        const raw = Buffer.concat(chunks).toString("utf8");
        if (!raw.trim()) return {};
        try {
            return JSON.parse(raw);
        } catch {
            return { _parseError: raw.slice(0, 200) };
        }
    }

    function send(res: ServerResponse, status: number, body: unknown) {
        if (res.headersSent) return;
        res.statusCode = status;
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify(body));
    }
    const ok = (res: ServerResponse, data: unknown) => send(res, 200, { code: 0, data });
    const fail = (res: ServerResponse, status: number, code: number, msg: string) => send(res, status, { code, msg });

    const handle = async (req: IncomingMessage, res: ServerResponse) => {
        const url = new URL(req.url ?? "/", "http://localhost");
        const path = url.pathname;

        // ---------- client 侧（本地 BridgeClient） ----------
        if (path === "/api/canvas-bridge/register" && req.method === "POST") {
            const body = (await readBody(req)) as { bridgeId?: string; token?: string; endpoint?: string; capabilities?: Record<string, unknown> };
            if (!body.bridgeId || !body.token || !body.endpoint) {
                fail(res, 400, 40001, "register 需要 bridgeId/token/endpoint");
                return;
            }
            getOrCreateBridge(body.bridgeId, body.token, body.endpoint);
            const record = bridges.get(body.bridgeId)!;
            record.capabilities = body.capabilities;
            ok(res, { ok: true, bridgeId: body.bridgeId });
            return;
        }

        if (path === "/api/canvas-bridge/heartbeat" && req.method === "POST") {
            const body = (await readBody(req)) as { bridgeId?: string; capabilities?: Record<string, unknown> };
            if (!body.bridgeId) {
                fail(res, 400, 40002, "heartbeat 需要 bridgeId");
                return;
            }
            const record = bridges.get(body.bridgeId);
            if (!record) {
                fail(res, 404, 40401, `bridge 未注册：${body.bridgeId}`);
                return;
            }
            if (!authorize(req, record)) {
                fail(res, 401, 40101, "鉴权失败");
                return;
            }
            record.lastSeenAt = now();
            if (body.capabilities) record.capabilities = body.capabilities;
            ok(res, { ok: true });
            return;
        }

        if (path === "/api/canvas-bridge/poll" && req.method === "GET") {
            const bridgeId = url.searchParams.get("bridgeId") ?? "";
            const waitSec = Math.min(Math.max(Number(url.searchParams.get("wait")) || 0, 0), maxWaitMs / 1000);
            if (!bridgeId) {
                fail(res, 400, 40003, "poll 需要 bridgeId");
                return;
            }
            const record = bridges.get(bridgeId);
            if (!record) {
                fail(res, 404, 40401, `bridge 未注册：${bridgeId}`);
                return;
            }
            if (!authorize(req, record)) {
                fail(res, 401, 40101, "鉴权失败");
                return;
            }
            record.lastSeenAt = now();

            // 队列有请求 → 立即取队首（标记 running）
            const first = record.queue.find((item) => item.status === "pending");
            if (first) {
                first.status = "running";
                ok(res, { request: { requestId: first.requestId, name: first.name, input: first.input } });
                return;
            }
            // 队列空 → 长轮询等待最多 wait 秒
            const waitMs = Math.min(waitSec * 1000, maxWaitMs);
            const arrived = await new Promise<boolean>((resolve) => {
                let settled = false;
                const timer = setTimeout(() => {
                    if (!settled) {
                        settled = true;
                        removeWaiter();
                        resolve(false);
                    }
                }, waitMs);
                const wake = () => {
                    if (!settled) {
                        settled = true;
                        clearTimeout(timer);
                        removeWaiter();
                        resolve(true);
                    }
                };
                const removeWaiter = () => {
                    const list = waiters.get(bridgeId);
                    if (list) {
                        const index = list.indexOf(wake);
                        if (index >= 0) list.splice(index, 1);
                    }
                };
                const list = waiters.get(bridgeId) ?? [];
                list.push(wake);
                waiters.set(bridgeId, list);
            });
            if (!arrived) {
                ok(res, { request: null });
                return;
            }
            const next = record.queue.find((item) => item.status === "pending");
            if (next) {
                next.status = "running";
                ok(res, { request: { requestId: next.requestId, name: next.name, input: next.input } });
            } else {
                ok(res, { request: null });
            }
            return;
        }

        if (path === "/api/canvas-bridge/result" && req.method === "POST") {
            const body = (await readBody(req)) as { bridgeId?: string; requestId?: string; status?: string; result?: unknown; error?: string };
            if (!body.bridgeId || !body.requestId) {
                fail(res, 400, 40004, "result 需要 bridgeId/requestId");
                return;
            }
            const record = bridges.get(body.bridgeId);
            if (!record) {
                fail(res, 404, 40401, `bridge 未注册：${body.bridgeId}`);
                return;
            }
            if (!authorize(req, record)) {
                fail(res, 401, 40101, "鉴权失败");
                return;
            }
            const item = record.queue.find((q) => q.requestId === body.requestId);
            if (!item) {
                fail(res, 404, 40402, `请求不存在或已消费：${body.requestId}`);
                return;
            }
            item.status = body.status === "failed" ? "done" : "done";
            if (body.status === "failed") {
                item.error = body.error ?? "执行失败";
            } else {
                item.result = body.result;
            }
            ok(res, { ok: true });
            return;
        }

        // ---------- 远程 Agent 侧 ----------
        if (path === "/api/canvas-bridge/request" && req.method === "POST") {
            const body = (await readBody(req)) as { bridgeId?: string; name?: string; input?: Record<string, unknown> };
            if (!body.bridgeId || !body.name) {
                fail(res, 400, 40005, "request 需要 bridgeId/name");
                return;
            }
            const record = bridges.get(body.bridgeId);
            if (!record) {
                fail(res, 404, 40401, `bridge 未注册：${body.bridgeId}`);
                return;
            }
            // [connector] P2 §9.5 限流：远程提交侧按 bridge 限频，超限 429
            if (rateLimited(body.bridgeId)) {
                fail(res, 429, 42901, "请求过于频繁，请稍后重试（限流窗口内已达上限）");
                return;
            }
            const requestId = enqueue(body.bridgeId, { name: body.name, input: body.input ?? {} });
            if (!requestId) {
                fail(res, 409, 40901, "bridge 队列不可用");
                return;
            }
            ok(res, { requestId, status: "pending" });
            return;
        }

        const resultMatch = path.match(/^\/api\/canvas-bridge\/request\/([^/]+)$/);
        if (resultMatch && req.method === "GET") {
            const requestId = decodeURIComponent(resultMatch[1]);
            for (const record of bridges.values()) {
                const item = record.queue.find((q) => q.requestId === requestId);
                if (item) {
                    if (item.status === "done") {
                        if (item.error) {
                            ok(res, { requestId, status: "failed", error: item.error });
                        } else {
                            ok(res, { requestId, status: "succeeded", result: item.result });
                        }
                    } else {
                        ok(res, { requestId, status: item.status });
                    }
                    return;
                }
            }
            fail(res, 404, 40402, `请求不存在：${requestId}`);
            return;
        }

        if (path === "/api/canvas-bridge/bridges" && req.method === "GET") {
            const list = Array.from(bridges.values()).map((record) => ({
                bridgeId: record.bridgeId,
                endpoint: record.endpoint,
                online: now() - record.lastSeenAt < offlineAfterMs,
                lastSeenAt: record.lastSeenAt,
                pending: record.queue.filter((q) => q.status !== "done").length,
                capabilities: record.capabilities ?? null,
            }));
            ok(res, { bridges: list });
            return;
        }

        fail(res, 404, 40400, `Not found: ${req.method} ${path}`);
    };

    // 定期清理超过心跳超时的"幽灵"bridge 及其队列
    const sweepTimer = setInterval(() => {
        const cutoff = now() - offlineAfterMs;
        for (const [bridgeId, record] of bridges) {
            if (record.lastSeenAt < cutoff && record.queue.every((q) => q.status === "done")) {
                bridges.delete(bridgeId);
            }
        }
    }, offlineAfterMs);

    return {
        handle,
        listBridges: () => Array.from(bridges.values()),
        enqueueForTest: (bridgeId, request) => enqueue(bridgeId, request),
        close: () => clearInterval(sweepTimer),
    };
}
