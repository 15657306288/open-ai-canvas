import { expect, test } from "bun:test";
import { applyCommerceOutput, commerceBatchTable, commercePlanningImageIds, commercePlanningPrompt, newCommerceWorkflow } from "../src/lib/canvas/commerce-workflow";
import { createCommerceResult } from "../src/lib/canvas/commerce-workflow-graph";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";
import type { CommerceWorkflow } from "../src/types/commerce-workflow";
import { replicaPlanFixture } from "./helpers/commerce-replica-plan-fixture";

const picture = (id: string): CanvasNodeData => ({ id, title: id, type: CanvasNodeType.Image, position: { x: 0, y: 0 }, width: 300, height: 300, metadata: { storageKey: `resource:${id}` } });
const screens = ["t1", "t2"].map((id) => ({ id: `screen-${id}`, templateNodeId: id, title: id, copy: "", prompt: "" }));
const workflow = (): CommerceWorkflow => ({ ...newCommerceWorkflow(), secondaryNodeIds: ["t1", "t2"], screens, runMode: "direct", autoGenerate: "awaiting-plan", pending: { kind: "plan", operationId: "op", inputSnapshot: "", screens, replicaPlanVersion: 1 } });
const source = (data = workflow()): CanvasNodeData => ({ ...picture("source"), type: CanvasNodeType.ProductReplica, metadata: { commerceWorkflow: data } });
const pictures = ["p1", "p2", "t1", "t2"].map(picture);
const edges = pictures.map((node) => ({ id: node.id, fromNodeId: node.id, toNodeId: "source" }));

test("replica planning requests template decomposition and concrete replacement decisions", () => {
    const node = source();
    const prompt = commercePlanningPrompt(node, [node, ...pictures], edges, screens);
    for (const key of ["productAnalysis", "templateAnalysis", "replacementPlan", "screenId", "templateNodeId"]) expect(prompt).toContain(key);
    expect(prompt).toContain("不是自由创作新的详情页");
    expect(prompt).toContain("原文替换映射");
});

test("replica analysis compiles into editable prompts and preserves template identity even if rows reorder", () => {
    const data = workflow(); const raw = replicaPlanFixture(screens); raw.screens.reverse();
    const result = applyCommerceOutput(data, JSON.stringify(raw));
    expect(result.autoGenerate).toBe("ready"); expect(result.pending).toBeUndefined();
    expect(result.screens.map((screen) => screen.id)).toEqual(screens.map((screen) => screen.id));
    for (let i = 0; i < screens.length; i++) {
        expect(result.screens[i].templateNodeId).toBe(screens[i].templateNodeId);
        for (const value of Object.values(raw.screens[i].replacementPlan)) expect(result.screens[i].prompt).toContain(value);
        expect(result.screens[i].prompt).toContain("模板高脚杯");
    }
    expect(data.screens).toBe(screens);
});

test("wrong template mapping, duplicate rows and incomplete plans stop before image generation", () => {
    for (const change of [
        (plan: ReturnType<typeof replicaPlanFixture>) => { plan.screens[0].templateNodeId = "t2"; },
        (plan: ReturnType<typeof replicaPlanFixture>) => { plan.screens[1].screenId = plan.screens[0].screenId; },
        (plan: ReturnType<typeof replicaPlanFixture>) => { plan.screens[0].replacementPlan.placement = ""; },
    ]) {
        const plan = replicaPlanFixture(screens); change(plan);
        expect(() => applyCommerceOutput(workflow(), JSON.stringify(plan))).toThrow();
    }
    expect(() => applyCommerceOutput(workflow(), JSON.stringify({ screens: screens.map((screen) => ({ ...screen, prompt: "参考模板换产品" })) }))).toThrow("分析");
});

test("new replica groups require a filled plan, while explicit full prompts and legacy groups remain usable", () => {
    const node = source({ ...workflow(), pending: undefined }); const nodes = [node, ...pictures];
    expect(() => commerceBatchTable(node, nodes, edges)).toThrow("画面描述");
    node.metadata!.commerceWorkflow!.screens = screens.map((screen) => ({ ...screen, promptOverride: "人工确认的完整提示词" }));
    expect(commerceBatchTable(node, nodes, edges).rows).toHaveLength(2);
    node.metadata!.commerceWorkflow = { ...workflow(), runMode: "replica", screens };
    expect(commerceBatchTable(node, nodes, edges).rows).toHaveLength(2);
});

test("planned replica images retain only the matching template, and raw briefs stay with the planner", () => {
    const data = applyCommerceOutput(workflow(), JSON.stringify(replicaPlanFixture(screens)));
    data.brief = "只用于策划的全组需求";
    const node = source(data);
    const rows = commerceBatchTable(node, [node, ...pictures], edges).rows;
    expect(rows.map((row) => row.inputNodeIds)).toEqual([["p1", "p2", "t1"], ["p1", "p2", "t2"]]);
    expect(rows[0].prompt).not.toContain(data.brief);
    expect(rows[0].prompt).toContain("保持本屏模板中的原文案");
    expect(rows[0].prompt).toContain("重建杯底接触阴影");
});

test("single-template reanalysis only sends products and that template, and direct replica groups await planning", () => {
    const node = source(); const nodes = [node, ...pictures];
    expect(commercePlanningImageIds(node, nodes, edges, [screens[1]])).toEqual(["p1", "p2", "t2"]);
    const created = createCommerceResult(node, nodes, edges, "direct");
    expect(created.result.title).toContain("复刻");
    expect(created.result.metadata?.commerceWorkflow?.autoGenerate).toBe("awaiting-plan");
});

test("legacy pending plans keep their original schema and image order instead of silently upgrading a charged request", () => {
    const data = workflow(); delete data.pending!.replicaPlanVersion;
    const node = source(data); const nodes = [node, ...pictures];
    expect(commercePlanningPrompt(node, nodes, edges, [screens[1]], false)).not.toContain("templateAnalysis");
    expect(commercePlanningImageIds(node, nodes, edges, [screens[1]], false)).toEqual(["p1", "p2", "t1", "t2"]);
    const parsed = applyCommerceOutput(data, JSON.stringify({ screens: screens.map((screen) => ({ title: screen.title, copy: "", prompt: "已提交的旧版方案" })) }));
    expect(parsed.screens.every((screen) => screen.prompt === "已提交的旧版方案")).toBe(true);
});

test("replica rejects oversized or missing visual decisions without truncating or discarding manual overrides", () => {
    const data = workflow(); data.pending!.screens = screens.map((screen) => ({ ...screen, promptOverride: "人工覆盖" }));
    const plan = replicaPlanFixture(screens); plan.screens[0].replacementPlan.execution = "x".repeat(6001);
    expect(() => applyCommerceOutput(data, JSON.stringify(plan))).toThrow("6000");
    expect(data.pending).toBeDefined(); expect(data.pending!.screens[0].promptOverride).toBe("人工覆盖");
    const restored = JSON.parse(JSON.stringify(data)) as CommerceWorkflow;
    const parsed = applyCommerceOutput(restored, JSON.stringify(replicaPlanFixture(screens)));
    expect(parsed.screens[0].promptOverride).toBe("人工覆盖");
    expect(parsed.screens[0].prompt).toContain("未提供容量和耐热参数");
});
