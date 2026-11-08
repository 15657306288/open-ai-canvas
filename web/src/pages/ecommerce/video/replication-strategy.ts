import { translate } from "@/i18n";
import type { VideoCapabilityConfig } from "@/lib/model-capabilities";

const ts = (key: string, options?: Record<string, unknown>) => translate(`ecommerceVideo.strategy.${key}`, options);

/**
 * VideoModelCapabilityRegistry + VideoGenerationStrategyRouter（前端）。
 * 能力只取模型能力配置（与后端准入 validateVideoTask 同一来源），不按模型名猜测；
 * 后端还会在建任务时再次校验时长、参考图数量等，路由结果无法越过真实限制。
 */

export type ReferenceMode = "reference" | "first_frame" | "none";

export type VideoModelCapability = {
    model: string;
    label: string;
    referenceMode: ReferenceMode;
    maxReferenceImages: number;
    referenceVideoInput: boolean;
    maxReferenceVideoSeconds: number;
    firstFrame: boolean;
    lastFrame: boolean;
    nativeAudio: boolean;
    lipSync: false;
    durations: number[];
    maxSeconds: number;
    ratios: string[];
    resolutions: string[];
};

export function durationValues(profile: VideoCapabilityConfig | undefined) {
    if (!profile || profile.durationSupported === false) return [];
    if (profile.duration.selection === "enum") return [...(profile.duration.values || [])].sort((a, b) => a - b);
    const min = profile.duration.min || 1;
    const max = profile.duration.max || 15;
    const step = profile.duration.step || 1;
    return Array.from({ length: Math.floor((max - min) / step) + 1 }, (_, index) => min + index * step);
}

/**
 * 参考图如何进入生成任务：参考图生视频 > 首帧图生视频 > 不支持。
 * max 就是后台给这个模型配的每段参考图数（如 Seedance 2.5 配 30 张就是 30），不另设上限；
 * 后端按元素参考图张数留出名额，其余给商品 / 人物等替换素材。
 */
export function replicationReferenceMode(profile: VideoCapabilityConfig | undefined): { mode: ReferenceMode; max: number } {
    if (!profile) return { mode: "none", max: 0 };
    const operations = profile.operations || [];
    const maxImages = profile.references.maxImages || 0;
    if (maxImages > 0 && operations.includes("reference_to_video")) return { mode: "reference", max: maxImages };
    if (maxImages > 0 && (operations.includes("image_to_video") || !operations.length)) return { mode: "first_frame", max: 1 };
    return { mode: "none", max: 0 };
}

export function videoModelCapability(model: string, label: string, profile: VideoCapabilityConfig): VideoModelCapability {
    const reference = replicationReferenceMode(profile);
    const durations = durationValues(profile);
    const operations = profile.operations || [];
    return {
        model,
        label,
        referenceMode: reference.mode,
        maxReferenceImages: reference.max,
        referenceVideoInput: (profile.references.maxVideos || 0) > 0,
        maxReferenceVideoSeconds: profile.references.maxVideoDurationSeconds || 0,
        firstFrame: operations.includes("image_to_video") && (profile.references.maxImages || 0) > 0,
        lastFrame: false,
        nativeAudio: profile.generateAudio.supported,
        // 仓库内没有任何口型同步 / 数字人 Provider，统一为不支持。
        lipSync: false,
        durations,
        maxSeconds: durations.length ? durations[durations.length - 1] : 0,
        ratios: profile.ratios,
        resolutions: profile.resolutions,
    };
}

export type StrategyOption = {
    strategy: "native" | "segmented";
    capability: VideoModelCapability;
    taskCount: number;
    segmentSeconds: number[];
    totalSeconds: number;
    reasons: string[];
    blocked?: string;
};

/**
 * 路由：对目标时长与素材需求逐个模型判断。
 * native 只在模型单次时长档位包含目标时长且能接收参考图时成立；不会为凑时长而缩短目标。
 * segmented 用当前模型按分镜逐段生成。
 */
export function routeReplicationStrategy({ current, candidates, targetSeconds, shotSeconds, needsAudio }: { current: VideoModelCapability | null; candidates: VideoModelCapability[]; targetSeconds: number; shotSeconds: number[]; needsAudio: boolean }) {
    const native: StrategyOption[] = candidates.map((capability) => {
        const reasons: string[] = [];
        let blocked = "";
        const seconds = snapToDuration(targetSeconds, capability.durations);
        if (!capability.durations.length || targetSeconds > capability.maxSeconds) blocked = ts("maxSeconds", { max: capability.maxSeconds || "?", target: targetSeconds });
        else if (capability.referenceMode === "none") blocked = ts("noReference");
        if (capability.referenceMode === "first_frame") reasons.push(ts("firstFrameOnly"));
        if (needsAudio && !capability.nativeAudio) reasons.push(ts("noNativeAudio"));
        return { strategy: "native", capability, taskCount: 1, segmentSeconds: [seconds], totalSeconds: seconds, reasons, ...(blocked ? { blocked } : {}) };
    });
    native.sort((a, b) => Number(Boolean(a.blocked)) - Number(Boolean(b.blocked)) || a.reasons.length - b.reasons.length);
    const segmented: StrategyOption | null = current
        ? {
              strategy: "segmented",
              capability: current,
              taskCount: shotSeconds.length,
              segmentSeconds: shotSeconds,
              totalSeconds: shotSeconds.reduce((sum, value) => sum + value, 0),
              reasons: [current.referenceMode === "first_frame" ? ts("segmentedChain") : ts("segmentedParallel"), ts("ffmpeg")],
              ...(current.referenceMode === "none" ? { blocked: ts("currentNoReference") } : !shotSeconds.length ? { blocked: ts("noShots") } : {}),
          }
        : null;
    const currentNative = current ? native.find((option) => option.capability.model === current.model) : undefined;
    // 优先级：满足全部能力的原生直出 > 原生直出但需独立音频处理 > 智能分段。只给出推荐，最终由用户在确认弹窗选择。
    const recommended =
        (currentNative && !currentNative.blocked && !currentNative.reasons.length ? currentNative : undefined) ||
        native.find((option) => !option.blocked && !option.reasons.length) ||
        native.find((option) => !option.blocked) ||
        (segmented && !segmented.blocked ? segmented : null);
    return { native, segmented, recommended };
}

/** 取模型支持的最接近时长（与后端 snapVideoReplicationSeconds 一致：距离相同时取较长的一档）。 */
export function snapToDuration(seconds: number, durations: number[]) {
    if (!durations.length) return seconds;
    return durations.reduce((best, value) => (Math.abs(value - seconds) < Math.abs(best - seconds) || (Math.abs(value - seconds) === Math.abs(best - seconds) && value > best) ? value : best), durations[0]);
}

/** 一键生成的单段时长档位：模型允许、且不短于 5 秒；模型没声明档位时按 5 秒一档到 15 秒。 */
export function segmentSecondOptions(allowed: number[]) {
    const usable = allowed.filter((value) => value >= 5);
    if (usable.length) return usable;
    if (allowed.length) return [allowed[allowed.length - 1]];
    return [5, 10, 15];
}

/**
 * 按单段上限预估生成次数，服务端可在同样的次数内调整切点。
 */
export function estimateSegments(totalMs: number, segmentSeconds: number, allowed: number[] = []) {
    // 与服务端一致：整片只比单段上限多出不到 1 秒时裁尾成一段。
    const overMs = totalMs - segmentSeconds * 1000;
    const total = overMs > 0 && overMs <= 1000 ? segmentSeconds : Math.max(4, Math.round(totalMs / 1000));
    const seconds: number[] = [];
    let left = total;
    while (left > 0) {
        if (left <= segmentSeconds) {
            seconds.push(Math.max(4, left));
            break;
        }
        const rest = left - segmentSeconds;
        if (rest < 4) {
            seconds.push(Math.max(4, left - 4), 4);
            break;
        }
        seconds.push(segmentSeconds);
        left = rest;
    }
    const options = allowed.filter((value) => value >= 4);
    return seconds.map((value) => snapToDuration(value, options.length ? options : allowed));
}
