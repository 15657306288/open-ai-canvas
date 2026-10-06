import { expect, test } from "bun:test";
import { applyCommerceOutput, commerceBatchTable, commercePlanningPrompt, newCommerceWorkflow } from "../src/lib/canvas/commerce-workflow";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";
import { detailPlanFixture } from "./helpers/commerce-detail-plan-fixture";
import type { CommerceWorkflow } from "../src/types/commerce-workflow";

const picture = (id: string): CanvasNodeData => ({ id, title: id, type: CanvasNodeType.Image, position: { x: 0, y: 0 }, width: 300, height: 300, metadata: { storageKey: `resource:${id}` } });
const cards = ["hero", "lifestyle"].map((id) => ({ id, title: id, copy: "", prompt: "" }));
const data = () => ({ ...newCommerceWorkflow(), screenCount: 2, secondaryNodeIds: ["style"], screens: cards,
    pending: { operationId: "op", kind: "plan" as const, inputSnapshot: "", screens: cards, detailPlanVersion: 2 as const }, autoGenerate: "awaiting-plan" as const });
const source = (workflow: CommerceWorkflow = data()): CanvasNodeData => ({ ...picture("source"), type: CanvasNodeType.ProductDetail, metadata: { commerceWorkflow: workflow } });
const edges = ["product", "style"].map((id) => ({ id, fromNodeId: id, toNodeId: "source" }));

test("detail planning requests visible evidence, style analysis and an executable visual plan", () => {
    const node = source();
    const prompt = commercePlanningPrompt(node, [node, picture("product"), picture("style")], edges, cards);
    for (const key of ["productAnalysis", "styleAnalysis", "visualSystem", "visualPlan", "visualEvidence", "variation"]) expect(prompt).toContain(key);
    expect(prompt).toContain("风格参考图不会发送给生图模型");
    expect(prompt).toContain("不确定");
    expect(prompt).toContain("不是原产品照片的背景");
});

test("structured visual design is compiled into editable per-screen prompts, not discarded", () => {
    const original = data(); const raw = JSON.stringify(detailPlanFixture(cards.map((card) => card.title)));
    const result = applyCommerceOutput(original, raw);
    expect(result.autoGenerate).toBe("ready"); expect(result.pending).toBeUndefined(); expect(result.rawOutput).toBe(raw);
    const plan = detailPlanFixture(cards.map((card) => card.title));
    for (let i = 0; i < cards.length; i++) {
        for (const value of Object.values(plan.screens[i].visualPlan)) expect(result.screens[i].prompt).toContain(value);
        for (const value of Object.values(plan.visualSystem)) expect(result.screens[i].prompt).toContain(value);
    }
    expect(original.screens).toBe(cards); expect(original.pending).toBeDefined();
});

test("new planning cannot auto-generate from a vague legacy outline or incomplete analysis", () => {
    const outline = { screens: cards.map((card) => ({ ...card, prompt: "浅色背景，产品特写" })) };
    expect(() => applyCommerceOutput(data(), JSON.stringify(outline))).toThrow("分析");
    const invalid = detailPlanFixture(cards.map((card) => card.title));
    invalid.screens[0].visualPlan.composition = "";
    expect(() => applyCommerceOutput(data(), JSON.stringify(invalid))).toThrow("composition");
    const duplicate = detailPlanFixture(cards.map((card) => card.title));
    duplicate.screens[1].visualPlan = duplicate.screens[0].visualPlan;
    expect(() => applyCommerceOutput(data(), JSON.stringify(duplicate))).toThrow("重复");
});

test("detail global and single-screen style images are planner-only, including prompt overrides", () => {
    const original = data();
    const parsed = applyCommerceOutput(original, JSON.stringify(detailPlanFixture(cards.map((card) => card.title))));
    parsed.extraReferenceNodeIds = ["extra"]; parsed.extraReferenceIdentities = { extra: "resource:extra" };
    parsed.screens[0].referenceNodeIds = ["extra"]; parsed.screens[0].promptOverride = "人工完整提示词";
    const node = source(parsed);
    const nodes = [node, ...["product", "style", "extra"].map(picture)];
    const before = JSON.stringify(nodes);
    const rows = commerceBatchTable(node, nodes, edges).rows;
    expect(rows.map((row) => row.inputNodeIds)).toEqual([["product"], ["product"]]);
    expect(rows[0].prompt).toBe("人工完整提示词"); expect(rows[1].prompt).not.toContain("其余图片仅为风格参考");
    expect(rows[1].prompt).toContain("不是原产品照片的背景");
    expect(JSON.stringify(nodes)).toBe(before);
});

test("detailed replanning preserves manual fields and only replaces the target screen", () => {
    const original = data();
    const complete = applyCommerceOutput(original, JSON.stringify(detailPlanFixture(cards.map((card) => card.title))));
    const target = { ...complete.screens[1], sceneType: "人工指定场景", sellingPoints: "人工确认特征", promptOverride: "完整人工覆盖" };
    const partial: CommerceWorkflow = { ...complete, screens: [complete.screens[0], target], pending: { operationId: "replan", kind: "replan", inputSnapshot: "", screens: [target], detailPlanVersion: 2 } };
    const result = applyCommerceOutput(partial, JSON.stringify(detailPlanFixture([target.title])));
    expect(result.screens[0]).toBe(complete.screens[0]);
    expect(result.screens[1]).toMatchObject({ id: target.id, sceneType: "人工指定场景", sellingPoints: "人工确认特征", promptOverride: "完整人工覆盖" });
});

test("new plans fail explicitly rather than silently truncate visual decisions or copy", () => {
    const oversized = detailPlanFixture(cards.map((card) => card.title));
    oversized.screens[0].copy = "字".repeat(8001);
    expect(() => applyCommerceOutput(data(), JSON.stringify(oversized))).toThrow("不会截断");
    oversized.screens[0].copy = "文案";
    for (const key of Object.keys(oversized.screens[0].visualPlan) as Array<keyof typeof oversized.screens[0]["visualPlan"]>) oversized.screens[0].visualPlan[key] = "描述".repeat(1000);
    expect(() => applyCommerceOutput(data(), JSON.stringify(oversized))).toThrow("过长");
});
