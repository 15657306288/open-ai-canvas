import { describe, expect, test } from "bun:test";

import { buildGenerationConfig, resolveCanvasGenerationModel, resolveCanvasImageEditModel } from "../src/lib/canvas/canvas-project-generation";
import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import { defaultConfig, type AiConfig, type ModelChannel } from "../src/stores/use-config-store";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";

const CHANNEL_ID = "sys-canvas";
const IMAGE_MODEL = `${CHANNEL_ID}::gpt-image-2.5`;
const TEXT_MODEL = `${CHANNEL_ID}::gemini-3.8-flash-high`;

// 影策云端目录形态：系统渠道同时发布图片模型与文字模型，全局默认模型（config.model）可能是文字模型。
function cloudConfig(overrides: Partial<AiConfig> = {}): AiConfig {
    const channel: ModelChannel = {
        id: CHANNEL_ID,
        name: "影策云端",
        baseUrl: "/api/sys-canvas",
        apiKey: "system",
        apiFormat: "openai",
        scope: "system",
        models: ["gpt-image-2.5", "gemini-3.8-flash-high"],
        modelCosts: [
            { model: "gpt-image-2.5", displayName: "GPT Image 2.5", capability: "image", billingMode: "fixed_request", unitPriceMicrocredits: 10, capabilityConfig: defaultModelCapabilityConfig(undefined, "gpt-image-2.5") },
            {
                model: "gemini-3.8-flash-high",
                displayName: "Gemini 3.8 Flash High",
                capability: "text",
                billingMode: "token",
                unitPriceMicrocredits: 0,
                inputTokenPriceMicrocredits: 0,
                outputTokenPriceMicrocredits: 0,
                cachedTokenPriceMicrocredits: 0,
                capabilityConfig: defaultModelCapabilityConfig(undefined, "gemini-3.8-flash-high"),
            },
        ],
    };
    return {
        ...defaultConfig,
        channels: [channel],
        models: [IMAGE_MODEL, TEXT_MODEL],
        imageModels: [IMAGE_MODEL],
        textModels: [TEXT_MODEL],
        model: TEXT_MODEL,
        imageModel: IMAGE_MODEL,
        textModel: TEXT_MODEL,
        ...overrides,
    };
}

// 上传/导入的图片节点没有 metadata.model，正是“图片工具回传全局默认模型”的触发场景。
function uploadedImageNode(): CanvasNodeData {
    return { id: "node-1", type: CanvasNodeType.Image, title: "图片", position: { x: 0, y: 0 }, width: 100, height: 100, metadata: { content: "[image omitted]" } };
}

describe("图片工具回传模型的图片能力准入", () => {
    test("对话框带回全局文字模型时，图生图请求仍落到图片模型", () => {
        const config = cloudConfig();
        const fallback = buildGenerationConfig(config, uploadedImageNode(), "image").model;

        expect(fallback).toBe(IMAGE_MODEL);
        expect(resolveCanvasImageEditModel(config, TEXT_MODEL, fallback)).toBe(IMAGE_MODEL);
    });

    test("回传为空或已下架模型时回退到调用方解析出的图片模型", () => {
        const config = cloudConfig();

        expect(resolveCanvasImageEditModel(config, "", IMAGE_MODEL)).toBe(IMAGE_MODEL);
        expect(resolveCanvasImageEditModel(config, `${CHANNEL_ID}::removed-model`, IMAGE_MODEL)).toBe(IMAGE_MODEL);
        expect(resolveCanvasImageEditModel(config, undefined, IMAGE_MODEL)).toBe(IMAGE_MODEL);
    });

    test("用户主动选择的图片模型原样保留", () => {
        expect(resolveCanvasImageEditModel(cloudConfig(), IMAGE_MODEL, TEXT_MODEL)).toBe(IMAGE_MODEL);
        expect(resolveCanvasGenerationModel(cloudConfig(), IMAGE_MODEL, "image")).toBe(IMAGE_MODEL);
    });

    test("文字模型不会被当作图片模型使用", () => {
        const config = cloudConfig();

        expect(resolveCanvasGenerationModel(config, TEXT_MODEL, "image")).toBe("");
        // 文字编辑等不传 generationConfig 的链路依赖调用方解析出的图片模型，不受本次改动影响。
        expect(resolveCanvasImageEditModel(config, "", buildGenerationConfig(config, uploadedImageNode(), "image").model)).toBe(IMAGE_MODEL);
    });
});
