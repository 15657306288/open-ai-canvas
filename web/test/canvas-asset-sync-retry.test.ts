import { describe, expect, test } from "bun:test";

import { ApiError } from "@/services/api/request";
import { retryCanvasAssetSyncOnTransientFailure } from "@/services/project-asset-sync";

// 资产入库失败会让用户看到「生成结果已保留，但项目资产同步失败」——结果其实已经生成好了，
// 只是入库这一次请求丢了。这里验证：临时故障会重试，永久失败立即抛出，限流尊重 Retry-After。

async function runWithFailures(failures: unknown[]) {
    const waits: number[] = [];
    let calls = 0;
    const result = await retryCanvasAssetSyncOnTransientFailure(
        async () => {
            const failure = failures[calls];
            calls += 1;
            if (failure !== undefined) throw failure;
            return "ok";
        },
        { wait: async (delayMs) => { waits.push(delayMs); } },
    ).catch((error) => error);
    return { result, calls, waits };
}

describe("画布资产入库的瞬时故障重试", () => {
    test("网关 520 后重试成功，不再把结果标记为同步失败", async () => {
        const gateway = new ApiError("云端网关暂时不可用（HTTP 520），请稍后重试", { status: 520 });
        const { result, calls, waits } = await runWithFailures([gateway]);
        expect(result).toBe("ok");
        expect(calls).toBe(2);
        expect(waits).toHaveLength(1);
    });

    test("断网（没有 HTTP 响应）也会重试", async () => {
        const offline = new ApiError("网络异常", {});
        const { result, calls } = await runWithFailures([offline]);
        expect(result).toBe("ok");
        expect(calls).toBe(2);
    });

    test("429 重试并尊重后端给出的 Retry-After", async () => {
        const limited = new ApiError("请求过于频繁", { status: 429, retryAfterMs: 5_000 });
        const { result, waits } = await runWithFailures([limited]);
        expect(result).toBe("ok");
        expect(waits[0]).toBe(5_000);
    });

    test("永久失败立即抛出，不做无意义重试", async () => {
        const unauthorized = new ApiError("未登录", { status: 401 });
        const { result, calls } = await runWithFailures([unauthorized]);
        expect(result).toBeInstanceOf(ApiError);
        expect((result as ApiError).status).toBe(401);
        expect(calls).toBe(1);
    });

    test("持续瞬时失败会重试到上限后抛出最后一个错误", async () => {
        const gateway = () => new ApiError("云端网关暂时不可用（HTTP 502），请稍后重试", { status: 502 });
        const { result, calls } = await runWithFailures([gateway(), gateway(), gateway(), gateway(), gateway()]);
        expect(result).toBeInstanceOf(ApiError);
        expect((result as ApiError).status).toBe(502);
        // 默认 maxRetries = 2：首次 + 2 次重试。
        expect(calls).toBe(3);
    });
});
