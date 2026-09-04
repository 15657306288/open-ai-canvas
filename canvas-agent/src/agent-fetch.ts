// [connector] P0-A-4 agent-fetch：稳定化 Runtime 内外部 HTTP 调用
//
// 背景：masterToken 链接"不稳定"的另一根因是裸 fetch 无 keepalive、无超时、无重试——
// 一个 5xx / 网络抖动 / 慢响应就会让调用直接失败，外部 agent 误以为 Runtime 掉线。
//
// 本模块提供统一的 agentFetch：
//  - keepalive: 连接复用（减少握手/断连，特别对 SSE 场景友好）
//  - timeoutMs: 可配超时（默认 10s），超时以 AbortError 终止
//  - retries: 只读请求（GET/HEAD/OPTIONS）对 5xx/网络异常/超时做指数退避重试（默认额外 2 次）
//  - 非只读请求（POST/PUT 等）不重试（避免重复副作用），但同样受益于 keepalive + 超时
//  - 外部 signal 取消不重试，直接透传

export type AgentFetchInit = RequestInit & {
    /** 请求超时（毫秒），默认 10000 */
    timeoutMs?: number;
    /** 只读请求（GET/HEAD/OPTIONS）的额外重试次数，默认 2；传 0 关闭重试 */
    retries?: number;
};

export async function agentFetch(input: RequestInfo | URL, init: AgentFetchInit = {}): Promise<Response> {
    const { timeoutMs = 10_000, retries = 2, ...fetchInit } = init;
    const method = String(fetchInit.method ?? "GET").toUpperCase();
    const idempotent = method === "GET" || method === "HEAD" || method === "OPTIONS";
    const attempts = idempotent ? Math.max(0, retries) + 1 : 1;
    let lastError: unknown;
    for (let attempt = 0; attempt < attempts; attempt += 1) {
        if (attempt > 0) await delay(Math.min(200 * (2 ** (attempt - 1)), 800));
        const controller = new AbortController();
        const timer = setTimeout(() => controller.abort(), timeoutMs);
        const forwardAbort = () => controller.abort();
        if (init.signal) {
            if (init.signal.aborted) throw new DOMException("The operation was aborted", "AbortError");
            init.signal.addEventListener("abort", forwardAbort, { once: true });
        }
        try {
            const response = await fetch(input, { ...fetchInit, keepalive: true, signal: controller.signal });
            if (idempotent && response.status >= 500 && attempt < attempts - 1) {
                response.body?.cancel();
                lastError = new Error(`HTTP ${response.status}`);
                continue;
            }
            return response;
        } catch (error) {
            if (init.signal?.aborted) throw error;
            if (attempt < attempts - 1) {
                lastError = error;
                continue;
            }
            throw error;
        } finally {
            clearTimeout(timer);
            if (init.signal) init.signal.removeEventListener("abort", forwardAbort);
        }
    }
    throw lastError instanceof Error ? lastError : new Error("agentFetch failed");
}

function delay(ms: number) {
    return new Promise<void>((resolve) => setTimeout(resolve, ms));
}
