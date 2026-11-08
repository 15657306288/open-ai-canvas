import { modelQuoteRequest } from "@/lib/model-pricing";
import { creationVideoConfig } from "@/pages/create/creation-generation-config";
import { quoteModel } from "@/services/api/logical-models";
import type { useEffectiveConfig } from "@/stores/use-config-store";

export type ReplicationQuote = { credits: number | null; estimated: boolean; usage: boolean };

/**
 * 按任务逐个报价求和：原生直出传 [目标秒数]，智能分段传每段秒数。
 * 使用与工作台相同的报价接口；任何一档报不出价格时返回 usage（按实际用量结算），不编造数字。
 */
export async function quoteReplicationTasks({
    config,
    model,
    ratio,
    resolution,
    seconds,
    referenceCount,
    referenceMode,
    referenceVideo = false,
    audioCount = 0,
    signal,
}: {
    config: ReturnType<typeof useEffectiveConfig>;
    model: string;
    ratio?: string;
    resolution?: string;
    seconds: number[];
    referenceCount: number;
    referenceMode: "reference" | "first_frame" | "none";
    referenceVideo?: boolean;
    audioCount?: number;
    signal?: AbortSignal;
}): Promise<ReplicationQuote> {
    if (!seconds.length) return { credits: null, estimated: true, usage: false };
    const distinct = [...new Set(seconds)];
    const quotes = await Promise.all(
        distinct.map(async (value) => {
            const videoConfig = creationVideoConfig(config, model, { ratio: ratio || "", seconds: String(value), videoQuality: (resolution || "").replace(/p$/i, "") });
            const request = modelQuoteRequest(videoConfig, model, "video", {
                capability: "video",
                input: {
                    textCount: 1,
                    imageCount: referenceCount,
                    referenceImageCount: referenceMode === "reference" ? referenceCount : 0,
                    videoCount: referenceVideo ? 1 : 0,
                    videoDurationsMs: referenceVideo ? [value * 1000] : undefined,
                    audioCount,
                    characterCount: 0,
                },
                videoSeconds: String(value),
                options: { size: ratio, videoSeconds: value, vquality: (resolution || "").replace(/p$/i, "") },
            });
            if (!request) return { value, amount: null as number | null, estimated: true };
            const { quote } = await quoteModel(request, signal);
            return { value, amount: quote.amountMicrocredits / 1_000_000, estimated: quote.estimated === true };
        }),
    );
    if (quotes.some((item) => item.amount === null)) return { credits: null, estimated: true, usage: true };
    const bySeconds = new Map(quotes.map((item) => [item.value, item.amount || 0]));
    return { credits: seconds.reduce((sum, value) => sum + (bySeconds.get(value) || 0), 0), estimated: quotes.some((item) => item.estimated), usage: false };
}
