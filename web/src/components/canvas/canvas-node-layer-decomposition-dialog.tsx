import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import { Button, Input, InputNumber, Modal, Tag } from "antd";
import { Layers3, Plus, RotateCcw, X } from "lucide-react";

import { ModelPicker } from "@/components/model-picker";
import { isDedicatedLayerDecompositionEndpoint, LAYER_DECOMPOSITION_DEFAULT_LAYERS, layerDecompositionCap, planLayerDecomposition } from "@/lib/canvas/canvas-layer-decomposition";
import { modelCapabilityConfigFor } from "@/lib/model-capabilities";
import type { AiConfig } from "@/stores/use-config-store";
import { resolveModelRequestConfig } from "@/stores/use-config-store";
import { defaultImageParamsForModel } from "@/lib/model-selection";
import { canvasDialogImageInput, type CanvasDialogImageInput } from "@/lib/canvas/canvas-node-image-source";
import { CanvasNodeImageStatus } from "@/components/canvas/canvas-node-image-status";
import { useCanvasNodeImage } from "@/hooks/use-canvas-node-image";

export type CanvasImageLayerDecompositionPayload = {
    prompt: string;
    count?: number;
    regions?: Array<[number, number, number, number]>;
    generationConfig?: Partial<Pick<AiConfig, "model" | "imageModel" | "size" | "quality">>;
};

const DEFAULT_PROMPT = "把图片拆分为可独立编辑的图层：识别主要主体、前景、背景和重要物体，保持原图外观与边缘细节，使用透明背景，不要把不同图层合并到同一张图里。";

export function CanvasNodeLayerDecompositionDialog({
    dataUrl,
    image: imageInput,
    open,
    config,
    onClose,
    onConfirm,
}: {
    /** 兼容老的 dataUrl 直传；新调用点传「storageKey 优先」的解析结果。 */
    dataUrl?: string;
    image?: CanvasDialogImageInput | null;
    open: boolean;
    config: AiConfig;
    onClose: () => void;
    onConfirm: (payload: CanvasImageLayerDecompositionPayload) => void;
}) {
    const requested = canvasDialogImageInput(dataUrl, imageInput);
    const loaded = useCanvasNodeImage(requested, open);
    const [prompt, setPrompt] = useState(DEFAULT_PROMPT);
    const [generationConfig, setGenerationConfig] = useState<AiConfig>(config);
    const [regions, setRegions] = useState<Array<[number, number, number, number]>>([]);
    const [layerCount, setLayerCount] = useState(LAYER_DECOMPOSITION_DEFAULT_LAYERS);
    const [drawing, setDrawing] = useState<{ x: number; y: number } | null>(null);
    const [draft, setDraft] = useState<[number, number, number, number] | null>(null);
    const imageFrameRef = useRef<HTMLDivElement>(null);

    const selectedModel = generationConfig.imageModel || generationConfig.model;
    const imageProfile = modelCapabilityConfigFor(generationConfig, selectedModel).image;
    const dedicatedEndpoint = isDedicatedLayerDecompositionEndpoint(resolveModelRequestConfig(generationConfig, selectedModel).interfaceType, selectedModel);
    const layerCap = layerDecompositionCap({ dedicatedEndpoint, maxOutputs: imageProfile?.maxOutputs });
    const plan = planLayerDecomposition({ requestedLayers: layerCount, regionCount: regions.length, maxOutputs: imageProfile?.maxOutputs, dedicatedEndpoint, transparentBackground: imageProfile?.transparentBackground.supported });
    // 专用接口虽然一次返回多层，但豆包账号池这类上游不输出 alpha 通道；
    // planLayerDecomposition 只在通用模型路径提示透明度，这里需要单独说明。
    const opaqueDedicatedLayers = dedicatedEndpoint && !imageProfile?.transparentBackground.supported;

    useEffect(() => {
        if (!open) return;
        setPrompt(DEFAULT_PROMPT);
        setGenerationConfig(config);
        setRegions([]);
        setLayerCount(LAYER_DECOMPOSITION_DEFAULT_LAYERS);
        setDrawing(null);
        setDraft(null);
    }, [config, open, requested?.storageKey, requested?.url]);

    // 换模型会改变上限，必须把已选图层数收敛到新上限之内。
    useEffect(() => {
        setLayerCount((current) => Math.max(1, Math.min(layerCap, current)));
    }, [layerCap]);

    const point = (event: ReactPointerEvent<HTMLDivElement>) => {
        const rect = imageFrameRef.current?.getBoundingClientRect();
        if (!rect) return null;
        return {
            x: Math.max(0, Math.min(1000, ((event.clientX - rect.left) / Math.max(1, rect.width)) * 1000)),
            y: Math.max(0, Math.min(1000, ((event.clientY - rect.top) / Math.max(1, rect.height)) * 1000)),
        };
    };

    const startBox = (event: ReactPointerEvent<HTMLDivElement>) => {
        const next = point(event);
        if (!next) return;
        event.preventDefault();
        event.currentTarget.setPointerCapture(event.pointerId);
        setDrawing(next);
        setDraft([next.x, next.y, next.x, next.y]);
    };

    const moveBox = (event: ReactPointerEvent<HTMLDivElement>) => {
        if (!drawing) return;
        const next = point(event);
        if (!next) return;
        setDraft([Math.min(drawing.x, next.x), Math.min(drawing.y, next.y), Math.max(drawing.x, next.x), Math.max(drawing.y, next.y)]);
    };

    const finishBox = () => {
        if (!draft) return;
        const [x1, y1, x2, y2] = draft;
        if (x2 - x1 >= 2 && y2 - y1 >= 2) {
            const nextRegion: [number, number, number, number] = [Math.round(x1), Math.round(y1), Math.round(x2), Math.round(y2)];
            setRegions([...regions, nextRegion]);
            // 每个框选区域都应得到一个图层，新选区自动抬高图层数量（不超过当前上限）。
            setLayerCount(Math.max(1, Math.min(layerCap, Math.max(layerCount, regions.length + 1))));
        }
        setDrawing(null);
        setDraft(null);
    };

    // 选区不再拼进基础提示词：逐层模式由执行层按第 k 个图层拼对应选区，单请求模式拼全部选区。
    const selectedPrompt = prompt.trim();

    return (
        <Modal open={open && Boolean(requested)} onCancel={onClose} footer={null} centered destroyOnHidden width={900} title="AI 图层拆分">
            <div className="grid gap-5 md:grid-cols-[minmax(0,1fr)_320px]">
                <div className="grid min-h-[340px] place-items-center overflow-hidden rounded-xl bg-black/5 p-3 dark:bg-white/[0.04]">
                    <div
                        ref={imageFrameRef}
                        className={`relative inline-block max-h-[60vh] max-w-full select-none ${loaded.status === "ready" ? "" : "min-h-[240px] min-w-[320px]"}`}
                        onPointerDown={loaded.status === "ready" ? startBox : undefined}
                        onPointerMove={loaded.status === "ready" ? moveBox : undefined}
                        onPointerUp={finishBox}
                        onPointerCancel={finishBox}
                    >
                        {loaded.status === "ready" ? <img src={loaded.url} alt="待拆分图片" className="block max-h-[60vh] max-w-full object-contain" draggable={false} /> : null}
                        <CanvasNodeImageStatus status={loaded.status} error={loaded.error} onRetry={loaded.reload} />
                        <div className="pointer-events-none absolute inset-0">
                            {regions.map((region, index) => <RegionBox key={`${region.join("-")}-${index}`} region={region} label={index + 1} />)}
                            {draft ? <RegionBox region={draft} label={regions.length + 1} draft /> : null}
                        </div>
                    </div>
                </div>
                <div className="flex flex-col gap-4">
                    <div>
                        <h3 className="text-lg font-semibold">拆分图片图层</h3>
                        <p className="mt-1 text-sm opacity-60">可在图片上拖拽框选对象，AI 会返回多个透明背景图层，并在画布中自动排列为独立节点。</p>
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                        <Tag color={regions.length ? "blue" : "default"}>{regions.length ? `已框选 ${regions.length} 个区域` : "未框选，按描述拆分"}</Tag>
                        {regions.length ? <Button size="small" icon={<RotateCcw className="size-3.5" />} onClick={() => setRegions([])}>清除选区</Button> : <span className="text-xs opacity-55"><Plus className="mr-1 inline size-3" />在左侧图片上拖动添加选区</span>}
                    </div>
                    <Input.TextArea rows={7} value={prompt} placeholder="例如：分别提取人物、产品、前景装饰和背景" onChange={(event) => setPrompt(event.target.value)} />
                    <div className="space-y-2">
                        <div className="text-sm font-medium opacity-75">图层数量</div>
                        <div className="flex items-center gap-3">
                            <InputNumber min={1} max={plan.cap} value={layerCount} onChange={(value) => setLayerCount(Math.max(1, Math.min(plan.cap, Math.floor(Number(value) || 1))))} />
                            <span className="text-xs opacity-60">上限 {plan.cap} 层{dedicatedEndpoint ? "（当前模型单次返回上限）" : "（单次拆分上限）"}</span>
                        </div>
                    </div>
                    {plan.notices.length || opaqueDedicatedLayers ? (
                        <div className="space-y-1 rounded-lg bg-amber-400/10 p-2 text-xs leading-5 text-amber-700 dark:text-amber-300">
                            {plan.notices.map((notice) => <p key={notice}>{notice}</p>)}
                            {opaqueDedicatedLayers ? <p key="opaque-layers">当前模型不输出透明通道：拆出的图层是不透明图片；需要透明底请改用支持透明背景的模型。</p> : null}
                        </div>
                    ) : null}
                    <div className="space-y-2">
                        <div className="text-sm font-medium opacity-75">图层拆分模型</div>
                        <ModelPicker
                            config={generationConfig}
                            value={generationConfig.imageModel || generationConfig.model}
                            capability="image"
                            fullWidth
                            showSelectedPrice={false}
                            onChange={(model) => setGenerationConfig((current) => ({ ...current, model, imageModel: model, ...defaultImageParamsForModel(current, model) }))}
                        />
                    </div>
                    <div className="mt-auto flex justify-end gap-2">
                        <Button icon={<X className="size-4" />} onClick={onClose}>取消</Button>
                        <Button type="primary" icon={<Layers3 className="size-4" />} disabled={!selectedPrompt} onClick={() => onConfirm({ prompt: selectedPrompt, count: plan.layers, regions, generationConfig: { model: generationConfig.model, imageModel: generationConfig.imageModel, size: generationConfig.size, quality: generationConfig.quality } })}>
                            开始拆分
                        </Button>
                    </div>
                </div>
            </div>
        </Modal>
    );
}

function RegionBox({ region, label, draft = false }: { region: [number, number, number, number]; label: number; draft?: boolean }) {
    const [x1, y1, x2, y2] = region;
    return <div className={`absolute rounded-sm border-2 ${draft ? "border-dashed border-blue-500 bg-blue-500/10" : "border-solid border-amber-400 bg-amber-400/10"}`} style={{ left: `${x1 / 10}%`, top: `${y1 / 10}%`, width: `${(x2 - x1) / 10}%`, height: `${(y2 - y1) / 10}%` }}><span className="absolute -left-0.5 -top-0.5 grid size-5 -translate-y-1/2 -translate-x-1/2 place-items-center rounded-full bg-amber-400 text-[11px] font-semibold text-black shadow">{label}</span></div>;
}
