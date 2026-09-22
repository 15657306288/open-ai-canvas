import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { App, Button, Checkbox, Segmented, Select, Switch, Tooltip } from "antd";
import { Copy, Film, Image as ImageIcon, ListChecks, LoaderCircle, Minus, Play, Plus, Rows3, Trash2, Upload, X } from "lucide-react";

import { CachedResourceImage } from "@/components/cached-resource-image";
import { CanvasResourceMentionTextarea } from "@/components/canvas/canvas-resource-mention-textarea";
import { AppModal } from "@/components/ui/product/app-modal";
import {
    BATCH_REFERENCE_HANDLE_GAP,
    BATCH_REFERENCE_HANDLE_TOP,
    MAX_BATCH_REFERENCE_COLUMNS,
    MAX_BATCH_TEXT_COLUMNS,
    MIN_BATCH_REFERENCE_COLUMNS,
    batchPromptForRow,
    batchInputColumns,
    batchReferenceColumns,
    batchReferenceHandleId,
    batchReferenceMentionToken,
    batchRowHasResult,
    batchRowOutputNodes,
    batchTextColumns,
    batchTextHandleId,
    batchTextHandleTop,
    fillBatchReferenceColumn,
    batchRowReady as rowReady,
} from "@/lib/canvas/canvas-batch-table";
import type { BatchReferenceFillMode } from "@/lib/canvas/canvas-batch-table";
import { BATCH_REFERENCE_CELL_DROP_EVENT, BATCH_REFERENCE_CELL_HOVER_EVENT, findBatchReferenceCellAtPoint, type BatchReferenceCellDropDetail, type BatchReferenceCellRect, type BatchReferenceCellRef } from "@/lib/canvas/canvas-batch-table-drop";
import type { CanvasResourceReference } from "@/lib/canvas/canvas-resource-references";
import type { CanvasTheme } from "@/lib/canvas-theme";
import type { CanvasBatchOperation, CanvasBatchRow, CanvasBatchTableData, CanvasConnection, CanvasGenerationBatch, CanvasGenerationBatchItem, CanvasNodeData } from "@/types/canvas";

type ReferenceCell = { rowId: string; columnIndex: number };

type Props = {
    node: CanvasNodeData;
    nodes: CanvasNodeData[];
    connections: CanvasConnection[];
    batch?: CanvasGenerationBatch;
    theme: CanvasTheme;
    onPatchTable: (patch: Partial<CanvasBatchTableData>) => void;
    onAddRow: () => void;
    onRemoveRow: (rowId: string) => void;
    onUpdateRow: (rowId: string, patch: Partial<CanvasBatchRow>) => void;
    onFillRows: () => void;
    onGenerate: (rowIds?: string[]) => void;
    onCreateStoryboard?: () => void;
    onRetryItem: (batchId: string, itemId: string) => void;
    onAddReferenceColumn: () => void;
    onAddTextColumn?: () => void;
    onRemoveReferenceColumn?: () => void;
    onFocusOutput?: (nodeId: string) => void;
    onReorderReferenceColumns?: (fromColumnId: string, toColumnId: string) => void;
    onMoveReferenceCell?: (sourceRowId: string, sourceColumnIndex: number, targetRowId: string, targetColumnIndex: number) => void;
    onDropCanvasNodeToCell?: (nodeId: string, rowId: string, columnIndex: number) => void;
    onUploadReference?: (rowId: string, columnIndex: number, file: File) => void;
    onRemoveReference?: (rowId: string, columnIndex: number) => void;
    onConnectStart: (event: ReactPointerEvent, handleId: string) => void;
    onConnectDrop?: (event: ReactPointerEvent, handleId: string) => void;
    readOnly?: boolean;
};

const OPERATION_OPTIONS = [
    { value: "try_on", label: "批量换装" },
    { value: "creative", label: "创意生图" },
] satisfies Array<{ value: CanvasBatchOperation; label: string }>;

const CONCURRENCY_OPTIONS = [1, 5, 10] as const;

/** 批量填充里代表“整个当前画布”的来源标识。 */
const CANVAS_FILL_SOURCE_KEY = "canvas";

export function CanvasBatchTableNodeContent({ node, nodes, connections, batch, theme, onPatchTable, onAddRow, onRemoveRow, onUpdateRow, onFillRows, onGenerate, onCreateStoryboard, onRetryItem, onAddReferenceColumn, onAddTextColumn = () => {}, onRemoveReferenceColumn, onReorderReferenceColumns, onMoveReferenceCell, onDropCanvasNodeToCell, onUploadReference, onRemoveReference, onFocusOutput, onConnectStart, onConnectDrop, readOnly = false }: Props) {
    const table = node.metadata?.batchTable || { operation: "try_on" as const, concurrency: 10, rows: [] };
    const { message } = App.useApp();
    const referenceColumns = batchReferenceColumns(table);
    const textColumns = batchTextColumns(table);
    const globalPrompt = table.globalPrompt || "";
    const hasGlobalPrompt = Boolean(globalPrompt.trim());
    const nodeById = useMemo(() => new Map(nodes.map((item) => [item.id, item])), [nodes]);
    const batchItemByRowId = useMemo(() => new Map((batch?.items || []).map((item) => [item.rowId, item])), [batch?.items]);
    const connectedReferenceGroups = useMemo(() => batchInputColumns(node, connections).map((nodeIds, index) => ({
        key: String(index),
        label: referenceColumns[index]?.label || `参考图 ${index + 1}`,
        nodeIds: nodeIds.filter((nodeId) => {
            const source = nodeById.get(nodeId);
            return source?.type === "image" && hasNodeMedia(source);
        }),
    })), [connections, node, nodeById, referenceColumns]);
    const connectedImageIds = useMemo(() => Array.from(new Set(connectedReferenceGroups.flatMap((group) => group.nodeIds))), [connectedReferenceGroups]);
    const connectedImageCount = connectedImageIds.length;
    // 画布上的全部可用图片：批量填充的来源不限于连线，也能直接用整个当前画布。
    const canvasImageNodes = useMemo(() => nodes.filter((item) => item.id !== node.id && (item.type === "image" || item.type === "video") && !item.parentId && hasNodeMedia(item)), [node.id, nodes]);
    const canvasImageIds = useMemo(() => canvasImageNodes.map((item) => item.id), [canvasImageNodes]);
    const activeRowIds = useMemo(() => new Set((batch?.items || []).filter((item) => ["waiting", "submitting", "queued", "running"].includes(item.status)).map((item) => item.rowId)), [batch?.items]);
    const completed = table.rows.filter((row) => batchRowHasResult(row, nodeById)).length;
    // 右上角按钮按“所有就绪行”判断，只要还有可提交的行就不禁用；没有未完成行时转为“重新生成”。
    const readyRowCount = table.rows.filter((row) => rowReady(row, table, nodeById) && !activeRowIds.has(row.id)).length;
    const unfinishedReadyCount = table.rows.filter((row) => rowReady(row, table, nodeById) && !activeRowIds.has(row.id) && !batchRowHasResult(row, nodeById)).length;
    const regenerateAll = readyRowCount > 0 && unfinishedReadyCount === 0;
    const gridTemplateColumns = `88px repeat(${referenceColumns.length}, 88px) ${textColumns.length ? `repeat(${textColumns.length}, minmax(168px, 0.75fr)) ` : ""}minmax(280px, 1fr) 88px 80px`;
    const subtleSurface = `color-mix(in srgb, ${theme.node.text} 4%, transparent)`;
    const inputSurface = theme.node.panel;
    const fileInputRef = useRef<HTMLInputElement>(null);
    const uploadTargetRef = useRef<ReferenceCell | null>(null);
    const [draggingColumnId, setDraggingColumnId] = useState<string | null>(null);
    const [draggingCell, setDraggingCell] = useState<ReferenceCell | null>(null);
    const [selectedRowIds, setSelectedRowIds] = useState<Set<string>>(() => new Set());
    const [fillOpen, setFillOpen] = useState(false);
    const [fillScope, setFillScope] = useState<"selected" | "all">("all");
    const [fillSourceKey, setFillSourceKey] = useState(CANVAS_FILL_SOURCE_KEY);
    const [fillTargetColumn, setFillTargetColumn] = useState(0);
    const [fillMode, setFillMode] = useState<BatchReferenceFillMode>("sequential");
    const [fillOverwrite, setFillOverwrite] = useState(true);
    // 点击已有参考图缩略图时打开的替换面板；记录目标单元格。
    const [replaceCell, setReplaceCell] = useState<ReferenceCell | null>(null);
    // 拖到哪一格：格子自己拖是内部状态，画布素材拖进来由 window 事件通知。
    const [dropTargetCell, setDropTargetCell] = useState<ReferenceCell | null>(null);
    // 素材落格后的脉冲序号：同一格重复落入也要重放动效。
    const [cellDropPulse, setCellDropPulse] = useState<{ cell: ReferenceCell; seq: number } | null>(null);
    // 拖动参考图时跟随指针的浮层预览。
    const [dragGhost, setDragGhost] = useState<{ src?: string } | null>(null);
    const dragGhostRef = useRef<HTMLDivElement | null>(null);
    // 画布素材放进格子时，从素材原位缩小飞入格子的动效。
    const [flyInGhost, setFlyInGhost] = useState<{ key: number; src?: string; from: BatchReferenceCellRect; to: BatchReferenceCellRect } | null>(null);
    const flyInGhostRef = useRef<HTMLDivElement | null>(null);
    const draggingCellRef = useRef<ReferenceCell | null>(null);
    const dragStartRef = useRef<{ x: number; y: number; pointerId: number; cell: ReferenceCell } | null>(null);
    const suppressClickRef = useRef(false);

    useEffect(() => {
        const validRowIds = new Set(table.rows.map((row) => row.id));
        setSelectedRowIds((current) => {
            const next = new Set(Array.from(current).filter((rowId) => validRowIds.has(rowId)));
            return next.size === current.size ? current : next;
        });
    }, [table.rows]);

    const allRowsSelected = table.rows.length > 0 && selectedRowIds.size === table.rows.length;
    const someRowsSelected = selectedRowIds.size > 0 && !allRowsSelected;
    // 来源只保留两种：整个当前画布、表格端口上全部已连图片。
    const fillSourceIds = fillSourceKey === CANVAS_FILL_SOURCE_KEY ? canvasImageIds : connectedImageIds;
    const fillTargetRows = table.rows.filter((row) => fillScope === "all" || selectedRowIds.has(row.id));
    const fillSourceIsCanvas = fillSourceKey === CANVAS_FILL_SOURCE_KEY;
    const fillEligibleRows = fillTargetRows.filter((row) => fillOverwrite || !row.inputNodeIds[fillTargetColumn]);
    const fillCount = fillMode === "sequential" ? Math.min(fillEligibleRows.length, fillSourceIds.length) : fillSourceIds.length ? fillEligibleRows.length : 0;
    const fillModeDescription = fillMode === "sequential"
        ? "按来源顺序一一对应目标行；来源图片用完后，剩余行保持不变。"
        : fillMode === "cycle"
            ? "按来源顺序循环使用；来源图片不足时，从第一张重新开始。"
            : "只使用来源列表中的第一张图片，填入每个目标行。";

    const toggleRowSelection = (rowId: string, checked: boolean) => {
        setSelectedRowIds((current) => {
            const next = new Set(current);
            if (checked) next.add(rowId);
            else next.delete(rowId);
            return next;
        });
    };

    const openBatchFill = () => {
        setFillScope(selectedRowIds.size ? "selected" : "all");
        // 来源只保留「整个当前画布」和「全部已连图片」：旧的分组来源，或所选来源已无素材时自动退回。
        const nextSource = fillSourceKey === CANVAS_FILL_SOURCE_KEY
            ? (canvasImageIds.length || !connectedImageIds.length ? CANVAS_FILL_SOURCE_KEY : "all")
            : fillSourceKey === "all"
                ? (connectedImageIds.length || !canvasImageIds.length ? "all" : CANVAS_FILL_SOURCE_KEY)
                : (canvasImageIds.length ? CANVAS_FILL_SOURCE_KEY : "all");
        if (nextSource !== fillSourceKey) setFillSourceKey(nextSource);
        if (fillTargetColumn >= referenceColumns.length) setFillTargetColumn(0);
        setFillOpen(true);
    };

    const applyBatchFill = () => {
        const next = fillBatchReferenceColumn(table, fillSourceIds, {
            targetColumnIndex: fillTargetColumn,
            targetRowIds: fillTargetRows.map((row) => row.id),
            mode: fillMode,
            overwrite: fillOverwrite,
        });
        // 批量填写写的是精确单元格，之后的连线变化不要再按端口重建行。
        if (next !== table) onPatchTable({ rows: next.rows, manualRows: true });
        setFillOpen(false);
    };

    /** 「全局提示词」标签本身就是按钮：点一下把这段提示词写进每一行，之后还能单行微调。 */
    const applyGlobalPromptToRows = () => {
        const prompt = globalPrompt.trim();
        if (readOnly || !prompt || !table.rows.length) return;
        onPatchTable({ rows: table.rows.map((row) => ({ ...row, prompt })) });
        message.success(`已把全局提示词写入 ${table.rows.length} 行任务`);
    };

    /** 复制参考图本体到剪贴板，方便粘到别处。 */
    const copyReferenceImage = async (source?: CanvasNodeData) => {
        const src = source?.metadata?.previewContent || source?.metadata?.content;
        if (!src) return message.warning("这一格还没有可复制的图片");
        try {
            if (!navigator.clipboard || typeof ClipboardItem === "undefined") throw new Error("clipboard unavailable");
            const blob = await (await fetch(src)).blob();
            await navigator.clipboard.write([new ClipboardItem({ [blob.type || "image/png"]: blob })]);
            message.success("已复制参考图到剪贴板");
        } catch {
            message.warning("当前浏览器不支持复制图片，可改用「复制到本列其他行」");
        }
    };

    /** 同列里还有多少行不是这张图，用于复制到本列其他行的按钮文案。 */
    const referenceCopyTargetCount = (rowId: string, columnIndex: number) => {
        const sourceNodeId = table.rows.find((row) => row.id === rowId)?.inputNodeIds[columnIndex];
        if (!sourceNodeId) return 0;
        return table.rows.filter((row) => row.id !== rowId && row.inputNodeIds[columnIndex] !== sourceNodeId).length;
    };

    /** 把一个参考图格子的素材铺到同列其他行，复用批量填充的写入规则（换图后旧结果作废）。 */
    const copyReferenceToColumn = (rowId: string, columnIndex: number) => {
        if (readOnly) return;
        const sourceNodeId = table.rows.find((row) => row.id === rowId)?.inputNodeIds[columnIndex];
        if (!sourceNodeId) return;
        const targetRowIds = table.rows.filter((row) => row.id !== rowId && row.inputNodeIds[columnIndex] !== sourceNodeId).map((row) => row.id);
        if (!targetRowIds.length) return message.info("本列其他行已经是这张参考图");
        const next = fillBatchReferenceColumn(table, [sourceNodeId], { targetColumnIndex: columnIndex, targetRowIds, mode: "same", overwrite: true });
        if (next === table) return;
        onPatchTable({ rows: next.rows, manualRows: true });
        message.success(`已复制到本列其余 ${targetRowIds.length} 行`);
    };

    /** 浮层预览和落格判定都用屏幕坐标：拖动中的素材会盖在格子上，按层级取真实命中格。 */
    const referenceCellAtPoint = useCallback((clientX: number, clientY: number): ReferenceCell | null => {
        const hit = findBatchReferenceCellAtPoint(clientX, clientY);
        return hit ? { rowId: hit.rowId, columnIndex: hit.columnIndex } : null;
    }, []);

    /** 浮层预览定位直接改 DOM，避免拖动时每一帧都触发 React 重渲。 */
    const moveDragGhost = useCallback((clientX: number, clientY: number) => {
        const ghost = dragGhostRef.current;
        if (!ghost) return;
        ghost.style.transform = `translate3d(${Math.round(clientX - 36)}px, ${Math.round(clientY - 36)}px, 0)`;
    }, []);

    /** 同一格重复落图也要重放动效，所以用递增序号标记脉冲。 */
    const pulseCell = useCallback((cell: ReferenceCell) => {
        setCellDropPulse((current) => ({ cell, seq: (current?.seq || 0) + 1 }));
    }, []);

    const clearReferenceDrag = useCallback(() => {
        dragStartRef.current = null;
        draggingCellRef.current = null;
        setDraggingCell(null);
        setDragGhost(null);
        setDropTargetCell(null);
    }, []);
    useEffect(() => {
        if (readOnly) return;
        const handleMove = (event: PointerEvent) => {
            const start = dragStartRef.current;
            if (!start || start.pointerId !== event.pointerId) return;
            const distance = Math.hypot(event.clientX - start.x, event.clientY - start.y);
            if (!draggingCellRef.current && distance < 5) return;
            if (!draggingCellRef.current) {
                draggingCellRef.current = start.cell;
                suppressClickRef.current = true;
                setDraggingCell(start.cell);
                // 有素材才需要浮层预览；空格子拖动只做落点高亮。
                const source = table.rows.find((row) => row.id === start.cell.rowId)?.inputNodeIds[start.cell.columnIndex];
                const sourceNode = source ? nodeById.get(source) : undefined;
                const src = sourceNode?.metadata?.previewContent || sourceNode?.metadata?.content;
                setDragGhost(src || sourceNode?.metadata?.storageKey ? { src } : null);
                moveDragGhost(start.x, start.y);
            }
            // 拖动过程实时高亮目标格，这就是跨行跨列的落点预览。
            const hovered = referenceCellAtPoint(event.clientX, event.clientY);
            setDropTargetCell((current) => current?.rowId === hovered?.rowId && current?.columnIndex === hovered?.columnIndex ? current : hovered);
            moveDragGhost(event.clientX, event.clientY);
        };
        const handleUp = (event: PointerEvent) => {
            const start = dragStartRef.current;
            if (!start || start.pointerId !== event.pointerId) return;
            const sourceCell = draggingCellRef.current;
            const targetCell = sourceCell ? referenceCellAtPoint(event.clientX, event.clientY) : null;
            if (sourceCell && targetCell) {
                onMoveReferenceCell?.(sourceCell.rowId, sourceCell.columnIndex, targetCell.rowId, targetCell.columnIndex);
                pulseCell(targetCell);
            }
            const didDrag = Boolean(sourceCell);
            clearReferenceDrag();
            if (didDrag) {
                suppressClickRef.current = true;
                window.setTimeout(() => { suppressClickRef.current = false; }, 0);
            }
        };
        window.addEventListener("pointermove", handleMove);
        window.addEventListener("pointerup", handleUp);
        window.addEventListener("pointercancel", handleUp);
        return () => {
            window.removeEventListener("pointermove", handleMove);
            window.removeEventListener("pointerup", handleUp);
            window.removeEventListener("pointercancel", handleUp);
        };
    }, [clearReferenceDrag, moveDragGhost, nodeById, pulseCell, referenceCellAtPoint, onMoveReferenceCell, readOnly, table.rows]);

    /**
     * 画布素材拖到表格上：拖动中高亮落点格，松手把素材放进那一格并连线。
     * 画布节点拖动走指针手势，所以通过 window 事件接收落点。
     */
    useEffect(() => {
        if (readOnly) return;
        const handleHover = (event: Event) => {
            const detail = (event as CustomEvent<BatchReferenceCellRef | null>).detail;
            const next = detail ? { rowId: detail.rowId, columnIndex: detail.columnIndex } : null;
            setDropTargetCell((current) => current?.rowId === next?.rowId && current?.columnIndex === next?.columnIndex ? current : next);
        };
        const handleDrop = (event: Event) => {
            const detail = (event as CustomEvent<BatchReferenceCellDropDetail>).detail;
            setDropTargetCell(null);
            if (!detail?.nodeId || !detail.rowId || detail.columnIndex == null) return;
            pulseCell({ rowId: detail.rowId, columnIndex: detail.columnIndex });
            if (detail.fromRect && detail.toRect) setFlyInGhost({ key: Date.now(), src: detail.imageSrc, from: detail.fromRect, to: detail.toRect });
            onDropCanvasNodeToCell?.(detail.nodeId, detail.rowId, detail.columnIndex);
        };
        window.addEventListener(BATCH_REFERENCE_CELL_HOVER_EVENT, handleHover);
        window.addEventListener(BATCH_REFERENCE_CELL_DROP_EVENT, handleDrop);
        return () => {
            window.removeEventListener(BATCH_REFERENCE_CELL_HOVER_EVENT, handleHover);
            window.removeEventListener(BATCH_REFERENCE_CELL_DROP_EVENT, handleDrop);
        };
    }, [onDropCanvasNodeToCell, pulseCell, readOnly]);

    /** 落格动效：素材从原位置缩到格子里，然后消失，视觉上就是“缩小放进表格”。 */
    useEffect(() => {
        if (!flyInGhost) return;
        const ghost = flyInGhostRef.current;
        if (!ghost || typeof ghost.animate !== "function") {
            setFlyInGhost(null);
            return;
        }
        const { from, to } = flyInGhost;
        const dx = Math.round(to.x + to.width / 2 - (from.x + from.width / 2));
        const dy = Math.round(to.y + to.height / 2 - (from.y + from.height / 2));
        const scale = Math.min(1, Math.max(0.12, to.width / Math.max(from.width, 1)));
        const animation = ghost.animate(
            [
                { transform: "translate3d(0, 0, 0) scale(1)", opacity: 1 },
                { transform: `translate3d(${dx}px, ${dy}px, 0) scale(${scale})`, opacity: 0.12 },
            ],
            { duration: 300, easing: "cubic-bezier(.22,.85,.24,1)", fill: "forwards" },
        );
        const key = flyInGhost.key;
        const timer = window.setTimeout(() => setFlyInGhost((current) => current?.key === key ? null : current), 320);
        return () => {
            animation.cancel();
            window.clearTimeout(timer);
        };
    }, [flyInGhost]);

    const startCellDrag = (event: ReactPointerEvent, rowId: string, columnIndex: number) => {
        if (readOnly || event.button !== 0) return;
        event.stopPropagation();
        dragStartRef.current = { x: event.clientX, y: event.clientY, pointerId: event.pointerId, cell: { rowId, columnIndex } };
    };

    const pickReferenceFile = (rowId: string, columnIndex: number) => {
        if (readOnly) return;
        if (suppressClickRef.current) {
            suppressClickRef.current = false;
            return;
        }
        uploadTargetRef.current = { rowId, columnIndex };
        fileInputRef.current?.click();
    };

    return (
        // 左侧 pl-11 是端口轨道：绝对定位的端口钉在节点左边缘，表格整体右移，端口不再压在缩略图上。
        <div data-canvas-batch-table data-canvas-no-zoom data-canvas-wheel-scroll className="relative flex h-full w-full flex-col overflow-visible pl-11 text-xs" style={{ color: theme.node.text }}>
            {!readOnly ? <input ref={fileInputRef} type="file" accept="image/*" className="hidden" onChange={(event) => {
                const file = event.currentTarget.files?.[0];
                const target = uploadTargetRef.current;
                event.currentTarget.value = "";
                uploadTargetRef.current = null;
                if (file && target) onUploadReference?.(target.rowId, target.columnIndex, file);
            }} /> : null}
            {!readOnly ? <BatchReferenceHandles columns={referenceColumns} textColumns={textColumns} theme={theme} onAdd={onAddReferenceColumn} onAddText={onAddTextColumn} onConnectStart={onConnectStart} onConnectDrop={onConnectDrop} /> : null}
            {!readOnly ? <span aria-hidden className="pointer-events-none absolute inset-y-3 left-11 w-px" style={{ background: theme.node.stroke }} /> : null}

            <div className="shrink-0 overflow-hidden rounded-t-[inherit] border-b" style={{ borderColor: theme.node.stroke, background: subtleSurface }}>
                <div data-canvas-batch-drag className="flex h-11 cursor-grab items-center gap-2 px-3 active:cursor-grabbing">
                    <div className="flex min-w-0 flex-1 items-center gap-2 overflow-hidden" onPointerDown={(event) => event.stopPropagation()}>
                        <BatchChoiceGroup ariaLabel="批量任务类型" theme={theme} disabled={readOnly} options={OPERATION_OPTIONS} value={table.operation} onChange={(operation) => onPatchTable({ operation: operation as CanvasBatchOperation })} />
                        <div className="flex h-8 shrink-0 items-center gap-1.5 rounded-lg px-2" style={{ background: theme.node.panel }}>
                            <span className="font-medium" style={{ color: theme.node.muted }}>并发</span>
                            <BatchChoiceGroup ariaLabel="并发数" theme={theme} disabled={readOnly} compact options={CONCURRENCY_OPTIONS.map((value) => ({ value, label: String(value) }))} value={table.concurrency} onChange={(concurrency) => onPatchTable({ concurrency: Number(concurrency) })} />
                        </div>
                        <div className="flex h-8 shrink-0 items-center gap-0.5 rounded-lg px-2" style={{ background: theme.node.panel, color: theme.node.muted }}>
                            <span className="pr-1">{referenceColumns.length}/{MAX_BATCH_REFERENCE_COLUMNS} 组参考</span>
                            {!readOnly ? (
                                <>
                                    <Tooltip title={referenceColumns.length <= MIN_BATCH_REFERENCE_COLUMNS ? "至少保留 1 组参考图" : "减少一组参考图"}>
                                        <button type="button" aria-label="减少一组参考图" className="grid size-5 place-items-center rounded-md transition-colors hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-1 dark:hover:bg-white/10" style={{ color: theme.node.text }} disabled={referenceColumns.length <= MIN_BATCH_REFERENCE_COLUMNS} onClick={onRemoveReferenceColumn}>
                                            <Minus className="size-3.5" />
                                        </button>
                                    </Tooltip>
                                    <Tooltip title={referenceColumns.length >= MAX_BATCH_REFERENCE_COLUMNS ? `最多支持 ${MAX_BATCH_REFERENCE_COLUMNS} 组参考图` : `新增参考图 ${referenceColumns.length + 1}`}>
                                        <button type="button" aria-label={`新增参考图 ${referenceColumns.length + 1}`} className="grid size-5 place-items-center rounded-md transition-colors hover:bg-black/5 focus-visible:outline-2 focus-visible:outline-offset-1 dark:hover:bg-white/10" style={{ color: theme.node.text }} disabled={referenceColumns.length >= MAX_BATCH_REFERENCE_COLUMNS} onClick={onAddReferenceColumn}>
                                            <Plus className="size-3.5" />
                                        </button>
                                    </Tooltip>
                                </>
                            ) : null}
                        </div>
                        <span className="shrink-0 tabular-nums" style={{ color: theme.node.muted }}>已连 {connectedImageCount} · 完成 {completed}/{table.rows.length}</span>
                    </div>
                    {!readOnly ? (
                        <div className="ml-auto flex shrink-0 items-center gap-1.5" onPointerDown={(event) => event.stopPropagation()}>
                            <Tooltip title="增量同步画布连线，不会删除已有任务行">
                                <Button size="small" type="text" icon={<Rows3 className="size-3.5" />} onClick={onFillRows}>同步连线</Button>
                            </Tooltip>
                            <Tooltip title="将连线图片分配到指定参考图列">
                                <Button size="small" type="text" icon={<ListChecks className="size-3.5" />} disabled={!table.rows.length} onClick={openBatchFill}>批量填充</Button>
                            </Tooltip>
                            <Button size="small" type="text" icon={<Plus className="size-3.5" />} onClick={onAddRow}>添加任务</Button>
                            {table.contentKind === "storyboard" && onCreateStoryboard ? <Button size="small" icon={<Film className="size-3.5" />} onClick={onCreateStoryboard}>创建视频脚本</Button> : null}
                            <Tooltip title={regenerateAll ? "所有就绪行都已有结果，将按当前设置重新生成" : "只提交还没有结果的任务"}>
                                <Button size="small" type={regenerateAll ? "default" : "primary"} icon={<Play className="size-3.5" />} disabled={!readyRowCount} onClick={() => onGenerate()}>
                                    {regenerateAll ? `重新生成 · ${readyRowCount}` : `生成未完成项${unfinishedReadyCount ? ` · ${unfinishedReadyCount}` : ""}`}
                                </Button>
                            </Tooltip>
                        </div>
                    ) : null}
                </div>
                <div data-canvas-no-drag className="flex h-9 items-center gap-2 border-t px-3" style={{ borderColor: theme.node.stroke }} onPointerDown={(event) => event.stopPropagation()}>
                    <Tooltip title={hasGlobalPrompt ? `把这段提示词写入下面 ${table.rows.length} 行任务` : "先输入提示词，再点这个按钮批量替换各任务提示词"}>
                        <span className="shrink-0">
                            <Button type="text" size="small" className="px-1.5 font-medium" style={{ color: theme.node.muted }} disabled={readOnly || !hasGlobalPrompt || !table.rows.length} onClick={applyGlobalPromptToRows}>全局提示词</Button>
                        </span>
                    </Tooltip>
                    <input
                        value={globalPrompt}
                        readOnly={readOnly}
                        placeholder="填写后覆盖各任务提示词，留空则使用每行自己的提示词"
                        aria-label="全局提示词"
                        className="h-7 min-w-0 flex-1 rounded-md border px-2.5 text-xs outline-none"
                        style={{ background: inputSurface, borderColor: theme.node.stroke, color: theme.node.text }}
                        onChange={(event) => onPatchTable({ globalPrompt: event.target.value })}
                    />
                </div>
            </div>

            <div data-canvas-no-drag className="thin-scrollbar min-h-0 flex-1 overflow-auto rounded-b-[inherit]" onPointerDown={(event) => event.stopPropagation()}>
                <div className="sticky top-0 z-10 grid h-9 items-center border-b px-3 text-center text-[11px] font-medium" style={{ borderColor: theme.node.stroke, background: theme.node.panel, color: theme.node.muted, gridTemplateColumns }}>
                    <span className="flex min-w-0 items-center justify-center gap-2 px-1">
                        {!readOnly ? <Checkbox aria-label="选择全部任务行" checked={allRowsSelected} indeterminate={someRowsSelected} onChange={(event) => setSelectedRowIds(event.target.checked ? new Set(table.rows.map((row) => row.id)) : new Set())} /> : null}
                        <span className="truncate">任务</span>
                    </span>
                    {referenceColumns.map((column) => (
                        <span
                            key={column.id}
                            draggable={!readOnly}
                            title={column.label}
                            className="min-w-0 cursor-grab truncate px-1 active:cursor-grabbing"
                            style={{ opacity: draggingColumnId === column.id ? 0.45 : 1 }}
                            onDragStart={() => setDraggingColumnId(column.id)}
                            onDragEnd={() => setDraggingColumnId(null)}
                            onDragOver={(event) => { event.preventDefault(); }}
                            onDrop={() => { if (draggingColumnId) onReorderReferenceColumns?.(draggingColumnId, column.id); setDraggingColumnId(null); }}
                        >
                            {column.label}
                        </span>
                    ))}
                    {textColumns.map((column) => <span key={column.id} className="min-w-0 truncate px-1" title={column.label}>{column.label}</span>)}
                    <span className="min-w-0 truncate px-1">任务提示词</span>
                    <span className="min-w-0 truncate px-1">生成结果</span>
                    <span className="min-w-0 truncate px-1">操作</span>
                </div>

                {table.rows.length ? (
                    table.rows.map((row, index) => {
                        const outputs = batchRowOutputNodes(row, nodeById);
                        const output = outputs[0];
                        const item = batchItemByRowId.get(row.id);
                        const status = row.enabled ? rowStatus(item, output) : { label: "已停用", tone: "idle" as const, loading: false, retryable: false };
                        const ready = rowReady(row, table, nodeById);
                        const completedRow = batchRowHasResult(row, nodeById);
                        const references = batchRowMentionReferences(row, referenceColumns, nodeById);
                        const effectivePrompt = batchPromptForRow(table, row);
                        const disabledReason = status.loading ? "当前任务正在生成" : !row.enabled ? "请先启用这一行" : !effectivePrompt.trim() ? "请填写任务提示词" : table.operation === "try_on" && row.inputNodeIds.filter(Boolean).length < 2 ? "批量换装至少需要两张参考图" : !ready ? "请补齐有效参考图" : "";
                        return (
                            <div key={row.id} className="group grid items-center border-b px-3 py-3 transition-colors hover:bg-black/[.025] dark:hover:bg-white/[.025]" style={{ borderColor: theme.node.stroke, gridTemplateColumns, opacity: row.enabled ? 1 : 0.58 }}>
                                <div className="flex items-center justify-center gap-1.5">
                                    {!readOnly ? <Checkbox checked={selectedRowIds.has(row.id)} aria-label={`选择任务 ${index + 1}`} onChange={(event) => toggleRowSelection(row.id, event.target.checked)} /> : null}
                                    {!readOnly ? <Switch size="small" checked={row.enabled} aria-label={`启用任务 ${index + 1}`} onChange={(enabled) => onUpdateRow(row.id, { enabled })} /> : null}
                                    <span className="tabular-nums" style={{ color: theme.node.muted }}>{index + 1}</span>
                                </div>
                                {referenceColumns.map((column, columnIndex) => (
                                    <div key={column.id} className="flex flex-col items-center justify-center gap-1">
                                        <ReferenceThumbnail
                                            node={nodeById.get(row.inputNodeIds[columnIndex])}
                                            label={batchReferenceMentionToken(columnIndex)}
                                            theme={theme}
                                            readOnly={readOnly}
                                            rowId={row.id}
                                            columnIndex={columnIndex}
                                            isDraggingCell={draggingCell?.rowId === row.id && draggingCell.columnIndex === columnIndex}
                                            isDropTarget={dropTargetCell?.rowId === row.id && dropTargetCell.columnIndex === columnIndex}
                                            dropPulseSeq={cellDropPulse?.cell.rowId === row.id && cellDropPulse.cell.columnIndex === columnIndex ? cellDropPulse.seq : 0}
                                            copyTargetCount={referenceCopyTargetCount(row.id, columnIndex)}
                                            onPickFile={() => pickReferenceFile(row.id, columnIndex)}
                                            onUploadFile={(file) => onUploadReference?.(row.id, columnIndex, file)}
                                            onReplace={() => setReplaceCell({ rowId: row.id, columnIndex })}
                                            onRemove={() => onRemoveReference?.(row.id, columnIndex)}
                                            onCopyImage={() => void copyReferenceImage(nodeById.get(row.inputNodeIds[columnIndex]))}
                                            onCopyToColumn={() => copyReferenceToColumn(row.id, columnIndex)}
                                            onPointerDown={(event) => startCellDrag(event, row.id, columnIndex)}
                                        />
                                    </div>
                                ))}
                                {textColumns.map((column, columnIndex) => (
                                    <div key={column.id} className="min-w-0 px-2">
                                        {table.aiGenerated ? <textarea
                                            aria-label={`任务 ${index + 1} ${column.label}`}
                                            value={row.cells?.[column.id] || ""}
                                            readOnly={readOnly}
                                            className="thin-scrollbar h-[108px] w-full resize-none rounded-lg border px-2 py-2 text-xs outline-none focus-visible:ring-2"
                                            style={{ background: inputSurface, borderColor: theme.node.stroke, color: theme.node.text }}
                                            onChange={(event) => onUpdateRow(row.id, { cells: { ...row.cells, [column.id]: event.target.value } })}
                                        /> : <TextNodeCell node={nodeById.get(row.textNodeIds?.[columnIndex] || "")} theme={theme} />}
                                    </div>
                                ))}
                                <div className="min-w-0 pr-3">
                                    <CanvasResourceMentionTextarea
                                        value={row.prompt}
                                        references={references}
                                        readOnly={readOnly}
                                        sendOnEnter={false}
                                        mentionMenuWidth={300}
                                        aria-label={`任务 ${index + 1} 提示词`}
                                        placeholder={hasGlobalPrompt ? "已使用全局提示词，可在此填写行级覆盖" : "描述生成目标，输入 @ 引用本行参考图"}
                                        containerClassName="h-[108px]"
                                        className="thin-scrollbar h-full w-full overflow-y-auto rounded-lg border px-3 py-2 text-xs leading-5 outline-none transition-shadow focus-visible:ring-2"
                                        style={{ background: inputSurface, borderColor: theme.node.stroke, color: theme.node.text }}
                                        onChange={(prompt) => onUpdateRow(row.id, { prompt })}
                                        onSubmit={!readOnly && ready && !status.loading ? () => onGenerate([row.id]) : undefined}
                                        onPointerDown={(event) => event.stopPropagation()}
                                        onWheel={(event) => event.stopPropagation()}
                                    />
                                    <div className="mt-1 truncate px-0.5 text-[10px]" style={{ color: theme.node.faint }}>
                                        {readOnly ? "输入 @ 插入参考图" : "输入 @ 插入参考图 · ⌘/Ctrl + Enter 生成此行"}
                                    </div>
                                </div>
                                <div className="flex h-16 min-h-16 items-center justify-center">
                                    <ResultThumbnail
                                        outputs={outputs}
                                        status={status}
                                        theme={theme}
                                        onFocus={(nodeId) => onFocusOutput?.(nodeId)}
                                    />
                                </div>
                                {!readOnly ? (
                                    <div className="flex items-center justify-center gap-1">
                                        <Tooltip title={disabledReason || (completedRow ? "重新生成这一行" : "只生成这一行")}>
                                            <Button type={completedRow ? "text" : "primary"} size="small" className="w-8 px-0" disabled={Boolean(disabledReason)} icon={status.loading ? <LoaderCircle className="size-3.5 animate-spin" /> : <Play className="size-3.5" />} onClick={() => onGenerate([row.id])} />
                                        </Tooltip>
                                        <Tooltip title="删除这一行"><Button type="text" size="small" className="w-7 px-0 opacity-60 transition-opacity group-hover:opacity-100" danger icon={<Trash2 className="size-3.5" />} onClick={() => onRemoveRow(row.id)} /></Tooltip>
                                    </div>
                                ) : <span />}
                            </div>
                        );
                    })
                ) : (
                    <div className="grid min-h-44 place-items-center px-5 text-center">
                        <div className="flex max-w-sm flex-col items-center gap-2">
                            <div className="grid size-10 place-items-center rounded-xl" style={{ background: theme.accent.primarySoft, color: theme.node.text }}><Rows3 className="size-5" /></div>
                            <div className="font-medium">还没有批量任务</div>
                            <p className="m-0 leading-5" style={{ color: theme.node.muted }}>把图片连接到左侧参考图端口后同步连线，或先添加一行手工配置。</p>
                            {!readOnly ? <Button size="small" icon={<Plus className="size-3.5" />} onClick={onAddRow}>添加第一条任务</Button> : null}
                        </div>
                    </div>
                )}
            </div>
            {!readOnly ? (
                <AppModal
                    open={fillOpen}
                    title="批量填充参考图"
                    width={520}
                    okText={`填充 ${fillCount} 行`}
                    cancelText="取消"
                    okButtonProps={{ disabled: !fillSourceIds.length || !fillEligibleRows.length || !fillCount }}
                    onOk={applyBatchFill}
                    onCancel={() => setFillOpen(false)}
                >
                    <div className="flex flex-col gap-5 py-2" onPointerDown={(event) => event.stopPropagation()}>
                        <div className="grid grid-cols-[112px_minmax(0,1fr)] items-center gap-3">
                            <span className="font-medium">填充范围</span>
                            <Segmented
                                block
                                value={fillScope}
                                options={[
                                    { label: `已勾选行 (${selectedRowIds.size})`, value: "selected", disabled: !selectedRowIds.size },
                                    { label: `全部行 (${table.rows.length})`, value: "all" },
                                ]}
                                onChange={(value) => setFillScope(value as "selected" | "all")}
                            />
                            <span className="font-medium">图片来源</span>
                            <Select
                                value={fillSourceKey}
                                options={[
                                    { label: <BatchFillSourceLabel text={`整个当前画布 (${canvasImageIds.length})`} nodeIds={canvasImageIds} nodeById={nodeById} theme={theme} />, value: CANVAS_FILL_SOURCE_KEY },
                                    { label: <BatchFillSourceLabel text={`全部已连图片 (${connectedImageIds.length})`} nodeIds={connectedImageIds} nodeById={nodeById} theme={theme} />, value: "all" },
                                ]}
                                onChange={setFillSourceKey}
                            />
                            <span className="self-start pt-1 font-medium">参考图预览</span>
                            <BatchFillImageStrip nodeIds={fillSourceIds} nodeById={nodeById} theme={theme} />
                            <span className="font-medium">填充到</span>
                            <Select value={fillTargetColumn} options={referenceColumns.map((column, index) => ({ label: column.label, value: index }))} onChange={setFillTargetColumn} />
                            <span className="font-medium">分配方式</span>
                            <Segmented
                                block
                                value={fillMode}
                                options={[
                                    { label: "按顺序", value: "sequential" },
                                    { label: "循环", value: "cycle" },
                                    { label: "同一张", value: "same" },
                                ]}
                                onChange={(value) => setFillMode(value as BatchReferenceFillMode)}
                            />
                            <span />
                            <p className="m-0 -mt-2 text-[11px] leading-5" style={{ color: theme.node.muted }}>{fillModeDescription}</p>
                            <span className="font-medium">已有图片</span>
                            <Segmented
                                block
                                value={fillOverwrite ? "replace" : "empty"}
                                options={[{ label: "替换已有", value: "replace" }, { label: "仅填空位", value: "empty" }]}
                                onChange={(value) => setFillOverwrite(value === "replace")}
                            />
                        </div>
                        <div className="rounded-lg border px-3 py-2.5 text-xs leading-5" style={{ borderColor: theme.node.stroke, background: subtleSurface, color: theme.node.muted }}>
                            {fillSourceIds.length
                                ? `${fillSourceIds.length} 张${fillSourceIsCanvas ? "画布" : "连线"}图片 -> ${fillTargetRows.length} 个${fillScope === "selected" ? "已勾选" : "全部"}任务 -> ${referenceColumns[fillTargetColumn]?.label || "参考图"}，本次将更新 ${fillCount} 行。`
                                : fillSourceIsCanvas
                                    ? "当前画布上还没有可用的图片或视频素材。"
                                    : "当前没有可用的连线图片。请先将图片节点连接到批量创作表的参考图端口，或把图片来源换成整个当前画布。"}
                        </div>
                    </div>
                </AppModal>
            ) : null}
            {!readOnly ? (
                <AppModal
                    open={Boolean(replaceCell)}
                    title="替换参考图"
                    width={480}
                    footer={null}
                    onCancel={() => setReplaceCell(null)}
                >
                    <div className="flex flex-col gap-3 py-2" onPointerDown={(event) => event.stopPropagation()}>
                        <p className="m-0 text-xs leading-5" style={{ color: theme.node.muted }}>选择画布上的素材替换当前位置，或直接上传本地图片。</p>
                        <BatchFillImageStrip
                            nodeIds={canvasImageIds.filter((nodeId) => nodeId !== (replaceCell ? nodeById.get(table.rows.find((row) => row.id === replaceCell.rowId)?.inputNodeIds[replaceCell.columnIndex] || "")?.id : undefined))}
                            nodeById={nodeById}
                            theme={theme}
                            onPick={(nodeId) => {
                                if (!replaceCell) return;
                                const nextRows = table.rows.map((row) => {
                                    if (row.id !== replaceCell.rowId) return row;
                                    const inputNodeIds = [...row.inputNodeIds];
                                    inputNodeIds[replaceCell.columnIndex] = nodeId;
                                    return { ...row, inputNodeIds };
                                });
                                onPatchTable({ rows: nextRows });
                                setReplaceCell(null);
                            }}
                        />
                        <div className="flex justify-end gap-2">
                            <Button size="small" danger type="text" icon={<Trash2 className="size-3.5" />} onClick={() => { if (replaceCell) onRemoveReference?.(replaceCell.rowId, replaceCell.columnIndex); setReplaceCell(null); }}>清空这一格</Button>
                            <Button size="small" icon={<Upload className="size-3.5" />} onClick={() => { if (replaceCell) pickReferenceFile(replaceCell.rowId, replaceCell.columnIndex); setReplaceCell(null); }}>上传本地图片</Button>
                        </div>
                    </div>
                </AppModal>
            ) : null}
            {/* 拖动参考图时的浮层预览：跟随指针，松手即消失。 */}
            {dragGhost ? createPortal(
                <div ref={dragGhostRef} className="pointer-events-none fixed left-0 top-0 z-[1400] size-[72px] overflow-hidden rounded-xl border-2 shadow-2xl" style={{ borderColor: theme.accent.primary, background: theme.node.panel, willChange: "transform" }}>
                    {dragGhost.src ? <CachedResourceImage eager src={dragGhost.src} alt="拖动预览" className="block size-full object-cover" fallback={<EmptyThumbnail theme={theme} compact />} /> : <EmptyThumbnail theme={theme} compact />}
                </div>,
                document.body,
            ) : null}
            {/* 画布素材放进格子：从素材原位置缩小飞入格子。 */}
            {flyInGhost ? createPortal(
                <div ref={flyInGhostRef} className="pointer-events-none fixed z-[1400] overflow-hidden rounded-xl border-2 shadow-2xl" style={{ left: flyInGhost.from.x, top: flyInGhost.from.y, width: flyInGhost.from.width, height: flyInGhost.from.height, borderColor: theme.accent.primary, background: theme.node.panel, willChange: "transform", transformOrigin: "center center" }}>
                    {flyInGhost.src ? <CachedResourceImage eager src={flyInGhost.src} alt="放入表格" className="block size-full object-cover" fallback={<EmptyThumbnail theme={theme} compact />} /> : <EmptyThumbnail theme={theme} compact />}
                </div>,
                document.body,
            ) : null}
        </div>
    );
}
function BatchFillImageStrip({ nodeIds, nodeById, theme, onPick }: { nodeIds: string[]; nodeById: Map<string, CanvasNodeData>; theme: CanvasTheme; onPick?: (nodeId: string) => void }) {
    if (!nodeIds.length) {
        return <div className="rounded-lg border border-dashed px-3 py-2 text-xs" style={{ borderColor: theme.node.stroke, color: theme.node.muted }}>暂无可用参考图</div>;
    }
    return (
        <div className="flex min-w-0 items-center gap-2 overflow-x-auto rounded-lg border px-2 py-1.5" style={{ borderColor: theme.node.stroke, background: `color-mix(in srgb, ${theme.node.text} 3%, transparent)` }}>
            {nodeIds.map((nodeId, index) => {
                const source = nodeById.get(nodeId);
                if (!source) return null;
                const fallback = <div className="grid size-12 place-items-center" style={{ color: theme.node.muted }}><ImageIcon className="size-4" /></div>;
                return (
                    <Tooltip key={nodeId} title={onPick ? `${source.title || `参考图 ${index + 1}`} · 点击用作替换` : source.title || `参考图 ${index + 1}`}>
                        <button
                            type="button"
                            disabled={!onPick}
                            onClick={onPick ? () => onPick(nodeId) : undefined}
                            className="size-12 shrink-0 overflow-hidden rounded-md border transition-transform enabled:hover:-translate-y-0.5 enabled:hover:shadow-md enabled:cursor-pointer disabled:cursor-default"
                            style={{ borderColor: theme.node.stroke, background: theme.node.panel }}
                        >
                            <CachedResourceImage eager src={source.metadata?.previewContent || source.metadata?.content} storageKey={source.metadata?.storageKey} alt={source.title || `参考图 ${index + 1}`} className="block size-full object-cover" fallback={fallback} />
                        </button>
                    </Tooltip>
                );
            })}
        </div>
    );
}

function BatchFillSourceLabel({ text, nodeIds, nodeById, theme }: { text: string; nodeIds: string[]; nodeById: Map<string, CanvasNodeData>; theme: CanvasTheme }) {
    return (
        <span className="flex min-w-0 items-center gap-2">
            <span className="flex shrink-0 items-center gap-0.5">
                {nodeIds.slice(0, 3).map((nodeId) => {
                    const source = nodeById.get(nodeId);
                    if (!source) return null;
                    return <CachedResourceImage key={nodeId} eager src={source.metadata?.previewContent || source.metadata?.content} storageKey={source.metadata?.storageKey} alt="" className="size-5 rounded object-cover" fallback={<ImageIcon className="size-3.5" />} />;
                })}
            </span>
            <span className="truncate">{text}</span>
        </span>
    );
}

function BatchChoiceGroup({ ariaLabel, options, value, onChange, theme, compact = false, disabled = false }: { ariaLabel: string; options: Array<{ value: string | number; label: string }>; value: string | number; onChange: (value: string | number) => void; theme: CanvasTheme; compact?: boolean; disabled?: boolean }) {
    return (
        <div role="group" aria-label={ariaLabel} className="flex shrink-0 items-center rounded-lg p-0.5" style={{ background: theme.node.panel }}>
            {options.map((option) => {
                const selected = option.value === value;
                return <button key={option.value} type="button" aria-pressed={selected} disabled={disabled} className={`rounded-md font-medium transition-colors focus-visible:outline-2 focus-visible:outline-offset-1 disabled:cursor-not-allowed disabled:opacity-50 ${compact ? "min-w-7 px-1.5 py-1 text-[10px]" : "px-2.5 py-1.5 text-[11px]"}`} style={{ background: selected ? theme.accent.primary : "transparent", color: selected ? theme.accent.onPrimary : theme.node.muted }} onClick={() => onChange(option.value)}>{option.label}</button>;
            })}
        </div>
    );
}

function BatchTextHandle(props: Omit<React.ComponentProps<typeof BatchReferenceHandle>, "column"> & { column: { id: string; label: string } }) {
    return <BatchReferenceHandle {...props} column={props.column} handlePrefix="text" />;
}

function BatchReferenceHandles({ columns, textColumns, theme, onAdd, onAddText, onConnectStart, onConnectDrop }: { columns: ReturnType<typeof batchReferenceColumns>; textColumns: ReturnType<typeof batchTextColumns>; theme: CanvasTheme; onAdd: () => void; onAddText: () => void; onConnectStart: (event: ReactPointerEvent, handleId: string) => void; onConnectDrop?: (event: ReactPointerEvent, handleId: string) => void }) {
    const commonStyle = { left: 0, width: 36, height: 36, transform: "translate(-50%, -50%)", transformOrigin: "center" };
    const referenceAddTop = BATCH_REFERENCE_HANDLE_TOP + columns.length * BATCH_REFERENCE_HANDLE_GAP;
    const textTop = batchTextHandleTop(columns.length);
    return (
        <>
            {columns.map((column, index) => <BatchReferenceHandle key={column.id} column={column} top={BATCH_REFERENCE_HANDLE_TOP + index * BATCH_REFERENCE_HANDLE_GAP} badge={String(index + 1)} theme={theme} commonStyle={commonStyle} onConnectStart={onConnectStart} onConnectDrop={onConnectDrop} />)}
            {textColumns.map((column, index) => <BatchTextHandle key={column.id} column={column} top={textTop + index * BATCH_REFERENCE_HANDLE_GAP} badge={`T${index + 1}`} theme={theme} commonStyle={commonStyle} onConnectStart={onConnectStart} onConnectDrop={onConnectDrop} />)}
            {columns.length < MAX_BATCH_REFERENCE_COLUMNS ? (
                <Tooltip title={`新增参考图 ${columns.length + 1}`} placement="left">
                    <button type="button" data-canvas-no-drag aria-label={`新增参考图 ${columns.length + 1}`} className="group absolute z-[var(--node-z-handle)] grid place-items-center rounded-full outline-none" style={{ ...commonStyle, top: referenceAddTop, color: theme.accent.primary }} onPointerDown={(event) => event.stopPropagation()} onClick={(event) => { event.stopPropagation(); onAdd(); }}>
                        <span className="grid size-[22px] place-items-center rounded-full border shadow-sm transition-transform group-hover:scale-110 group-focus-visible:scale-110" style={{ background: theme.node.panel, borderColor: theme.accent.primary }}><Plus className="size-3" /></span>
                    </button>
                </Tooltip>
            ) : null}
            {textColumns.length < MAX_BATCH_TEXT_COLUMNS ? (
                <Tooltip title={`新增文字 ${textColumns.length + 1}`} placement="left">
                    <button type="button" data-canvas-no-drag aria-label={`新增文字 ${textColumns.length + 1}`} className="group absolute z-[var(--node-z-handle)] grid place-items-center rounded-full outline-none" style={{ ...commonStyle, top: textTop + textColumns.length * BATCH_REFERENCE_HANDLE_GAP, color: theme.node.text }} onPointerDown={(event) => event.stopPropagation()} onClick={(event) => { event.stopPropagation(); onAddText(); }}>
                        <span className="grid size-[22px] place-items-center rounded-full border shadow-sm transition-transform group-hover:scale-110 group-focus-visible:scale-110" style={{ background: theme.node.panel, borderColor: theme.node.stroke }}><Plus className="size-3" /></span>
                    </button>
                </Tooltip>
            ) : null}
        </>
    );
}

function BatchReferenceHandle({ column, top, badge, theme, commonStyle, onConnectStart, onConnectDrop, handlePrefix = "reference" }: { column: { id: string; label: string }; top: number; badge: string; theme: CanvasTheme; commonStyle: { left: number; width: number; height: number; transform: string; transformOrigin: string }; onConnectStart: (event: ReactPointerEvent, handleId: string) => void; onConnectDrop?: (event: ReactPointerEvent, handleId: string) => void; handlePrefix?: "reference" | "text" }) {
    const [hovered, setHovered] = useState(false);
    const [offset, setOffset] = useState({ x: 0, y: 0 });
    const handleId = handlePrefix === "text" ? batchTextHandleId(column.id) : batchReferenceHandleId(column.id);
    const reset = useCallback(() => { setHovered(false); setOffset({ x: 0, y: 0 }); }, []);
    const update = useCallback((event: ReactPointerEvent<HTMLButtonElement>) => {
        const bounds = event.currentTarget.getBoundingClientRect();
        const dx = event.clientX - (bounds.left + bounds.width / 2);
        const dy = event.clientY - (bounds.top + bounds.height / 2);
        const limit = 10;
        setOffset({ x: Math.max(-limit, Math.min(limit, dx)), y: Math.max(-limit, Math.min(limit, dy)) });
    }, []);
    return (
        <Tooltip title={`连接到${column.label}`} placement="left">
            <button type="button" data-canvas-no-drag aria-label={`${column.label}连线点`} className="group absolute z-[var(--node-z-handle)] grid place-items-center rounded-full outline-none" style={{ ...commonStyle, top, cursor: "crosshair" }} onPointerEnter={(event) => { setHovered(true); update(event); }} onPointerMove={update} onPointerLeave={reset} onPointerDown={(event) => { event.stopPropagation(); onConnectStart(event, handleId); }} onPointerUp={(event) => { event.stopPropagation(); onConnectDrop?.(event, handleId); }}>
                    <span className="grid size-[18px] place-items-center rounded-full border text-[8px] font-semibold shadow-sm transition-transform duration-100 group-hover:scale-125 group-focus-visible:scale-125" style={{ transform: `translate(${offset.x}px, ${offset.y}px) scale(${hovered ? 1.06 : 1})`, background: theme.node.panel, borderColor: handlePrefix === "text" ? theme.node.stroke : theme.accent.primary, color: handlePrefix === "text" ? theme.node.muted : theme.accent.primary }}>{badge}</span>
            </button>
        </Tooltip>
    );
}

function batchRowMentionReferences(row: CanvasBatchRow, columns: ReturnType<typeof batchReferenceColumns>, nodeById: Map<string, CanvasNodeData>): CanvasResourceReference[] {
    return columns.flatMap((column, index) => {
        const source = nodeById.get(row.inputNodeIds[index]);
        if (!source) return [];
        return [{ id: `${row.id}:${column.id}:${source.id}`, nodeId: source.id, kind: "image" as const, label: `参考图${index + 1}`, title: `${column.label} · ${source.title || "图片"}`, previewUrl: source.metadata?.previewContent || source.metadata?.content, storageKey: source.metadata?.storageKey, active: true, sourceType: source.type, mentionToken: batchReferenceMentionToken(index) }];
    });
}

type CellMenuItem = { key: string; label: string; icon: ReactNode; danger?: boolean; disabled?: boolean; onSelect: () => void };

/**
 * 参考图格子：固定正方形缩略图，和结果格、空格子尺寸一致。
 * 悬停时格内左上角复制、右上角清空，右键出更多操作菜单。
 * 菜单通过 portal 挂到 body，避免被表格的滚动容器裁掉。
 */
function ReferenceThumbnail({ node, label, theme, readOnly, rowId, columnIndex, isDraggingCell, isDropTarget, dropPulseSeq, copyTargetCount, onPickFile, onUploadFile, onReplace, onRemove, onCopyImage, onCopyToColumn, onPointerDown }: { node?: CanvasNodeData; label: string; theme: CanvasTheme; readOnly: boolean; rowId: string; columnIndex: number; isDraggingCell: boolean; isDropTarget: boolean; dropPulseSeq: number; copyTargetCount: number; onPickFile: () => void; onUploadFile: (file: File) => void; onReplace: (node: CanvasNodeData) => void; onRemove: () => void; onCopyImage: () => void; onCopyToColumn: () => void; onPointerDown: (event: ReactPointerEvent) => void }) {
    const filled = node && hasNodeMedia(node);
    const fallback = <EmptyThumbnail theme={theme} />;
    const [menu, setMenu] = useState<{ x: number; y: number; full: boolean } | null>(null);

    // 键盘聚焦状态：鼠标点击也要看得到“这一格已选中”，Delete / 回退键才有明确的删除对象。
    const [focused, setFocused] = useState(false);
    const cellRef = useRef<HTMLButtonElement | null>(null);

    // 素材落进这一格时缩一下再回到原位，视觉上就是“缩小放进表格”。
    useEffect(() => {
        if (!dropPulseSeq) return;
        const cell = cellRef.current;
        if (!cell || typeof cell.animate !== "function") return;
        const animation = cell.animate(
            [{ transform: "scale(1.18)", opacity: 0.45 }, { transform: "scale(1)", opacity: 1 }],
            { duration: 260, easing: "cubic-bezier(.22,.85,.24,1)" },
        );
        return () => animation.cancel();
    }, [dropPulseSeq]);
    useEffect(() => {
        if (!menu) return;
        const close = (event: Event) => {
            if ((event.target as Element | null)?.closest?.("[data-canvas-cell-menu]")) return;
            setMenu(null);
        };
        window.addEventListener("pointerdown", close);
        window.addEventListener("resize", close);
        window.addEventListener("keydown", close);
        return () => {
            window.removeEventListener("pointerdown", close);
            window.removeEventListener("resize", close);
            window.removeEventListener("keydown", close);
        };
    }, [menu]);

    const copyItems: CellMenuItem[] = [
        { key: "image", label: "复制图片到剪贴板", icon: <Copy />, onSelect: onCopyImage },
        { key: "column", label: copyTargetCount ? `复制到本列其他行 (${copyTargetCount})` : "复制到本列其他行", icon: <Copy />, disabled: !copyTargetCount, onSelect: onCopyToColumn },
    ];
    const menuItems: CellMenuItem[] = filled
        ? [
            { key: "replace", label: "替换参考图", icon: <Upload />, onSelect: () => { if (node) onReplace(node); } },
            ...copyItems,
            { key: "clear", label: "清空这一格", icon: <Trash2 />, danger: true, onSelect: onRemove },
        ]
        : [{ key: "upload", label: "上传本地图片", icon: <Upload />, onSelect: onPickFile }];
    const openMenu = (x: number, y: number, full: boolean) => setMenu({ x, y, full });
    // 缩略图获得焦点时，Delete / 回退键只作用于这一格。
    // 无论这一格是否已有素材都拦截冒泡，否则焦点在格子上按 Delete 会把整个节点删掉。
    const handleCellKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
        if (event.key !== "Delete" && event.key !== "Backspace") return;
        event.stopPropagation();
        if (readOnly || !filled) return;
        event.preventDefault();
        setMenu(null);
        onRemove();
    };

    return (
        <div data-canvas-cell-group className="contents" onKeyDown={handleCellKeyDown}>
        {/* 缩略图不挂 Tooltip：鼠标划过会挡住图片，格子的说明只留在无障碍名称里。 */}
            <button
                type="button"
                ref={cellRef}
                aria-label={filled ? `${label} ${node.title || "图片"}，点击替换，左上角复制，右上角清空` : `${label} 点击上传或拖入图片`}
                data-batch-reference-cell
                data-row-id={rowId}
                data-column-index={columnIndex}
                className="group relative box-border grid shrink-0 place-items-center overflow-hidden rounded-lg border outline-none transition-[transform,box-shadow,border-color,opacity] duration-150 ease-out"
                style={{ width: REFERENCE_THUMB_SIZE, height: REFERENCE_THUMB_SIZE, borderColor: isDropTarget || filled ? (isDropTarget ? theme.accent.primary : theme.node.stroke) : "transparent", opacity: isDraggingCell ? 0.35 : 1, boxShadow: isDropTarget || focused ? `0 0 0 2px ${theme.accent.primary}` : undefined, transform: isDropTarget ? "scale(1.08)" : undefined }}
                onFocus={() => setFocused(true)}
                onBlur={() => setFocused(false)}
                disabled={readOnly}
                onContextMenu={(event) => {
                    event.preventDefault();
                    event.stopPropagation();
                    if (readOnly) return;
                    openMenu(event.clientX, event.clientY, true);
                }}
                onPointerDown={onPointerDown}
                // 格子拖拽走的是 pointer 事件；如果不拦掉浏览器对 <img> 的原生拖拽，
                // 拖到别的行列会被当成“上传文件”，在画布上凭空多出一张素材。
                onDragStart={(event) => event.preventDefault()}
                onClick={(event) => {
                    event.stopPropagation();
                    if (readOnly) return;
                    // 已有素材时点击打开替换面板；空位仍然走上传。
                    if (filled) onReplace(node);
                    else onPickFile();
                }}
                onDragOver={(event) => { event.preventDefault(); event.stopPropagation(); }}
                onDrop={(event) => {
                    event.preventDefault();
                    event.stopPropagation();
                    const file = Array.from(event.dataTransfer.files).find((item) => item.type.startsWith("image/"));
                    if (file && !readOnly) onUploadFile(file);
                }}
            >
                {/* 图片必须绝对定位铺满方格：作为 grid item 时 h-full 会退化成按原始宽高比排版，比格子还高，只能被裁掉一截。 */}
                {filled ? <CachedResourceImage eager draggable={false} src={node.metadata?.previewContent || node.metadata?.content} storageKey={node.metadata?.storageKey} alt={node.title || "参考图"} className="absolute inset-0 size-full object-cover" fallback={fallback} /> : fallback}
                {filled && !readOnly ? <span className="pointer-events-none absolute inset-0 grid place-items-center bg-black/45 text-[9px] font-medium text-white opacity-0 transition-opacity group-hover:opacity-100">点击替换</span> : null}
                {/* 复制和清空都放进格子内部，跟格同高，不会在某一格下面另起一行把行高撞高。 */}
                {filled && !readOnly ? (
                    <span
                        role="button"
                        aria-label={`复制${label}`}
                        tabIndex={-1}
                        className="absolute left-0.5 top-0.5 grid size-4 place-items-center rounded-full bg-black/60 text-white opacity-0 transition-opacity group-hover:opacity-100"
                        onPointerDown={(event) => event.stopPropagation()}
                        onClick={(event) => {
                            event.stopPropagation();
                            event.preventDefault();
                            const rect = event.currentTarget.getBoundingClientRect();
                            openMenu(rect.left, rect.bottom + 6, false);
                        }}
                    >
                        <Copy className="size-3" />
                    </span>
                ) : null}
                {filled && !readOnly ? (
                    <span
                        role="button"
                        aria-label={`清空${label}`}
                        tabIndex={-1}
                        className="absolute right-0.5 top-0.5 grid size-4 place-items-center rounded-full bg-black/60 text-white opacity-0 transition-opacity group-hover:opacity-100"
                        onPointerDown={(event) => event.stopPropagation()}
                        onClick={(event) => { event.stopPropagation(); event.preventDefault(); onRemove(); }}
                    >
                        <X className="size-3" />
                    </span>
                ) : null}
            </button>
        {menu ? createPortal(
            <div
                data-canvas-cell-menu
                data-canvas-no-zoom
                className="fixed z-[var(--z-modal)] min-w-44 rounded-lg border p-1.5 text-xs shadow-xl"
                style={{ left: Math.min(menu.x, Math.max(8, window.innerWidth - 190)), top: Math.min(menu.y, Math.max(8, window.innerHeight - (menu.full ? menuItems.length : copyItems.length) * 34 - 24)), background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
                onContextMenu={(event) => event.preventDefault()}
            >
                {(menu.full ? menuItems : copyItems).map((item) => (
                    <button
                        key={item.key}
                        type="button"
                        disabled={item.disabled}
                        className={`flex h-8 w-full items-center gap-2 rounded-md px-2 text-left transition hover:bg-black/5 disabled:cursor-not-allowed disabled:opacity-45 dark:hover:bg-white/10 ${item.danger ? "text-[var(--status-error)]" : ""}`}
                        onClick={() => { setMenu(null); item.onSelect(); }}
                    >
                        <span className="grid size-4 place-items-center [&>svg]:size-3.5">{item.icon}</span>
                        <span className="truncate">{item.label}</span>
                    </button>
                ))}
            </div>,
            document.body,
        ) : null}
        </div>
    );
}
/** 文字列：展示连在 T 端口上的文字节点内容，没连时给出可操作的提示。 */
function TextNodeCell({ node, theme }: { node?: CanvasNodeData; theme: CanvasTheme }) {
    const content = node?.metadata?.content || node?.metadata?.prompt || "";
    return (
        <Tooltip title={content || "把文字节点连接到左侧 T 端口"}>
            <div
                className="thin-scrollbar h-[108px] w-full overflow-y-auto whitespace-pre-wrap break-words rounded-lg border px-2 py-2 text-xs leading-5"
                style={{ background: theme.node.panel, borderColor: theme.node.stroke, color: content ? theme.node.text : theme.node.faint }}
            >
                {content || "未连接文字"}
            </div>
        </Tooltip>
    );
}

function ResultThumbnail({ outputs, status, theme, onFocus }: { outputs: CanvasNodeData[]; status: ReturnType<typeof rowStatus>; theme: CanvasTheme; onFocus: (nodeId: string) => void }) {
    const output = outputs[0];
    const filled = hasNodeMedia(output);
    const tone = statusColor(status.tone, theme.node.stroke);
    const extraCount = Math.max(0, outputs.length - 1);
    const title = filled ? `${status.label} · 点击定位到画布节点` : status.label;
    return (
        <Tooltip title={extraCount ? `${title}（本行共 ${outputs.length} 张结果，点击定位第 1 张）` : title}>
            <button
                type="button"
                aria-label={title}
                disabled={!output}
                className="relative box-border grid size-16 shrink-0 place-items-center overflow-hidden rounded-lg border-2"
                style={{ borderColor: tone, cursor: output ? "pointer" : "default" }}
                onClick={(event) => {
                    event.stopPropagation();
                    if (output) onFocus(output.id);
                }}
            >
                {/* 和参考图格子同样的问题：grid item 上的 h-full 不生效，结果图会超出方框。 */}
                {filled && output ? <CachedResourceImage eager draggable={false} src={output.metadata?.previewContent || output.metadata?.content} storageKey={output.metadata?.storageKey} alt="生成结果" className="absolute inset-0 size-full object-cover" fallback={<EmptyThumbnail theme={theme} compact />} /> : <EmptyThumbnail theme={theme} compact />}
                {status.loading ? <span className="absolute inset-0 grid place-items-center bg-black/35"><LoaderCircle className="size-4 animate-spin" style={{ color: tone }} /></span> : null}
                {extraCount ? <span className="absolute bottom-1 left-1 rounded px-1 text-[9px] font-medium text-white" style={{ background: "rgba(0,0,0,.58)" }}>+{extraCount}</span> : null}
                <span className="absolute right-1 top-1 size-2 rounded-full" style={{ background: tone }} />
            </button>
        </Tooltip>
    );
}

function statusColor(tone: RowStatusTone, fallback: string) {
    if (tone === "success") return "var(--status-success)";
    if (tone === "error") return "var(--status-error)";
    if (tone === "loading") return "var(--status-loading)";
    return fallback;
}

function EmptyThumbnail({ theme, compact = false }: { theme: CanvasTheme; compact?: boolean }): ReactNode {
    const sizeClass = "size-full";
    return (
        <div
            className={`grid shrink-0 place-items-center rounded-lg border border-dashed ${sizeClass}`}
            style={{ borderColor: theme.node.stroke, color: theme.node.placeholder, background: `color-mix(in srgb, ${theme.node.text} 3%, transparent)` }}
        >
            {compact ? <ImageIcon className="size-4" /> : (
                <span className="flex flex-col items-center gap-0.5">
                    <Upload className="size-4" />
                    <span className="text-[9px] leading-none">上传</span>
                </span>
            )}
        </div>
    );
}

/**
 * 参考图预览格子：固定正方形，和结果格、空格子同一尺寸，行列不再因为素材比例高低不齐；
 * 图片用 object-cover 居中裁切，竖图/横图看起来都是同一套 UI。
 */
const REFERENCE_THUMB_SIZE = 64;

function hasNodeMedia(node?: CanvasNodeData) {
    return Boolean(node?.metadata?.content || node?.metadata?.storageKey);
}

type RowStatusTone = "success" | "error" | "loading" | "idle";
type RowStatus = { label: string; tone: RowStatusTone; loading: boolean; retryable: boolean };

function rowStatus(item: CanvasGenerationBatchItem | undefined, output: CanvasNodeData | undefined): RowStatus {
    if (hasNodeMedia(output)) return { label: "生成完成", tone: "success", loading: false, retryable: false };
    if (item?.status === "failed") return { label: item.errorDetails || "生成失败", tone: "error", loading: false, retryable: true };
    if (item?.status === "cancelled") return { label: "已停止", tone: "error", loading: false, retryable: false };
    if (item && ["waiting", "submitting", "queued", "running"].includes(item.status)) return { label: item.status === "waiting" ? "等待中" : item.status === "submitting" ? "正在提交" : item.status === "queued" ? "已排队" : "生成中", tone: "loading", loading: true, retryable: false };
    if (output?.metadata?.status === "error") return { label: output.metadata.errorDetails || "生成失败", tone: "error", loading: false, retryable: false };
    return { label: "待生成", tone: "idle", loading: false, retryable: false };
}
