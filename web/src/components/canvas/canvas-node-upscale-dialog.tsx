import { useEffect, useMemo, useState } from "react";
import { Button, Modal, Segmented } from "antd";
import { ImagePlus } from "lucide-react";

import { canvasDialogImageInput, type CanvasDialogImageInput } from "@/lib/canvas/canvas-node-image-source";
import { CanvasNodeImageStatus } from "@/components/canvas/canvas-node-image-status";
import { useCanvasNodeImage } from "@/hooks/use-canvas-node-image";
import { MAX_UPSCALE_LONG_EDGE, resolveUpscaleSize, type ImageUpscaleAlgorithm, type ImageUpscaleParams } from "@/lib/canvas/canvas-image-data";

export type CanvasImageUpscaleParams = ImageUpscaleParams;

const algorithms: Array<{ value: ImageUpscaleAlgorithm; title: string; description: string }> = [
    { value: "high", title: "高质量插值", description: "适合照片和细节图" },
    { value: "bilinear", title: "双线性", description: "平滑、速度快" },
    { value: "nearest", title: "最近邻", description: "适合像素风格" },
];

const targetOptions = [
    { label: "1K", value: 1024 },
    { label: "2K", value: 2048 },
    { label: "4K", value: MAX_UPSCALE_LONG_EDGE },
];

const defaultParams: CanvasImageUpscaleParams = {
    targetLongEdge: 2048,
    algorithm: "high",
};

export function CanvasNodeUpscaleDialog({ dataUrl, image: imageInput, open, onClose, onConfirm }: {
    /** 兼容老的 dataUrl 直传；新调用点传「storageKey 优先」的解析结果。 */
    dataUrl?: string;
    image?: CanvasDialogImageInput | null;
    open: boolean;
    onClose: () => void;
    onConfirm: (params: CanvasImageUpscaleParams) => void;
}) {
    const requested = canvasDialogImageInput(dataUrl, imageInput);
    const loaded = useCanvasNodeImage(requested, open);
    const [params, setParams] = useState<CanvasImageUpscaleParams>(defaultParams);
    // 放大倍率与输出尺寸按解码出的真实像素计算，节点宽高不等于原图尺寸。
    const image = useMemo(() => (loaded.status === "ready" ? { width: loaded.width, height: loaded.height } : null), [loaded.height, loaded.status, loaded.width]);
    const sourceReady = loaded.status === "ready";
    const sourceLongEdge = image ? Math.max(image.width, image.height) : 0;
    const outputSize = useMemo(() => (image ? resolveUpscaleSize(image.width, image.height, params.targetLongEdge) : null), [image, params.targetLongEdge]);
    const canUpscale = Boolean(image && sourceLongEdge < params.targetLongEdge && params.targetLongEdge <= MAX_UPSCALE_LONG_EDGE);
    const reachedMax = Boolean(image && sourceLongEdge >= MAX_UPSCALE_LONG_EDGE);

    useEffect(() => {
        if (!open) return;
        setParams(defaultParams);
    }, [open, requested?.storageKey, requested?.url]);



    useEffect(() => {
        if (!image) return;
        const nextTarget = targetOptions.find((option) => sourceLongEdge < option.value)?.value || MAX_UPSCALE_LONG_EDGE;
        setParams((current) => ({ ...current, targetLongEdge: nextTarget }));
    }, [image, sourceLongEdge]);

    return (
        <Modal title={null} open={open && Boolean(requested)} onCancel={onClose} footer={null} width={820} centered destroyOnHidden>
            <div className="space-y-5">
                <div>
                    <h2 className="text-xl font-semibold">调整尺寸</h2>
                    <p className="mt-2 text-sm opacity-60">通过插值放大像素尺寸，另存为新图片，保留原图。这不是 AI 超分，不会生成新的真实细节。</p>
                </div>
                <div className="grid gap-6 md:grid-cols-[minmax(260px,1fr)_360px]">
                    <div className="rounded-xl border p-4">
                        <div className="relative grid min-h-[280px] place-items-center rounded-lg bg-black/5">
                            {sourceReady ? <img src={loaded.url} alt="" className="max-h-[320px] max-w-full rounded-lg object-contain shadow-xl" draggable={false} /> : null}
                            <CanvasNodeImageStatus status={loaded.status} error={loaded.error} onRetry={loaded.reload} />
                        </div>
                        <div className="mt-3 flex items-center justify-between text-sm">
                            <span className="opacity-60">源图</span>
                            <span className="font-semibold">{image ? `${image.width} x ${image.height} px` : "读取中"}</span>
                        </div>
                    </div>
                    <div className="space-y-6 py-2">
                        <div className="space-y-2">
                            <div className="font-medium opacity-75">目标像素</div>
                            <Segmented
                                block
                                value={params.targetLongEdge}
                                options={targetOptions.map((option) => ({ label: `${option.label} · ${option.value}px`, value: option.value, disabled: Boolean(image && sourceLongEdge >= option.value) }))}
                                onChange={(value) => setParams((current) => ({ ...current, targetLongEdge: Number(value) }))}
                            />
                            {image && !canUpscale ? <div className="text-xs font-medium text-[#ef4444]">{reachedMax ? "图片已达到 4K，无需放大" : "图片已达到当前目标像素，无需放大"}</div> : null}
                        </div>
                        <div className="space-y-2">
                            <div className="font-medium opacity-75">放大算法</div>
                            <Segmented
                                block
                                value={params.algorithm}
                                options={algorithms.map((item) => ({
                                    value: item.value,
                                    label: (
                                        <span className="flex min-h-12 flex-col justify-center text-left leading-5">
                                            <span className="font-medium">{item.title}</span>
                                            <span className="text-xs opacity-55">{item.description}</span>
                                        </span>
                                    ),
                                }))}
                                onChange={(value) => setParams((current) => ({ ...current, algorithm: value as ImageUpscaleAlgorithm }))}
                            />
                        </div>
                        <div className="rounded-xl border px-4 py-3 text-sm">
                            <div className="flex items-center justify-between">
                                <span className="opacity-60">输出尺寸</span>
                                <span className="font-semibold">{outputSize ? `${outputSize.width} x ${outputSize.height} px` : "未知"}</span>
                            </div>
                        </div>
                    </div>
                </div>
                <div className="flex justify-end">
                    <Button type="primary" size="large" icon={<ImagePlus className="size-4" />} disabled={!canUpscale} onClick={() => onConfirm(params)}>
                        生成放大图
                    </Button>
                </div>
            </div>
        </Modal>
    );
}
