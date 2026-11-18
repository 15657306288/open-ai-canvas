import type { VideoReplicationModelRef, VideoReplicationRoute } from "@/services/api/video-replication";

export function sameRouteModel(ref: VideoReplicationModelRef, option: VideoReplicationModelRef) {
    if (ref.logicalModelId || option.logicalModelId) return Boolean(ref.logicalModelId && ref.logicalModelId === option.logicalModelId);
    const key = ref.modelKey?.replace(/^models\//, "");
    return Boolean(ref.channelId && ref.channelId === option.channelId && key && key === option.modelKey?.replace(/^models\//, ""));
}

export type RouteInput = { durationMs: number; videoType: string; replaceMode: string };
export type RouteReply = { input: RouteInput; route?: VideoReplicationRoute; error?: string };

/** 只有当前选区的推荐且模型仍在开放名单内，才可用于展示、报价和提交。 */
export function resolveAutoRoute(input: RouteInput | null, reply: RouteReply | null, options: { value: string; ref: VideoReplicationModelRef }[]) {
    if (!input) return { status: "idle" } as const;
    if (reply?.input !== input) return { status: "pending" } as const;
    const route = reply.route;
    const recommendation = route?.recommended;
    if (!route || !recommendation) return { status: "unavailable", reason: reply.error || route?.reason || "暂无适合此选区的方案" } as const;
    const match = options.find((option) => sameRouteModel(recommendation.model, option.ref));
    if (!match) return { status: "unavailable", reason: "推荐模型不在当前可用名单中" } as const;
    return { status: "ready", route, model: match.value } as const;
}
