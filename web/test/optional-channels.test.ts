import assert from "node:assert/strict";
import test from "node:test";

import { modelCompatibilityError } from "../src/lib/model-selection.ts";
import { useConfigStore } from "../src/stores/use-config-store.ts";
import { createVideoGenerationTask } from "../src/services/api/video.ts";
import { defaultConfig, createModelChannel, isOptionalChannelEnabled, modelPricingLabel, normalizeConfigSnapshot, optionalChannelSubmitError, selectableModelsByCapability, type ModelChannel } from "../src/stores/use-config-store.ts";

// 账号池可选渠道：目录里始终存在；默认是否进候选列表看目录下发的 defaultEnabled，
// 用户自己开关过就以用户的选择为准。
function poolChannel(overrides: Partial<ModelChannel> = {}): ModelChannel {
    return createModelChannel({
        id: "doubao-pool",
        name: "豆包账号池",
        baseUrl: "/api/doubao-pool",
        apiKey: "system",
        scope: "system",
        optional: true,
        availability: { state: "ready", readyAccounts: 2, totalAccounts: 3 },
        models: ["doubao-seedream-image", "doubao-seedream-layer-decomposition", "doubao-seedance-video-1.0"],
        modelCosts: [
            { model: "doubao-seedream-image", displayName: "Seedream 图片", capability: "image", protocol: "doubao-pool", pricePolicy: "channel", pricingMode: "pool", priceLabel: "账号池 · 不扣费", billingMode: "fixed_request", unitPriceMicrocredits: 0 },
            {
                model: "doubao-seedream-layer-decomposition",
                displayName: "Seedream 图层拆分",
                capability: "image",
                protocol: "doubao-pool",
                pricePolicy: "channel",
                pricingMode: "pool",
                priceLabel: "账号池 · 不扣费",
                billingMode: "fixed_request",
                unitPriceMicrocredits: 0,
            },
            {
                model: "doubao-seedance-video-1.0",
                displayName: "Seedance 视频",
                capability: "video",
                protocol: "doubao-pool",
                pricePolicy: "channel",
                pricingMode: "pool",
                priceLabel: "账号池 · 不扣费",
                billingMode: "fixed_request",
                unitPriceMicrocredits: 0,
            },
        ],
        ...overrides,
    });
}

function systemChannel(): ModelChannel {
    return createModelChannel({
        id: "managed",
        name: "平台模型",
        baseUrl: "/api/managed",
        apiKey: "system",
        scope: "system",
        models: ["seedream-4.0", "seedance-2.0"],
        modelCosts: [
            { model: "seedream-4.0", displayName: "Seedream 4.0", capability: "image", protocol: "volcengine-ark-image", billingMode: "fixed_request", unitPriceMicrocredits: 1000000 },
            { model: "seedance-2.0", displayName: "Seedance 2.0", capability: "video", protocol: "seedance-videos-compatible", billingMode: "fixed_request", unitPriceMicrocredits: 2000000 },
        ],
    });
}

test("optional pool channels stay out of the model list until the user turns them on", () => {
    const snapshot = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [systemChannel(), poolChannel()] } });

    assert.deepEqual(selectableModelsByCapability(snapshot.config, "image"), ["managed::seedream-4.0"]);
    assert.deepEqual(selectableModelsByCapability(snapshot.config, "video"), ["managed::seedance-2.0"]);
    // 渠道本身要留在配置里，设置页才能给出开关。
    assert.ok(snapshot.config.channels.some((channel) => channel.id === "doubao-pool"));
    assert.equal(isOptionalChannelEnabled(snapshot.config, poolChannel()), false);
});

test("enabling an optional pool channel exposes its models with the pool price label", () => {
    const snapshot = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [systemChannel(), poolChannel()], enabledOptionalChannels: { "doubao-pool": true } } });

    const images = selectableModelsByCapability(snapshot.config, "image");
    assert.ok(images.includes("doubao-pool::doubao-seedream-image"));
    assert.ok(images.includes("doubao-pool::doubao-seedream-layer-decomposition"));
    assert.deepEqual(selectableModelsByCapability(snapshot.config, "video"), ["managed::seedance-2.0", "doubao-pool::doubao-seedance-video-1.0"]);

    const pool = snapshot.config.channels.find((channel) => channel.id === "doubao-pool")!;
    assert.equal(pool.modelCosts?.[0].priceLabel, "账号池 · 不扣费");
});

test("legacy snapshots without optional-channel state keep working and keep pools off", () => {
    const legacy = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [systemChannel(), poolChannel()] } as any });
    assert.deepEqual(legacy.config.enabledOptionalChannels, {});
    assert.deepEqual(selectableModelsByCapability(legacy.config, "image"), ["managed::seedream-4.0"]);

    const broken = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [poolChannel()], enabledOptionalChannels: "yes" } as any });
    assert.deepEqual(broken.config.enabledOptionalChannels, {});
    assert.equal(selectableModelsByCapability(broken.config, "image").length, 0);
});

test("empty account pool blocks submit and marks the model unusable", () => {
    const empty = poolChannel({ availability: { state: "empty", readyAccounts: 0, totalAccounts: 3 } });
    const config = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [systemChannel(), empty], enabledOptionalChannels: { "doubao-pool": true } } }).config;

    const error = optionalChannelSubmitError(config, "doubao-pool::doubao-seedream-image", "doubao-pool");
    assert.match(error, /没有可用账号/);
    // 候选列表里仍然可见，但被标为不可用（下拉框据此禁用）。
    assert.ok(selectableModelsByCapability(config, "image").includes("doubao-pool::doubao-seedream-image"));
    assert.match(modelCompatibilityError(config, "doubao-pool::doubao-seedream-image", { capability: "image" }), /暂无可用账号/);
});

test("ready account pool and normal channels pass the submit gate", () => {
    const ready = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [systemChannel(), poolChannel()], enabledOptionalChannels: { "doubao-pool": true } } }).config;
    assert.equal(optionalChannelSubmitError(ready, "doubao-pool::doubao-seedream-image", "doubao-pool"), "");
    assert.equal(optionalChannelSubmitError(ready, "managed::seedream-4.0", "managed"), "");
    assert.equal(modelCompatibilityError(ready, "doubao-pool::doubao-seedream-image", { capability: "image" }), "");
    assert.equal(modelCompatibilityError(ready, "managed::seedream-4.0", { capability: "image" }), "");
});

test("a disabled pool model cannot be used by canvas nodes that saved it earlier", () => {
    const config = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [systemChannel(), poolChannel()] } }).config;
    assert.match(modelCompatibilityError(config, "doubao-pool::doubao-seedream-image", { capability: "image" }), /设置中开启/);
});

test("pool price label falls back to the account-pool wording when only the pricing mode is known", () => {
    const channel = poolChannel({ modelCosts: [{ model: "doubao-seedream-image", capability: "image", protocol: "doubao-pool", pricingMode: "pool", billingMode: "fixed_request", unitPriceMicrocredits: 0 }] as any });
    assert.equal(modelPricingLabel(channel.modelCosts![0]), "账号池 · 不扣费");
});

// 账号池视频必须走渠道专用 provider（后端 /api/doubao-accounts/generate/video），
// 不能落到 OpenAI 视频协议；这里只验证路由分支，不连真实上游。
test("account pool video config routes to the doubao provider instead of the OpenAI one", async () => {
    const config = normalizeConfigSnapshot({
        config: {
            ...defaultConfig,
            channels: [systemChannel(), poolChannel()],
            enabledOptionalChannels: { "doubao-pool": true },
            model: "doubao-pool::doubao-seedance-video-1.0",
            videoModel: "doubao-pool::doubao-seedance-video-1.0",
        },
    }).config;

    const task = await createVideoGenerationTask(config, "一只猫在草地上奔跑");
    assert.equal(task.provider, "doubao-pool");
    assert.equal(task.model, "doubao-seedance-video-1.0");
});

test("the settings toggle flips the optional channel in the live config store", () => {
    useConfigStore.getState().replaceConfig(normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [systemChannel(), poolChannel()] } }).config);
    assert.deepEqual(selectableModelsByCapability(useConfigStore.getState().config, "image"), ["managed::seedream-4.0"]);

    useConfigStore.getState().setOptionalChannelEnabled("doubao-pool", true);
    const enabled = useConfigStore.getState().config;
    assert.equal(enabled.enabledOptionalChannels["doubao-pool"], true);
    assert.ok(selectableModelsByCapability(enabled, "image").includes("doubao-pool::doubao-seedream-image"));

    useConfigStore.getState().setOptionalChannelEnabled("doubao-pool", false);
    assert.equal(selectableModelsByCapability(useConfigStore.getState().config, "image").includes("doubao-pool::doubao-seedream-image"), false);
});
// 平台默认暴露（defaultEnabled=true）时：模型直接出现在候选列表，用户关掉后立刻消失。
test("pool channels exposed by the catalog show up by default and can be switched off", () => {
    const exposed = poolChannel({ defaultEnabled: true });
    const snapshot = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [systemChannel(), exposed] } });

    assert.equal(isOptionalChannelEnabled(snapshot.config, exposed), true);
    const images = selectableModelsByCapability(snapshot.config, "image");
    assert.ok(images.includes("doubao-pool::doubao-seedream-image"));
    // 暴露不等于替用户默认选中：平台渠道仍然排在前面，默认模型不受影响。
    assert.equal(images[0], "managed::seedream-4.0");
    assert.equal(optionalChannelSubmitError(snapshot.config, "doubao-pool::doubao-seedream-image", "doubao-pool"), "");

    useConfigStore.getState().replaceConfig(snapshot.config);
    useConfigStore.getState().setOptionalChannelEnabled("doubao-pool", false);
    const disabled = useConfigStore.getState().config;
    assert.equal(isOptionalChannelEnabled(disabled, exposed), false);
    assert.equal(selectableModelsByCapability(disabled, "image").includes("doubao-pool::doubao-seedream-image"), false);
    assert.match(optionalChannelSubmitError(disabled, "doubao-pool::doubao-seedream-image", "doubao-pool"), /设置/);
});
