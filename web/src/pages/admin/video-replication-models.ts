import type { ModelCatalogResponse, PublicChannelModel } from "@/services/api/logical-models";
import type { VideoReplicationVoiceOption } from "@/services/api/video-replication";
import { encodeChannelModel } from "@/stores/config-model-options";

/** 电商视频 Agent 后台的模型选项与推荐：只按目录里声明的能力与近 24 小时可用率排序，不按模型名称猜测。 */
export type ReplicationModelOption = { value: string; label: string; disabled?: boolean; score: number; reason: string };
export type ReplicationTextModelOption = ReplicationModelOption & { pickerValue: string };

function channelModels(catalog: ModelCatalogResponse) {
    const rows: Array<{ channel: string; channelId: string; model: PublicChannelModel }> = [];
    for (const channel of catalog.channels || []) for (const model of channel.models || []) if (model.available !== false) rows.push({ channel: channel.displayName || channel.name, channelId: channel.id, model });
    return rows;
}

function label(model: PublicChannelModel, channel: string) {
    return `${model.displayName && model.displayName !== model.modelKey ? `${model.displayName} · ` : ""}${model.modelKey} · ${channel}`;
}

function availability(model: PublicChannelModel) {
    const rate = model.availability?.rate24h;
    return typeof rate === "number" ? rate : 0.8;
}

/** 视频理解：必须能读图；能读视频的优先，其次可读图片多、上下文长、近期可用率高。 */
export function visionModelOptions(catalog: ModelCatalogResponse): ReplicationTextModelOption[] {
    return channelModels(catalog)
        .filter(({ model }) => model.capability === "text")
        .map(({ channel, channelId, model }) => {
            const refs = model.capabilityConfig?.text?.references || {};
            const images = Number(refs.maxImages) || 0;
            const videos = Number(refs.maxVideos) || 0;
            const context = Number(model.capabilityConfig?.text?.contextWindowTokens) || 0;
            const score = (videos > 0 ? 1000 : 0) + Math.min(images, 20) * 20 + context / 20000 + availability(model) * 100;
            const reason = [videos > 0 ? "支持视频输入" : "", images ? `图片 ≤${images} 张` : "", context ? `上下文 ${Math.round(context / 1000)}k` : ""].filter(Boolean).join(" · ");
            return { value: `channel:${model.id}`, pickerValue: encodeChannelModel(channelId, model.modelKey), label: `${label(model, channel)} · ${reason || "未配置图片输入"}`, disabled: images < 1, score, reason: reason || "未配置图片输入" };
        })
        .sort((a, b) => Number(a.disabled) - Number(b.disabled) || b.score - a.score);
}

/** 分镜策划：输出长度与上下文优先（需要一次写完整条分镜与口播），近期可用率作为次要因素。 */
export function planningModelOptions(catalog: ModelCatalogResponse): ReplicationTextModelOption[] {
    return channelModels(catalog)
        .filter(({ model }) => model.capability === "text")
        .map(({ channel, channelId, model }) => {
            const output = Number(model.capabilityConfig?.text?.maxOutputTokens) || 0;
            const context = Number(model.capabilityConfig?.text?.contextWindowTokens) || 0;
            const score = output / 500 + context / 20000 + availability(model) * 100;
            const reason = [output ? `输出 ${Math.round(output / 1000)}k` : "", context ? `上下文 ${Math.round(context / 1000)}k` : ""].filter(Boolean).join(" · ");
            return { value: `channel:${model.id}`, pickerValue: encodeChannelModel(channelId, model.modelKey), label: `${label(model, channel)}${reason ? ` · ${reason}` : ""}`, score, reason: reason || "文本模型" };
        })
        .sort((a, b) => b.score - a.score);
}

/** 语音合成 / 语音识别候选：音频能力的系统渠道模型（语音识别也可以选文本渠道里接了 /audio/transcriptions 的模型）。 */
export function audioModelOptions(catalog: ModelCatalogResponse, includeText = false): ReplicationModelOption[] {
    return channelModels(catalog)
        .filter(({ model }) => model.capability === "audio" || (includeText && model.capability === "text"))
        .map(({ channel, model }) => ({
            value: `channel:${model.id}`,
            label: `${label(model, channel)}${model.capability === "audio" ? " · 音频" : " · 文本渠道"}`,
            score: (model.capability === "audio" ? 100 : 0) + availability(model) * 100,
            reason: model.capability === "audio" ? "音频模型" : "文本渠道",
        }))
        .sort((a, b) => b.score - a.score);
}

export function recommended(options: ReplicationModelOption[]) {
    return options.find((item) => !item.disabled);
}

/** OpenAI 兼容 /audio/speech 的内置音色；其他语音模型（如豆包语音）请填写该模型自己的音色 ID。 */
export const OPENAI_COMPATIBLE_VOICES: VideoReplicationVoiceOption[] = [
    { key: "alloy", name: "Alloy", gender: "中性", style: "平稳自然" },
    { key: "ash", name: "Ash", gender: "男", style: "清晰干练" },
    { key: "ballad", name: "Ballad", gender: "男", style: "温柔叙事" },
    { key: "coral", name: "Coral", gender: "女", style: "温暖亲和" },
    { key: "echo", name: "Echo", gender: "男", style: "沉稳" },
    { key: "fable", name: "Fable", gender: "男", style: "故事感" },
    { key: "nova", name: "Nova", gender: "女", style: "明亮活泼" },
    { key: "onyx", name: "Onyx", gender: "男", style: "低沉有力" },
    { key: "sage", name: "Sage", gender: "女", style: "知性" },
    { key: "shimmer", name: "Shimmer", gender: "女", style: "轻柔" },
];
