import assert from "node:assert/strict";
import test from "node:test";

// Bun 直接执行 TypeScript 测试时需要保留扩展名；生产 tsconfig 不包含 test/。
import { DEFAULT_VIDEO_PROMPT_MAX_CHARS, defaultModelCapabilityConfig, normalizeVideoValue } from "../src/lib/model-capabilities.ts";

test("known multimodal text model families default to reference media", () => {
    const gemini = defaultModelCapabilityConfig("chat-completion", "gemini-3.8-flash-high").text!;
    const gpt = defaultModelCapabilityConfig("chat-completion", "gpt-5.6-sol").text!;
    const claude = defaultModelCapabilityConfig("claude-api", "claude-fable-5.1").text!;
    const deepseek = defaultModelCapabilityConfig("chat-completion", "deepseek-vl2").text!;
    const plain = defaultModelCapabilityConfig("chat-completion", "plain-text-model").text!;

    assert.equal(gemini.references.maxImages, 16);
    assert.equal(gemini.references.maxVideos, 3);
    assert.equal(gpt.references.maxImages, 16);
    assert.equal(claude.references.maxImages, 16);
    assert.equal(claude.references.maxVideos, 0);
    assert.equal(deepseek.references.maxImages, 16);
    assert.equal(plain.references.maxImages, 0);
});

test("switching to MiniMax H3 replaces an unsupported 720p value with 768P", () => {
    const profile = defaultModelCapabilityConfig("minimax-video", "MiniMax-H3").video!;

    assert.deepEqual(normalizeVideoValue(profile, { seconds: "11", ratio: "16:9", resolution: "720" }), {
        seconds: "11",
        ratio: "16:9",
        resolution: "768P",
    });
});

// 视频提示词由「输入框文本 + 连线内容 + 技能上下文」合成，技能上下文预算为 32000，
// 合成结果远长于用户手输内容。默认上限过小会把正常可用的画布工作流拦在本地预检。
// 这里锁定默认值本身，避免被改回偏小值（前端放行/后端拒绝的判定必须同源）。
test("video prompt default allows a composed canvas prompt", () => {
    assert.equal(DEFAULT_VIDEO_PROMPT_MAX_CHARS, 8000);
    for (const protocol of [undefined, "seedance-videos-compatible", "agnes-video", "volcengine-ark-video"]) {
        const profile = defaultModelCapabilityConfig(protocol, "test-model");
        assert.equal(profile.video!.references.promptMaxChars, DEFAULT_VIDEO_PROMPT_MAX_CHARS);
    }
});

test("raising the video default leaves text and image limits untouched", () => {
    // 只放宽视频默认值，避免顺带改变其它能力的判定口径。
    const profile = defaultModelCapabilityConfig("seedance-videos-compatible", "sd-2.5");
    assert.equal(profile.text!.references.promptMaxChars, 32000);
    assert.equal(profile.image!.references.promptMaxChars, 32000);
});

// 豆包 / Dola 账号池：上游是网页 samantha 协议（参考图 attachments），没有蒙版端点、不输出透明通道；
// 图层拆分由后端在同一任务里逐层图生图，层数上限必须与后端 doubaoPoolMaxLayers 对齐。
test("doubao account pool image profile matches upstream limits", () => {
    const profile = defaultModelCapabilityConfig("doubao-pool", "doubao-seedream-layer-decomposition");
    assert.equal(profile.image!.maxOutputs, 6);
    assert.equal(profile.image!.transparentBackground.supported, false);
    assert.equal(profile.image!.references.maskSupported, false);
    assert.equal(profile.image!.quality.supported, false);

    // 官方 Ark Seedream 是另一条渠道（API key），不能被账号池口径覆盖。
    const ark = defaultModelCapabilityConfig("volcengine-ark-image", "doubao-seedream-4-0-250828");
    assert.equal(ark.image!.maxOutputs, 15);
});
