import { afterAll, afterEach, beforeAll, beforeEach, expect, mock, spyOn, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { flushSync } from "react-dom";
import { App } from "antd";
import { useCommerceWorkflow } from "@/pages/canvas/use-commerce-workflow";
import { useCanvasBatchTable } from "@/pages/canvas/use-canvas-batch-table";
import { newCommerceWorkflow } from "@/lib/canvas/commerce-workflow";
import { buildGenerationTaskNodeResult } from "@/lib/canvas/canvas-generation-task-sync";
import * as generation from "@/lib/canvas/canvas-project-generation";
import * as hydration from "@/components/canvas/canvas-node-generation";
import * as tasks from "@/services/api/task-center";
import * as imageMerge from "@/lib/canvas/commerce-image-merge";
import * as fileSaver from "file-saver";
import { createModelChannel, defaultConfig, useConfigStore } from "@/stores/use-config-store";
import { defaultModelCapabilityConfig } from "@/lib/model-capabilities";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "@/types/canvas";
import { detailPlanFixture } from "./helpers/commerce-detail-plan-fixture";
import { replicaPlanFixture } from "./helpers/commerce-replica-plan-fixture";

const dom = new Window({ url: "http://localhost:3000" });
const globals = new Map<string, PropertyDescriptor | undefined>();
const originalConfig = useConfigStore.getState();
let root: Root | undefined, host: HTMLDivElement;
const errors: string[] = [];
let confirm: (options: { title?: unknown; content?: unknown; onOk: () => void; onCancel: () => void }) => void;
beforeAll(() => {
    for (const [key, value] of Object.entries({ window: dom, document: dom.document, navigator: dom.navigator, HTMLElement: dom.HTMLElement, Element: dom.Element, Node: dom.Node, SVGElement: dom.SVGElement,
        getComputedStyle: dom.getComputedStyle.bind(dom), IS_REACT_ACT_ENVIRONMENT: true })) {
        globals.set(key, Object.getOwnPropertyDescriptor(globalThis, key)); Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
    }
});
beforeEach(() => {
    errors.length = 0; confirm = (options) => options.onOk();
    spyOn(App, "useApp").mockReturnValue({ modal: { confirm: (options: Parameters<typeof confirm>[0]) => confirm(options) },
        message: { error: (text: string) => errors.push(text), warning: (text: string) => errors.push(text), info: () => {}, success: () => {} } } as unknown as ReturnType<typeof App.useApp>);
    const textCaps = defaultModelCapabilityConfig(); textCaps.text!.references.maxImages = 40;
    const imageCaps = defaultModelCapabilityConfig(); imageCaps.image!.references.maxImages = 40;
    const channel = createModelChannel({ id: "commerce-test", scope: "system", name: "Internal channel", models: ["vision", "image"], apiFormat: "openai", baseUrl: "/api", apiKey: "test-no-network",
        modelCosts: [{ model: "vision", capability: "text", billingMode: "token", inputTokenPriceMicrocredits: 1, outputTokenPriceMicrocredits: 1, cachedTokenPriceMicrocredits: 0, capabilityConfig: textCaps },
            { model: "image", capability: "image", billingMode: "per_call", unitPriceMicrocredits: 1, capabilityConfig: imageCaps }] });
    useConfigStore.setState({ config: { ...defaultConfig, channels: [channel], textModel: "commerce-test::vision", imageModel: "commerce-test::image" } });
    host = document.createElement("div"); document.body.append(host); root = createRoot(host);
    spyOn(hydration, "hydrateNodeGenerationContext").mockImplementation(async (context) => context);
});
afterEach(async () => {
    await act(async () => { root?.unmount(); }); root = undefined; host.remove(); mock.restore(); useConfigStore.setState(originalConfig);
});
afterAll(() => { dom.happyDOM.abort(); for (const [key, descriptor] of globals) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else Reflect.deleteProperty(globalThis, key); } });

const picture = (id: string): CanvasNodeData => ({ id, type: CanvasNodeType.Image, title: id, position: { x: 0, y: 0 }, width: 200, height: 200, metadata: { storageKey: `resource:${id}`, content: `resource:${id}`, status: "success" } });
async function setup(replica = false, readOnly = false) {
    const nodesRef = { current: [{ id: "source", type: replica ? CanvasNodeType.ProductReplica : CanvasNodeType.ProductDetail, title: "测试配置", position: { x: 0, y: 0 }, width: 660, height: 860,
        metadata: { model: "commerce-test::image", commerceWorkflow: { ...newCommerceWorkflow(), screenCount: 2, secondaryNodeIds: replica ? ["template"] : [] } } }, picture("product"), ...(replica ? [picture("template")] : [])] as CanvasNodeData[] };
    const connectionsRef = { current: [{ id: "p", fromNodeId: "product", toNodeId: "source" }, ...(replica ? [{ id: "t", fromNodeId: "template", toNodeId: "source" }] : [])] as CanvasConnection[] };
    let execution: ReturnType<typeof useCommerceWorkflow>;
    let batch: ReturnType<typeof useCanvasBatchTable>;
    let projectId = "canvas";
    let beforeComplete: (() => void | Promise<void>) | undefined;
    let failure: "none" | "invalid" | "unknown" | "outline" = "none";
    let mixedPlan = false;
    const submitted: Array<Parameters<typeof generation.runCanvasGenerationTaskToConsumer>[0]> = [];
    const queued: string[][] = [];
    const saved = new Map<string, tasks.GenerationTask>();
    const focused: string[] = [];
    const createFileNode = mock(async () => null);
    spyOn(generation, "runCanvasGenerationTaskToConsumer").mockImplementation(async (input, consumer) => {
        submitted.push(input);
        if (failure === "unknown") throw new Error("提交超时，状态未知");
        const node = nodesRef.current.find((node) => node.id === input.nodeId)!;
        const pending = node.metadata!.commerceWorkflow!.pending!;
        const detailed = detailPlanFixture(pending.screens.map((screen) => screen.title));
        for (const [index, screen] of detailed.screens.entries()) {
            screen.copy = mixedPlan && index === 0 ? "" : "产品细节";
            if (mixedPlan) { screen.sceneType = "产品摄影"; screen.sellingPoints = "已确认的银质耳针"; }
        }
        const raw = failure === "invalid" ? "not JSON" : pending.kind === "ocr" ? '{"texts":["旧文案","保持不变"]}' : pending.replicaPlanVersion === 1 && failure !== "outline" ? JSON.stringify(replicaPlanFixture(pending.screens)) : pending.detailPlanVersion === 2 && failure !== "outline" ? JSON.stringify(detailed) : JSON.stringify({ screens: pending.screens.map((screen) => ({ title: screen.title, copy: "产品细节", prompt: "浅色背景，产品特写" })) });
        const task: tasks.GenerationTask = { id: `task-${submitted.length}`, projectId, type: "canvas_text", status: "running", prompt: input.prompt,
            inputJson: JSON.stringify({ mode: "text", metadata: { nodeId: input.nodeId, clientOperationId: input.clientOperationId } }), attempts: 1, createdAt: "", updatedAt: "" };
        consumer.bindTask(task); input.onTextDelta?.(raw); await beforeComplete?.();
        task.status = "succeeded"; task.resultJson = JSON.stringify({ text: raw }); saved.set(task.id, task);
        consumer.bindTask(task); await consumer.consumeTask(task); return { text: raw };
    });
    spyOn(tasks, "queryGenerationTask").mockImplementation(async (id) => saved.get(id)!);
    const cancellation = spyOn(tasks, "cancelGenerationTask").mockImplementation(async (id) => ({ id, projectId, type: "canvas_text", status: "cancelled", prompt: "", attempts: 1, createdAt: "", updatedAt: "" }));
    function Harness() {
        const setNodes = (update: Parameters<React.Dispatch<React.SetStateAction<CanvasNodeData[]>>>[0]) => { nodesRef.current = typeof update === "function" ? update(nodesRef.current) : update; };
        const setConnections = (update: Parameters<React.Dispatch<React.SetStateAction<CanvasConnection[]>>>[0]) => { connectionsRef.current = typeof update === "function" ? update(connectionsRef.current) : update; };
        batch = useCanvasBatchTable({ nodesRef, connectionsRef, setNodes, setConnections, readOnly, setSelectedNodeIds: () => {},
            enqueueGenerationBatch: (_source, _mode, targets) => { queued.push(targets.map((target) => target.nodeId)); return `batch-${queued.length}`; } });
        execution = useCommerceWorkflow({ projectId, readOnly, nodesRef, connectionsRef, setNodes, setConnections,
            generateConfirmedRows: batch.generateConfirmedCommerceRows, createFileNode,
            startGenerationRequest: (_target, _origin, _running, controller) => controller!, finishGenerationRequest: () => {},
            bindGenerationTask: (id, task) => { nodesRef.current = nodesRef.current.map((node) => node.id === id ? { ...node, metadata: { ...node.metadata, taskId: task.id, taskStatus: task.status, status: "loading" } } : node); },
            applyGenerationTaskResult: async (id, task) => { const next = await buildGenerationTaskNodeResult(nodesRef.current.find((node) => node.id === id)!, task); nodesRef.current = nodesRef.current.map((node) => node.id === id ? next : node); },
            onResultCreated: (id) => focused.push(id),
        });
        return null;
    }
    await act(async () => root!.render(<Harness />));
    return { nodesRef, connectionsRef, submitted, queued, focused, cancellation, createFileNode,
        results: () => nodesRef.current.filter((node) => node.metadata?.commerceWorkflow?.role === "result"),
        setFailure: (value: typeof failure) => { failure = value; },
        useMixedPlan: () => { mixedPlan = true; },
        onComplete: (handler: () => void | Promise<void>) => { beforeComplete = handler; },
        actionRaw: (...args: Parameters<typeof execution.run>) => execution.run(...args),
        rerender: async (nextProject = projectId) => { projectId = nextProject; await act(async () => { flushSync(() => root!.render(<Harness />)); }); },
        patch: async (...args: Parameters<typeof execution.patch>) => { await act(async () => execution.patch(...args)); },
        merge: async (...args: Parameters<typeof execution.merge>) => { await act(async () => { await execution.merge(...args); }); },
        run: async (...args: Parameters<typeof execution.run>) => { await act(async () => { await execution.run(...args); }); },
    };
}

function completedGroup(work: Awaited<ReturnType<typeof setup>>) {
    const group: CanvasNodeData = { ...work.nodesRef.current[0], id: "completed", metadata: {
        commerceWorkflow: { ...newCommerceWorkflow(), role: "result", screens: ["s2", "s1"].map((id) => ({ id, title: id, copy: "", prompt: "" })) },
        batchTable: { operation: "creative", concurrency: 2, rows: ["s1", "s2"].map((id) => ({ id, enabled: true, prompt: "", inputNodeIds: [], outputNodeId: `out-${id}` })) },
    } };
    work.nodesRef.current.push(group, picture("out-s1"), picture("out-s2"));
    return group;
}

test("一键合并只建资源引用组与连线，不合成、不上传、不提交生成", async () => {
    const work = await setup(); const group = completedGroup(work);
    const composite = spyOn(imageMerge, "mergeCommerceImages").mockRejectedValue(new Error("不应合成"));
    await work.merge(group.id, false);
    expect(errors).toEqual([]); expect(composite).not.toHaveBeenCalled(); expect(work.createFileNode).not.toHaveBeenCalled();
    expect(work.submitted).toHaveLength(0); expect(work.queued).toHaveLength(0);
    const frame = work.nodesRef.current.find((node) => node.metadata?.imageAssembly)!;
    expect(frame.type).toBe(CanvasNodeType.Frame); expect(work.focused).toEqual([frame.id]);
    expect(work.nodesRef.current.filter((node) => node.parentId === frame.id).map((node) => node.metadata?.storageKey)).toEqual(["resource:out-s2", "resource:out-s1"]);
    expect(work.connectionsRef.current.at(-1)).toMatchObject({ fromNodeId: group.id, toNodeId: frame.id, relation: "batch-output" });
});

test("只有下载长图才合成原图，仍不上传或创建新节点", async () => {
    const work = await setup(); const group = completedGroup(work); const blob = new Blob(["png"], { type: "image/png" });
    const composite = spyOn(imageMerge, "mergeCommerceImages").mockResolvedValue(blob);
    const download = spyOn(fileSaver, "saveAs").mockImplementation(() => {});
    const count = work.nodesRef.current.length;
    await work.merge(group.id, true);
    expect(errors).toEqual([]); expect(composite).toHaveBeenCalledTimes(1);
    expect(composite.mock.calls[0][0].map((node) => node.id)).toEqual(["out-s2", "out-s1"]);
    expect(download).toHaveBeenCalledWith(blob, `${group.title}.png`);
    expect(work.createFileNode).not.toHaveBeenCalled(); expect(work.nodesRef.current).toHaveLength(count);
});

test("只读、锁定和未完成结果不能创建引用拼合组", async () => {
    const work = await setup(); const group = completedGroup(work);
    const composite = spyOn(imageMerge, "mergeCommerceImages").mockRejectedValue(new Error("不应合成"));
    group.metadata!.locked = true; await work.merge(group.id, false);
    group.metadata!.locked = false; work.nodesRef.current.find((node) => node.id === "out-s1")!.metadata!.status = "loading";
    const count = work.nodesRef.current.length; await work.merge(group.id, false);
    expect(errors.at(-1)).toContain("所有屏"); expect(work.nodesRef.current).toHaveLength(count); expect(composite).not.toHaveBeenCalled();
    expect(work.createFileNode).not.toHaveBeenCalled();
});

test("只读页面不合并或导出", async () => {
    const work = await setup(false, true); const group = completedGroup(work); const count = work.nodesRef.current.length;
    await work.merge(group.id, false); await work.merge(group.id, true);
    expect(work.nodesRef.current).toHaveLength(count); expect(work.createFileNode).not.toHaveBeenCalled();
});

test("下载等待期间切换项目，迟到合成结果不触发保存", async () => {
    const work = await setup(); const group = completedGroup(work);
    spyOn(imageMerge, "mergeCommerceImages").mockImplementation(async () => { await work.rerender("other-canvas"); return new Blob(["png"]); });
    const download = spyOn(fileSaver, "saveAs").mockImplementation(() => {});
    await work.merge(group.id, true);
    expect(download).not.toHaveBeenCalled(); expect(work.createFileNode).not.toHaveBeenCalled();
});

test("一键策划只交文本任务，完成停在独立结果组，确认后才出图", async () => {
    const work = await setup(); await work.run("source", "plan");
    expect(errors).toEqual([]); expect(work.submitted).toHaveLength(1); expect(work.queued).toHaveLength(0);
    const result = work.results()[0]; expect(result.metadata?.commerceWorkflow?.screens).toHaveLength(2);
    await work.run(result.id, "generate"); expect(errors).toEqual([]); expect(work.queued[0]).toHaveLength(2);
    expect(work.submitted).toHaveLength(1);
});

test("直接生成明确授权后连跑策划与图片，两种模型不相互覆盖", async () => {
    const work = await setup(); await work.run("source", "direct");
    expect(errors).toEqual([]); expect(work.submitted).toHaveLength(1); expect(work.queued[0]).toHaveLength(2);
    const group = work.results()[0]; expect(group.metadata?.commerceWorkflow?.autoGenerate).toBeUndefined();
    expect(group.metadata?.model).toBe("commerce-test::image");
    expect(work.nodesRef.current.find((node) => node.id === work.queued[0][0])?.metadata?.generationSpec?.modelSelection).toEqual({ kind: "channel", channelId: "commerce-test", modelKey: "image" });
});

for (const action of ["plan", "direct"] as const) {
    test(`${action} 空文案摄影屏与有字参数屏均可生成，单屏再生成不补收费策划`, async () => {
        const work = await setup(); work.useMixedPlan(); await work.run("source", action);
        expect(errors).toEqual([]); expect(work.submitted).toHaveLength(1);
        let group = work.results()[0];
        expect(group.metadata!.commerceWorkflow!.screens[0]).toMatchObject({ copy: "", sceneType: "产品摄影", sellingPoints: "已确认的银质耳针" });
        if (action === "plan") { expect(work.queued).toHaveLength(0); await work.run(group.id, "generate"); }
        expect(errors).toEqual([]); expect(work.queued[0]).toHaveLength(2);
        const [photoId, textId] = work.queued[0];
        expect(work.nodesRef.current.find((node) => node.id === photoId)!.metadata!.prompt).toContain("不要添加任何排版文字");
        expect(work.nodesRef.current.find((node) => node.id === textId)!.metadata!.prompt).toContain("画面文字必须逐字使用以下文案");
        work.nodesRef.current = work.nodesRef.current.map((node) => node.id === photoId ? { ...node, metadata: { ...node.metadata, status: "success", storageKey: "resource:prior-photo", content: "resource:prior-photo" } } : node);
        group = work.results()[0];
        await work.run(group.id, "generate", group.metadata!.commerceWorkflow!.screens[0].id);
        expect(errors).toEqual([]); expect(work.queued[1]).toHaveLength(1); expect(work.submitted).toHaveLength(1);
        expect(work.nodesRef.current.find((node) => node.id === photoId)!.metadata!.storageKey).toBe("resource:prior-photo");
    });
}

for (const action of ["plan", "direct"] as const) {
    test(`${action} 真实工作流先理解全局资料，确认、直接出图及单卡重生成均只采用该屏方案`, async () => {
        const work = await setup();
        const brief = "第一屏产品特写，第二屏工艺展示；仅第二屏说明接缝；全部西班牙语。";
        const linked = "连线策划要求：雨衣，不虚构任何认证。";
        work.nodesRef.current[0].metadata!.commerceWorkflow!.brief = brief;
        work.nodesRef.current.push({ ...picture("brief"), type: CanvasNodeType.Text, metadata: { content: linked } });
        work.connectionsRef.current.push({ id: "brief-edge", fromNodeId: "brief", toNodeId: "source" });
        await work.run("source", action);
        expect(errors).toEqual([]); expect(work.submitted).toHaveLength(1);
        expect(work.submitted[0].prompt).toContain(brief); expect(work.submitted[0].prompt).toContain(linked);
        const group = work.results()[0];
        if (action === "plan") { expect(work.queued).toHaveLength(0); await work.run(group.id, "generate"); }
        expect(work.queued[0]).toHaveLength(2);
        const firstOutput = work.queued[0][0];
        work.nodesRef.current = work.nodesRef.current.map((node) => node.id === firstOutput ? { ...node, metadata: { ...node.metadata, status: "success", content: "resource:completed", storageKey: "resource:completed" } } : node);
        const card = group.metadata!.commerceWorkflow!.screens[0];
        await work.patch(group.id, { screens: group.metadata!.commerceWorkflow!.screens.map((item) => item.id === card.id ? { ...item, prompt: "本屏手改画面：雨衣袖口特写", copy: "Costuras" } : item) });
        await work.run(group.id, "generate", card.id);
        expect(errors).toEqual([]); expect(work.queued[1]).toHaveLength(1); expect(work.submitted).toHaveLength(1);
        expect(work.nodesRef.current.find((node) => node.id === firstOutput)?.metadata?.storageKey).toBe("resource:completed");
        for (const outputId of work.queued.flat()) {
            const output = work.nodesRef.current.find((node) => node.id === outputId)!;
            for (const prompt of [output.metadata?.prompt, output.metadata?.composerContent, output.metadata?.generationSpec?.prompt]) {
                expect(prompt).toBeTruthy();
                expect(prompt).not.toContain(brief); expect(prompt).not.toContain(linked);
                expect(prompt).not.toContain("模特：自动判断"); expect(prompt).not.toContain("字体：自动判断");
            }
            expect(output.metadata?.batchInputNodeIds).toEqual(["product"]);
            expect(work.connectionsRef.current.some((edge) => edge.toNodeId === outputId && edge.fromNodeId === "brief")).toBe(false);
        }
        const retry = work.nodesRef.current.find((node) => node.id === work.queued[1][0])!;
        expect(retry.metadata?.prompt).toContain("本屏手改画面：雨衣袖口特写"); expect(retry.metadata?.prompt).toContain("Costuras");
    });
}

test("自定义策划填写单卡后仅出图，不把全局要求当隐含提示词，也不补收费策划", async () => {
    const work = await setup();
    await work.patch("source", { brief: "完整六屏资料仅供AI策划", font: "粗黑标题" });
    await work.run("source", "manual"); const group = work.results()[0];
    await work.patch(group.id, { screens: group.metadata!.commerceWorkflow!.screens.map((item) => ({ ...item, prompt: "手写画面方案", copy: "手写文案" })) });
    await work.run(group.id, "generate");
    expect(errors).toEqual([]); expect(work.submitted).toHaveLength(0); expect(work.queued[0]).toHaveLength(2);
    for (const id of work.queued[0]) {
        const prompt = work.nodesRef.current.find((node) => node.id === id)!.metadata!.prompt;
        expect(prompt).toContain("手写画面方案"); expect(prompt).toContain("手写文案");
        expect(prompt).not.toContain("完整六屏资料仅供AI策划"); expect(prompt).not.toContain("粗黑标题");
    }
});

test("自定义策划免费建组，空白提交报错，不自动补策划", async () => {
    const work = await setup(); await work.run("source", "manual"); const group = work.results()[0];
    expect(work.submitted).toHaveLength(0); await work.run(group.id, "generate");
    expect(work.focused).toEqual([group.id]);
    expect(work.queued).toHaveLength(0); expect(work.submitted).toHaveLength(0); expect(errors[0]).toContain("不会自动调用收费策划");
    await work.patch(group.id, { screens: group.metadata!.commerceWorkflow!.screens.map((screen) => ({ ...screen, promptOverride: "手工完整提示词" })) });
    await work.run(group.id, "generate"); expect(work.queued[0]).toHaveLength(2); expect(work.submitted).toHaveLength(0);
});

test("复刻保持原文先分析再出图，旧调用入口不绕过分析且每次保留旧结果", async () => {
    const work = await setup(true); await work.run("source", "generate"); await work.run("source", "generate");
    expect(errors).toEqual([]); expect(work.submitted).toHaveLength(2); expect(work.results()).toHaveLength(2);
    expect(work.queued.map((batch) => batch.length)).toEqual([1, 1]); expect(work.queued[0][0]).not.toBe(work.queued[1][0]);
    expect(work.submitted[0].referenceImages).toHaveLength(2);
    for (const id of work.queued.flat()) {
        const result = work.nodesRef.current.find((node) => node.id === id)!;
        expect(result.metadata?.batchInputNodeIds).toEqual(["product", "template"]);
        expect(result.metadata?.prompt).toContain("重建杯底接触阴影");
        expect(result.metadata?.prompt).toContain("保持本屏模板中的原文案");
    }
});

test("复刻分析后待确认，单模板重分析只发送对应模板并保留人工覆盖", async () => {
    const work = await setup(true);
    work.nodesRef.current.push(picture("template-2"));
    work.connectionsRef.current.push({ id: "t2", fromNodeId: "template-2", toNodeId: "source" });
    const brief = "仅供分析的全组要求，保留模板的景别";
    await work.patch("source", { secondaryNodeIds: ["template", "template-2"], brief, copyMode: "rewrite", templateCopies: {
        template: { identity: "resource:template", recognized: true, pairs: [{ id: "pair", original: "旧原文", replacement: "用户指定新文案" }] },
    } });
    await work.run("source", "plan");
    expect(errors).toEqual([]); expect(work.queued).toHaveLength(0); expect(work.submitted).toHaveLength(1);
    expect(work.submitted[0].referenceImages).toHaveLength(3);
    expect(work.submitted[0].prompt).toContain(brief); expect(work.submitted[0].prompt).toContain("用户指定新文案");
    let group = work.results()[0]; const screens = group.metadata!.commerceWorkflow!.screens;
    await work.patch(group.id, { screens: screens.map((screen, index) => index === 1 ? { ...screen, promptOverride: "人工完整替换方案" } : screen) });
    await work.run(group.id, "replan", screens[1].id);
    expect(errors).toEqual([]); expect(work.submitted[1].referenceImages).toHaveLength(2);
    expect(work.submitted[1].prompt).not.toContain('"nodeId":"template"');
    expect(work.submitted[1].prompt).toContain('"nodeId":"template-2"');
    expect(work.queued).toHaveLength(0);
    group = work.results()[0];
    expect(group.metadata!.commerceWorkflow!.screens[0]).toEqual(screens[0]);
    expect(group.metadata!.commerceWorkflow!.screens[1].promptOverride).toBe("人工完整替换方案");
    await work.run(group.id, "generate");
    expect(errors).toEqual([]); expect(work.submitted).toHaveLength(2); expect(work.queued[0]).toHaveLength(2);
    const outputs = work.queued[0].map((id) => work.nodesRef.current.find((node) => node.id === id)!);
    expect(outputs.map((node) => node.metadata?.batchInputNodeIds)).toEqual([["product", "template"], ["product", "template-2"]]);
    expect(outputs[0].metadata?.prompt).toContain("用户指定新文案");
    expect(outputs[0].metadata?.prompt).not.toContain(brief);
    expect(outputs[1].metadata?.prompt).toBe("人工完整替换方案");
});

test("复刻分析只有笼统大纲时保留错误响应，不继续收费出图", async () => {
    const work = await setup(true); work.setFailure("outline"); await work.run("source", "direct");
    const group = work.results()[0];
    expect(group.metadata?.status).toBe("error"); expect(group.metadata?.errorDetails).toContain("分析");
    expect(group.metadata?.commerceWorkflow?.rawOutput).toContain("浅色背景，产品特写");
    expect(group.metadata?.commerceWorkflow?.pending?.replicaPlanVersion).toBe(1);
    expect(work.submitted).toHaveLength(1); expect(work.queued).toHaveLength(0);
});

test("复刻确认披露两阶段费用及真实模板数量，取消不建组不提交", async () => {
    const work = await setup(true);
    confirm = (options) => {
        expect(options.title).toBe("分析模板并一键复刻？");
        expect(options.content).toContain("分析产品和模板");
        expect(options.content).toContain("1 个图片任务");
        expect(options.content).toContain("两个阶段分别");
        options.onCancel();
    };
    await work.run("source", "direct");
    expect(work.results()).toHaveLength(0); expect(work.submitted).toHaveLength(0); expect(work.queued).toHaveLength(0);
});

test("复刻分析未知提交恢复复用指纹与操作 ID，不降级直接出图", async () => {
    const work = await setup(true); work.setFailure("unknown"); await work.run("source", "direct");
    expect(work.queued).toHaveLength(0);
    const group = work.results()[0]; const pending = group.metadata!.commerceWorkflow!.pending!;
    expect(pending.replicaPlanVersion).toBe(1);
    await work.run(group.id, "release"); expect(errors.at(-1)).toContain("避免重复计费");
    work.setFailure("none"); await work.run(group.id, "resume");
    expect(work.submitted[1].clientOperationId).toBe(work.submitted[0].clientOperationId);
    expect(work.submitted[1].prompt).toBe(work.submitted[0].prompt);
    expect(work.queued).toHaveLength(1);
});

for (const action of ["stop", "switch"] as const) {
    test(`复刻分析期间 ${action} 后不自动出图`, async () => {
        const work = await setup(true);
        work.onComplete(async () => { if (action === "stop") await work.actionRaw(work.results()[0].id, "stop"); else await work.rerender("new-project"); });
        await work.run("source", "direct");
        expect(work.submitted).toHaveLength(1); expect(work.queued).toHaveLength(0);
    });
}

test("模板 OCR 仅传一张模板，复用不发第二次请求", async () => {
    const work = await setup(true); await work.run("source", "ocr", "template");
    expect(errors).toEqual([]); expect(work.submitted).toHaveLength(1); expect(work.submitted[0].referenceImages).toHaveLength(1);
    expect(work.nodesRef.current[0].metadata?.commerceWorkflow?.templateCopies?.template.recognized).toBe(true);
    await work.run("source", "ocr", "template"); expect(work.submitted).toHaveLength(1); expect(work.queued).toHaveLength(0);
});

test("策划解析失败不自动生图，保留原响应供修正，修正后仍需确认", async () => {
    const work = await setup(); work.setFailure("invalid"); await work.run("source", "direct");
    const group = work.results()[0]; expect(work.queued).toHaveLength(0); expect(group.metadata?.commerceWorkflow?.pending).toBeDefined();
    await work.patch(group.id, { rawOutput: JSON.stringify(detailPlanFixture(["新策划一", "新策划二"])) });
    await work.run(group.id, "apply"); expect(work.queued).toHaveLength(0);
    expect(work.results()[0].metadata?.commerceWorkflow?.pending).toBeUndefined();
});

test("详情真实流程将产品和风格图交给策划，但整组、单屏与重生成仅连入产品", async () => {
    const work = await setup();
    work.nodesRef.current.push(picture("style"), picture("screen-style"));
    work.connectionsRef.current.push({ id: "style-input", fromNodeId: "style", toNodeId: "source" });
    await work.patch("source", { secondaryNodeIds: ["style"] });
    await work.run("source", "direct");
    expect(errors).toEqual([]); expect(work.submitted).toHaveLength(1);
    expect(work.submitted[0].referenceImages.map((image) => image.storageKey)).toEqual(["resource:product", "resource:style"]);
    const group = work.results()[0]; const data = group.metadata!.commerceWorkflow!;
    expect(data.screens[0].prompt).toContain("镜头与构图");
    expect(data.screens[0].prompt).toContain("左下低机位近景");
    const oldOutputs = [...work.queued[0]];
    for (const outputId of oldOutputs) {
        const output = work.nodesRef.current.find((node) => node.id === outputId)!;
        expect(output.metadata?.batchInputNodeIds).toEqual(["product"]);
        expect(work.connectionsRef.current.filter((edge) => edge.toNodeId === outputId).map((edge) => edge.fromNodeId)).not.toContain("style");
    }
    work.nodesRef.current = work.nodesRef.current.map((node) => oldOutputs.includes(node.id) ? { ...node, metadata: { ...node.metadata, status: "success", content: `resource:${node.id}` } } : node);
    await work.patch(group.id, { extraReferenceNodeIds: ["screen-style"], extraReferenceIdentities: { "screen-style": "resource:screen-style" }, screens: data.screens.map((screen, index) => index === 0 ? { ...screen, referenceNodeIds: ["screen-style"] } : screen) });
    await work.run(group.id, "replan", data.screens[0].id);
    expect(errors).toEqual([]); expect(work.submitted).toHaveLength(2);
    expect(work.submitted[1].referenceImages.map((image) => image.storageKey)).toEqual(["resource:product", "resource:style", "resource:screen-style"]);
    expect(work.submitted[1].prompt).toContain("其它屏已确定的方案");
    await work.run(group.id, "generate", data.screens[0].id);
    expect(errors).toEqual([]); expect(work.queued).toHaveLength(2); expect(work.queued[1]).toHaveLength(1);
    const output = work.nodesRef.current.find((node) => node.id === work.queued[1][0])!;
    expect(output.metadata?.batchInputNodeIds).toEqual(["product"]);
    expect(work.connectionsRef.current.filter((edge) => edge.toNodeId === output.id).map((edge) => edge.fromNodeId)).not.toContain("screen-style");
});

test("直接生成遇到仅有大纲的策划停止在可修正状态，不启动图片收费队列", async () => {
    const work = await setup(); work.setFailure("outline");
    await work.run("source", "direct");
    const result = work.results()[0];
    expect(result.metadata?.status).toBe("error");
    expect(result.metadata?.errorDetails).toContain("产品分析");
    expect(result.metadata?.commerceWorkflow?.rawOutput).toContain("浅色背景，产品特写");
    expect(result.metadata?.commerceWorkflow?.pending?.detailPlanVersion).toBe(2);
    expect(work.submitted).toHaveLength(1); expect(work.queued).toHaveLength(0);
});

test("未知提交不得解锁重发，恢复使用原幂等 operationId", async () => {
    const work = await setup(); work.setFailure("unknown"); await work.run("source", "plan");
    const group = work.results()[0]; const operation = work.submitted[0].clientOperationId;
    await work.run(group.id, "release"); expect(errors.at(-1)).toContain("避免重复计费");
    work.setFailure("none"); await work.run(group.id, "resume");
    expect(work.submitted[1].clientOperationId).toBe(operation); expect(work.queued).toHaveLength(0);
});

test("只读或取消确认不创建结果、不提交任务", async () => {
    const work = await setup(false, true); await work.run("source", "direct"); expect(work.results()).toHaveLength(0);
    expect(work.submitted).toHaveLength(0);
});

test("生成确认期间输入改变，原授权失效，不创建结果组", async () => {
    const work = await setup(); confirm = (options) => { work.nodesRef.current[1].metadata!.storageKey = "resource:changed"; options.onOk(); };
    await work.run("source", "direct"); expect(work.results()).toHaveLength(0); expect(work.submitted).toHaveLength(0); expect(errors[0]).toContain("配置或引用已变化");
});

test("直接生成等待模型期间重复点击不会重复创建收费请求", async () => {
    const work = await setup();
    work.onComplete(async () => { await work.actionRaw("source", "direct"); await work.actionRaw(work.results()[0].id, "generate"); });
    await work.run("source", "direct");
    expect(work.submitted).toHaveLength(1); expect(work.results()).toHaveLength(1); expect(work.queued).toHaveLength(1);
});

test("单屏生成时另一屏可独立入队，同一屏不可重复提交", async () => {
    const work = await setup(); await work.run("source", "manual"); const group = work.results()[0];
    const screens = group.metadata!.commerceWorkflow!.screens.map((screen) => ({ ...screen, promptOverride: "手工完整提示词" }));
    await work.patch(group.id, { screens });
    await work.run(group.id, "generate", screens[0].id);
    const firstOutput = work.queued[0][0];
    work.nodesRef.current = work.nodesRef.current.map((node) => node.id === group.id ? { ...node, metadata: { ...node.metadata, generationBatches: [{
        id: "active-first", projectId: "canvas", sourceNodeId: group.id, mode: "batch_image", status: "running", createdAt: "", updatedAt: "",
        items: [{ id: "first-item", rowId: screens[0].id, nodeId: firstOutput, status: "running", retryCount: 0 }] }] } } : node);
    await work.run(group.id, "generate", screens[1].id);
    expect(errors).toEqual([]); expect(work.queued).toHaveLength(2);
    expect(work.queued[1]).toHaveLength(1); expect(work.queued[1][0]).not.toBe(firstOutput);
    expect(work.results()[0].metadata?.batchTable?.rows[0].outputNodeId).toBe(firstOutput);
    await work.run(group.id, "generate", screens[0].id);
    expect(work.queued).toHaveLength(2); expect(errors.at(-1)).toContain("本屏");
    expect(work.submitted).toHaveLength(0);
});

test("直接生成停止后，即使文本结果晚到也不继续收费出图", async () => {
    const work = await setup(); work.onComplete(async () => { await work.actionRaw(work.results()[0].id, "stop"); });
    await work.run("source", "direct");
    expect(work.cancellation).toHaveBeenCalledTimes(1); expect(work.queued).toHaveLength(0);
    expect(work.results()[0].metadata?.commerceWorkflow?.autoGenerate).toBeUndefined();
});

test("用户取消付费确认不创建新组或模型任务", async () => {
    const work = await setup(); confirm = (options) => options.onCancel(); await work.run("source", "direct");
    expect(work.results()).toHaveLength(0); expect(work.submitted).toHaveLength(0); expect(work.queued).toHaveLength(0);
});

test("项目切换使原流程失效，晚到策划不会向新项目出图", async () => {
    const work = await setup(); work.onComplete(async () => { await work.rerender("another-canvas"); });
    await work.run("source", "direct"); expect(work.queued).toHaveLength(0);
    expect(work.results()[0].metadata?.commerceWorkflow?.screens.every((screen) => !screen.prompt)).toBe(true);
});
