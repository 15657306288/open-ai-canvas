import { getImageBlob, resolveImageUrl } from "@/services/image-storage";
import type { CanvasNodeData } from "@/types/canvas";

/** Sequential decoding caps bitmap memory; refuse oversized long images rather than silently truncating. */
export async function mergeCommerceImages(nodes: CanvasNodeData[], signal: AbortSignal) {
    if (!nodes.length || nodes.length > 30) throw new Error("一次合并 1 到 30 屏");
    const sources: Array<{ blob: Blob; width: number; height: number }> = [];
    let totalBytes = 0;
    for (const node of nodes) {
        signal.throwIfAborted();
        let blob = node.metadata?.storageKey ? await getImageBlob(node.metadata.storageKey) : null;
        if (!blob) {
            const url = await resolveImageUrl(node.metadata?.storageKey, node.metadata?.content || "");
            const response = await fetch(url, { signal, credentials: url.startsWith("/") ? "include" : "same-origin" });
            if (!response.ok) throw new Error(`读取图片失败（${response.status}）`);
            blob = await response.blob();
        }
        if (blob.size > 32 * 1024 * 1024) throw new Error("单屏图片过大，请先缩小后合并");
        totalBytes += blob.size;
        if (totalBytes > 128 * 1024 * 1024) throw new Error("图片总量超过 128 MiB，请分组合并");
        signal.throwIfAborted();
        const bitmap = await createImageBitmap(blob);
        const { width, height } = bitmap; bitmap.close();
        if (!width || !height || width * height > 32_000_000) throw new Error("图片尺寸过大，无法安全合并");
        sources.push({ blob, width, height });
    }
    const width = Math.min(2048, ...sources.map((source) => source.width));
    const heights = sources.map((source) => Math.round(source.height * width / source.width));
    const height = heights.reduce((sum, value) => sum + value, 0);
    if (height > 32760 || width * height > 32_000_000) throw new Error("长图超过安全尺寸，请减少屏数后合并");
    const canvas = document.createElement("canvas"); canvas.width = width; canvas.height = height;
    try {
        const context = canvas.getContext("2d"); if (!context) throw new Error("浏览器无法创建长图");
        let y = 0;
        for (let index = 0; index < sources.length; index++) {
            signal.throwIfAborted();
            const bitmap = await createImageBitmap(sources[index].blob);
            try { context.drawImage(bitmap, 0, y, width, heights[index]); } finally { bitmap.close(); }
            y += heights[index];
        }
        return await new Promise<Blob>((resolve, reject) => canvas.toBlob((blob) => blob ? resolve(blob) : reject(new Error("长图编码失败")), "image/png"));
    } finally { canvas.width = 0; canvas.height = 0; }
}
