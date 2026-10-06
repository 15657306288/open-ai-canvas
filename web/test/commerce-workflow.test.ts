import { expect, test } from "bun:test";
import { applyCommerceOutput, commerceBatchTable, commerceInputIdentity, commerceInputs, commercePlanningPrompt, commerceSnapshot, currentTemplateCopy, manualCommerceScreens, newCommerceWorkflow, parseCommerceOcr, validateCommerceInputs } from "@/lib/canvas/commerce-workflow";
import { createCommerceResult } from "@/lib/canvas/commerce-workflow-graph";
import { isolateCopiedNodeMetadata } from "@/lib/canvas/canvas-node-copy";
import { buildGenerationTaskNodeResult } from "@/lib/canvas/canvas-generation-task-sync";
import { canvasConnectionError } from "@/lib/canvas/canvas-connection-policy";
import { defaultConfig } from "@/stores/use-config-store";
import { defaultModelCapabilityConfig } from "@/lib/model-capabilities";
import { commerceImageSize } from "@/lib/canvas/commerce-image-size";
import { CanvasNodeType, type CanvasNodeData, type CanvasConnection } from "@/types/canvas";
import type { CommerceWorkflow } from "@/types/commerce-workflow";
import type { GenerationTask } from "@/services/api/task-center";

const image = (id: string): CanvasNodeData => ({ id, type: CanvasNodeType.Image, title: id, position: { x: 0, y: 0 }, width: 300, height: 300, metadata: { storageKey: `resource:${id}`, status: "success" } });
const source = (replica = false, patch: Partial<CommerceWorkflow> = {}): CanvasNodeData => ({ id: "source", type: replica ? CanvasNodeType.ProductReplica : CanvasNodeType.ProductDetail, title: "电商", position: { x: 0, y: 0 }, width: 660, height: 860,
    metadata: { model: "image-model", size: "9:16", commerceWorkflow: { ...newCommerceWorkflow(), screenCount: 2, ...patch } } });
const edge = (fromNodeId: string, toNodeId = "source"): CanvasConnection => ({ id: `${fromNodeId}-${toNodeId}`, fromNodeId, toNodeId });
const screen = (id: string) => ({ id, title: id, copy: "日常搭配", prompt: "浅色背景，产品特写" });
const task = (raw: string, operation = "op"): GenerationTask => ({ id: "task", projectId: "canvas", type: "canvas_text", status: "succeeded", prompt: "", resultJson: JSON.stringify({ text: raw }), clientOperationId: operation, attempts: 1, createdAt: "", updatedAt: "" });

test("详情与复刻生成表不再写入固定两张并发，整组与单屏均跟随任务额度", () => {
    for (const replica of [false, true]) {
        const screens = [screen("a"), screen("b")].map((item) => ({ ...item, ...(replica ? { templateNodeId: "t" } : {}) }));
        const node = source(replica, { screens, secondaryNodeIds: replica ? ["t"] : [] });
        const nodes = [node, image("p"), ...(replica ? [image("t")] : [])];
        const edges = [edge("p"), ...(replica ? [edge("t")] : [])];
        for (const rowId of [undefined, "a"]) {
            const table = commerceBatchTable(node, nodes, edges, rowId);
            expect(table.concurrency).toBe(0);
            expect(table.rows).toHaveLength(rowId ? 1 : 2);
        }
    }
});

const pageBrief = "第一屏核心展示；第二屏痛点左右对比；第三屏防水卖点；第四屏墨西哥使用场景；第五屏细节工艺；第六屏属性。全组西班牙语、白灰背景。";
const linkedBrief = "连线补充资料：商品是雨衣，只在第五屏展示接缝，不要虚构防水等级。";
const briefNode: CanvasNodeData = { id: "brief", type: CanvasNodeType.Text, title: "策划资料", position: { x: 0, y: 0 }, width: 300, height: 300, metadata: { content: linkedBrief } };

test("全局要求、连线资料与风格偏好交给策划模型分配，不原样塞入每屏生图", () => {
    const planned = [{ ...screen("s1"), copy: "Protección diaria", prompt: "白灰背景，雨衣特写，现代无衬线排版" }, { ...screen("s2"), copy: "Listo para la lluvia", prompt: "墨西哥街头雨景，人像远景" }];
    const node = source(false, { productName: "雨衣", brief: pageBrief, language: "西班牙语", modelMode: "自动判断", font: "现代无衬线", colorRhythm: "错落", screens: planned, secondaryNodeIds: ["style"] });
    const nodes = [node, image("p"), image("style"), briefNode]; const edges = [edge("p"), edge("style"), edge("brief")];
    const planning = commercePlanningPrompt(node, nodes, edges, planned);
    for (const requirement of [pageBrief, linkedBrief, "西班牙语", "现代无衬线", "错落"]) expect(planning).toContain(requirement);
    expect(planning).toContain("策划资料，不是每屏的全局生图提示词");
    const before = JSON.stringify(node);
    for (const row of commerceBatchTable(node, nodes, edges).rows) {
        const card = planned.find((item) => item.id === row.id)!;
        expect(row.prompt).toContain(card.prompt); expect(row.prompt).toContain(card.copy);
        expect(row.prompt).toContain("产品：雨衣"); expect(row.prompt).toContain("严格保留产品外观");
        expect(row.inputNodeIds).toEqual(["p"]);
        for (const raw of [pageBrief, linkedBrief, "模特：自动判断", "字体：现代无衬线", "色彩节奏：", "文案语言："]) expect(row.prompt).not.toContain(raw);
        expect(row.prompt).not.toContain(planned.find((item) => item.id !== row.id)!.prompt);
    }
    expect(JSON.stringify(node)).toBe(before);
    const revised = source(false, { ...node.metadata!.commerceWorkflow!, brief: "新的整页资料", language: "英语", modelMode: "使用模特", font: "粗黑标题", colorRhythm: "统一" });
    expect(commerceBatchTable(revised, [revised, ...nodes.slice(1)], edges).rows).toEqual(commerceBatchTable(node, nodes, edges).rows);
});

test("单屏重策划保留全局资料和原屏序，但只要求返回当前屏", () => {
    const screens = [screen("首屏"), screen("场景"), screen("细节")];
    const node = source(false, { screens, brief: pageBrief });
    const planning = commercePlanningPrompt(node, [node, image("p"), briefNode], [edge("p"), edge("brief")], [screens[2]]);
    expect(planning).toContain("严格输出 1 项"); expect(planning).toContain(pageBrief);
    expect(planning).toContain('"position":3,"title":"细节"');
    expect(planning).toContain("不是整页或多屏拼图");
});

test("手工单卡方案不拼全局偏好，纯图与单屏卖点仍有效，完整覆盖不被改写", () => {
    const node = source(false, { brief: pageBrief, textMode: "none", screens: [{ ...screen("a"), copy: "", sceneType: "户外", sellingPoints: "封边接缝" }, { ...screen("b"), prompt: "", copy: "" }] });
    const nodes = [node, image("p"), briefNode]; const edges = [edge("p"), edge("brief")];
    const prompt = commerceBatchTable(node, nodes, edges, "a").rows[0].prompt;
    expect(prompt).toContain("不要添加任何排版文字"); expect(prompt).toContain("本屏场景类型：户外"); expect(prompt).toContain("本屏绑定的已确认卖点：封边接缝");
    expect(prompt).not.toContain(pageBrief); expect(prompt).not.toContain(linkedBrief);
    expect(() => commerceBatchTable(node, nodes, edges, "b")).toThrow("不会自动调用收费策划");
    node.metadata!.commerceWorkflow!.screens[0].promptOverride = pageBrief;
    expect(commerceBatchTable(node, nodes, edges, "a").rows[0].prompt).toBe(pageBrief);
});

test("本次详情修复不移除复刻直出的产品要求、连线资料和模板映射", () => {
    const node = source(true, { productName: "雨衣", brief: "保留模板布局，替换成雨衣", secondaryNodeIds: ["t"], screens: [{ ...screen("a"), templateNodeId: "t" }] });
    const prompt = commerceBatchTable(node, [node, image("p"), image("t"), briefNode], [edge("p"), edge("t"), edge("brief")]).rows[0].prompt;
    expect(prompt).toContain("要求：保留模板布局，替换成雨衣"); expect(prompt).toContain(linkedBrief);
    expect(prompt).toContain("第 2 张是本屏版式模板"); expect(prompt).toContain("保持本屏模板中的原文案");
});

test("主图零字及单屏精确文案仍是出图约束，不随策划偏好移除", () => {
    const node = source(false, { platform: "亚马逊 · 主图组", brief: pageBrief, screens: [{ ...screen("a"), copy: "" }, { ...screen("b"), copy: "Uso diario" }] });
    const rows = commerceBatchTable(node, [node, image("p")], [edge("p")]).rows;
    expect(rows[0].prompt).toContain("不要添加任何排版文字");
    expect(rows[1].prompt).toContain("画面文字必须逐字使用以下文案，不添加其他文字：\nUso diario");
    expect(rows[1].prompt).not.toContain("文案语言：中文");
});

test("AI策划同组可混合纯产品图和参数文案，空copy不阻止整组或单屏出图", () => {
    const cards = [{ ...screen("photo"), copy: "  ", title: "核心卖点" }, { ...screen("info"), copy: "Height: 3.6 cm" }];
    const node = source(false, { runMode: "plan", brief: pageBrief, screens: cards });
    const nodes = [node, image("p")]; const edges = [edge("p")]; const before = JSON.stringify(node);
    const rows = commerceBatchTable(node, nodes, edges).rows;
    expect(rows).toHaveLength(2);
    expect(rows[0].prompt).toContain("不要添加任何排版文字");
    expect(rows[0].prompt).not.toContain("画面文字必须逐字使用");
    expect(rows[0].prompt).not.toContain("Height: 3.6 cm");
    expect(rows[1].prompt).toContain("画面文字必须逐字使用以下文案，不添加其他文字：\nHeight: 3.6 cm");
    expect(commerceBatchTable(node, nodes, edges, "photo").rows[0]).toEqual(rows[0]);
    expect(rows.every((row) => !row.prompt.includes(pageBrief))).toBe(true);
    expect(JSON.stringify(node)).toBe(before);
    node.metadata!.commerceWorkflow!.screens[0].copy = "Product detail";
    expect(commerceBatchTable(node, nodes, edges, "photo").rows[0].prompt).toContain("Product detail");
    expect(commerceBatchTable(node, nodes, edges, "photo").rows[0].prompt).not.toContain("不要添加任何排版文字");
});

test("策划返回场景与已确认卖点写回，手工绑定和完整覆盖不被改写", () => {
    const expected = [screen("a"), { ...screen("b"), sceneType: "人工场景", sellingPoints: "人工确认事实", promptOverride: "锁定提示词" }];
    const data = { ...newCommerceWorkflow(), screens: expected, pending: { operationId: "op", kind: "plan" as const, inputSnapshot: "", screens: expected } };
    const response = expected.map((item) => ({ ...item, copy: "", sceneType: "产品特写", sellingPoints: "925银耳针", prompt: "商品微距摄影" }));
    const parsed = applyCommerceOutput(data, JSON.stringify({ screens: response }));
    expect(parsed.screens[0]).toMatchObject({ sceneType: "产品特写", sellingPoints: "925银耳针", copy: "", prompt: "商品微距摄影" });
    expect(parsed.screens[1]).toMatchObject({ sceneType: "人工场景", sellingPoints: "人工确认事实", promptOverride: "锁定提示词" });
    for (const key of ["sceneType", "sellingPoints"] as const) {
        expect(() => applyCommerceOutput(data, JSON.stringify({ screens: response.map((row) => ({ ...row, [key]: { invalid: true } })) }))).toThrow("场景类型或卖点绑定");
    }
    const legacy = applyCommerceOutput(data, JSON.stringify({ screens: expected.map(({ title, copy, prompt }) => ({ title, copy, prompt })) }));
    expect(legacy.screens[1].sellingPoints).toBe("人工确认事实");
    expect(legacy.screens[0].sceneType).toBeUndefined();
});

test("策划协议明确逐屏文字语义并要求场景卖点，不把占位主题当完整策划", () => {
    const node = source(false, { screens: [screen("a")] });
    const prompt = commercePlanningPrompt(node, [node, image("p")], [edge("p")], node.metadata!.commerceWorkflow!.screens);
    expect(prompt).toContain('"sceneType"'); expect(prompt).toContain('"sellingPoints"');
    expect(prompt).toContain("copy 留空表示本屏不添加排版文字");
    expect(prompt).toContain("不强制每屏有字");
    expect(prompt).toContain("不能保留‘详情 7’这类占位主题");
});

test("详情自定义支持 1–30 屏，非法数量不静默降级", () => {
    expect(manualCommerceScreens(source(false, { screenCount: 30 }), [], [])).toHaveLength(30);
    for (const screenCount of [0, 31, 2.2, NaN]) expect(() => manualCommerceScreens(source(false, { screenCount }), [], [])).toThrow();
});

test("两张产品加两张模板只产生两行，产品共同引用，模板逐行隔离", () => {
    const node = source(true, { secondaryNodeIds: ["t1", "t2"] });
    const nodes = [node, ...["p1", "p2", "t1", "t2"].map(image)];
    const edges = ["p1", "p2", "t1", "t2"].map((id) => edge(id));
    node.metadata!.commerceWorkflow!.screens = manualCommerceScreens(node, nodes, edges);
    const table = commerceBatchTable(node, nodes, edges);
    expect(table.rows.map((row) => row.inputNodeIds)).toEqual([["p1", "p2", "t1"], ["p1", "p2", "t2"]]);
    expect(table.rows[0].prompt).toContain("保持本屏模板中的原文案");
});

test("每次策划独立建组，旧组不覆盖，输入与源配置隔离", () => {
    const node = source(); const nodes = [node, image("p")];
    const first = createCommerceResult(node, nodes, [edge("p")], "manual");
    const second = createCommerceResult(node, [...nodes, first.result], [edge("p"), ...first.edges], "plan");
    expect(first.result.id).not.toBe(second.result.id);
    expect(second.result.position.y).toBeGreaterThan(first.result.position.y + first.result.height);
    node.metadata!.commerceWorkflow!.brief = "新要求";
    expect(first.result.metadata!.commerceWorkflow!.brief).toBe("");
    expect(first.result.metadata!.commerceWorkflow!.pending).toBeUndefined();
    expect(first.edges.map((item) => item.fromNodeId)).toEqual(["p", "source"]);
});

test("结果组更换源素材时明确拒绝，源节点新增连线不污染已确认引用", () => {
    const node = source(); const p = image("p");
    const { result } = createCommerceResult(node, [node, p], [edge("p")], "manual");
    expect(commerceInputs(result, [result, p, image("new")], [edge("new", result.id)]).products.map((item) => item.id)).toEqual(["p"]);
    p.metadata!.storageKey = "resource:changed";
    expect(() => validateCommerceInputs(result, [result, p], [])).toThrow("原素材");
});

test("纯手工完整提示词可以直接生成，空策划不会暗中补收费任务", () => {
    const node = source(false, { screens: [{ ...screen("a"), prompt: "", copy: "", promptOverride: "严格采用手工要求" }] });
    expect(commerceBatchTable(node, [node, image("p")], [edge("p")]).rows[0].prompt).toBe("严格采用手工要求");
    node.metadata!.commerceWorkflow!.screens[0].promptOverride = "";
    expect(() => commerceBatchTable(node, [node, image("p")], [edge("p")])).toThrow("不会自动调用收费策划");
});

test("单屏重策划不覆盖人工完整提示词或其他屏，不串参考图", () => {
    const first = { ...screen("a"), promptOverride: "人工锁定", referenceNodeIds: ["extra"], size: "1:1" };
    const data = { ...newCommerceWorkflow(), screens: [first, screen("b")], pending: { operationId: "op", kind: "replan" as const, inputSnapshot: "", screens: [first] } };
    const next = applyCommerceOutput(data, JSON.stringify({ screens: [{ title: "新主题", copy: "新文案", prompt: "新构图" }] }));
    expect(next.screens[0]).toMatchObject({ id: "a", promptOverride: "人工锁定", referenceNodeIds: ["extra"], size: "1:1", prompt: "新构图" });
    expect(next.screens[1]).toEqual(data.screens[1]);
    expect(applyCommerceOutput(next, "重复回调")).toBe(next);
});

test("OCR 保留人工规则和填写内容，空数组是有效无字模板，畸形响应失败", () => {
    const copy = { identity: "resource:template", recognized: false, pairs: [{ id: "x", original: "原文", replacement: "新文", manual: true }] };
    const parsed = parseCommerceOcr('{"texts":["原文","其它"]}', copy);
    expect(parsed.pairs[0]).toEqual(copy.pairs[0]); expect(parsed.recognized).toBe(true);
    expect(parseCommerceOcr('{"texts":[]}', copy).pairs).toEqual(copy.pairs);
    for (const raw of ['{}', '{"texts":[3]}', '{"texts":"bad"}']) expect(() => parseCommerceOcr(raw, copy)).toThrow();
});

test("模板资源身份改变使 OCR 缓存失效，不被临时 URL 变化影响", () => {
    const template = image("t");
    const data = { ...newCommerceWorkflow(), templateCopies: { t: { identity: "resource:t", recognized: true, pairs: [] } } };
    template.metadata!.content = "temporary-url";
    expect(currentTemplateCopy(data, template).recognized).toBe(true);
    template.metadata!.storageKey = "resource:replacement";
    expect(currentTemplateCopy(data, template).recognized).toBe(false);
});

test("复刻部分替换只传当前模板已填规则，不把空替换当作删字", () => {
    const node = source(true, { copyMode: "rewrite", secondaryNodeIds: ["t"], screens: [{ ...screen("a"), templateNodeId: "t" }], templateCopies: {
        t: { identity: "resource:t", recognized: true, pairs: [{ id: "1", original: "旧字", replacement: "新字" }, { id: "2", original: "留着", replacement: "" }] },
    } });
    const prompt = commerceBatchTable(node, [node, image("p"), image("t")], [edge("p"), edge("t")]).rows[0].prompt;
    expect(prompt).toContain('"original":"旧字","replacement":"新字"');
    expect(prompt).not.toContain('"original":"留着"'); expect(prompt).toContain("保持不变");
});

test("单屏参考与配置变更进入提交快照，运行状态/原始响应不改变业务快照", () => {
    const node = source(false, { screens: [screen("a")], extraReferenceNodeIds: ["extra"] });
    const extra = image("extra"); const nodes = [node, image("p"), extra]; const edges = [edge("p")];
    const before = commerceSnapshot(node, nodes, edges);
    node.metadata!.commerceWorkflow!.rawOutput = "delta";
    expect(commerceSnapshot(node, nodes, edges)).toBe(before);
    extra.metadata!.storageKey = "resource:changed";
    expect(commerceSnapshot(node, nodes, edges)).not.toBe(before);
});

test("跨请求旧文本任务不回填，解析失败保留原始响应和 checkpoint", async () => {
    const node = source(false, { screens: [screen("a")], pending: { operationId: "op", kind: "plan", inputSnapshot: "", screens: [screen("a")] } });
    expect(await buildGenerationTaskNodeResult(node, task("{}", "other"))).toBe(node);
    const failed = await buildGenerationTaskNodeResult(node, task("not json"));
    expect(failed.metadata?.status).toBe("error");
    expect(failed.metadata?.commerceWorkflow?.pending?.operationId).toBe("op");
    expect(failed.metadata?.commerceWorkflow?.rawOutput).toBe("not json");
});

test("真实任务从 inputJson.metadata 读取操作标识并自动回填六屏", async () => {
    const screens = Array.from({ length: 6 }, (_, index) => screen(`s${index}`));
    const node = source(false, { role: "result", screens: screens.map((item) => ({ ...item, copy: "", prompt: "" })), autoGenerate: "awaiting-plan",
        pending: { operationId: "op", kind: "plan", inputSnapshot: "", screens } });
    const completed = { ...task(JSON.stringify({ screens })), clientOperationId: undefined,
        inputJson: JSON.stringify({ mode: "text", metadata: { nodeId: node.id, clientOperationId: "op" } }) };
    const result = await buildGenerationTaskNodeResult(node, completed);
    expect(result.metadata?.commerceWorkflow?.screens.map((item) => item.prompt)).toEqual(screens.map((item) => item.prompt));
    expect(result.metadata?.commerceWorkflow?.pending).toBeUndefined();
    expect(result.metadata?.commerceWorkflow?.autoGenerate).toBe("ready");
    expect(result.metadata?.status).toBe("success");
    const unrelated = await buildGenerationTaskNodeResult(node, { ...completed, inputJson: JSON.stringify({ metadata: { clientOperationId: "other" } }) });
    expect(unrelated).toBe(node);
});

test("复制隔离付费任务和输出，保留手工内容并映射所有引用", () => {
    const node = source(false, { role: "result", sourceNodeId: "s", autoGenerate: "ready", inputNodeIds: ["p"], inputIdentities: { p: "resource:p" }, extraReferenceNodeIds: ["e"], screens: [{ ...screen("a"), referenceNodeIds: ["e"], promptOverride: "手写" }], pending: { operationId: "op", kind: "plan", screens: [], inputSnapshot: "" } });
    const copied = isolateCopiedNodeMetadata(node, new Map([["p", "p2"], ["e", "e2"]]));
    expect(copied.commerceWorkflow).toMatchObject({ inputNodeIds: ["p2"], inputIdentities: { p2: "resource:p" }, screens: [{ referenceNodeIds: ["e2"], promptOverride: "手写" }] });
    expect(copied.commerceWorkflow?.pending).toBeUndefined(); expect(copied.commerceWorkflow?.autoGenerate).toBeUndefined(); expect(copied.batchTable).toBeUndefined();
});

test("配置节点不输出媒体，结果不得回流成循环引用", () => {
    const node = source(); const output = image("out");
    expect(canvasConnectionError(defaultConfig, [node, output], [], edge(node.id, output.id))).toContain("独立图片结果");
    expect(canvasConnectionError(defaultConfig, [node, output], [edge(node.id, output.id)], edge(output.id))).toContain("循环引用");
});

test("输出完整文字不得改变产品素材身份", () => {
    const p = image("p"); const identity = commerceInputIdentity(p);
    p.title = "重命名"; p.width = 1000; p.metadata!.previewContent = "thumb";
    expect(commerceInputIdentity(p)).toBe(identity);
});

test("单屏生成不被其它尚未填写的自定义屏阻塞", () => {
    const node = source(false, { screens: [screen("a"), { ...screen("b"), prompt: "", copy: "" }] });
    expect(commerceBatchTable(node, [node, image("p")], [edge("p")], "a").rows.map((row) => row.id)).toEqual(["a"]);
    expect(() => commerceBatchTable(node, [node, image("p")], [edge("p")])).toThrow();
});

test("跟随模板映射模型尺寸，不因比例相同从 2K 降到 1K", () => {
    const profile = defaultModelCapabilityConfig().image!;
    profile.size = { ...profile.size, parameter: "size", allowCustom: false, presets: undefined, values: ["1024x1024", "1024x1536", "2048x2048", "2048x3072"] };
    expect(commerceImageSize(profile, "2048x2048", "2:3")).toBe("2048x3072");
    expect(commerceImageSize(profile, "1024x1024", "2:3")).toBe("1024x1536");
});

test("单屏参考图更换资源后必须重新确认，不能沿用原参数暗换素材", () => {
    const node = source(false, { screens: [{ ...screen("a"), referenceNodeIds: ["e"] }], extraReferenceNodeIds: ["e"], extraReferenceIdentities: { e: "resource:e" } });
    const extra = image("e"); const nodes = [node, image("p"), extra];
    expect(commerceBatchTable(node, nodes, [edge("p")]).rows[0].inputNodeIds).toEqual(["p"]);
    extra.metadata!.storageKey = "resource:changed";
    expect(() => commerceBatchTable(node, nodes, [edge("p")])).toThrow("单屏参考图已失效");
});
