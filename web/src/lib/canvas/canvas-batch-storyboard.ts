import { createStoryboardRow } from "@/lib/canvas/canvas-project-domain";
import type { CanvasBatchRow, CanvasBatchTableData, StoryboardRow } from "@/types/canvas";

type StoryboardField =
    | "shotNumber" | "durationSeconds" | "plotDescription" | "dialogue" | "narrativeIntent" | "viewerPOV"
    | "performanceBlocking" | "shotSize" | "emotion" | "lightingAndAtmosphere" | "audioEffects" | "camera"
    | "motion" | "timeBeats" | "imageGenerationPrompt" | "videoMotionPrompt" | "mustHave" | "optionalDetails"
    | "continuityOut" | "negativePrompt";

const FIELD_ALIASES: Record<StoryboardField, string[]> = {
    shotNumber: ["镜头", "镜头号", "镜号", "分镜", "序号"],
    durationSeconds: ["时长", "秒数", "持续时间", "镜头时长"],
    plotDescription: ["画面描述", "剧情", "画面", "镜头内容", "场景描述", "分镜描述"],
    dialogue: ["台词", "对白", "旁白", "文案", "口播"],
    narrativeIntent: ["叙事目的", "剧情目的", "表达目的"],
    viewerPOV: ["视角", "观看视角", "观众视角"],
    performanceBlocking: ["表演调度", "动作调度", "人物动作", "表演"],
    shotSize: ["景别", "镜头景别", "景别设计"],
    emotion: ["情绪", "情感"],
    lightingAndAtmosphere: ["光影氛围", "灯光", "氛围", "光线"],
    audioEffects: ["音效", "声音", "音乐", "音频"],
    camera: ["镜头设计", "摄影机", "摄影", "机位", "相机"],
    motion: ["运镜", "镜头运动", "运动方式", "镜头动作"],
    timeBeats: ["时间节拍", "节拍", "时间点", "时间线"],
    imageGenerationPrompt: ["首帧提示词", "图片提示词", "画面提示词", "生图提示词"],
    videoMotionPrompt: ["视频提示词", "动态提示词", "视频生成提示词", "视频描述"],
    mustHave: ["必须包含", "必备元素", "关键元素"],
    optionalDetails: ["可选细节", "补充细节"],
    continuityOut: ["衔接", "结尾状态", "镜头衔接", "连续性"],
    negativePrompt: ["负面提示词", "不要出现", "禁用元素"],
};

function normalize(value: string) {
    return value.trim().toLocaleLowerCase().replace(/[\s\-_：:，,。.!！？?（）()【】\[\]]/g, "");
}

function cellValue(table: CanvasBatchTableData, row: CanvasBatchRow, field: StoryboardField, index: number) {
    const cells = row.cells || {};
    const columns = table.textColumns || [];
    const aliases = [field, ...(FIELD_ALIASES[field] || [])].map(normalize);
    const column = columns.find((item) => aliases.some((alias) => normalize(item.label).includes(alias) || alias.includes(normalize(item.label))));
    if (column && cells[column.id] !== undefined) return cells[column.id] || "";
    const candidate = Object.entries(cells).find(([key]) => aliases.some((alias) => normalize(key).includes(alias) || alias.includes(normalize(key))));
    if (candidate) return candidate[1] || "";
    return columns[index] ? cells[columns[index].id] || "" : "";
}

function numberValue(value: string, fallback: number) {
    const match = value.match(/\d+(?:\.\d+)?/);
    const parsed = match ? Number(match[0]) : fallback;
    return Number.isFinite(parsed) ? Math.max(1, Math.min(60, Math.round(parsed))) : fallback;
}

function arrayValue(value: string) {
    return value.split(/[\n,，、;；|]/).map((item) => item.trim()).filter(Boolean);
}

/** 把 AI 多维表格行转换成视频脚本节点使用的稳定分镜结构。 */
export function storyboardRowsFromBatchTable(table: CanvasBatchTableData): StoryboardRow[] {
    return table.rows.map((row, index) => {
        const base = table.storyboardRows?.find((item) => item.id === row.id) || table.storyboardRows?.[index];
        const get = (field: StoryboardField) => cellValue(table, row, field, index) || String(base?.[field] || "");
        const plotDescription = get("plotDescription");
        const motion = get("motion");
        const videoMotionPrompt = get("videoMotionPrompt") || [plotDescription, motion].filter(Boolean).join("；");
        const imageGenerationPrompt = get("imageGenerationPrompt") || plotDescription;
        return createStoryboardRow(index + 1, {
            id: row.id,
            shotNumber: numberValue(get("shotNumber"), index + 1),
            durationSeconds: numberValue(get("durationSeconds"), 6),
            plotDescription,
            dialogue: get("dialogue"),
            narrativeIntent: get("narrativeIntent"),
            viewerPOV: get("viewerPOV"),
            performanceBlocking: get("performanceBlocking"),
            shotSize: get("shotSize"),
            emotion: get("emotion"),
            lightingAndAtmosphere: get("lightingAndAtmosphere"),
            audioEffects: get("audioEffects"),
            camera: get("camera"),
            motion,
            timeBeats: get("timeBeats"),
            imageGenerationPrompt,
            videoMotionPrompt,
            sourceStartMs: base?.sourceStartMs,
            sourceEndMs: base?.sourceEndMs,
            keyframeTimeMs: base?.keyframeTimeMs,
            mustHave: arrayValue(get("mustHave")),
            optionalDetails: arrayValue(get("optionalDetails")),
            continuityOut: get("continuityOut"),
            negativePrompt: get("negativePrompt"),
        });
    });
}
