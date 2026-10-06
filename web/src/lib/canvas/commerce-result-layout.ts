import type { CanvasNodeData } from "@/types/canvas";

export function commercePreviewRatio(size?: string) {
    const match = size?.match(/^(\d+(?:\.\d+)?)[:x](\d+(?:\.\d+)?)$/);
    return match && Number(match[1]) > 0 && Number(match[2]) > 0 ? Number(match[1]) / Number(match[2]) : 3 / 4;
}

/** Preview slots exist before generation; canvas resizing changes columns, never the paid image size. */
export function commerceResultLayout(node: Pick<CanvasNodeData, "width" | "metadata">, columns?: number) {
    const count = node.metadata?.commerceWorkflow?.screens.length || 1;
    const cols = columns === undefined ? Math.max(1, Math.min(6, Math.floor((node.width - 24) / 280))) : Math.max(1, Math.min(6, Math.floor(columns)));
    const width = columns === undefined ? node.width : Math.max(520, cols * 300 + (cols - 1) * 16 + 40);
    const cardWidth = (width - 40 - (cols - 1) * 16) / cols;
    const ratio = commercePreviewRatio(node.metadata?.size);
    const previewHeight = Math.min(480, Math.max(180, cardWidth / ratio));
    const rows = Math.ceil(count / cols);
    return { columns: cols, rows, width, previewHeight, height: Math.max(600, Math.ceil(rows * (previewHeight + 108) + (rows - 1) * 16 + 184)) };
}
