import { describe, expect, test } from "bun:test";

import { buildImageToolbarTools } from "../src/components/canvas/canvas-image-toolbar-tools";
import { canvasAnnotationActionState, canvasAnnotationSubmission } from "../src/components/canvas/canvas-node-annotation-dialog";
import { canvasImageAnnotationReferenceError } from "../src/lib/canvas/canvas-project-generation";
import { defaultModelCapabilityConfig, type ModelCapabilityConfig } from "../src/lib/model-capabilities";
import { defaultConfig, type AiConfig, type ModelChannel } from "../src/stores/use-config-store";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";

const noop = () => undefined;

function imageCapability(maxImages: number): ModelCapabilityConfig {
    const capability = defaultModelCapabilityConfig(undefined, "mark-image");
    capability.image!.references.maxImages = maxImages;
    return capability;
}

function imageChannel(id: string, model: string, displayName: string, maxImages: number): ModelChannel {
    return {
        id,
        name: displayName,
        baseUrl: `/api/${id}`,
        apiKey: "system",
        apiFormat: "openai",
        scope: "system",
        models: [model],
        modelCosts: [{ model, displayName, capability: "image", billingMode: "fixed_request", unitPriceMicrocredits: 10, capabilityConfig: imageCapability(maxImages) }],
    };
}

function markAiConfig(channels: ModelChannel[]): AiConfig {
    const models = channels.map((channel) => `${channel.id}::${channel.models[0]}`);
    return { ...defaultConfig, channels, models, imageModels: models, model: models[0], imageModel: models[0] };
}

function imageNode(overrides: Partial<CanvasNodeData> = {}): CanvasNodeData {
    return { id: "image-1", type: CanvasNodeType.Image, title: "图片", position: { x: 0, y: 0 }, width: 320, height: 180, metadata: { content: "[image omitted]" }, ...overrides };
}

describe("画布图片工具栏的标记入口", () => {
    test("「标注」与「标注编辑」已合并为唯一的「标记」入口", () => {
        const calls: string[] = [];
        const handlers = {
            onUpload: noop,
            onToggleFreeResize: noop,
            onAnnotate: () => calls.push("annotate"),
            onAnnotationEdit: () => calls.push("annotationEdit"),
            onTextEdit: noop,
            onTextLayers: noop,
            onMaskEdit: noop,
            onRemoveBackground: noop,
            onLayerDecomposition: noop,
            onEmotion: noop,
            onPortraitTexture: noop,
            onCrop: noop,
            onUpscale: noop,
            onSuperResolve: noop,
            onAngle: noop,
            onLighting: noop,
            onPanorama: noop,
            onViewImage: noop,
            onCopyPrompt: noop,
            onReversePrompt: noop,
            onNineGrid: noop,
        } as unknown as Parameters<typeof buildImageToolbarTools>[1];

        const tools = buildImageToolbarTools(imageNode(), handlers);
        const sectionTools = tools.filter((tool) => tool.section === "拆分与标记");

        expect(sectionTools.map((tool) => tool.label)).toEqual(["标记", "宫格切分"]);
        expect(tools.some((tool) => tool.label === "标注" || tool.label === "标注编辑")).toBe(false);

        const mark = tools.find((tool) => tool.id === "annotation");
        expect(mark?.label).toBe("标记");
        expect(mark?.description).toContain("按标记");
        mark?.onClick();

        // 合并后的入口只负责打开弹窗，不直接走生成链路。
        expect(calls).toEqual(["annotate"]);
    });
});

describe("标记弹窗的两个动作", () => {
    const composed = { sourceDataUrl: "data:image/png;base64,SOURCE", mergedDataUrl: "data:image/png;base64,MERGED", annotatedDataUrl: "data:image/png;base64,STROKES" };

    test("保存标记图提交本地合成结果，按标记生成提交原图与笔迹两张参考图", () => {
        expect(canvasAnnotationSubmission("save", composed)).toEqual({ action: "save", dataUrl: composed.mergedDataUrl });
        expect(canvasAnnotationSubmission("generate", composed)).toEqual({
            action: "generate",
            payload: { sourceDataUrl: composed.sourceDataUrl, annotatedDataUrl: composed.annotatedDataUrl },
        });
    });

    test("没有画标记时两个动作都禁用并说明原因", () => {
        expect(canvasAnnotationActionState({ hasStrokes: false })).toEqual({ saveDisabledReason: "请先在图片上画出标记", generateDisabledReason: "请先在图片上画出标记" });
        expect(canvasAnnotationActionState({ hasStrokes: true })).toEqual({ saveDisabledReason: "", generateDisabledReason: "" });
    });

    test("模型能力不足时只禁用「按标记生成」，保存标记图仍可用", () => {
        const state = canvasAnnotationActionState({ hasStrokes: true, generateDisabledReason: "当前图片模型最多支持 1 张参考图" });
        expect(state.saveDisabledReason).toBe("");
        expect(state.generateDisabledReason).toBe("当前图片模型最多支持 1 张参考图");
    });
});

describe("按标记生成的模型参考图前置条件", () => {
    test("模型最多支持一张参考图时给出中文原因", () => {
        const config = markAiConfig([imageChannel("sys-canvas", "mark-image", "Mark Image", 1)]);
        const reason = canvasImageAnnotationReferenceError(config, imageNode());
        expect(reason).toContain("最多支持 1 张参考图");
        expect(reason).toContain("至少 2 张参考图");
    });

    test("模型支持两张参考图时不拦截", () => {
        expect(canvasImageAnnotationReferenceError(markAiConfig([imageChannel("sys-canvas", "mark-image", "Mark Image", 2)]), imageNode())).toBe("");
    });

    test("节点上单独选择的模型同样参与判定", () => {
        const config = markAiConfig([
            imageChannel("sys-canvas", "mark-image", "Mark Image", 16),
            imageChannel("sys-canvas-low", "mark-image-low", "Mark Image Low", 1),
        ]);
        const node = imageNode({ metadata: { content: "[image omitted]", model: "sys-canvas-low::mark-image-low" } });

        expect(canvasImageAnnotationReferenceError(config, node)).toContain("最多支持 1 张参考图");
        expect(canvasImageAnnotationReferenceError(config, imageNode())).toBe("");
    });
});
