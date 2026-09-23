// 「AI 图层拆分」的请求规划：把用户选择的图层数量收敛到当前模型能力之内，
// 并决定走「专用图层分解端点一次返回多图」还是「通用图片模型逐层生成」。
//
// 事实依据：
// - 专用插件 image-tools-layer-decomposition 的上游会返回图层列表（plugin-packages/image-tools/manifest.json）。
// - 通用图片协议的 count/n 返回的是同一提示词的多张变体，不是多个独立图层；
//   画布与智能创作对「多张图」的既有做法都是 N 个请求各生成 1 张（如 canvas-image-generation-executor.ts）。
// 因此逐层模式不受 maxOutputs 限制，只受成本保护上限约束；专用端点模式必须按 maxOutputs 收敛。

export const LAYER_DECOMPOSITION_HARD_MAX_LAYERS = 6;
export const LAYER_DECOMPOSITION_DEFAULT_LAYERS = 3;

export type LayerDecompositionMode = "single-request" | "per-layer";

export type LayerDecompositionPlanInput = {
    requestedLayers?: number;
    regionCount?: number;
    maxOutputs?: number;
    dedicatedEndpoint?: boolean;
    transparentBackground?: boolean;
};

export type LayerDecompositionPlan = {
    mode: LayerDecompositionMode;
    requestedLayers: number;
    layers: number;
    cap: number;
    capped: boolean;
    notices: string[];
};

export function isDedicatedLayerDecompositionEndpoint(interfaceType?: string, model?: string) {
    return /layer-decomposition/i.test(`${interfaceType || ""} ${model || ""}`);
}

// 任务元数据：后端凭它识别「图层拆分」、层数与每层选区。
// 单请求模式（专用接口）一次任务拆 N 层，regions 让后端把选区落到对应层；
// 逐层模式一次任务只拆 1 层，只带层序号。
export function layerDecompositionTaskMetadata({
    sourceNodeId,
    layerCount,
    layerIndex,
    regions,
}: {
    sourceNodeId?: string;
    layerCount: number;
    layerIndex?: number;
    regions?: Array<[number, number, number, number]>;
}): Record<string, unknown> {
    const metadata: Record<string, unknown> = {
        edit: "layer-decomposition",
        layerDecomposition: true,
        layerCount: Math.max(1, Math.floor(Number(layerCount) || 1)),
    };
    const nodeId = String(sourceNodeId || "").trim();
    if (nodeId) metadata.sourceNodeId = nodeId;
    if (layerIndex) metadata.layerIndex = Math.max(1, Math.floor(layerIndex));
    // 选区必须是纯数值四元组：[x1,y1,x2,y2]（0-1000 归一化坐标），后端按层拆提示词。
    const boxes = (regions || []).filter((region) => region.length === 4 && region.every((value) => Number.isFinite(value)));
    if (boxes.length) metadata.layerRegions = boxes.map((region) => [...region]);
    return metadata;
}

export function layerDecompositionCap({ dedicatedEndpoint, maxOutputs }: { dedicatedEndpoint?: boolean; maxOutputs?: number }) {
    if (!dedicatedEndpoint) return LAYER_DECOMPOSITION_HARD_MAX_LAYERS;
    const modelCap = Math.max(1, Math.floor(Number(maxOutputs) || 1));
    return Math.max(1, Math.min(modelCap, LAYER_DECOMPOSITION_HARD_MAX_LAYERS));
}

export function planLayerDecomposition({ requestedLayers, regionCount, maxOutputs, dedicatedEndpoint, transparentBackground }: LayerDecompositionPlanInput): LayerDecompositionPlan {
    const cap = layerDecompositionCap({ dedicatedEndpoint, maxOutputs });
    const explicit = Math.max(0, Math.floor(Number(requestedLayers) || 0));
    const regions = Math.max(0, Math.floor(Number(regionCount) || 0));
    const requested = explicit > 0 ? explicit : regions > 0 ? regions : LAYER_DECOMPOSITION_DEFAULT_LAYERS;
    const layers = Math.max(1, Math.min(cap, requested));
    const capped = layers < requested;
    const notices: string[] = [];
    if (capped) {
        notices.push(
            dedicatedEndpoint
                ? `当前模型单次最多返回 ${cap} 张图片，已按上限收敛为 ${layers} 个图层。`
                : `单次最多拆分 ${cap} 个图层，已按上限收敛为 ${layers} 个；要更多图层请分两次拆分。`,
        );
    }
    if (regions > cap) {
        notices.push(`已框选 ${regions} 个区域，超过当前上限 ${cap} 个图层；请减少选区，或改用上限更高的模型。`);
    }
    if (!dedicatedEndpoint && transparentBackground === false) {
        notices.push("当前模型不支持透明背景，拆出的图层是不透明图片；如需透明 PNG，请改用支持透明背景的模型（例如 gpt-image-2.5）。");
    }
    return { mode: dedicatedEndpoint ? "single-request" : "per-layer", requestedLayers: requested, layers, cap, capped, notices };
}

export function buildLayerDecompositionPrompt({
    basePrompt,
    layerIndex,
    layerCount,
    region,
}: {
    basePrompt: string;
    layerIndex: number;
    layerCount: number;
    region?: [number, number, number, number];
}) {
    const lines = [basePrompt.trim()];
    lines.push(`本次只输出第 ${layerIndex} 个图层（共 ${layerCount} 个图层），其余图层的内容不要出现在这张图片里。`);
    if (region) {
        lines.push(`第 ${layerIndex} 个图层对应图中框选区域（图像 0-1000 坐标系）：<bbox>${region.join(" ")}</bbox>，以该区域中的主体作为这一层的内容，保持原始边缘细节。`);
    }
    lines.push("只输出这一张图层图片，不要输出拼图或多图层合成图，不要添加新的元素。");
    return lines.filter(Boolean).join("\n");
}

// 单请求模式（专用图层分解端点）需要把全部选区写进同一条提示词。
export function buildLayerDecompositionBatchPrompt(basePrompt: string, regions?: Array<[number, number, number, number]>) {
    const prompt = basePrompt.trim();
    const boxes = (regions || []).map((region, index) => `区域${index + 1} <bbox>${region.join(" ")}</bbox>`).join("；");
    const regionLine = boxes ? `重点处理用户框选的区域，并分别输出这些区域中的主体为独立图层。选区坐标（图像 0-1000 坐标系）：${boxes}。` : "";
    return `${prompt}\n\n${regionLine}请按图层分别输出，每个图层一张独立图片，不要把不同图层合并到同一张图里。`;
}

// 图层节点命名与顺序：返回 N 张就必须建 N 个节点，节点数量不允许用 1 张图冒充。
export function layerDecompositionLayerTitles(baseTitle: string, layerCount: number) {
    const count = Math.max(1, Math.floor(Number(layerCount) || 1));
    const title = baseTitle.trim() || "图片";
    return Array.from({ length: count }, (_, index) => ({ layerIndex: index + 1, title: `${title} · 图层 ${index + 1}` }));
}
