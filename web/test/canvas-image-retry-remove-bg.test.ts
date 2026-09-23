import { describe, expect, test } from "bun:test";

import { resolveCanvasImageRetryModel, resolveCanvasRemoveBackgroundIntent } from "../src/lib/canvas/canvas-project-generation";
import { defaultModelCapabilityConfig, type ModelCapabilityConfig } from "../src/lib/model-capabilities";
import { defaultConfig, type AiConfig, type ModelChannel } from "../src/stores/use-config-store";

const CHANNEL_ID = "sys-canvas";
const IMAGE_MODEL = `${CHANNEL_ID}::gpt-image-2.5`;
const NANO_BANANA_MODEL = `${CHANNEL_ID}::nano-banana2`;
const TEXT_MODEL = `${CHANNEL_ID}::gemini-3.8-flash-high`;

// 与云端目录一致：图片模型的能力声明来自渠道目录，nano-banana2 明确不支持透明背景。
function transparencyCapability(model: string, supported: boolean): ModelCapabilityConfig {
    const capability = defaultModelCapabilityConfig(undefined, model);
    capability.image = { ...capability.image!, transparentBackground: { supported, default: false } };
    return capability;
}

// 影策云端目录形态：系统渠道同时发布图片模型与文字模型，全局默认模型（config.model）可能是文字模型。
function cloudConfig(overrides: Partial<AiConfig> = {}): AiConfig {
    const channel: ModelChannel = {
        id: CHANNEL_ID,
        name: "影策云端",
        baseUrl: "/api/sys-canvas",
        apiKey: "system",
        apiFormat: "openai",
        scope: "system",
        models: ["gpt-image-2.5", "nano-banana2", "gemini-3.8-flash-high"],
        modelCosts: [
            { model: "gpt-image-2.5", displayName: "GPT Image 2.5", capability: "image", billingMode: "fixed_request", unitPriceMicrocredits: 10, capabilityConfig: transparencyCapability("gpt-image-2.5", true) },
            { model: "nano-banana2", displayName: "Nano Banana 2", capability: "image", billingMode: "fixed_request", unitPriceMicrocredits: 8, capabilityConfig: transparencyCapability("nano-banana2", false) },
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
        models: [IMAGE_MODEL, NANO_BANANA_MODEL, TEXT_MODEL],
        imageModels: [IMAGE_MODEL, NANO_BANANA_MODEL],
        textModels: [TEXT_MODEL],
        model: TEXT_MODEL,
        imageModel: IMAGE_MODEL,
        textModel: TEXT_MODEL,
        ...overrides,
    };
}

describe("失败图片节点重试的模型能力准入", () => {
    test("metadata.model 是文字模型时，重试改用图片模型", () => {
        const resolution = resolveCanvasImageRetryModel(cloudConfig(), TEXT_MODEL, "");

        expect(resolution.error).toBe("");
        expect(resolution.model).toBe(IMAGE_MODEL);
    });

    test("没有可用图片模型时给出中文提示且不提交", () => {
        const catalog = cloudConfig().channels[0];
        const textOnly: ModelChannel = { ...catalog, models: ["gemini-3.8-flash-high"], modelCosts: catalog.modelCosts?.filter((cost) => cost.model === "gemini-3.8-flash-high") };
        const config = cloudConfig({ channels: [textOnly], models: [TEXT_MODEL], imageModels: [], imageModel: "" });

        const resolution = resolveCanvasImageRetryModel(config, TEXT_MODEL, "");

        // model 为空是重试链路提前 return 的判据：先提示用户去模型设置，而不是发一个必然失败的请求。
        expect(resolution.model).toBe("");
        expect(resolution.error).toContain("没有可用的图片模型");
        expect(resolution.error).toContain("模型设置");
    });

    test("正常图片节点重试行为不变", () => {
        const resolution = resolveCanvasImageRetryModel(cloudConfig(), IMAGE_MODEL, IMAGE_MODEL);

        expect(resolution.error).toBe("");
        expect(resolution.model).toBe(IMAGE_MODEL);
    });

    test("已下架的旧模型回落为当前可用图片模型", () => {
        const resolution = resolveCanvasImageRetryModel(cloudConfig(), `${CHANNEL_ID}::removed-image-model`, IMAGE_MODEL);

        expect(resolution.error).toBe("");
        expect(resolution.model).toBe(IMAGE_MODEL);
    });
});

describe("去除背景的透明底能力判定", () => {
    test("支持透明背景的模型带上透明背景意图", () => {
        const intent = resolveCanvasRemoveBackgroundIntent(cloudConfig(), IMAGE_MODEL);

        expect(intent.transparentBackground).toBe("true");
        expect(intent.notice).toBe("");
    });

    test("不支持透明背景的模型不硬传并给出中文提示", () => {
        const intent = resolveCanvasRemoveBackgroundIntent(cloudConfig(), NANO_BANANA_MODEL);

        expect(intent.transparentBackground).toBeUndefined();
        expect(intent.notice).toContain("不支持透明背景");
        expect(intent.notice).toContain("gpt-image-2.5");
    });
});
