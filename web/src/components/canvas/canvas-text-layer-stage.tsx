import { buildCanvasTextLayerStyle, canvasTextLayerStageFrame, type CanvasTextLayer } from "@/lib/canvas/canvas-text-layer";

type CanvasTextLayerStageProps = {
    layers: CanvasTextLayer[];
    /** 节点内容框尺寸，等于节点宽高（画布世界单位）。 */
    containerWidth: number;
    containerHeight: number;
    /** 底图原始像素尺寸；缺省时按内容框 1:1 处理。 */
    imageWidth: number;
    imageHeight: number;
    /** 节点被拉伸成自由比例时底图是 object-fill，舞台跟着铺满内容框。 */
    fill?: boolean;
};

/**
 * 画布节点上的文字图层渲染层：把原图像素坐标的图层等比映射到节点内容框。
 *
 * 这一层只负责绘制，不拦截指针事件——底图双击、节点拖拽、连接锚点仍然归画布；
 * 拖动/缩放/样式编辑放在独立的文字图层面板里。
 */
export function CanvasTextLayerStage({ layers, containerWidth, containerHeight, imageWidth, imageHeight, fill = false }: CanvasTextLayerStageProps) {
    const visibleLayers = layers.filter((layer) => layer.visible && layer.text.trim().length > 0);
    if (!visibleLayers.length) return null;

    const stageImageWidth = imageWidth > 0 ? imageWidth : containerWidth;
    const stageImageHeight = imageHeight > 0 ? imageHeight : containerHeight;
    const frame = canvasTextLayerStageFrame({ containerWidth, containerHeight, imageWidth: stageImageWidth, imageHeight: stageImageHeight, fill });
    const scale = frame.width / Math.max(1, stageImageWidth);

    return (
        <div className="pointer-events-none absolute z-10 select-none" style={{ left: frame.left, top: frame.top, width: frame.width, height: frame.height }} aria-hidden>
            <div className="relative origin-top-left" style={{ width: stageImageWidth, height: stageImageHeight, transform: `scale(${scale})` }}>
                {visibleLayers.map((layer) => (
                    <div key={layer.id} style={buildCanvasTextLayerStyle(layer)}>
                        {layer.text}
                    </div>
                ))}
            </div>
        </div>
    );
}
