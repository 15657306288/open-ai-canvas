import { expect, test } from "bun:test";
import { resolveAutoRoute, sameRouteModel, type RouteInput } from "../src/pages/ecommerce/video/auto-route";
import type { VideoReplicationRoute, VideoReplicationRouteOption } from "../src/services/api/video-replication";

const input: RouteInput = { durationMs: 25000, videoType: "product", replaceMode: "product" };
const model = { channelId: "system-a", modelKey: "models/video" };
const options = [{ value: "available", ref: { channelId: "system-a", modelKey: "video" } }];
const option: VideoReplicationRouteOption = { model, label: "A", track: "reference", tasks: 2, taskSeconds: [13, 12], faceSafe: false, clipsPerTask: 2, estimatedMicrocredits: 0, priced: false };
const route: VideoReplicationRoute = { sourceSeconds: 25, preference: "continuity", reason: "", recommended: option, options: null };

test("自动选中开放模型，使用服务端返回的轨道与时长方案", () => {
    for (const track of ["reference", "script"] as const) {
        const result = resolveAutoRoute(input, { input, route: { ...route, recommended: { ...option, track } } }, options);
        expect(result.status).toBe("ready");
        expect(result.model).toBe("available");
        expect(result.route?.recommended?.track).toBe(track);
        expect(result.route?.recommended?.taskSeconds).toEqual([13, 12]);
    }
});
test("裁剪或变更替换对象后不复用旧响应；手动模式忽略迟到结果", () => {
    const reply = { input, route };
    expect(resolveAutoRoute({ ...input, durationMs: 5000 }, reply, options)).toEqual({ status: "pending" });
    expect(resolveAutoRoute({ ...input, videoType: "face" }, reply, options)).toEqual({ status: "pending" });
    expect(resolveAutoRoute(null, reply, options)).toEqual({ status: "idle" });
});
test("不向无权限用户显示或套用推荐分段，失败和无推荐保留原因", () => {
    expect(resolveAutoRoute(input, { input, route }, []).status).toBe("unavailable");
    expect(resolveAutoRoute(input, { input, route }, [{ value: "other", ref: { ...model, channelId: "other" } }]).route).toBeUndefined();
    expect(resolveAutoRoute(input, { input, error: "网络不可用" }, options).reason).toBe("网络不可用");
    expect(resolveAutoRoute(input, { input, route: { ...route, recommended: undefined, reason: "时长不匹配" } }, options).reason).toBe("时长不匹配");
});
test("逻辑模型身份优先，不能凭同名渠道模型或缺失的键匹配", () => {
    expect(sameRouteModel({ logicalModelId: "L" }, { logicalModelId: "L" })).toBe(true);
    expect(sameRouteModel({ ...model, logicalModelId: "L" }, model)).toBe(false);
    expect(sameRouteModel({}, {})).toBe(false);
});
