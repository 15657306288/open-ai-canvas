import { afterEach, expect, test } from "bun:test";

import {
    BATCH_REFERENCE_CELL_DROP_EVENT,
    BATCH_REFERENCE_CELL_HOVER_EVENT,
    batchReferenceCellRefFromElement,
    dispatchBatchReferenceCellDrop,
    dispatchBatchReferenceCellHover,
    findBatchReferenceCellAtPoint,
} from "../src/lib/canvas/canvas-batch-table-drop";

type FakeCell = {
    dataset: { rowId?: string; columnIndex?: string };
    closest: (selector: string) => FakeCell | null;
    getBoundingClientRect: () => { x: number; y: number; width: number; height: number };
};

function fakeCell(rowId?: string, columnIndex?: string): FakeCell {
    const cell: FakeCell = {
        dataset: { rowId, columnIndex },
        closest: (selector) => (selector === "[data-batch-reference-cell]" ? cell : null),
        getBoundingClientRect: () => ({ x: 10, y: 20, width: 64, height: 96 }),
    };
    return cell;
}

/** 拖动中的素材盖在表格上，栈底才是真正的落点格。 */
function fakeStack(cells: unknown[]): Element[] {
    return cells as Element[];
}

const originalDocument = Object.getOwnPropertyDescriptor(globalThis, "document");
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");

afterEach(() => {
    if (originalDocument) Object.defineProperty(globalThis, "document", originalDocument);
    else Reflect.deleteProperty(globalThis, "document");
    if (originalWindow) Object.defineProperty(globalThis, "window", originalWindow);
    else Reflect.deleteProperty(globalThis, "window");
});

test("从格子内部元素向上找最近的行列", () => {
    const cell = fakeCell("row-1", "2");
    expect(batchReferenceCellRefFromElement(cell as unknown as Element)).toEqual({ rowId: "row-1", columnIndex: 2 });
    expect(batchReferenceCellRefFromElement(fakeCell("row-2", undefined) as unknown as Element)).toBeNull();
    expect(batchReferenceCellRefFromElement(fakeCell(undefined, "0") as unknown as Element)).toBeNull();
    expect(batchReferenceCellRefFromElement(null)).toBeNull();
});

test("落点按层级顺序找到被素材节点盖住的那一格", () => {
    const cell = fakeCell("row-3", "1");
    const hit = findBatchReferenceCellAtPoint(100, 200);
    expect(hit).toBeNull();

    Object.defineProperty(globalThis, "document", {
        configurable: true,
        value: {
            elementsFromPoint: (x: number, y: number) => fakeStack([{ dragged: true, closest: () => null }, x === 100 && y === 200 ? cell : null]),
        },
    });
    const found = findBatchReferenceCellAtPoint(100, 200);
    expect(found?.rowId).toBe("row-3");
    expect(found?.columnIndex).toBe(1);
    expect(found?.element.getBoundingClientRect().height).toBe(96);
    expect(findBatchReferenceCellAtPoint(0, 0)).toBeNull();
});

test("悬停与落格事件都带行列信息，方便表格直接写入", () => {
    const received: unknown[] = [];
    const listeners = { [BATCH_REFERENCE_CELL_HOVER_EVENT]: [] as Array<(event: CustomEvent<unknown>) => void>, [BATCH_REFERENCE_CELL_DROP_EVENT]: [] as Array<(event: CustomEvent<unknown>) => void> };
    Object.defineProperty(globalThis, "window", {
        configurable: true,
        value: {
            dispatchEvent: (event: CustomEvent<unknown>) => {
                listeners[event.type as keyof typeof listeners]?.forEach((listener) => listener(event));
                received.push(event.detail);
                return true;
            },
        },
    });

    dispatchBatchReferenceCellHover({ rowId: "row-1", columnIndex: 0 });
    dispatchBatchReferenceCellHover(null);
    dispatchBatchReferenceCellDrop({ nodeId: "image-1", rowId: "row-1", columnIndex: 2, imageSrc: "https://yingce.cc.cd/a.png", fromRect: { x: 0, y: 0, width: 200, height: 300 }, toRect: { x: 10, y: 20, width: 64, height: 96 } });

    expect(received).toEqual([
        { rowId: "row-1", columnIndex: 0 },
        null,
        { nodeId: "image-1", rowId: "row-1", columnIndex: 2, imageSrc: "https://yingce.cc.cd/a.png", fromRect: { x: 0, y: 0, width: 200, height: 300 }, toRect: { x: 10, y: 20, width: 64, height: 96 } },
    ]);
});

test("缩略图关掉浏览器原生拖拽，掉格改走指针手势", async () => {
    const source = await Bun.file(new URL("../src/components/canvas/canvas-batch-table-node.tsx", import.meta.url)).text();

    // 原生 <img> 拖拽会被画布当成“拖入了文件”，在画布上凭空多出一张素材。
    expect(source).toContain("draggable={false}");
    expect(source).toContain("onDragStart={(event) => event.preventDefault()}");
    // 拖拽落点靠 data 属性定位，画布侧才能判定跨行跨列。
    expect(source).toContain("data-batch-reference-cell");
    expect(source).toContain("data-row-id={rowId}");
    expect(source).toContain("data-column-index={columnIndex}");
    // 拖动预览与落格动效
    expect(source).toContain("BATCH_REFERENCE_CELL_HOVER_EVENT");
    expect(source).toContain("BATCH_REFERENCE_CELL_DROP_EVENT");
    expect(source).toContain("onDropCanvasNodeToCell?.(detail.nodeId, detail.rowId, detail.columnIndex)");
    expect(source).toContain("dragGhostRef");
    expect(source).toContain("flyInGhostRef");
});

test("画布素材落在格子上时留在原位，并按列连线写入那一格", async () => {
    const [controller, project] = await Promise.all([
        Bun.file(new URL("../src/pages/canvas/use-canvas-selection-controller.ts", import.meta.url)).text(),
        Bun.file(new URL("../src/pages/canvas/project.tsx", import.meta.url)).text(),
    ]);

    expect(controller).toContain("findBatchReferenceCellAtPoint(clientX, clientY)");
    expect(controller).toContain("dispatchBatchReferenceCellDrop({");
    expect(controller).toContain("if (cellDropTargetRef.current) dispatchBatchReferenceCellHover(null);");
    // 落格时不提交位移：素材回到拖动前的位置。
    expect(controller).toContain("} else {");

    expect(project).toContain("connectNodesFromSource(imageNodeId, tableNodeId, batchReferenceHandleId(column.id));");
    expect(project).toContain("updateBatchRow(tableNodeId, rowId, { inputNodeIds, outputNodeId: undefined, outputNodeIds: undefined });");
    // 落格写的是精确单元格，不能被按端口重算的行覆盖。
    expect(project).toContain("skipBatchRowSyncRef");
});

/**
 * 手动挪格子之后，行与格子的对应关系只存在于行内引用里。
 * 刷新/连线变化时如果按端口重算，格子会错位到别的行（用户实测过），
 * 所以人工排列过的表格只能由用户显式点「同步连线」重建。
 */
test("手动排过格子的表格不被连线同步覆盖，只有显式同步才重建", async () => {
    const [hook, project, node] = await Promise.all([
        Bun.file(new URL("../src/pages/canvas/use-canvas-batch-table.ts", import.meta.url)).text(),
        Bun.file(new URL("../src/pages/canvas/project.tsx", import.meta.url)).text(),
        Bun.file(new URL("../src/components/canvas/canvas-batch-table-node.tsx", import.meta.url)).text(),
    ]);

    // 连线同步（含加载时的静默同步）遇到人工排列直接返回，不动 rows。
    expect(hook).toContain("syncRowsFromConnections = useCallback((nodeId: string, silent = false, force = false) => {");
    expect(hook).toContain("if (table.manualRows && !force) return false;");
    expect(project).toContain("syncRowsFromConnections(node.id, true);");
    // 「同步连线」按钮是唯一的重建入口：强制重建后清掉标记。
    expect(hook).toContain("syncRowsFromConnections(nodeId, false, true);");
    expect(hook).toContain("patchTable(nodeId, { rows: rowsWithText, manualRows: false });");

    // 触碰到具体格子的操作都要置位：挪格、清格、上传、画布素材落格、批量填写。
    expect(hook).toContain("patchTable(nodeId, { rows: nextTable.rows, manualRows: true });");
    expect(project).toContain("patchBatchTable(tableNodeId, { rows: nextRows, manualRows: true });");
    expect(project).toContain("patchBatchTable(tableNodeId, { manualRows: true });");
    expect(node).toContain("onPatchTable({ rows: next.rows, manualRows: true });");
});

/**
 * 参考图格子的两个视觉问题：
 * 1) 按素材比例缩放会让竖图比空格子高出一截，行列高低不齐 —— 改回固定正方形；
 * 2) 复制按钮原本挂在缩略图下面，只有光标停过的行才看得出来，还会把行高撞高 —— 挪进格子左上角。
 */
test("参考图格子尺寸统一，复制与清空都在格内", async () => {
    const source = await Bun.file(new URL("../src/components/canvas/canvas-batch-table-node.tsx", import.meta.url)).text();

    expect(source).toContain("const REFERENCE_THUMB_SIZE = 64;");
    expect(source).not.toContain("referenceThumbnailSize");
    expect(source).toContain("width: REFERENCE_THUMB_SIZE, height: REFERENCE_THUMB_SIZE");

    // 格子下面不再有独立按钮，复制/清空是格内左上角、右上角的一对悬浮图标。
    expect(source).not.toContain("<Copy className=\"size-3\" />复制");
    expect(source).toContain("aria-label={`复制${label}`}");
    expect(source).toContain("absolute left-0.5 top-0.5");
    expect(source).toContain("absolute right-0.5 top-0.5");

    // 缩略图上不再叠 @参考图N 角标；列名只留给表头，格子里的标识放悬停提示和无障碍名称。
    expect(source).not.toContain("absolute bottom-1 left-1 rounded px-1 py-0.5");
    expect(source).not.toContain("text-[8px] font-medium text-white");
    // 提示里仍然能看出这一格是哪个参考图。
    // 说明只留在无障碍名称里：鼠标划过缩略图不会弹出浮层挡住图片。
    expect(source).not.toContain("<Tooltip title={filled ?");
    expect(source).toContain("aria-label={filled ?");
    expect(source).toContain("${label} 点击上传或拖入图片");

    // 图片绝对定位铺满方框：作为 grid item 时 h-full 失效，图会比格子高、被裁掉一截。
    expect(source).toContain("className=\"absolute inset-0 size-full object-cover\"");
    expect(source).not.toContain("block size-full max-h-full max-w-full object-cover");
});
