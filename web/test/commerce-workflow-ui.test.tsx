import { afterAll, afterEach, expect, mock, test } from "bun:test";
import { Window } from "happy-dom";
import { canvasThemes } from "@/lib/canvas-theme";
import { newCommerceWorkflow } from "@/lib/canvas/commerce-workflow";
import { commerceResultLayout } from "@/lib/canvas/commerce-result-layout";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";
import type { useCommerceWorkflow } from "@/pages/canvas/use-commerce-workflow";
import { createModelChannel, defaultConfig, useConfigStore } from "@/stores/use-config-store";
import { useUserStore } from "@/stores/use-user-store";

const dom = new Window({ url: "http://localhost:3000" });
const originals = new Map<string, PropertyDescriptor | undefined>();
for (const [key, value] of Object.entries({ window: dom, document: dom.document, navigator: dom.navigator,
    HTMLElement: dom.HTMLElement, HTMLBodyElement: dom.HTMLBodyElement, HTMLHtmlElement: dom.HTMLHtmlElement,
    Element: dom.Element, Node: dom.Node, SVGElement: dom.SVGElement, ShadowRoot: dom.ShadowRoot,
    Event: dom.Event, MouseEvent: dom.MouseEvent, PointerEvent: dom.PointerEvent, ResizeObserver: dom.ResizeObserver,
    getComputedStyle: dom.getComputedStyle.bind(dom), requestAnimationFrame: dom.requestAnimationFrame.bind(dom), cancelAnimationFrame: dom.cancelAnimationFrame.bind(dom), IS_REACT_ACT_ENVIRONMENT: true })) {
    originals.set(key, Object.getOwnPropertyDescriptor(globalThis, key)); Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
}
const { act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { CommerceWorkflowNode } = await import("@/components/canvas/commerce-workflow-node");
const savedConfig = useConfigStore.getState().config;
const savedFeatures = useUserStore.getState().features;
let root: ReturnType<typeof createRoot> | undefined, host: HTMLDivElement | undefined;
afterEach(async () => { await act(async () => root?.unmount()); root = undefined; host?.remove(); host = undefined; useConfigStore.setState({ config: savedConfig }); useUserStore.setState({ features: savedFeatures }); });
afterAll(() => { dom.happyDOM.abort(); for (const [key, value] of originals) { if (value) Object.defineProperty(globalThis, key, value); else Reflect.deleteProperty(globalThis, key); } });

function resultNode(): CanvasNodeData {
    const node: CanvasNodeData = { id: "group", type: CanvasNodeType.ProductDetail, title: "详情策划", position: { x: 0, y: 0 }, width: 972, height: 1000,
        metadata: { size: "1:1", commerceWorkflow: { ...newCommerceWorkflow(), role: "result", runMode: "manual", screens: Array.from({ length: 6 }, (_, index) => ({ id: `s${index}`, title: `画面 ${index + 1}`, copy: "", prompt: "" })) } } };
    return node;
}
async function render(node: CanvasNodeData, readOnly = false, outputs: CanvasNodeData[] = []) {
    host = document.createElement("div"); document.body.append(host); root = createRoot(host);
    const run = mock(async () => {}), patch = mock(() => {}), layout = mock(() => {}), canvasMouseDown = mock(() => {});
    const merge = mock(async () => {}), canvasPointerDown = mock(() => {}), canvasWheel = mock(() => {});
    const workflow = { run, patch, layout, upload: mock(async () => {}), merge, activeIds: new Set<string>() } as unknown as ReturnType<typeof useCommerceWorkflow>;
    await act(async () => root!.render(<div onMouseDown={canvasMouseDown} onPointerDown={canvasPointerDown} onWheel={canvasWheel}><CommerceWorkflowNode node={node} nodes={[node, ...outputs]} connections={[]} readOnly={readOnly} theme={canvasThemes.dark} workflow={workflow}
        patchMetadata={() => {}} disconnect={() => {}} focus={() => {}} stopBatch={() => {}} retryItem={() => {}} /></div>));
    return { run, patch, layout, merge, canvasMouseDown, canvasPointerDown, canvasWheel };
}

async function press(target: Element) {
    await act(async () => {
        target.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, buttons: 1 }));
        target.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0, buttons: 1 }));
    });
}

for (const type of [CanvasNodeType.ProductDetail, CanvasNodeType.ProductReplica]) {
    for (const role of ["source", "result"] as const) {
        test(`${type} ${role} 空白、说明文字和底栏可选中拖动，内部滚轮仍隔离`, async () => {
            const node = resultNode(); node.type = type; node.metadata!.commerceWorkflow!.role = role;
            const { run, canvasMouseDown, canvasPointerDown, canvasWheel } = await render(node);
            const selectors = [".commerce-workflow-node", ".commerce-scroll", ".commerce-scroll > p", ".commerce-footer",
                ...(role === "source" ? [".commerce-configuration", ".commerce-input-group", ".commerce-inputs"] : [".commerce-result-head", ".commerce-result-grid", ".commerce-plan-caption"])];
            for (const selector of selectors) {
                const target = host!.querySelector(selector)!; expect(target).not.toBeNull();
                await press(target);
            }
            expect(canvasMouseDown).toHaveBeenCalledTimes(selectors.length);
            expect(canvasPointerDown).toHaveBeenCalledTimes(selectors.length);
            await act(async () => host!.querySelector(".commerce-scroll")!.dispatchEvent(new Event("wheel", { bubbles: true })));
            expect(canvasWheel).not.toHaveBeenCalled(); expect(run).not.toHaveBeenCalled();
        });
    }
}

test("配置输入、上传标签和按钮内部不启动节点拖动", async () => {
    const node = resultNode(); node.metadata!.commerceWorkflow!.role = "source";
    const { canvasMouseDown, canvasPointerDown, run } = await render(node);
    for (const selector of ["input", "textarea", ".commerce-upload", ".commerce-field", ".commerce-footer button span"]) {
        const target = host!.querySelector(selector)!; expect(target).not.toBeNull(); await press(target);
    }
    expect(canvasMouseDown).not.toHaveBeenCalled(); expect(canvasPointerDown).not.toHaveBeenCalled();
    expect(run).not.toHaveBeenCalled();
});

for (const type of [CanvasNodeType.ProductDetail, CanvasNodeType.ProductReplica]) {
    test(`${type} 配置说明区分策划资料与复刻修改要求`, async () => {
        const node = resultNode(); node.type = type; node.metadata!.commerceWorkflow!.role = "source";
        const { run } = await render(node);
        const input = host!.querySelector<HTMLTextAreaElement>('textarea[aria-label="产品要求"]')!;
        if (type === CanvasNodeType.ProductDetail) {
            expect(input.placeholder).toContain("不直接作为全局生图提示词");
            expect(host!.textContent).toContain("自定义策划需自己填写各屏");
        } else {
            expect(input.placeholder).toContain("交给 AI 分析并落实到各模板替换方案");
            expect(host!.textContent).toContain("每张版式模板生成一张图");
        }
        expect(run).not.toHaveBeenCalled();
    });
}

test("结果组列数选择器外壳与编辑弹窗空白不冒泡拖动底层节点", async () => {
    const { canvasMouseDown, canvasPointerDown, run } = await render(resultNode());
    await press(host!.querySelector(".commerce-footer .ant-select")!);
    await act(async () => (host!.querySelector('[aria-label="编辑第 2 屏"]') as HTMLButtonElement).click());
    const modalBody = document.querySelector(".ant-modal-body")!; expect(modalBody).not.toBeNull();
    await press(modalBody);
    expect(canvasMouseDown).not.toHaveBeenCalled(); expect(canvasPointerDown).not.toHaveBeenCalled();
    expect(run).not.toHaveBeenCalled();
});

test("一键合并与下载长图为不同操作，按钮说明引用组不上传", async () => {
    const node = resultNode(); node.metadata!.commerceWorkflow!.screens = node.metadata!.commerceWorkflow!.screens.slice(0, 1);
    node.metadata!.batchTable = { operation: "creative", concurrency: 2, rows: [{ id: "s0", enabled: true, prompt: "", inputNodeIds: [], outputNodeId: "output" }] };
    const output: CanvasNodeData = { id: "output", type: CanvasNodeType.Image, title: "生成结果", width: 200, height: 200, position: { x: 0, y: 0 }, metadata: { status: "success" } };
    const { merge } = await render(node, false, [output]);
    const buttons = Array.from(host!.querySelectorAll<HTMLButtonElement>("button"));
    const assemble = buttons.find((button) => button.textContent === "一键合并")!;
    const download = buttons.find((button) => button.textContent === "下载长图")!;
    expect(assemble.disabled).toBe(false); expect(assemble.title).toContain("不合成或上传");
    expect(download.title).toContain("下载时才");
    await act(async () => { assemble.click(); download.click(); });
    expect(merge.mock.calls).toEqual([[node.id, false], [node.id, true]]);
});

test("自定义策划结果直接展示六张预生成卡片，不展开六份输入表单", async () => {
    const { run } = await render(resultNode());
    expect(host!.querySelectorAll('[data-commerce-screen]')).toHaveLength(6);
    expect(host!.querySelector('[aria-label="预生成卡片组"]')?.getAttribute("style")).toContain("repeat(3");
    expect(host!.querySelectorAll("textarea")).toHaveLength(0);
    expect(host!.textContent).toContain("待填写画面描述");
    expect(run).not.toHaveBeenCalled();
});

test("单击铅笔打开单屏编辑弹窗，关闭和翻页不调用模型", async () => {
    const { run } = await render(resultNode());
    await act(async () => (host!.querySelector('[aria-label="编辑第 2 屏"]') as HTMLButtonElement).click());
    expect(document.querySelector('[aria-label="第 2 屏文案"]')).not.toBeNull();
    const next = [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("下一屏"))!;
    await act(async () => next.click());
    expect(document.querySelector('[aria-label="第 3 屏文案"]')).not.toBeNull();
    const save = [...document.querySelectorAll("button")].find((button) => button.textContent?.includes("仅保存参数"))!;
    await act(async () => save.click());
    expect(run).not.toHaveBeenCalled();
});

test("第一次鼠标点击不会冒泡触发画布拖拽或重挂载节点", async () => {
    const node = resultNode(); node.metadata!.commerceWorkflow!.role = "source";
    const { run, canvasMouseDown } = await render(node);
    const button = [...host!.querySelectorAll("button")].find((button) => button.textContent?.includes("自定义策划"))!;
    await act(async () => { button.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })); button.click(); });
    expect(canvasMouseDown).not.toHaveBeenCalled(); expect(run).toHaveBeenCalledWith("group", "manual");
});

test("只读卡片允许查看编辑器，但禁止文案写入、重排和收费生成", async () => {
    const { run } = await render(resultNode(), true);
    expect((host!.querySelector('[aria-label="第 2 屏前移"]') as HTMLButtonElement).disabled).toBe(true);
    await act(async () => (host!.querySelector('[aria-label="编辑第 2 屏"]') as HTMLButtonElement).click());
    expect((document.querySelector('[aria-label="第 2 屏文案"]') as HTMLTextAreaElement).disabled).toBe(true);
    expect([...document.querySelectorAll("button")].filter((button) => button.textContent?.includes("生成此屏")).every((button) => button.disabled)).toBe(true);
    expect(run).not.toHaveBeenCalled();
});

test("已策划无字卡显示纯图而非缺文案，完整描述可查看且文案仍可手工补充", async () => {
    const node = resultNode(); const data = node.metadata!.commerceWorkflow!;
    data.runMode = "plan";
    const longPrompt = "完整产品摄影方案。".repeat(40);
    data.screens[0] = { ...data.screens[0], prompt: longPrompt, copy: "", sceneType: "产品特写" };
    data.screens[1] = { ...data.screens[1], prompt: "参数排版", copy: "Height: 3.6 cm" };
    await render(node);
    const card = host!.querySelector('[data-commerce-screen="s0"]')!;
    expect(card.textContent).toContain("纯图无字");
    expect(card.textContent).not.toContain("待补充画面文案");
    expect(card.querySelector('.commerce-plan-description')?.getAttribute("title")).toBe(longPrompt);
    expect([...card.querySelectorAll("button")].find((button) => button.textContent === "生成此屏")?.disabled).toBe(false);
    expect(host!.querySelector('[data-commerce-screen="s1"]')!.textContent).toContain("Height: 3.6 cm");
    await act(async () => (host!.querySelector('[aria-label="编辑第 1 屏预生成卡片"]') as HTMLButtonElement).click());
    const copy = document.querySelector('[aria-label="第 1 屏文案"]') as HTMLTextAreaElement;
    expect(copy.disabled).toBe(false);
    expect(copy.placeholder).toContain("留空按纯图生成");
    expect((document.querySelector('[aria-label="第 1 屏画面描述"]') as HTMLTextAreaElement).value).toBe(longPrompt);
    expect(document.body.textContent).toContain("不会额外调用模型补文案");
});

test("完整提示词覆盖不误标纯图，完全空白的自定义卡仍须先写方案", async () => {
    const node = resultNode();
    node.metadata!.commerceWorkflow!.screens[0].promptOverride = "手工完整提示词";
    await render(node);
    const override = host!.querySelector('[data-commerce-screen="s0"]')!;
    expect(override.textContent).toContain("已填写完整提示词");
    expect(override.textContent).not.toContain("纯图无字");
    const blank = host!.querySelector('[data-commerce-screen="s1"]')!;
    expect(blank.textContent).toContain("待填写画面描述");
    expect([...blank.querySelectorAll("button")].find((button) => button.textContent === "生成此屏")?.disabled).toBe(true);
});

test("组列数改变展示尺寸，不改生成比例、屏内容或数量", () => {
    const node = resultNode(), before = JSON.stringify(node.metadata);
    for (const columns of [1, 2, 3, 4, 6]) {
        const layout = commerceResultLayout(node, columns);
        expect(commerceResultLayout({ ...node, width: layout.width }).columns).toBe(columns);
        expect(layout.rows).toBe(Math.ceil(6 / columns)); expect(layout.height).toBeGreaterThanOrEqual(600);
    }
    expect(JSON.stringify(node.metadata)).toBe(before);
});

test("一屏在生成时仅禁用本屏，其他预生成卡片仍可提交", async () => {
    const node = resultNode();
    node.metadata!.commerceWorkflow!.screens = node.metadata!.commerceWorkflow!.screens.map((screen) => ({ ...screen, prompt: "产品特写", copy: "产品" }));
    node.metadata!.batchTable = { operation: "creative", concurrency: 2, rows: [{ id: "s0", enabled: true, inputNodeIds: [], prompt: "产品特写", outputNodeId: "out0" }] };
    node.metadata!.generationBatches = [{ id: "b1", projectId: "canvas", sourceNodeId: node.id, mode: "batch_image", status: "running", createdAt: "", updatedAt: "",
        items: [{ id: "i0", rowId: "s0", nodeId: "out0", status: "running", retryCount: 0 }] }];
    const { run } = await render(node);
    const cards = host!.querySelectorAll('[data-commerce-screen]');
    const generateButton = (index: number) => [...cards[index].querySelectorAll<HTMLButtonElement>(".commerce-plan-actions button")].find((button) => /生成此屏|重新生成|生成中/.test(button.textContent || ""))!;
    expect(generateButton(0).disabled).toBe(true); expect(generateButton(1).disabled).toBe(false);
    expect(cards[0].textContent).toContain("生成中");
    await act(async () => generateButton(1).click()); expect(run).toHaveBeenCalledWith("group", "generate", "s1");
});

function setPricedModels() {
    const channel = createModelChannel({ id: "price-ui", scope: "system", models: ["image", "text"], modelCosts: [
        { model: "image", capability: "image", protocol: "openai-image", billingMode: "fixed_request", unitPriceMicrocredits: 200_000 },
        { model: "text", capability: "text", protocol: "chat-completion", billingMode: "fixed_request", unitPriceMicrocredits: 10_000 },
    ] });
    useConfigStore.setState({ config: { ...defaultConfig, channels: [channel], imageModel: "price-ui::image", textModel: "price-ui::text", count: "10" } });
    useUserStore.setState({ features: { ...savedFeatures, creditsEnabled: true } });
}

test("保持原文案也展示复刻分析模型与费用，两个入口区分先审阅和自动出图", async () => {
    setPricedModels(); const node = resultNode(); node.type = CanvasNodeType.ProductReplica;
    node.metadata!.commerceWorkflow!.role = "source";
    const { run } = await render(node);
    expect(host!.textContent).toContain("分析 / 文字识别模型");
    expect(host!.querySelector('[aria-label="生成费用预估"]')!.textContent).toContain("产品 / 模板分析：0.01 积分/次");
    expect(host!.textContent).not.toContain("无额外策划费用");
    expect(run).not.toHaveBeenCalled();
    const buttons = [...host!.querySelectorAll<HTMLButtonElement>("button")];
    await act(async () => buttons.find((button) => button.textContent === "AI 分析模板")!.click());
    expect(run).toHaveBeenLastCalledWith("group", "plan");
    await act(async () => buttons.find((button) => button.textContent?.startsWith("一键复刻"))!.click());
    expect(run).toHaveBeenLastCalledWith("group", "direct");
});

test("复刻卡片可显式重分析，查看编辑不自动调用，未选卡片不受影响", async () => {
    const node = resultNode(); node.type = CanvasNodeType.ProductReplica;
    const { run } = await render(node);
    await act(async () => (host!.querySelector('[aria-label="编辑第 2 屏"]') as HTMLButtonElement).click());
    expect(run).not.toHaveBeenCalled();
    const button = [...document.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === "AI 重新分析此模板")!;
    expect(button).toBeDefined(); await act(async () => button.click());
    expect(run).toHaveBeenCalledWith("group", "replan", "s1");
});

test("详情费用显示标价×张数，发布新价格会立即更新，不触发生成", async () => {
    setPricedModels(); const node = resultNode(); node.metadata!.commerceWorkflow!.role = "source";
    const { run } = await render(node);
    expect(host!.querySelector('[aria-label="生成费用预估"]')?.textContent).toContain("6 张 × 0.2 积分/张 = 1.2 积分");
    expect(host!.textContent).toContain("直接生成预计合计 1.21 积分");
    await act(async () => {
        const config = useConfigStore.getState().config;
        useConfigStore.setState({ config: { ...config, channels: config.channels.map((channel) => ({ ...channel, modelCosts: channel.modelCosts?.map((cost) => ({ ...cost, unitPriceMicrocredits: cost.capability === "image" ? 300_000 : cost.unitPriceMicrocredits })) })) } });
    });
    expect(host!.querySelector('[aria-label="生成费用预估"]')?.textContent).toContain("6 张 × 0.3 积分/张 = 1.8 积分");
    expect(run).not.toHaveBeenCalled();
});

test("单屏卡片及编辑弹窗都显示一张的价格，关闭积分功能后不显示", async () => {
    setPricedModels(); const { run } = await render(resultNode());
    expect(host!.querySelector('[aria-label="第 2 屏预估费用"]')?.textContent).toBe("预计 0.2 积分/张");
    await act(async () => (host!.querySelector('[aria-label="编辑第 2 屏"]') as HTMLButtonElement).click());
    expect(document.querySelector('.commerce-editor-footer')?.textContent).toContain("本屏预计：0.2 积分/张");
    await act(async () => useUserStore.setState({ features: { ...savedFeatures, creditsEnabled: false } }));
    expect(host!.querySelector('[aria-label="生成费用预估"]')).toBeNull();
    expect(host!.querySelector('[aria-label="第 2 屏预估费用"]')).toBeNull();
    expect(document.querySelector('.commerce-editor-footer')?.textContent).not.toContain("积分");
    expect(run).not.toHaveBeenCalled();
});
