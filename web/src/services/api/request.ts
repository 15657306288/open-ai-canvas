import axios, { type AxiosRequestConfig, type AxiosResponse } from "axios";

export type ApiParams = Record<string, string | string[] | number | number[] | undefined>;

export type BackendEnvelope<T> = {
    code: number;
    data: T;
    msg: string;
    reason?: string;
    /**
     * 后端显式声明的重试语义。缺省时按 HTTP 状态推断，显式 false 优先于推断。
     * 典型场景：上传幂等键命中「同一素材正在上传」返回 409——它其实是并发重试，
     * 稍后用同一幂等键重试就能拿到已就绪的资源，不该被当成终态失败。
     */
    retryable?: boolean;
};

/**
 * 业务错误的统一载体：HTTP 200 只代表传输完成，code/reason 才决定业务是否成功。
 * 调用方应依赖机器可读字段做分支，message 仅用于向用户展示，避免把后端文案当协议解析。
 */

export class ApiError extends Error {
    readonly status?: number;
    readonly code?: number;
    readonly reason?: string;
    readonly retryable: boolean;
    readonly retryAfterMs?: number;
    readonly cause?: unknown;

    constructor(message: string, options: { status?: number; code?: number; reason?: string; retryable?: boolean; retryAfterMs?: number; cause?: unknown } = {}) {
        super(message);
        this.name = "ApiError";
        this.status = options.status;
        this.code = options.code;
        this.reason = options.reason;
        this.retryable = options.retryable ?? isRetryableStatus(options.status ?? options.code);
        this.retryAfterMs = options.retryAfterMs;
        this.cause = options.cause;
    }
}

// 所有后端 JSON 请求共用同一实例，避免认证、Base URL 和错误语义在模块间漂移。
export const apiBaseURL = import.meta.env.VITE_CANVAS_BACKEND_URL || "/api";
export const apiClient = axios.create({ baseURL: apiBaseURL, withCredentials: true });

/**
 * 解包后端业务信封的唯一边界。这里把非零业务 code 转成 ApiError，保留 reason、重试语义和 Retry-After；
 * 取消请求必须继续抛出 AbortError，不能被包装成普通失败，否则页面切换会被误报为错误。
 */

export async function request<T>(promise: Promise<{ data: BackendEnvelope<T>; status?: number; headers?: unknown }>) {
    try {
        const response = await promise;
        if (response.data.code !== 0) {
            throw new ApiError(response.data.msg || "请求失败", {
                status: response.status,
                code: response.data.code,
                reason: response.data.reason,
                // 后端显式声明优先：只有它知道 409 是「同一素材正在上传」这种可重试冲突。
                retryable: response.data.retryable ?? (isRetryableStatus(response.status) || isRetryableStatus(response.data.code)),
                retryAfterMs: retryAfterMilliseconds(response.headers),
            });
        }
        return response.data.data;
    } catch (error) {
        throw unwrapTransportError(error);
    }
}

function unwrapTransportError(error: unknown): never {
    if (error instanceof ApiError || (error instanceof DOMException && error.name === "AbortError")) {
        throw error;
    }
    if (axios.isCancel(error) || (axios.isAxiosError(error) && error.code === axios.AxiosError.ERR_CANCELED)) {
        throw new DOMException("请求已取消", "AbortError");
    }
    if (axios.isAxiosError<BackendEnvelope<unknown>>(error)) {
        const status = error.response?.status;
        const code = error.response?.data?.code;
        throw new ApiError(error.response?.data?.msg || transportFailureMessage(status, error.message), {
            status,
            code,
            reason: error.response?.data?.reason,
            retryable: error.response?.data?.retryable ?? (isRetryableStatus(status) || isRetryableStatus(code)),
            retryAfterMs: retryAfterMilliseconds(error.response?.headers),
            cause: error,
        });
    }
    throw error;
}

function transportFailureMessage(status?: number, fallback?: string) {
    switch (status) {
        case 408:
            return "请求超时，请稍后重试";
        case 500:
            return "服务处理失败，请稍后重试";
        case 502:
            return "后端服务暂时不可用，请稍后重试";
        case 503:
            return "服务暂时不可用，请稍后重试";
        case 504:
            return "服务响应超时，请稍后重试";
        // Cloudflare 家族的网关错误：520/521/522/523/524/525/526 都是边缘节点拿不到有效的源站响应。
        // 不翻译的话，浏览器只能拿到 axios 的英文 "Request failed with status code 520"。
        case 520:
        case 521:
        case 522:
        case 523:
        case 524:
        case 525:
        case 526:
            return `云端网关暂时不可用（HTTP ${status}），请稍后重试`;
        default:
            return fallback || "请求失败";
    }
}

function isRetryableStatus(status?: number) {
    return status === 408 || status === 425 || status === 429 || (status !== undefined && status >= 500 && status <= 599);
}

function retryAfterMilliseconds(headers: unknown) {
    if (!headers || typeof headers !== "object") return undefined;
    const headerBag = headers as { get?: (name: string) => unknown; [key: string]: unknown };
    const rawValue = headerBag.get?.("retry-after") ?? headerBag["retry-after"] ?? headerBag["Retry-After"];
    const value = Array.isArray(rawValue) ? rawValue[0] : rawValue;
    if (value === undefined || value === null) return undefined;
    const text = String(value).trim();
    if (!text) return undefined;
    const seconds = Number(text);
    if (Number.isFinite(seconds) && seconds >= 0) return Math.ceil(seconds * 1000);
    const retryAt = Date.parse(text);
    if (!Number.isFinite(retryAt)) return undefined;
    return Math.max(0, retryAt - Date.now());
}

export type HttpRequestConfig = Omit<AxiosRequestConfig, "method" | "url" | "data" | "baseURL">;

async function send<T>(method: string, url: string, data?: unknown, config?: HttpRequestConfig) {
    return request<T>(apiClient.request<BackendEnvelope<T>>({ method, url, data, ...config }));
}

/**
 * 业务 JSON 的唯一调用入口。模块不要再写 `request(apiClient.get(...))`，也不要再 `axios.create`。
 * 拦截器仍挂在 `apiClient` 上；流式 fetch、媒体 blob 和渠道中转走各自边界。
 */
export const http = {
    get: <T>(url: string, config?: HttpRequestConfig) => send<T>("get", url, undefined, config),
    post: <T>(url: string, data?: unknown, config?: HttpRequestConfig) => send<T>("post", url, data, config),
    put: <T>(url: string, data?: unknown, config?: HttpRequestConfig) => send<T>("put", url, data, config),
    patch: <T>(url: string, data?: unknown, config?: HttpRequestConfig) => send<T>("patch", url, data, config),
    delete: <T>(url: string, config?: HttpRequestConfig) => send<T>("delete", url, undefined, config),
    async raw<T>(config: AxiosRequestConfig): Promise<AxiosResponse<T>> {
        try {
            return await apiClient.request<T>(config);
        } catch (error) {
            throw unwrapTransportError(error);
        }
    },
};

export function compactApiParams(params: ApiParams) {
    return Object.fromEntries(Object.entries(params).filter(([, value]) => value !== "" && value !== undefined && (!Array.isArray(value) || value.length > 0))) as ApiParams;
}

export function serializeApiParams(params?: ApiParams) {
    const queryParams = new URLSearchParams();
    for (const [key, value] of Object.entries(params || {})) {
        if (value === undefined) continue;
        if (Array.isArray(value)) value.forEach((item) => queryParams.append(key, String(item)));
        else queryParams.set(key, String(value));
    }
    return queryParams;
}
