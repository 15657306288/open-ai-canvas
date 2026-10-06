import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";
import { createCanvasNode } from "./canvas-project-domain";
import { FRAME_HEADER_HEIGHT, FRAME_PADDING } from "./canvas-frame";
import { isCanvasNodeGenerating } from "./canvas-node-task-state";

/** Resolve screen order, not batch-row order; never silently omit a missing result. */
export function commerceCompletedImages(source: CanvasNodeData, nodes: CanvasNodeData[]) {
    const workflow = source.metadata?.commerceWorkflow;
    if (workflow?.role !== "result" || !workflow.screens.length || workflow.screens.length > 30) throw new Error("请选择已完成的 1 到 30 屏结果组");
    const byId = new Map(nodes.map((node) => [node.id, node]));
    return workflow.screens.map((screen) => {
        const outputId = source.metadata?.batchTable?.rows.find((row) => row.id === screen.id)?.outputNodeId;
        const output = outputId ? byId.get(outputId) : undefined;
        if (!output || output.type !== CanvasNodeType.Image || output.metadata?.status !== "success" || isCanvasNodeGenerating(output)
            || output.metadata.fileUpload || !(output.metadata.storageKey || output.metadata.content)) throw new Error("请先完成所有屏的图片生成");
        return output;
    });
}

/** A native frame containing resource references, not new pixels or task-owned copies. */
export function createCommerceImageAssembly(source: CanvasNodeData, nodes: CanvasNodeData[]) {
    const outputs = commerceCompletedImages(source, nodes);
    const width = 420;
    const heights = outputs.map((output) => {
        const natural = [output.metadata?.naturalWidth, output.metadata?.naturalHeight];
        const dimensions = natural.every((value) => Number.isFinite(value) && value! > 0) ? natural : [output.width, output.height];
        if (!dimensions.every((value) => Number.isFinite(value) && value! > 0)) throw new Error("图片比例未知，请等待图片加载后再拼合");
        const height = width * dimensions[1]! / dimensions[0]!;
        if (!Number.isFinite(height) || height <= 0) throw new Error("图片比例无效，无法拼合");
        return height;
    });
    const frameWidth = width + FRAME_PADDING * 2;
    const frameHeight = Math.max(240, heights.reduce((sum, height) => sum + height, 0) + FRAME_HEADER_HEIGHT + FRAME_PADDING * 2);
    if (frameHeight > 32760) throw new Error("拼合组过长，请减少屏数后重试");
    const x = source.position.x + source.width + 600;
    const y = Math.max(source.position.y, ...nodes.filter((node) => node.position.x < x + frameWidth && node.position.x + node.width > x)
        .map((node) => node.position.y + node.height + 80));
    const frame = createCanvasNode(CanvasNodeType.Frame, { x: 0, y: 0 }, {
        imageAssembly: "vertical", workflowKind: "reference_set", workflowTitle: "图片引用拼合组",
        frame: { collapsed: false, expandedWidth: frameWidth, expandedHeight: frameHeight },
    });
    frame.title = `${source.metadata?.commerceWorkflow?.productName || "详情页"} · 图片拼合组`;
    Object.assign(frame, { position: { x, y }, width: frameWidth, height: frameHeight });
    let top = y + FRAME_HEADER_HEIGHT + FRAME_PADDING;
    const images = outputs.map((output, index) => {
        const original = output.metadata!;
        const image = createCanvasNode(CanvasNodeType.Image, { x: 0, y: 0 }, {
            // Persist the existing resource identity so saving does not upload a new image.
            storageKey: original.storageKey,
            content: original.storageKey?.startsWith("resource:") ? original.storageKey : original.content || original.storageKey,
            previewContent: original.previewContent, naturalWidth: original.naturalWidth, naturalHeight: original.naturalHeight,
            mimeType: original.mimeType, bytes: original.bytes, assetId: original.assetId,
            producedModel: original.producedModel, status: "success", manualSize: true,
            generationResultPlacement: "replace-node", referenceSetId: frame.id,
        });
        image.title = source.metadata!.commerceWorkflow!.screens[index].title || output.title;
        Object.assign(image, { parentId: frame.id, position: { x: x + FRAME_PADDING, y: top }, width, height: heights[index] });
        top += heights[index];
        return image;
    });
    frame.metadata!.referenceAssetNodeIds = images.map((image) => image.id);
    return { frame, images };
}
