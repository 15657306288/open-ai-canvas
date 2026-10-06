import { useCallback, useMemo, useState, type Dispatch, type SetStateAction } from "react";
import { App } from "antd";
import { nanoid } from "nanoid";

import {
    MAX_BATCH_REFERENCE_COLUMNS,
    MAX_BATCH_TEXT_COLUMNS,
    batchGenerationRows,
    batchInputColumns,
    batchPromptForRow,
    batchReferenceColumns,
    batchReferenceHandleId,
    batchRowOutputNodeIds,
    batchTextInputColumns,
    createInheritedBatchRow,
    createBatchRowsFromColumns,
    moveBatchReferenceCell,
    removeLastBatchReferenceColumn,
    reorderBatchReferenceColumns,
} from "@/lib/canvas/canvas-batch-table";
import { commerceSnapshot, isCommerceNode } from "@/lib/canvas/commerce-workflow";
import { commerceScreenImageSize } from "@/lib/canvas/commerce-image-size";
import { modelCapabilityConfigFor } from "@/lib/model-capabilities";
import { modelCompatibilityError } from "@/lib/model-selection";
import { synchronizeGenerationSpec } from "@/lib/canvas/generation-contract";
import { createCanvasNode } from "@/lib/canvas/canvas-project-domain";
import { buildGenerationConfig, getGenerationCount, resetGenerationTaskMetadata } from "@/lib/canvas/canvas-project-generation";
import { MEDIA_NODE_MIN_SIZE, nodeSizeFromRatio } from "@/lib/canvas/canvas-node-size";
import { navigateToSettings } from "@/lib/settings-navigation";
import { useConfigStore, useEffectiveConfig } from "@/stores/use-config-store";
import { CanvasNodeType, type CanvasBatchRow, type CanvasBatchTableData, type CanvasConnection, type CanvasGenerationBatchMode, type CanvasNodeData } from "@/types/canvas";
import type { BatchGenerationSettings } from "@/components/canvas/batch-generation-settings-dialog";

type Options = {
    readOnly?: boolean;
    nodesRef: { current: CanvasNodeData[] };
    connectionsRef: { current: CanvasConnection[] };
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    setConnections: Dispatch<SetStateAction<CanvasConnection[]>>;
    setSelectedNodeIds: Dispatch<SetStateAction<Set<string>>>;
    enqueueGenerationBatch: (sourceNodeId: string, mode: CanvasGenerationBatchMode, targets: Array<{ rowId: string; nodeId: string }>, options?: { concurrency?: number }) => string | undefined;
};

type PendingBatchGen = {
    commerceSnapshot?: string;
    nodeId: string;
    rows: CanvasBatchRow[];
    concurrency: number;
    tableSnapshot: string;
    requestedRowIds?: string[];
};

// 批量结果卡片统一尺寸并按行横向排列，避免多结果挤在一列窄长占位框。
const BATCH_OUTPUT_CARD = MEDIA_NODE_MIN_SIZE;
const BATCH_OUTPUT_GAP_X = 28;
const BATCH_OUTPUT_GAP_Y = 28;
const BATCH_OUTPUT_OFFSET_X = 140;

function batchOutputLabels(operation: CanvasBatchTableData["operation"], rowIndex: number, index: number, outputCount: number) {
    const label = operation === "try_on" ? "换装" : "创意";
    return {
        title: outputCount > 1 ? `${label} · ${rowIndex + 1}-${index + 1}` : `${label} · ${rowIndex + 1}`,
        workflowTitle: outputCount > 1 ? `${label}任务 ${rowIndex + 1} · 第 ${index + 1} 张` : `${label}任务 ${rowIndex + 1}`,
    };
}

export function useCanvasBatchTable({ nodesRef, connectionsRef, setNodes, setConnections, setSelectedNodeIds, enqueueGenerationBatch, readOnly = false }: Options) {
    const { message } = App.useApp();
    const effectiveConfig = useEffectiveConfig();
    const isAiConfigReady = useConfigStore((state) => state.isAiConfigReady);

    const [batchGenDialog, setBatchGenDialog] = useState<{ open: boolean; pending: PendingBatchGen | null }>({ open: false, pending: null });

    const patchTable = useCallback(
        (nodeId: string, patch: Partial<CanvasBatchTableData>) => {
            setNodes((current) => current.map((node) => (node.id !== nodeId ? node : { ...node, metadata: { ...node.metadata, batchTable: { operation: "try_on", concurrency: 10, rows: [], ...node.metadata?.batchTable, ...patch } } })));
        },
        [setNodes],
    );

    const updateRow = useCallback(
        (nodeId: string, rowId: string, patch: Partial<CanvasBatchRow>) => {
        const node = nodesRef.current.find((item) => item.id === nodeId);
        if (!node?.metadata?.batchTable) return;
            patchTable(nodeId, { rows: node.metadata.batchTable.rows.map((row) => (row.id === rowId ? { ...row, ...patch } : row)) });
        },
        [nodesRef, patchTable],
    );

    const addRow = useCallback(
        (nodeId: string) => {
        const table = nodesRef.current.find((item) => item.id === nodeId)?.metadata?.batchTable;
        if (!table) return;
            patchTable(nodeId, { rows: [...table.rows, createInheritedBatchRow(table.operation, table.rows)], manualRows: true });
        },
        [nodesRef, patchTable],
    );

    const removeRow = useCallback(
        (nodeId: string, rowId: string) => {
        const table = nodesRef.current.find((item) => item.id === nodeId)?.metadata?.batchTable;
        if (!table) return;
            patchTable(nodeId, { rows: table.rows.filter((row) => row.id !== rowId), manualRows: true });
        },
        [nodesRef, patchTable],
    );

    const addReferenceColumn = useCallback(
        (nodeId: string) => {
        const table = nodesRef.current.find((item) => item.id === nodeId)?.metadata?.batchTable;
        if (!table) return;
        const columns = batchReferenceColumns(table);
        if (columns.length >= MAX_BATCH_REFERENCE_COLUMNS) return message.info("最多支持 10 组参考图");
        const nextIndex = columns.length + 1;
        patchTable(nodeId, { referenceColumns: [...columns, { id: `reference-${nanoid()}`, label: `参考图 ${nextIndex}` }] });
        },
        [message, nodesRef, patchTable],
    );

    const removeReferenceColumn = useCallback(
        (nodeId: string) => {
        const table = nodesRef.current.find((item) => item.id === nodeId)?.metadata?.batchTable;
        if (!table) return;
        const columns = batchReferenceColumns(table);
        const nextTable = removeLastBatchReferenceColumn(table);
        if (!nextTable) return message.info("至少保留 1 组参考图");
        patchTable(nodeId, nextTable);
        const removed = columns.at(-1);
        if (removed) {
            const handleId = batchReferenceHandleId(removed.id);
            setConnections((current) => current.filter((connection) => !(connection.toNodeId === nodeId && connection.toHandleId === handleId)));
        }
        },
        [message, nodesRef, patchTable, setConnections],
    );

    const addTextColumn = useCallback(
        (nodeId: string) => {
        const table = nodesRef.current.find((item) => item.id === nodeId)?.metadata?.batchTable;
        if (!table) return;
        const columns = table.textColumns || [];
            if (columns.length >= MAX_BATCH_TEXT_COLUMNS) return message.info(`最多支持 ${MAX_BATCH_TEXT_COLUMNS} 组文字`);
        patchTable(nodeId, { textColumns: [...columns, { id: `text-${nanoid()}`, label: `文字 ${columns.length + 1}` }] });
        },
        [message, nodesRef, patchTable],
    );

    const syncRowsFromConnections = useCallback(
        (nodeId: string, silent = false, force = false) => {
        const node = nodesRef.current.find((item) => item.id === nodeId);
        const table = node?.metadata?.batchTable;
        if (!node || !table) return false;
        // AI 列表模式已经根据用户要求生成了独立行；参考图连线只负责
        // 提供素材，不能在保存/连线刷新时把 N 行重置成“每张图一行”。
            if (table.aiGenerated || (table.manualRows && !force)) return false;
        const nodeById = new Map(nodesRef.current.map((item) => [item.id, item]));
            const columns = batchInputColumns(node, connectionsRef.current).map((column) =>
                column.filter((inputNodeId) => {
            const input = nodeById.get(inputNodeId);
            return input?.type === CanvasNodeType.Image && Boolean(input.metadata?.content || input.metadata?.storageKey);
                }),
            );
        if (!columns.some((column) => column.length)) {
            if (!silent) message.warning("请先把图片节点连接到批量创作表");
            return false;
        }
        const rows = createBatchRowsFromColumns(table.operation, columns, table.rows);
            const textColumns = batchTextInputColumns(node, connectionsRef.current).map((column) =>
                column.filter((inputNodeId) => {
            const input = nodeById.get(inputNodeId);
            return input?.type === CanvasNodeType.Text && Boolean(input.metadata?.content || input.metadata?.prompt);
                }),
            );
        const rowsWithText = rows.map((row, index) => ({
            ...row,
                textNodeIds: textColumns.some((column) => column.length)
                    ? textColumns.flatMap((column) => {
                const input = column.length === 1 ? column[0] : column[index];
                return input ? [input] : [];
                      })
                    : row.textNodeIds,
        }));
        if (!rows.length) {
            if (!silent) message.warning("批量换装至少需要一张人物图和一张服装图");
            return false;
        }
        const rowsChanged = JSON.stringify(table.rows) !== JSON.stringify(rowsWithText);
        if (!rowsChanged) return false;
            patchTable(nodeId, { rows: rowsWithText, manualRows: false });
        if (!silent) message.success(`已按连线创建 ${rows.length} 行任务`);
        return true;
        },
        [connectionsRef, message, nodesRef, patchTable],
    );

    const fillRowsFromConnections = useCallback(
        (nodeId: string) => {
            syncRowsFromConnections(nodeId, false, true);
        },
        [syncRowsFromConnections],
    );

    const reorderReferenceColumns = useCallback(
        (nodeId: string, fromColumnId: string, toColumnId: string) => {
        const table = nodesRef.current.find((item) => item.id === nodeId)?.metadata?.batchTable;
        if (!table) return;
        const nextTable = reorderBatchReferenceColumns(table, fromColumnId, toColumnId);
        if (nextTable !== table) patchTable(nodeId, nextTable);
        },
        [nodesRef, patchTable],
    );

    const moveReferenceCell = useCallback(
        (nodeId: string, sourceRowId: string, sourceColumnIndex: number, targetRowId: string, targetColumnIndex: number) => {
        const table = nodesRef.current.find((item) => item.id === nodeId)?.metadata?.batchTable;
        if (!table) return;
        const nextTable = moveBatchReferenceCell(table, sourceRowId, sourceColumnIndex, targetRowId, targetColumnIndex);
            if (nextTable !== table) patchTable(nodeId, { rows: nextTable.rows, manualRows: true });
        },
        [nodesRef, patchTable],
    );

    /**
     * 默认提交未完成行；指定行时允许重新生成；没有未完成行时也允许
     * 用全部行重新生成，避免表格已有结果后按钮失效。
     */
    const selectableRows = useCallback(
        (sourceNode: CanvasNodeData, requestedRowIds?: string[]) => {
            const pending = batchGenerationRows(sourceNode, nodesRef.current, requestedRowIds);
            if (pending.length || requestedRowIds) return pending;
            const allRowIds = (sourceNode.metadata?.batchTable?.rows || []).map((row) => row.id);
            return allRowIds.length ? batchGenerationRows(sourceNode, nodesRef.current, allRowIds) : pending;
        },
        [nodesRef],
    );

    const executeBatchGeneration = useCallback(
        (pending: PendingBatchGen, settings: BatchGenerationSettings) => {
        const { nodeId, rows, concurrency } = pending;
        const sourceNode = nodesRef.current.find((item) => item.id === nodeId);
        const table = sourceNode?.metadata?.batchTable;
        if (!sourceNode || !table) return;

        // 电商表在提交前必须复核锁定/只读与输入快照，避免用过期确认产生新的收费任务。
        if (isCommerceNode(sourceNode) && (readOnly || sourceNode.metadata?.locked || pending.commerceSnapshot !== commerceSnapshot(sourceNode, nodesRef.current, connectionsRef.current))) {
            message.warning("配置或输入素材已变化，请重新确认生成");
            return;
        }

            const selectableRowIds = new Set(selectableRows(sourceNode, pending.requestedRowIds).map((row) => row.id));
        if (JSON.stringify(table) !== pending.tableSnapshot || !rows.every((row) => selectableRowIds.has(row.id))) {
            message.warning("表格、素材或任务状态已变化，请重新打开生成设置后提交");
            return;
        }

        const mergedConfig = { ...effectiveConfig, ...settings };
        if (isCommerceNode(sourceNode)) {
            // 逐屏按实际引用数量校验模型能力，而不是沿用全局配置的一次性判断。
            for (const row of rows) {
                const error = modelCompatibilityError(mergedConfig, mergedConfig.imageModel || mergedConfig.model, { capability: "image", input: { imageCount: row.inputNodeIds.length, textCount: 0, videoCount: 0, audioCount: 0, characterCount: 0 } });
                if (error) { message.error(error); return; }
            }
        }
        if (!isAiConfigReady(mergedConfig, mergedConfig.imageModel || mergedConfig.model)) {
            message.error("所选图片模型尚未配置，未提交生成任务");
            return;
        }

            const outputCount = getGenerationCount(settings.count);
            const cardSize = nodeSizeFromRatio(mergedConfig.size, BATCH_OUTPUT_CARD.width, BATCH_OUTPUT_CARD.height) || BATCH_OUTPUT_CARD;
        const nextNodes = [...nodesRef.current];
        let nextConnections = [...connectionsRef.current];
            const outputsByRowId = new Map<string, string[]>();
        const targets: Array<{ rowId: string; nodeId: string }> = [];
            rows.forEach((row) => {
                const rowIndex = Math.max(
                    0,
                    table.rows.findIndex((item) => item.id === row.id),
                );
            const prompt = batchPromptForRow(table, row).trim();
                const composerContent = [
                prompt,
                    ...(row.textNodeIds || []).map((textNodeId) => {
                    const textNode = nodesRef.current.find((node) => node.id === textNodeId);
                    return textNode?.metadata?.content || textNode?.metadata?.prompt || "";
                    }),
                ]
                    .filter(Boolean)
                    .join("\n\n");
                const commerce = isCommerceNode(sourceNode);
                // 电商表每次运行都产出新结果卡，旧成品必须保留；普通表沿用「复用空占位卡」的行为。
                const existingIds = commerce ? [] : batchRowOutputNodeIds(row).filter((outputNodeId) => nextNodes.some((node) => node.id === outputNodeId && node.type === CanvasNodeType.Image));
                const outputIds: string[] = [];
                for (let index = 0; index < outputCount; index += 1) {
                    const existingIndex = existingIds[index] ? nextNodes.findIndex((node) => node.id === existingIds[index]) : -1;
                    const labels = batchOutputLabels(table.operation, rowIndex, index, outputCount);
                    const metadata = {
                        ...(existingIndex >= 0 ? resetGenerationTaskMetadata(nextNodes[existingIndex].metadata) : {}),
                        prompt,
                        composerContent,
                model: buildGenerationConfig(mergedConfig, undefined, "image").model,
                // 逐屏规格由该屏的尺寸方案与所选模型能力共同决定。
                size: isCommerceNode(sourceNode)
                    ? commerceScreenImageSize(modelCapabilityConfigFor(mergedConfig, mergedConfig.imageModel || mergedConfig.model).image, mergedConfig.size, sourceNode, sourceNode.metadata?.commerceWorkflow?.screens.find((screen) => screen.id === row.id), nodesRef.current)
                    : mergedConfig.size,
                quality: mergedConfig.quality,
                transparentBackground: mergedConfig.transparentBackground,
                count: 1,
                generationMode: "image" as const,
                generationType: "edit" as const,
                workflowKind: "final" as const,
                        workflowTitle: labels.workflowTitle,
                status: "idle" as const,
                batchSourceNodeId: nodeId,
                batchRowId: row.id,
                batchOperation: table.operation,
                batchInputNodeIds: row.inputNodeIds,
                cameraControl: settings.cameraControl,
            };
                    const position = {
                        x: sourceNode.position.x + sourceNode.width + BATCH_OUTPUT_OFFSET_X + index * (cardSize.width + BATCH_OUTPUT_GAP_X) + cardSize.width / 2,
                        y: sourceNode.position.y + rowIndex * (cardSize.height + BATCH_OUTPUT_GAP_Y) + cardSize.height / 2,
                    };
                    const topLeft = { x: position.x - cardSize.width / 2, y: position.y - cardSize.height / 2 };
                    const output =
                        existingIndex >= 0
                            ? { ...nextNodes[existingIndex], metadata, position: topLeft, width: cardSize.width, height: cardSize.height }
                            : { ...createCanvasNode(CanvasNodeType.Image, position, metadata), position: topLeft, width: cardSize.width, height: cardSize.height };
                    output.title = commerce
                        ? sourceNode.metadata?.commerceWorkflow?.screens.find((screen) => screen.id === row.id)?.title || labels.title
                        : labels.title;
                    // 电商任务按行写入生成合同，确保入队、恢复调度与结果回填指向同一目标。
                    if (commerce) output.metadata = synchronizeGenerationSpec({ ...output, metadata: { ...metadata, generationSpec: undefined } }, metadata).metadata;
            if (existingIndex >= 0) nextNodes[existingIndex] = output;
            else nextNodes.push(output);
            nextConnections = nextConnections.filter((connection) => connection.toNodeId !== output.id);
            row.inputNodeIds.filter(Boolean).forEach((inputNodeId) => nextConnections.push({ id: nanoid(), fromNodeId: inputNodeId, toNodeId: output.id }));
            nextConnections.push({ id: nanoid(), fromNodeId: sourceNode.id, toNodeId: output.id, relation: "batch-output", storyboardRowId: row.id });
                    outputIds.push(output.id);
            targets.push({ rowId: row.id, nodeId: output.id });
                }
                // 数量下调时只清理尚未产出内容的旧占位节点，保留已完成的历史图片。
                existingIds.slice(outputCount).forEach((staleNodeId) => {
                    const staleIndex = nextNodes.findIndex((node) => node.id === staleNodeId);
                    if (staleIndex < 0) return;
                    const staleNode = nextNodes[staleIndex];
                    if (staleNode.metadata?.content || staleNode.metadata?.storageKey) return;
                    nextNodes.splice(staleIndex, 1);
                    nextConnections = nextConnections.filter((connection) => connection.toNodeId !== staleNodeId && connection.fromNodeId !== staleNodeId);
                });
                outputsByRowId.set(row.id, outputIds);
        });
        const sourceIndex = nextNodes.findIndex((node) => node.id === sourceNode.id);
            nextNodes[sourceIndex] = {
                ...sourceNode,
                metadata: {
                    ...sourceNode.metadata,
                    batchTable: {
                        ...table,
                        rows: table.rows.map((row) => {
                            const outputIds = outputsByRowId.get(row.id);
                            return outputIds?.length ? { ...row, outputNodeId: outputIds[0], outputNodeIds: outputIds } : row;
                        }),
                    },
                },
            };
        nodesRef.current = nextNodes;
        connectionsRef.current = nextConnections;
        setNodes(nextNodes);
        setConnections(nextConnections);
        setSelectedNodeIds(new Set(targets.map((target) => target.nodeId)));
        if (enqueueGenerationBatch(nodeId, "batch_image", targets, { concurrency })) {
            message.success(`${targets.length} 个任务已加入并发队列`);
            return true;
        }
        return false;
        },
        [connectionsRef, effectiveConfig, enqueueGenerationBatch, isAiConfigReady, message, nodesRef, readOnly, selectableRows, setConnections, setNodes, setSelectedNodeIds],
    );

    const generateRows = useCallback(
        (nodeId: string, requestedRowIds?: string[]) => {
        const sourceNode = nodesRef.current.find((item) => item.id === nodeId);
        const table = sourceNode?.metadata?.batchTable;
        if (!sourceNode || !table) return;
        if (isCommerceNode(sourceNode) && (readOnly || sourceNode.metadata?.locked)) return;
        const imageConfig = isCommerceNode(sourceNode) ? { ...effectiveConfig, model: sourceNode.metadata?.model || effectiveConfig.imageModel, imageModel: sourceNode.metadata?.model || effectiveConfig.imageModel } : effectiveConfig;
        const imageModel = imageConfig.imageModel || imageConfig.model;
        if (!isAiConfigReady(imageConfig, imageModel)) {
            navigateToSettings({ continueCreation: true });
            return;
        }
            const rows = selectableRows(sourceNode, requestedRowIds);
        if (!rows.length) return message.info("没有可提交的未完成任务，请检查参考图和提示词");

        setBatchGenDialog({ open: true, pending: { nodeId, rows, concurrency: table.concurrency, tableSnapshot: JSON.stringify(table), requestedRowIds,
            commerceSnapshot: isCommerceNode(sourceNode) ? commerceSnapshot(sourceNode, nodesRef.current, connectionsRef.current) : undefined } });
        },
        [effectiveConfig, isAiConfigReady, message, nodesRef, readOnly, selectableRows],
    );

    const closeBatchGenDialog = useCallback(() => {
        setBatchGenDialog({ open: false, pending: null });
    }, []);

    // 仅供已完成费用确认的 commerce 流程使用；不能拿它绕过其他入口的收费确认。
    const generateConfirmedCommerceRows = useCallback(
        (nodeId: string, requestedRowIds?: string[]) => {
            const source = nodesRef.current.find((node) => node.id === nodeId);
            const table = source?.metadata?.batchTable;
            if (!source || !table || !isCommerceNode(source) || readOnly || source.metadata?.locked) return false;
            const rows = selectableRows(source, requestedRowIds);
            if (!rows.length) return false;
            const model = source.metadata?.model || effectiveConfig.imageModel || effectiveConfig.model;
            const executed = executeBatchGeneration(
                { nodeId, rows, requestedRowIds, concurrency: table.concurrency, tableSnapshot: JSON.stringify(table), commerceSnapshot: commerceSnapshot(source, nodesRef.current, connectionsRef.current) },
                { model, imageModel: model, size: source.metadata?.size || effectiveConfig.size, quality: source.metadata?.quality || effectiveConfig.quality, count: "1", transparentBackground: effectiveConfig.transparentBackground },
            );
            return executed === true;
        },
        [connectionsRef, effectiveConfig, executeBatchGeneration, nodesRef, readOnly, selectableRows],
    );

    const confirmBatchGenDialog = useCallback(
        (settings: BatchGenerationSettings) => {
        if (batchGenDialog.pending) {
            executeBatchGeneration(batchGenDialog.pending, settings);
        }
        setBatchGenDialog({ open: false, pending: null });
        },
        [batchGenDialog.pending, executeBatchGeneration],
    );

    const dialogConfig = useMemo(() => {
        const source = nodesRef.current.find((node) => node.id === batchGenDialog.pending?.nodeId);
        if (!source || !isCommerceNode(source)) return effectiveConfig;
        const model = source.metadata?.model || effectiveConfig.imageModel;
        return { ...effectiveConfig, model, imageModel: model, size: source.metadata?.size || effectiveConfig.size, quality: source.metadata?.quality || effectiveConfig.quality };
    }, [effectiveConfig, batchGenDialog.pending, nodesRef]);

    return {
        addReferenceColumn,
        addTextColumn,
        addRow,
        fillRowsFromConnections,
        generateRows,
        generateConfirmedCommerceRows,
        moveReferenceCell,
        patchTable,
        removeReferenceColumn,
        removeRow,
        reorderReferenceColumns,
        syncRowsFromConnections,
        updateRow,
        batchGenDialogOpen: batchGenDialog.open,
        batchGenDialogRowCount: batchGenDialog.pending?.rows.length ?? 0,
        batchGenDialogConcurrency: batchGenDialog.pending?.concurrency ?? 1,
        batchGenDialogConfig: dialogConfig,
        closeBatchGenDialog,
        confirmBatchGenDialog,
    };
}
