import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";

export const MEDIA_NODE_MIN_SIZE = { width: 300, height: 220 } as const;
export const VIDEO_NODE_MAX_SIZE = { width: 480, height: 480 } as const;

export function fitNodeSize(width: number, height: number, maxWidth = 640, maxHeight = 640, minWidth = MEDIA_NODE_MIN_SIZE.width, minHeight = MEDIA_NODE_MIN_SIZE.height) {
    const w = Math.max(1, width);
    const h = Math.max(1, height);
    // 媒体节点既要保留原始比例，也要给生成状态、操作按钮留下稳定的可读空间。
    const preferredScale = Math.min(1, maxWidth / w, maxHeight / h);
    const minimumScale = Math.max(minWidth / w, minHeight / h);
    const scale = Math.max(preferredScale, minimumScale);
    return { width: w * scale, height: h * scale };
}

export function nodeSizeFromRatio(size: string, baseWidth: number, baseHeight: number) {
    const raw = String(size || "").trim();
    if (!raw || raw.toLowerCase() === "auto") return null;
    // 支持 16:9 / 1024x576 / 16:9-2k
    const match = raw.match(/^(\d+(?:\.\d+)?)(?:x|:)(\d+(?:\.\d+)?)/i);
    if (!match) return null;
    const width = Number(match[1]);
    const height = Number(match[2]);
    if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return null;
    const ratio = width / Math.max(1, height);
    if (ratio < 0.25 || ratio > 4) return { width: baseWidth, height: baseHeight };
    const candidateSize = ratio >= baseWidth / baseHeight ? { width: baseWidth, height: baseWidth / ratio } : { width: baseHeight * ratio, height: baseHeight };
    return fitNodeSize(candidateSize.width, candidateSize.height, baseWidth, baseHeight);
}

export function ensureMediaNodeMinimumSize(node: CanvasNodeData) {
    if (node.type !== CanvasNodeType.Image && node.type !== CanvasNodeType.Video) return node;
    if (node.width >= MEDIA_NODE_MIN_SIZE.width && node.height >= MEDIA_NODE_MIN_SIZE.height) return node;
    const scale = Math.max(1, MEDIA_NODE_MIN_SIZE.width / Math.max(1, node.width), MEDIA_NODE_MIN_SIZE.height / Math.max(1, node.height));
    const width = node.width * scale;
    const height = node.height * scale;
    return {
        ...node,
        position: {
            x: node.position.x + node.width / 2 - width / 2,
            y: node.position.y + node.height / 2 - height / 2,
        },
        width,
        height,
    };
}
