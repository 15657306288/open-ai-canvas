import { describe, expect, test } from "bun:test";

import { resolveCanvasImageTextModel } from "../src/lib/canvas/canvas-project-generation";
import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import { systemChannelCapabilityMismatch } from "../src/services/api/generation-task";
import { defaultConfig, type AiConfig, type ModelChannel } from "../src/stores/use-config-store";

const SYSTEM_CHANNEL_ID = "sys-canvas";
const IMAGE_MODEL = `${SYSTEM_CHANNEL_ID}::gpt-image-2.5`;
const VISION_TEXT_MODEL = `${SYSTEM_CHANNEL_ID}::gemini-3.8-flash-high`;
const PLAIN_TEXT_MODEL = `${SYSTEM_CHANNEL_ID}::plain-text-pro`;

// 影策云端目录形态：系统渠道同时发布图片模型与文字模型，图片节点默认选中图片模型。
function cloudConfig(overrides: Partial<AiConfig> = {}): AiConfig {
    const channel: ModelChannel = {
        id: SYSTEM_CHANNEL_ID,
        name: "影策云端",
        baseUrl: "/api/sys-canvas",
        apiKey: "system",
        apiFormat: "openai",
        scope: "system",
        models: ["gpt-image-2.5", "gemini-3.8-flash-high", "plain-text-pro"],
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
            {
                model: "plain-text-pro",
                displayName: "Plain Text Pro",
                capability: "text",
                billingMode: "token",
                unitPriceMicrocredits: 0,
                inputTokenPriceMicrocredits: 0,
                outputTokenPriceMicrocredits: 0,
                cachedTokenPriceMicrocredits: 0,
                capabilityConfig: defaultModelCapabilityConfig(undefined, "plain-text-pro"),
            },
        ],
    };
    return {
        ...defaultConfig,
        channels: [channel],
        models: [IMAGE_MODEL, VISION_TEXT_MODEL, PLAIN_TEXT_MODEL],
        imageModels: [IMAGE_MODEL],
        textModels: [VISION_TEXT_MODEL, PLAIN_TEXT_MODEL],
        model: IMAGE_MODEL,
        imageModel: IMAGE_MODEL,
        textModel: VISION_TEXT_MODEL,
        ...overrides,
    };
}

describe("图片文字编辑的模型能力匹配", () => {
    test("全局文字模型缺失时不把当前图片模型发给文字任务", () => {
        const resolution = resolveCanvasImageTextModel(cloudConfig({ textModel: "" }));
        expect(resolution.model).toBe("");
        expect(resolution.error).toContain("文字模型");
    });

    test("选中支持图片输入的文字模型时直接使用该模型", () => {
        const resolution = resolveCanvasImageTextModel(cloudConfig());
        expect(resolution.error).toBe("");
        expect(resolution.model).toBe(VISION_TEXT_MODEL);
    });

    test("文字模型不支持图片输入时给出可执行提示", () => {
        const resolution = resolveCanvasImageTextModel(cloudConfig({ textModel: PLAIN_TEXT_MODEL }));
        expect(resolution.model).toBe("");
        expect(resolution.error).toContain("不支持图片输入");
    });

    test("系统渠道模型能力与任务模式不一致时在前端前置拦截", () => {
        const config = cloudConfig();
        expect(systemChannelCapabilityMismatch(config, "text")).toContain("不能用于文字生成");
        expect(systemChannelCapabilityMismatch(config, "image")).toBe("");
    });

    test("用户自定义渠道不套用系统渠道能力合同", () => {
        const userChannel: ModelChannel = {
            id: "mine",
            name: "自建渠道",
            baseUrl: "https://api.example.com",
            apiKey: "user-key",
            apiFormat: "openai",
            scope: "user",
            models: ["nano-banana"],
            modelCosts: [{ model: "nano-banana", displayName: "Nano Banana", capability: "image", billingMode: "fixed_request", unitPriceMicrocredits: 1 }],
        };
        const config = cloudConfig({ channels: [userChannel], models: ["mine::nano-banana"], model: "mine::nano-banana", imageModel: "mine::nano-banana" });
        expect(systemChannelCapabilityMismatch(config, "text")).toBe("");
    });
});
