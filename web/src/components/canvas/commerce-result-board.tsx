import { Button } from "antd";
import { ArrowDown, ArrowUp, Check, ImageIcon, LoaderCircle, Pencil, Trash2 } from "lucide-react";
import { CommercePreviewImage } from "./commerce-preview-image";
import { useSyncExternalStore } from "react";
import { subscribeTaskImagePreviews, taskImagePreview } from "@/services/task-image-preview-store";
import { useUserStore } from "@/stores/use-user-store";
import { commerceResultLayout } from "@/lib/canvas/commerce-result-layout";
import { commerceScreenHasNoText } from "@/lib/canvas/commerce-workflow";
import { batchRunningRowIds } from "@/lib/canvas/canvas-batch-table";
import type { CommerceImagePrice } from "@/lib/canvas/commerce-pricing";
import { CanvasNodeType, type CanvasGenerationBatch, type CanvasNodeData } from "@/types/canvas";

type Props = {
    imagePrices?: CommerceImagePrice[];
    node: CanvasNodeData; nodes: CanvasNodeData[]; batch?: CanvasGenerationBatch; disabled: boolean; generationDisabled: boolean; planning: boolean;
    onEdit: (id: string) => void; onMove: (index: number, direction: number) => void; onRemove: (id: string) => void;
    onGenerate: (id: string) => void; onFocus: (id: string) => void; onRetry: (batchId: string, itemId: string) => void;
};

// A pre-generation storyboard, not another configuration form. Uses existing surface/status tokens.
export function CommerceResultBoard({ node, nodes, batch, imagePrices, disabled, generationDisabled, planning, onEdit, onMove, onRemove, onGenerate, onFocus, onRetry }: Props) {
    const scope = useUserStore((state) => state.user?.id);
    const previewIds = useSyncExternalStore(subscribeTaskImagePreviews, () => nodes.filter((item) => taskImagePreview(scope, item.metadata?.taskId)).map((item) => item.id).join(","), () => "");
    const previews = new Set(previewIds.split(","));
    const data = node.metadata!.commerceWorkflow!;
    const screens = data.screens;
    const replica = node.type === CanvasNodeType.ProductReplica;
    const layout = commerceResultLayout(node);
    const runningRows = batchRunningRowIds(node, nodes);
    const batches = node.metadata?.generationBatches || (batch ? [batch] : []);
    return <div className="commerce-result-grid" role="list" aria-label="预生成卡片组" style={{ gridTemplateColumns: `repeat(${layout.columns}, minmax(0, 1fr))` }}>
        {screens.map((screen, index) => {
            const outputId = node.metadata?.batchTable?.rows.find((row) => row.id === screen.id)?.outputNodeId;
            const output = nodes.find((item) => item.id === outputId);
            const rowBatch = batches.findLast((candidate) => candidate.items.some((item) => item.rowId === screen.id && item.nodeId === outputId));
            const item = rowBatch?.items.find((item) => item.rowId === screen.id && item.nodeId === outputId);
            const success = output?.metadata?.status === "success" && Boolean(output.metadata.storageKey || output.metadata.content);
            const running = runningRows.has(screen.id);
            const filled = Boolean(screen.promptOverride?.trim() || screen.prompt.trim());
            const copyPreview = !replica && !screen.promptOverride?.trim() && filled && commerceScreenHasNoText(data, screen)
                ? "纯图无字" : screen.copy.trim() || (screen.promptOverride?.trim() ? "已填写完整提示词" : replica ? "待补充画面文案" : "待填写画面描述");
            const status = success ? "已完成" : item ? ({ waiting: "等待提交", submitting: "正在提交", queued: "排队中", running: "生成中", failed: "生成失败", cancelled: "已取消", succeeded: "结果待同步" }[item.status]) : running ? "生成中" : planning ? "策划中" : filled ? "待确认" : "待编辑";
            return <section key={screen.id} role="listitem" className="commerce-plan-card" data-commerce-screen={screen.id} data-state={success ? "success" : running || planning ? "running" : "pending"}>
                <div className="commerce-plan-preview" style={{ height: layout.previewHeight }}>
                    {output && (success || previews.has(output.id)) ? <button type="button" className="commerce-plan-image" aria-label={`查看第 ${index + 1} 屏结果`} onClick={() => onFocus(output.id)}>
                        <CommercePreviewImage node={output} alt={screen.title} />
                        {!success && <span className="commerce-plan-wait">作品已生成 · {output.metadata?.status === "error" ? "保存未完成" : "正在保存"}</span>}
                    </button> : <button type="button" className="commerce-plan-placeholder" aria-label={`编辑第 ${index + 1} 屏预生成卡片`} onClick={() => onEdit(screen.id)}>
                        <span className="commerce-plan-scene">{screen.sceneType || screen.title || `详情 ${index + 1}`}</span>
                        {planning || running ? <LoaderCircle size={28} className="commerce-progress-icon" /> : <ImageIcon size={28} className="opacity-35" />}
                        <strong>{copyPreview}</strong>
                        <span className="commerce-plan-description" title={screen.prompt || screen.promptOverride || ""}>{screen.prompt || screen.promptOverride || "点击编辑本屏文案、画面与参考图"}</span>
                        <span className="commerce-plan-wait">{planning || running ? `${status} · 结果将在这里显示` : "待确认 · 编辑后再生成，不自动扣费"}</span>
                    </button>}
                    <div className="commerce-card-tools">
                        <button type="button" aria-label={`编辑第 ${index + 1} 屏`} title="编辑本屏文案与参数" onClick={() => onEdit(screen.id)}><Pencil size={16} /></button>
                        <button type="button" disabled={disabled} aria-label={`移除第 ${index + 1} 屏`} title="移除此屏，保留已经生成的独立图片" onClick={() => onRemove(screen.id)}><Trash2 size={16} /></button>
                    </div>
                </div>
                <div className="commerce-plan-caption"><strong>第 {index + 1} 屏 · {screen.title}</strong><span className="commerce-plan-status">{success && <Check size={13} />}{status}</span></div>
                <div className="commerce-plan-actions">
                    {imagePrices && <span className="commerce-screen-price" aria-label={`第 ${index + 1} 屏预估费用`}>预计 {imagePrices.find((image) => image.id === screen.id)?.description || "暂无标价"}</span>}
                    <button type="button" aria-label={`第 ${index + 1} 屏前移`} title="前移" disabled={disabled || index === 0} onClick={() => onMove(index, -1)}><ArrowUp size={14} /></button>
                    <button type="button" aria-label={`第 ${index + 1} 屏后移`} title="后移" disabled={disabled || index === screens.length - 1} onClick={() => onMove(index, 1)}><ArrowDown size={14} /></button>
                    {rowBatch && item?.status === "failed" && <Button size="small" disabled={generationDisabled || running} onClick={() => onRetry(rowBatch.id, item.id)}>核对并重试</Button>}
                    <Button size="small" disabled={generationDisabled || running || !filled && !screen.templateNodeId} onClick={() => onGenerate(screen.id)}>{running ? status : output ? "重新生成" : "生成此屏"}</Button>
                </div>
            </section>;
        })}
        {!screens.length && <p>本组已无策划卡片。请从配置节点重新创建一组。</p>}
    </div>;
}
