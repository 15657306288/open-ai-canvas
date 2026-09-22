import { useEffect, useState } from "react";
import { App, Button, Input, Modal } from "antd";
import { AlertTriangle, WandSparkles, X } from "lucide-react";
import { ModelPicker } from "@/components/model-picker";
import { resolveCanvasRemoveBackgroundIntent } from "@/lib/canvas/canvas-project-generation";
import type { AiConfig } from "@/stores/use-config-store";
import { defaultImageParamsForModel } from "@/lib/model-selection";
import { canvasDialogImageInput, type CanvasDialogImageInput } from "@/lib/canvas/canvas-node-image-source";
import { CanvasNodeImageStatus } from "@/components/canvas/canvas-node-image-status";
import { useCanvasNodeImage } from "@/hooks/use-canvas-node-image";

export type CanvasImageEditPayload = { prompt: string; generationConfig?: Partial<Pick<AiConfig, "model" | "imageModel" | "size" | "quality" | "transparentBackground">> };

export function CanvasNodeImageEditDialog({
    dataUrl,
    image: imageInput,
    open,
    config,
    preset,
    onClose,
    onConfirm,
}: {
    /** 兼容老的 dataUrl 直传；新调用点传「storageKey 优先」的解析结果。 */
    dataUrl?: string;
    image?: CanvasDialogImageInput | null;
    open: boolean;
    onClose: () => void;
    onConfirm: (payload: CanvasImageEditPayload) => void;
    config: AiConfig;
    preset?: "remove-background" | null;
}) {
    const requested = canvasDialogImageInput(dataUrl, imageInput);
    const loaded = useCanvasNodeImage(requested, open);
    const { message } = App.useApp();
    const [prompt, setPrompt] = useState(preset === "remove-background" ? "移除图片背景，保留主体完整轮廓、细节和边缘，输出透明背景。" : "");
    const [generationConfig, setGenerationConfig] = useState<AiConfig>(config);

    useEffect(() => {
        if (open) setPrompt(preset === "remove-background" ? "移除图片背景，保留主体完整轮廓、细节和边缘，输出透明背景。" : "");
        if (open) setGenerationConfig(config);
    }, [config, open, requested?.storageKey, requested?.url]);

    // 用户没有改选模型时，generationConfig.model 可能仍是全局默认的文字模型；
    // 图片编辑提交的是图生图任务，必须回传图片能力模型，否则后端预检直接判能力不匹配。
    const selectedImageModel = generationConfig.imageModel || generationConfig.model;

    // 「去除背景」的透明底是模型能力而不是提示词能保证的结果：先按当前模型能力决定是否提交透明背景意图。
    const removeBackgroundIntent = preset === "remove-background" ? resolveCanvasRemoveBackgroundIntent(generationConfig, selectedImageModel) : null;
    const submitEdit = () => {
        const promptValue = prompt.trim();
        if (!promptValue) return;
        if (removeBackgroundIntent?.notice) message.warning(removeBackgroundIntent.notice);
        onConfirm({
            prompt: promptValue,
            generationConfig: {
                model: selectedImageModel,
                imageModel: selectedImageModel,
                size: generationConfig.size,
                quality: generationConfig.quality,
                ...(removeBackgroundIntent?.transparentBackground ? { transparentBackground: removeBackgroundIntent.transparentBackground } : {}),
            },
        });
    };

    return (
        <Modal open={open && Boolean(requested)} onCancel={onClose} footer={null} centered destroyOnHidden width={860} title={preset === "remove-background" ? "去除背景" : "图片编辑"}>
            <div className="grid gap-5 md:grid-cols-[minmax(0,1fr)_300px]">
                <div className="relative grid min-h-[320px] place-items-center overflow-hidden rounded-xl bg-black/5 p-3 dark:bg-white/[0.04]">
                    {loaded.status === "ready" ? <img src={loaded.url} alt="待编辑图片" className="max-h-[58vh] max-w-full object-contain" draggable={false} /> : null}
                    <CanvasNodeImageStatus status={loaded.status} error={loaded.error} onRetry={loaded.reload} />
                </div>
                <div className="flex flex-col gap-4">
                    <div>
                        <h3 className="text-lg font-semibold">{preset === "remove-background" ? "去除背景" : "描述你要修改的内容"}</h3>
                        <p className="mt-1 text-sm opacity-60">保留主体和构图，只修改你描述的部分。</p>
                    </div>
                    <Input.TextArea
                        autoFocus
                        rows={7}
                        value={prompt}
                        placeholder="例如：把背景换成黄昏海边，保持人物姿势和服装不变"
                        onChange={(event) => setPrompt(event.target.value)}
                    />
                    <div className="space-y-2">
                        <div className="text-sm font-medium opacity-75">编辑模型</div>
                        <ModelPicker
                            config={generationConfig}
                            value={generationConfig.imageModel || generationConfig.model}
                            capability="image"
                            fullWidth
                            showSelectedPrice={false}
                            onChange={(model) => setGenerationConfig((current) => ({ ...current, model, imageModel: model, ...defaultImageParamsForModel(current, model) }))}
                        />
                        {removeBackgroundIntent?.notice ? (
                            <p className="flex items-start gap-1.5 text-xs leading-5 text-amber-600 dark:text-amber-300">
                                <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                                <span>{removeBackgroundIntent.notice}</span>
                            </p>
                        ) : null}
                    </div>
                    <div className="mt-auto flex justify-end gap-2">
                        <Button icon={<X className="size-4" />} onClick={onClose}>取消</Button>
                        <Button type="primary" icon={<WandSparkles className="size-4" />} disabled={!prompt.trim()} onClick={submitEdit}>
                            开始编辑
                        </Button>
                    </div>
                </div>
            </div>
        </Modal>
    );
}
