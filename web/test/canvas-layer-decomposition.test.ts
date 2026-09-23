import assert from "node:assert/strict";
import test from "node:test";

// Bun 直接执行 TypeScript 测试时需要保留扩展名；生产 tsconfig 不包含 test/。
import {
    buildLayerDecompositionBatchPrompt,
    buildLayerDecompositionPrompt,
    isDedicatedLayerDecompositionEndpoint,
    LAYER_DECOMPOSITION_HARD_MAX_LAYERS,
    layerDecompositionLayerTitles,
    layerDecompositionTaskMetadata,
    planLayerDecomposition,
} from "../src/lib/canvas/canvas-layer-decomposition.ts";

test("通用图片模型按用户选择的图层数逐层生成，单张上限不再压住图层数", () => {
    // gpt-image-2.5 这类模型单次只回 1 张图，但逐层模式下每层各发一次请求，图层数不应被压成 1。
    const plan = planLayerDecomposition({ requestedLayers: 3, maxOutputs: 1, dedicatedEndpoint: false, transparentBackground: true });

    assert.equal(plan.mode, "per-layer");
    assert.equal(plan.layers, 3);
    assert.equal(plan.capped, false);
    assert.deepEqual(plan.notices, []);
});

test("框选数量参与图层数推导", () => {
    const plan = planLayerDecomposition({ regionCount: 4, maxOutputs: 4, dedicatedEndpoint: false, transparentBackground: true });
    assert.equal(plan.layers, 4);

    const withoutRegions = planLayerDecomposition({ maxOutputs: 4, dedicatedEndpoint: false, transparentBackground: true });
    assert.equal(withoutRegions.layers, 3);
});

test("超出上限时收敛并给出显式提示，不静默截断", () => {
    const plan = planLayerDecomposition({ requestedLayers: 9, maxOutputs: 15, dedicatedEndpoint: false, transparentBackground: true });

    assert.equal(plan.layers, LAYER_DECOMPOSITION_HARD_MAX_LAYERS);
    assert.equal(plan.capped, true);
    assert.equal(plan.notices.length, 1);
    assert.match(plan.notices[0], /最多拆分 6 个图层/);
});

test("专用图层分解端点按模型返回上限收敛", () => {
    const plan = planLayerDecomposition({ requestedLayers: 6, maxOutputs: 4, dedicatedEndpoint: true, transparentBackground: false });

    assert.equal(plan.mode, "single-request");
    assert.equal(plan.layers, 4);
    assert.equal(plan.capped, true);
    assert.match(plan.notices[0], /单次最多返回 4 张图片/);
    // 专用端点自己保证透明度，不提示换模型。
    assert.equal(plan.notices.some((notice) => notice.includes("透明")), false);
});

test("专用端点缺少能力上限时收敛到 1 层", () => {
    assert.equal(planLayerDecomposition({ requestedLayers: 4, dedicatedEndpoint: true }).layers, 1);
    assert.equal(planLayerDecomposition({ requestedLayers: 4, maxOutputs: 0, dedicatedEndpoint: true }).layers, 1);
});

test("选区超过上限时提示减少选区或更换模型", () => {
    const plan = planLayerDecomposition({ requestedLayers: 6, regionCount: 8, maxOutputs: 4, dedicatedEndpoint: false, transparentBackground: true });
    assert.equal(plan.layers, 6);
    assert.equal(plan.notices.some((notice) => notice.includes("已框选 8 个区域")), true);
});

test("不支持透明背景的模型给出可执行的换模型提示", () => {
    const plan = planLayerDecomposition({ requestedLayers: 2, dedicatedEndpoint: false, transparentBackground: false });
    assert.equal(plan.notices.some((notice) => notice.includes("透明 PNG") && notice.includes("gpt-image-2.5")), true);
});

test("识别专用图层分解协议与模型名", () => {
    assert.equal(isDedicatedLayerDecompositionEndpoint("image-tools-layer-decomposition", "bytedance/seedream-v5.0-pro/layer-decomposition"), true);
    assert.equal(isDedicatedLayerDecompositionEndpoint("", "seedream-v5.0-pro-layer-decomposition"), true);
    assert.equal(isDedicatedLayerDecompositionEndpoint("openai-image", "gpt-image-2.5"), false);
    assert.equal(isDedicatedLayerDecompositionEndpoint("gemini-image", "nano-banana-pro"), false);
});

test("单层提示词带上层序号、总数与对应选区", () => {
    const prompt = buildLayerDecompositionPrompt({ basePrompt: "拆分为主体与背景", layerIndex: 2, layerCount: 3, region: [100, 200, 300, 400] });

    assert.match(prompt, /拆分为主体与背景/);
    assert.match(prompt, /第 2 个图层（共 3 个图层）/);
    assert.match(prompt, /<bbox>100 200 300 400<\/bbox>/);
    assert.match(prompt, /只输出这一张图层图片/);

    const withoutRegion = buildLayerDecompositionPrompt({ basePrompt: "拆分为主体与背景", layerIndex: 1, layerCount: 3 });
    assert.equal(withoutRegion.includes("<bbox>"), false);
});

test("单请求提示词包含全部选区", () => {
    const prompt = buildLayerDecompositionBatchPrompt("拆分为图层", [
        [1, 2, 3, 4],
        [5, 6, 7, 8],
    ]);

    assert.match(prompt, /区域1 <bbox>1 2 3 4<\/bbox>/);
    assert.match(prompt, /区域2 <bbox>5 6 7 8<\/bbox>/);
    assert.match(prompt, /每个图层一张独立图片/);
    const withoutRegions = buildLayerDecompositionBatchPrompt("拆分为图层");
    assert.equal(withoutRegions.includes("<bbox>"), false);
    assert.match(withoutRegions, /每个图层一张独立图片/);
});

test("节点命名按图层数生成，不使用 1 张图冒充 N 个图层", () => {
    assert.deepEqual(layerDecompositionLayerTitles("产品图", 3).map((layer) => layer.title), ["产品图 · 图层 1", "产品图 · 图层 2", "产品图 · 图层 3"]);
    assert.deepEqual(layerDecompositionLayerTitles("", 1), [{ layerIndex: 1, title: "图片 · 图层 1" }]);
    assert.equal(layerDecompositionLayerTitles("产品图", 0).length, 1);
});

test("图层拆分任务元数据带上层数、层序号与选区", () => {
    const single = layerDecompositionTaskMetadata({ sourceNodeId: "node-1", layerCount: 3, regions: [[0, 10, 20, 30], [1, 2, 3, 4]] });
    assert.equal(single.edit, "layer-decomposition");
    assert.equal(single.layerDecomposition, true);
    assert.equal(single.layerCount, 3);
    assert.equal(single.sourceNodeId, "node-1");
    assert.deepEqual(single.layerRegions, [[0, 10, 20, 30], [1, 2, 3, 4]]);
    assert.equal("layerIndex" in single, false);

    const perLayer = layerDecompositionTaskMetadata({ sourceNodeId: "node-1", layerCount: 3, layerIndex: 2 });
    assert.equal(perLayer.layerIndex, 2);
    assert.equal("layerRegions" in perLayer, false);

    // 非法选区不进元数据：后端宁可退化为无选区提示词，也不要拿半截坐标去算 bbox。
    const invalid = layerDecompositionTaskMetadata({ layerCount: 0, regions: [[Number.NaN, 1, 2, 3] as unknown as [number, number, number, number]] });
    assert.equal(invalid.layerCount, 1);
    assert.equal("layerRegions" in invalid, false);
    assert.equal("sourceNodeId" in invalid, false);
});
