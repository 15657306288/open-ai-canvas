import { expect, spyOn, test } from "bun:test";
import * as generation from "@/lib/canvas/canvas-project-generation";
import { commerceActionPriceLabel, commerceImagePriceLabel, commerceImageTotal, commercePricing } from "@/lib/canvas/commerce-pricing";
import { commerceBatchTable, newCommerceWorkflow } from "@/lib/canvas/commerce-workflow";
import { defaultModelCapabilityConfig } from "@/lib/model-capabilities";
import { createModelChannel, defaultConfig, type AiConfig } from "@/stores/use-config-store";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "@/types/canvas";

function fixture(replica = false) {
    const capabilityConfig = defaultModelCapabilityConfig("openai-image", "gpt-image-1");
    capabilityConfig.image!.size = { parameter: "size", values: ["1024x1024", "1024x1536"], default: "1024x1024", allowCustom: false };
    capabilityConfig.image!.references.maxImages = 40;
    capabilityConfig.image!.quality = { supported: true, values: ["low", "high"], default: "low" };
    const channel = createModelChannel({ id: "priced", scope: "system", models: ["image", "text"], modelCosts: [
        { model: "image", capability: "image", protocol: "openai-image", pricePolicy: "unified", billingMode: "fixed_request", unitPriceMicrocredits: 125_000, capabilityConfig },
        { model: "text", capability: "text", protocol: "chat-completion", billingMode: "fixed_request", unitPriceMicrocredits: 10_000 },
    ] });
    const config: AiConfig = { ...defaultConfig, channels: [channel], model: "priced::image", imageModel: "priced::image", textModel: "priced::text", size: "1024x1024", quality: "low", count: "15" };
    const node: CanvasNodeData = { id: "source", title: "电商", type: replica ? CanvasNodeType.ProductReplica : CanvasNodeType.ProductDetail, width: 660, height: 860, position: { x: 0, y: 0 },
        metadata: { model: config.imageModel, commerceWorkflow: { ...newCommerceWorkflow(), screenCount: 6, secondaryNodeIds: replica ? ["t1", "t2"] : [] } } };
    const ids = replica ? ["p1", "p2", "t1", "t2"] : ["p1"];
    const nodes = [node, ...ids.map((id): CanvasNodeData => ({ id, type: CanvasNodeType.Image, title: id, width: 200, height: 200, position: { x: 0, y: 0 }, metadata: { storageKey: `resource:${id}`, naturalWidth: 1024, naturalHeight: id === "t2" ? 1536 : 1024 } }))];
    const connections: CanvasConnection[] = ids.map((fromNodeId) => ({ id: fromNodeId, fromNodeId, toNodeId: node.id }));
    return { node, nodes, connections, config, cost: channel.modelCosts![0], textCost: channel.modelCosts![1] };
}

test("详情按屏数乘模型标价，不乘全局多图数量；保留微积分精度", () => {
    const f = fixture();
    let pricing = commercePricing(f.node, f.nodes, f.connections, f.config);
    expect(commerceImagePriceLabel(pricing.images)).toBe("6 张 × 0.125 积分/张 = 0.75 积分");
    expect(commerceActionPriceLabel(pricing, "direct")).toContain("两阶段预计合计 0.76 积分");
    f.node.metadata!.commerceWorkflow!.screenCount = 30; f.cost.unitPriceMicrocredits = 1;
    pricing = commercePricing(f.node, f.nodes, f.connections, f.config);
    expect(commerceImageTotal(pricing.images)).toBe(30);
    expect(commerceImagePriceLabel(pricing.images)).toContain("0.00003 积分");
});

test("复刻按模板数量，不按产品×模板数量；空模板为零张", () => {
    const f = fixture(true);
    expect(commerceImagePriceLabel(commercePricing(f.node, f.nodes, f.connections, f.config).images)).toBe("2 张 × 0.125 积分/张 = 0.25 积分");
    expect(commerceImagePriceLabel(commercePricing(f.node, f.nodes, [], f.config).images)).toBe("0 张 · 0 积分");
});

test("复刻分析按全组或目标模板传图计价，OCR 单图价格不混用", () => {
    const f = fixture(true); const spy = spyOn(generation, "buildGenerationConfig");
    try {
        const pricing = commercePricing(f.node, f.nodes, f.connections, f.config);
        expect(spy.mock.calls.filter((call) => call[2] === "text").map((call) => call[3]?.input?.imageCount)).toEqual([4, 1]);
        expect(commerceActionPriceLabel(pricing, "direct")).toContain("两阶段预计合计 0.26");
        expect(commerceActionPriceLabel({ ...pricing, ocr: { microcredits: 5, description: "独立单图识别价格" } }, "ocr")).toContain("独立单图识别价格");
        spy.mockClear();
        commercePricing(f.node, f.nodes, f.connections, f.config, "t2");
        expect(spy.mock.calls.filter((call) => call[2] === "text").map((call) => call[3]?.input?.imageCount)).toEqual([3, 1]);
    } finally { spy.mockRestore(); }
});

test("详情风格图只影响分析计价，不计入实际生图模型输入数量", () => {
    const f = fixture(true); f.node.type = CanvasNodeType.ProductDetail;
    const spy = spyOn(generation, "buildGenerationConfig");
    try {
        commercePricing(f.node, f.nodes, f.connections, f.config);
        expect(spy.mock.calls.filter((call) => call[2] === "image" && call[3]).every((call) => call[3]!.input!.imageCount === 2)).toBe(true);
        expect(spy.mock.calls.filter((call) => call[2] === "text")[0][3]?.input?.imageCount).toBe(4);
    } finally { spy.mockRestore(); }
});

test("跟随各模板尺寸分别匹配后台阶梯价，统一尺寸时改用统一标价", () => {
    const f = fixture(true);
    f.cost.pricePolicy = "channel";
    f.cost.logicalPriceTiers = ["1024x1024", "1024x1536"].map((size, index) => ({ selector: { size, operation: "image_to_image" }, resolution: "*", videoSeconds: 0, billingMode: "fixed_request", unitPriceMicrocredits: (index + 1) * 1_000_000, inputTokenPriceMicrocredits: 0, outputTokenPriceMicrocredits: 0, cachedTokenPriceMicrocredits: 0 }));
    let pricing = commercePricing(f.node, f.nodes, f.connections, f.config);
    expect(pricing.images.map((image) => image.size)).toEqual(["1024x1024", "1024x1536"]);
    expect(commerceImagePriceLabel(pricing.images)).toBe("2 张 · 按各屏规格合计 3 积分");
    f.node.metadata!.commerceWorkflow!.followTemplate = false;
    pricing = commercePricing(f.node, f.nodes, f.connections, f.config);
    expect(commerceImageTotal(pricing.images)).toBe(2_000_000);
});

test("修改模型标价、质量实时使用新价格，0 价模型明确免费", () => {
    const f = fixture(); f.cost.pricePolicy = "channel";
    f.cost.logicalPriceTiers = ["low", "high"].map((quality, index) => ({ selector: { quality }, resolution: "*", videoSeconds: 0, billingMode: "fixed_request", unitPriceMicrocredits: index * 1_000_000, inputTokenPriceMicrocredits: 0, outputTokenPriceMicrocredits: 0, cachedTokenPriceMicrocredits: 0 }));
    expect(commerceImageTotal(commercePricing(f.node, f.nodes, f.connections, f.config).images)).toBe(0);
    f.node.metadata!.quality = "high";
    expect(commerceImageTotal(commercePricing(f.node, f.nodes, f.connections, f.config).images)).toBe(6_000_000);
});

test("Token 只显示后台输入输出单价，不伪造固定总价或预留金额", () => {
    const f = fixture(); Object.assign(f.textCost, { billingMode: "token", inputTokenPriceMicrocredits: 2_000_000, outputTokenPriceMicrocredits: 8_000_000, cachedTokenPriceMicrocredits: 0 });
    const pricing = commercePricing(f.node, f.nodes, f.connections, f.config);
    expect(pricing.text.microcredits).toBeNull();
    expect(pricing.text.description).toContain("输入 2 / 输出 8 积分/百万 Token");
    expect(commerceActionPriceLabel(pricing, "direct")).toContain("图片合计不包含策划费用");
    expect(commerceActionPriceLabel(pricing, "generate")).not.toContain("策划");
    expect(commerceActionPriceLabel(pricing, "manual")).toContain("免费建组");
});

test("价格缺失、负数、Token 图片、自定义渠道不得伪报零积分或部分合计", () => {
    for (const kind of ["missing", "negative", "token", "external"]) {
        const f = fixture();
        if (kind === "missing") f.config.channels[0].modelCosts = [f.textCost];
        if (kind === "negative") f.cost.unitPriceMicrocredits = -1;
        if (kind === "token") f.cost.billingMode = "token";
        if (kind === "external") f.config.channels[0].scope = "user";
        const pricing = commercePricing(f.node, f.nodes, f.connections, f.config);
        expect(commerceImageTotal(pricing.images)).toBeNull();
        expect(commerceImagePriceLabel(pricing.images)).toContain("不显示不完整合计");
        expect(commerceImagePriceLabel(pricing.images)).not.toContain("= 0");
    }
});

test("重叠路由价不一致时不猜低价，缺失规格也不合计", () => {
    const f = fixture(); f.cost.pricePolicy = "channel";
    const tier = { selector: {}, resolution: "*", videoSeconds: 0, billingMode: "fixed_request" as const, unitPriceMicrocredits: 100_000, inputTokenPriceMicrocredits: 0, outputTokenPriceMicrocredits: 0, cachedTokenPriceMicrocredits: 0 };
    f.cost.logicalPriceTiers = [tier, { ...tier, unitPriceMicrocredits: 200_000 }];
    expect(commerceImageTotal(commercePricing(f.node, f.nodes, f.connections, f.config).images)).toBeNull();
    f.cost.logicalPriceTiers = [{ ...tier, selector: { size: "4096x4096" } }];
    expect(commerceImageTotal(commercePricing(f.node, f.nodes, f.connections, f.config).images)).toBeNull();
});

test("结果组按未完成项估算；单屏重新生成按一张；改提示词使旧输出失效", () => {
    const f = fixture(); const data = f.node.metadata!.commerceWorkflow!;
    Object.assign(data, { role: "result", inputNodeIds: ["p1"], screens: ["s1", "s2"].map((id) => ({ id, title: id, copy: "文案", prompt: "产品特写" })) });
    f.node.metadata!.batchTable = commerceBatchTable(f.node, f.nodes, []);
    f.node.metadata!.batchTable.rows[0].outputNodeId = "output";
    f.nodes.push({ ...f.nodes[1], id: "output" });
    const before = JSON.stringify([f.node, f.nodes, f.config]);
    let pricing = commercePricing(f.node, f.nodes, [], f.config);
    expect(pricing.remaining.map((image) => image.id)).toEqual(["s2"]);
    expect(commerceActionPriceLabel(pricing, "generate", "s1")).toContain("1 张 × 0.125");
    expect(JSON.stringify([f.node, f.nodes, f.config])).toBe(before);
    data.screens[0].prompt = "修改画面";
    pricing = commercePricing(f.node, f.nodes, [], f.config);
    expect(pricing.remaining).toHaveLength(2);
});

test("生成中的屏不重复计入整组；仍保留各屏单价", () => {
    const f = fixture(); const data = f.node.metadata!.commerceWorkflow!;
    Object.assign(data, { role: "result", inputNodeIds: ["p1"], screens: ["s1", "s2"].map((id) => ({ id, title: id, copy: "", prompt: "" })) });
    f.node.metadata!.generationBatches = [{ id: "b", projectId: "canvas", sourceNodeId: f.node.id, mode: "batch_image", status: "running", createdAt: "", updatedAt: "", items: [{ id: "item", rowId: "s1", nodeId: "out", status: "running", retryCount: 0 }] }];
    const pricing = commercePricing(f.node, f.nodes, [], f.config);
    expect(pricing.images).toHaveLength(2);
    expect(pricing.remaining.map((image) => image.id)).toEqual(["s2"]);
});
