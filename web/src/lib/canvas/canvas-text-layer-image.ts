/**
 * 「文字图层」底图来源解析（兼容导出）。
 *
 * 实现已抽到中性的 `canvas-node-image-source.ts`：弹窗、画布节点与文字图层共用同一套
 * storageKey → content → 字节读取的优先级。这里保留原有导出名，避免破坏已有引用与单测。
 */
export { canvasNodeImageCandidates as canvasTextLayerImageCandidates, canvasNodeImageSource as canvasTextLayerImageSource } from "./canvas-node-image-source";

export type { CanvasNodeImageLike as CanvasTextLayerNodeLike, CanvasNodeImageSource as CanvasTextLayerImageSource } from "./canvas-node-image-source";
