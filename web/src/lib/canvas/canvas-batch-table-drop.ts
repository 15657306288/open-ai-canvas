/**
 * 画布素材拖到批量创作表参考图格子上的落点桥接。
 * 画布节点拖动走的是指针手势（不是 HTML5 拖放），表格格子又藏在节点内容里，
 * 两边互相引用会绕出环，所以这里用 window 事件传递“当前悬停哪一格 / 松手落在哪一格”。
 */

export const BATCH_REFERENCE_CELL_HOVER_EVENT = "canvas:batch-reference-cell-hover";
export const BATCH_REFERENCE_CELL_DROP_EVENT = "canvas:batch-reference-cell-drop";

export type BatchReferenceCellRef = { rowId: string; columnIndex: number };

export type BatchReferenceCellRect = { x: number; y: number; width: number; height: number };

export type BatchReferenceCellDropDetail = BatchReferenceCellRef & {
    nodeId: string;
    /** 被拖素材的图片地址，用于落格时播放缩小飞入动效。 */
    imageSrc?: string;
    /** 拖拽起点在屏幕上的位置：动效从素材原位置缩到格子里。 */
    fromRect?: BatchReferenceCellRect;
    /** 落点格子在屏幕上的位置。 */
    toRect?: BatchReferenceCellRect;
};

export function toBatchReferenceCellRect(element: { getBoundingClientRect: () => DOMRect }): BatchReferenceCellRect {
    const rect = element.getBoundingClientRect();
    return { x: rect.x, y: rect.y, width: rect.width, height: rect.height };
}

/** 命中的参考图格子：格子元素可能跨 iframe 或来自测试替身，用鸭子类型判定。 */
export type BatchReferenceCellHit = BatchReferenceCellRef & { element: { getBoundingClientRect: () => DOMRect } };

/** 从任意元素向上找最近的参考图格子；找不到或没带行列信息时返回 null。 */
export function batchReferenceCellRefFromElement(element: Element | null | undefined): BatchReferenceCellRef | null {
    return batchReferenceCellHitFromElement(element)?.cell || null;
}

function batchReferenceCellHitFromElement(element: Element | null | undefined): { cell: BatchReferenceCellRef; element: { getBoundingClientRect: () => DOMRect } } | null {
    const cell = element?.closest?.("[data-batch-reference-cell]") as (Element & { dataset?: DOMStringMap; getBoundingClientRect?: () => DOMRect }) | null | undefined;
    const rowId = cell?.dataset?.rowId;
    const columnIndex = Number(cell?.dataset?.columnIndex);
    if (!rowId || !Number.isInteger(columnIndex) || columnIndex < 0) return null;
    if (typeof cell?.getBoundingClientRect !== "function") return null;
    return { cell: { rowId, columnIndex }, element: cell as { getBoundingClientRect: () => DOMRect } };
}

/**
 * 找到指针下方的参考图格子。
 * 拖动中的素材节点盖在表格上方，`elementFromPoint` 只会拿到节点本身，
 * 这里按层级顺序遍历，拿到真正命中的格子元素。
 */
export function findBatchReferenceCellAtPoint(clientX: number, clientY: number): BatchReferenceCellHit | null {
    if (typeof document === "undefined") return null;
    const stack = document.elementsFromPoint?.(clientX, clientY);
    if (!stack) return null;
    for (const element of stack) {
        const hit = batchReferenceCellHitFromElement(element);
        if (hit) return { ...hit.cell, element: hit.element };
    }
    return null;
}

/** 拖动过程中告诉表格高亮哪一格；传 null 表示离开所有格子。 */
export function dispatchBatchReferenceCellHover(cell: BatchReferenceCellRef | null) {
    if (typeof window === "undefined") return;
    window.dispatchEvent(new CustomEvent<BatchReferenceCellRef | null>(BATCH_REFERENCE_CELL_HOVER_EVENT, { detail: cell }));
}

/** 松手落在参考图格子上：素材放不进画布节点，改由表格自己写入这一格并连线。 */
export function dispatchBatchReferenceCellDrop(detail: BatchReferenceCellDropDetail) {
    if (typeof window === "undefined") return;
    window.dispatchEvent(new CustomEvent<BatchReferenceCellDropDetail>(BATCH_REFERENCE_CELL_DROP_EVENT, { detail }));
}
