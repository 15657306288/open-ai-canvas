import { CanvasNodeAnnotationDialog, type CanvasImageAnnotationPayload } from "@/components/canvas/canvas-node-annotation-dialog";
import { CanvasNodeCropDialog, type CanvasImageCropRect } from "@/components/canvas/canvas-node-crop-dialog";
import { CanvasNodeMaskEditDialog, type CanvasImageMaskEditPayload } from "@/components/canvas/canvas-node-mask-edit-dialog";
import { CanvasNodeUpscaleDialog, type CanvasImageUpscaleParams } from "@/components/canvas/canvas-node-upscale-dialog";
import { CanvasNodeImageEditDialog, type CanvasImageEditPayload } from "@/components/canvas/canvas-node-image-edit-dialog";
import { CanvasNodeLayerDecompositionDialog, type CanvasImageLayerDecompositionPayload } from "@/components/canvas/canvas-node-layer-decomposition-dialog";
import { CanvasNodeTextEditDialog, type CanvasImageTextEditPayload } from "@/components/canvas/canvas-node-text-edit-dialog";
import { CanvasTextLayerEditor } from "@/components/canvas/canvas-text-layer-editor";
import { canvasNodeImageSource, type CanvasNodeImageSource } from "@/lib/canvas/canvas-node-image-source";
import { readCanvasTextLayers, type CanvasTextLayer } from "@/lib/canvas/canvas-text-layer";
import { canvasImageAnnotationReferenceError } from "@/lib/canvas/canvas-project-generation";
import type { CanvasNodeData } from "@/types/canvas";
import type { AiConfig } from "@/stores/use-config-store";

type CanvasProjectMediaDialogsProps = {
    cropNode: CanvasNodeData | null;
    annotationNode: CanvasNodeData | null;
    annotationEditNode: CanvasNodeData | null;
    maskEditNode: CanvasNodeData | null;
    imageEditNode: CanvasNodeData | null;
    layerDecompositionNode: CanvasNodeData | null;
    textEditNode: CanvasNodeData | null;
    textLayerNode: CanvasNodeData | null;
    imageEditPreset?: "remove-background" | null;
    upscaleNode: CanvasNodeData | null;
    onCloseCrop: () => void;
    onCloseAnnotation: () => void;
    onCloseAnnotationEdit: () => void;
    onCloseMaskEdit: () => void;
    onCloseUpscale: () => void;
    onCloseImageEdit: () => void;
    onCloseLayerDecomposition: () => void;
    onCloseTextEdit: () => void;
    onCloseTextLayers: () => void;
    onCrop: (node: CanvasNodeData, crop: CanvasImageCropRect) => void;
    onAnnotate: (node: CanvasNodeData, dataUrl: string) => void;
    onAnnotationEdit: (node: CanvasNodeData, payload: CanvasImageAnnotationPayload) => void;
    onMaskEdit: (node: CanvasNodeData, payload: CanvasImageMaskEditPayload) => void;
    onUpscale: (node: CanvasNodeData, params: CanvasImageUpscaleParams) => void;
    onImageOperation: (node: CanvasNodeData, payload: CanvasImageEditPayload) => void;
    onLayerDecomposition: (node: CanvasNodeData, payload: CanvasImageLayerDecompositionPayload) => void;
    onDetectText: () => Promise<import("@/components/canvas/canvas-node-text-edit-dialog").CanvasImageTextLine[]>;
    onTextEdit: (node: CanvasNodeData, payload: CanvasImageTextEditPayload) => void;
    onDetectTextLayers: () => Promise<import("@/components/canvas/canvas-node-text-edit-dialog").CanvasImageTextLine[]>;
    onSaveTextLayers: (node: CanvasNodeData, layers: CanvasTextLayer[], imageSize: { width: number; height: number }) => void;
    config: AiConfig;
};

export function CanvasProjectMediaDialogs({
    cropNode,
    annotationNode,
    annotationEditNode,
    maskEditNode,
    imageEditNode,
    layerDecompositionNode,
    textEditNode,
    textLayerNode,
    imageEditPreset,
    upscaleNode,
    onCloseCrop,
    onCloseAnnotation,
    onCloseAnnotationEdit,
    onCloseMaskEdit,
    onCloseUpscale,
    onCloseImageEdit,
    onCloseLayerDecomposition,
    onCloseTextEdit,
    onCloseTextLayers,
    onCrop,
    onAnnotate,
    onAnnotationEdit,
    onMaskEdit,
    onUpscale,
    onImageOperation,
    onLayerDecomposition,
    onDetectText,
    onTextEdit,
    onDetectTextLayers,
    onSaveTextLayers,
    config,
}: CanvasProjectMediaDialogsProps) {
    // 「标记」是一个入口两个动作：annotationNode 是主路径，annotationEditNode 为历史入口兼容。
    const markNode = annotationNode || annotationEditNode;
    const markGenerateDisabledReason = markNode ? canvasImageAnnotationReferenceError(config, markNode) : "";
    // 所有图片工具弹窗用同一套底图解析（storageKey 优先，content 兜底），只有 storageKey 的节点也能打开。
    const cropImage = dialogImage(cropNode);
    const markImage = dialogImage(markNode);
    const maskEditImage = dialogImage(maskEditNode);
    const upscaleImage = dialogImage(upscaleNode);
    const imageEditImage = dialogImage(imageEditNode);
    const layerDecompositionImage = dialogImage(layerDecompositionNode);
    const textEditImage = dialogImage(textEditNode);
    return (
        <>
            {cropNode && cropImage ? <CanvasNodeCropDialog image={cropImage} open onClose={onCloseCrop} onConfirm={(crop) => onCrop(cropNode, crop)} /> : null}
            {markNode && markImage ? (
                <CanvasNodeAnnotationDialog
                    key={markNode.id}
                    image={{ url: markImage.url, storageKey: markImage.storageKey }}
                    open
                    onClose={annotationNode ? onCloseAnnotation : onCloseAnnotationEdit}
                    onSaveMark={(dataUrl) => onAnnotate(markNode, dataUrl)}
                    onGenerate={(payload) => onAnnotationEdit(markNode, payload)}
                    generateDisabledReason={markGenerateDisabledReason}
                />
            ) : null}
            {maskEditNode && maskEditImage ? <CanvasNodeMaskEditDialog image={maskEditImage} config={{ ...config, model: maskEditNode.metadata?.model || config.model, imageModel: maskEditNode.metadata?.model || config.imageModel, size: maskEditNode.metadata?.size || config.size, quality: maskEditNode.metadata?.quality || config.quality, count: String(maskEditNode.metadata?.count || config.count) }} open onClose={onCloseMaskEdit} onConfirm={(payload) => onMaskEdit(maskEditNode, payload)} /> : null}
            {upscaleNode && upscaleImage ? <CanvasNodeUpscaleDialog image={upscaleImage} open onClose={onCloseUpscale} onConfirm={(params) => onUpscale(upscaleNode, params)} /> : null}
            {imageEditNode && imageEditImage ? <CanvasNodeImageEditDialog image={imageEditImage} preset={imageEditPreset} config={{ ...config, model: imageEditNode.metadata?.model || config.model, imageModel: imageEditNode.metadata?.model || config.imageModel, size: imageEditNode.metadata?.size || config.size, quality: imageEditNode.metadata?.quality || config.quality }} open onClose={onCloseImageEdit} onConfirm={(payload) => onImageOperation(imageEditNode, payload)} /> : null}
            {layerDecompositionNode && layerDecompositionImage ? <CanvasNodeLayerDecompositionDialog image={layerDecompositionImage} config={{ ...config, model: layerDecompositionNode.metadata?.model || config.model, imageModel: layerDecompositionNode.metadata?.model || config.imageModel, size: layerDecompositionNode.metadata?.size || config.size, quality: layerDecompositionNode.metadata?.quality || config.quality }} open onClose={onCloseLayerDecomposition} onConfirm={(payload) => onLayerDecomposition(layerDecompositionNode, payload)} /> : null}
            {textEditNode && textEditImage ? <CanvasNodeTextEditDialog image={textEditImage} open onClose={onCloseTextEdit} onDetect={onDetectText} onConfirm={(payload) => onTextEdit(textEditNode, payload)} /> : null}
            {textLayerNode ? <CanvasProjectTextLayerDialog key={textLayerNode.id} node={textLayerNode} onDetectText={onDetectTextLayers} onSave={onSaveTextLayers} onClose={onCloseTextLayers} /> : null}
        </>
    );
}

/**
 * 文字图层弹窗：底图来源与画布节点用同一套解析（storageKey 优先，content 兜底），
 * 所以「只有 content」的历史节点和「只有 storageKey」的云端节点都能打开。
 */
function CanvasProjectTextLayerDialog({
    node,
    onDetectText,
    onSave,
    onClose,
}: {
    node: CanvasNodeData;
    onDetectText: () => Promise<import("@/components/canvas/canvas-node-text-edit-dialog").CanvasImageTextLine[]>;
    onSave: (node: CanvasNodeData, layers: CanvasTextLayer[], imageSize: { width: number; height: number }) => void;
    onClose: () => void;
}) {
    const source = canvasNodeImageSource(node);
    if (!source.hasSource) return null;
    return (
        <CanvasTextLayerEditor
            image={{ url: source.url, storageKey: source.storageKey, width: source.width, height: source.height }}
            layers={readCanvasTextLayers(node.metadata)}
            onDetectText={onDetectText}
            onSave={(layers, imageSize) => onSave(node, layers, imageSize)}
            onClose={onClose}
        />
    );
}

/** 弹窗底图判据：storageKey 或 content 任一存在即可打开，与画布节点「能显示图片」一致。 */
function dialogImage(node: CanvasNodeData | null): CanvasNodeImageSource | null {
    if (!node) return null;
    const source = canvasNodeImageSource(node);
    return source.hasSource ? source : null;
}
