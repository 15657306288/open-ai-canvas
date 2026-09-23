import { describe, expect, test } from "bun:test";
import axios from "axios";

import { apiClient } from "@/services/api/request";
import { ResourceUploadError, uploadResourceFile } from "@/services/api/resources";

type AdapterResult = { status: number; body: unknown };

// 直传失败的分类是这条边界的全部价值：permanent 决定调用方是当场报错还是退回本机等待重传。
// 测试里把等待换成 no-op，重试逻辑才能在不睡真实时间的前提下被验证。
const noWait = async () => {};
async function uploadWith(result: AdapterResult | Error) {
    const previous = apiClient.defaults.adapter;
    apiClient.defaults.adapter = async (config) => {
        if (result instanceof Error) throw result;
        if (result.status >= 400) {
            throw new axios.AxiosError(`Request failed with status code ${result.status}`, "ERR_BAD_REQUEST", config, undefined, {
                data: result.body,
                status: result.status,
                statusText: "Error",
                headers: {},
                config,
            });
        }
        return { data: result.body, status: result.status, statusText: "OK", headers: {}, config };
    };
    try {
        return await uploadResourceFile(new Blob(["x"], { type: "image/png" }), "image", { fileName: "x.png" }, undefined, { retry: { wait: noWait } });
    } finally {
        apiClient.defaults.adapter = previous;
    }
}

async function uploadFailure(result: AdapterResult | Error) {
    try {
        await uploadWith(result);
    } catch (error) {
        return error;
    }
    throw new Error("上传本应失败");
}

describe("资源直传失败分类", () => {
    test("鉴权、越权与体积超限属于重试不会自愈的永久失败", async () => {
        for (const status of [400, 401, 403, 404, 413, 415]) {
            const error = await uploadFailure({ status, body: { code: status, data: null, msg: "" } });
            expect(error).toBeInstanceOf(ResourceUploadError);
            expect((error as ResourceUploadError).permanent).toBe(true);
            expect((error as ResourceUploadError).status).toBe(status);
        }
    });

    test("限流与服务端故障属于瞬时失败，允许退回本机后重传", async () => {
        for (const status of [429, 500, 502, 503]) {
            const error = await uploadFailure({ status, body: { code: status, data: null, msg: "" } });
            expect(error).toBeInstanceOf(ResourceUploadError);
            expect((error as ResourceUploadError).permanent).toBe(false);
        }
    });

    test("断网等非 HTTP 失败按瞬时处理", async () => {
        const error = await uploadFailure(new Error("Network Error"));
        expect(error).toBeInstanceOf(ResourceUploadError);
        expect((error as ResourceUploadError).permanent).toBe(false);
    });

    test("保留后端可读文案，并把 multipart 超限翻译成中文", async () => {
        const quota = await uploadFailure({ status: 403, body: { code: 403, data: null, msg: "存储配额不足" } });
        expect((quota as ResourceUploadError).message).toBe("存储配额不足");

        const oversize = await uploadFailure({ status: 400, body: { code: 400, data: null, msg: "http: request body too large" } });
        expect((oversize as ResourceUploadError).message).toContain("文件过大");
        expect((oversize as ResourceUploadError).permanent).toBe(true);
    });

    test("成功响应仍返回后端资源本身", async () => {
        const resource = await uploadWith({
            status: 200,
            body: { code: 0, msg: "", data: { resource: { id: "res-1", kind: "image", status: "ready", publicUrl: "", size: 1 } } },
        });
        expect(resource.id).toBe("res-1");
    });
});

// 520 是 Cloudflare 家族的网关错误：边缘节点拿不到有效源站响应。
// 旧代码只会把 axios 的英文原文透出去，用户看到的是不可行动的报错。
describe("资源直传的瞬时故障与幂等重试", () => {
    test("520 网关错误属于瞬时失败，并翻译成可读中文", async () => {
        const error = await uploadFailure({ status: 520, body: { code: 520, data: null, msg: "" } });
        expect(error).toBeInstanceOf(ResourceUploadError);
        expect((error as ResourceUploadError).permanent).toBe(false);
        expect((error as ResourceUploadError).message).toContain("520");
        expect((error as ResourceUploadError).message).toContain("云端网关");
    });

    test("幂等键命中「同一素材正在上传」的 409 可重试，不是终态失败", async () => {
        const error = await uploadFailure({ status: 409, body: { code: 409, data: null, msg: "相同素材正在上传，请稍后重试", retryable: true } });
        expect(error).toBeInstanceOf(ResourceUploadError);
        expect((error as ResourceUploadError).permanent).toBe(false);
    });

    test("旧后端不带 retryable 字段时，409 靠文案也能识别为瞬时", async () => {
        const error = await uploadFailure({ status: 409, body: { code: 409, data: null, msg: "相同素材正在上传，请稍后重试" } });
        expect((error as ResourceUploadError).permanent).toBe(false);
    });

    test("网关 502 后重试成功，且整条重试链复用同一幂等键", async () => {
        const previous = apiClient.defaults.adapter;
        const idempotencyKeys: Array<string | undefined> = [];
        let attempt = 0;
        apiClient.defaults.adapter = async (config) => {
            idempotencyKeys.push(String(config.headers?.["X-Idempotency-Key"] ?? "") || undefined);
            attempt += 1;
            if (attempt === 1) {
                throw new axios.AxiosError("Request failed with status code 502", "ERR_BAD_RESPONSE", config, undefined, {
                    data: { code: 502, data: null, msg: "" },
                    status: 502,
                    statusText: "Bad Gateway",
                    headers: {},
                    config,
                });
            }
            return { data: { code: 0, msg: "", data: { resource: { id: "res-2", kind: "image", status: "ready", publicUrl: "", size: 1 } } }, status: 200, statusText: "OK", headers: {}, config };
        };
        try {
            const resource = await uploadResourceFile(new Blob(["x"], { type: "image/png" }), "image", { fileName: "x.png" }, undefined, { retry: { wait: noWait } });
            expect(resource.id).toBe("res-2");
            expect(attempt).toBe(2);
            expect(idempotencyKeys[0]).toBeTruthy();
            expect(idempotencyKeys[1]).toBe(idempotencyKeys[0]);
            // 自动生成的幂等键要保持简短：后端会摘要成 64 位，但不应靠后端做长度兑底。
            expect((idempotencyKeys[0] as string).length).toBeLessThanOrEqual(64);
        } finally {
            apiClient.defaults.adapter = previous;
        }
    });

    test("永久失败不重试，只发一次请求", async () => {
        const previous = apiClient.defaults.adapter;
        let attempt = 0;
        apiClient.defaults.adapter = async (config) => {
            attempt += 1;
            throw new axios.AxiosError("Request failed with status code 401", "ERR_BAD_REQUEST", config, undefined, {
                data: { code: 401, data: null, msg: "未登录" },
                status: 401,
                statusText: "Unauthorized",
                headers: {},
                config,
            });
        };
        try {
            await uploadResourceFile(new Blob(["x"], { type: "image/png" }), "image", { fileName: "x.png" }, undefined, { retry: { wait: noWait } });
            throw new Error("上传本应失败");
        } catch (error) {
            expect(error).toBeInstanceOf(ResourceUploadError);
            expect((error as ResourceUploadError).permanent).toBe(true);
            expect(attempt).toBe(1);
        } finally {
            apiClient.defaults.adapter = previous;
        }
    });
});
