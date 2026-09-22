import { useEffect, useState } from "react";
import { Button } from "antd";
import { AppModal } from "@/components/ui/product/app-modal";
import { WandSparkles, X } from "lucide-react";

import { CountInput, ImageSettingsPanel, ImageSettingsTheme, OptionPill } from "@/components/image-settings-panel";
import { ModelPicker } from "@/components/model-picker";
import { canvasThemes } from "@/lib/canvas-theme";
import { defaultImageParamsForModel } from "@/lib/model-selection";
import { CanvasCameraControlPopover } from "./canvas-camera-control-popover";
import type { CameraControlOptions } from "@/lib/canvas/camera-prompt-library";
import { useActiveTheme } from "@/stores/canvas/use-canvas-theme-store";
import type { AiConfig } from "@/stores/use-config-store";

export type BatchGenerationSettings = Pick<AiConfig, "model" | "imageModel" | "quality" | "size" | "transparentBackground" | "count"> & { cameraControl?: CameraControlOptions };

type BatchGenerationSettingsDialogProps = {
    open: boolean;
    config: AiConfig;
    rowCount: number;
    concurrency: number;
    onClose: () => void;
    onConfirm: (settings: BatchGenerationSettings) => void;
};

/** 每行生成张数上限；张数会展开成独立任务，所以不受单次请求的模型输出上限约束。 */
export const MAX_BATCH_ROW_OUTPUT_COUNT = 10;
const QUICK_ROW_OUTPUT_COUNTS = [1, 2, 3] as const;

export function BatchGenerationSettingsDialog({ open, config, rowCount, concurrency, onClose, onConfirm }: BatchGenerationSettingsDialogProps) {
    const theme = canvasThemes[useActiveTheme()];
    const [generationConfig, setGenerationConfig] = useState<AiConfig>(config);
    const [cameraControl, setCameraControl] = useState<CameraControlOptions | undefined>(undefined);
    const [rowOutputCount, setRowOutputCount] = useState(1);

    useEffect(() => {
        if (open) {
            setGenerationConfig(config);
            setCameraControl(undefined);
            setRowOutputCount(1);
        }
    }, [config, open]);

    const handleConfigChange = (key: "quality" | "size" | "transparentBackground" | "count", value: string) => {
        setGenerationConfig((current) => ({ ...current, [key]: value }));
    };

    const handleModelChange = (model: string) => {
        setGenerationConfig((current) => ({ ...current, model, imageModel: model, ...defaultImageParamsForModel(current, model) }));
    };

    const imageModel = generationConfig.imageModel || generationConfig.model;
    const outputCount = Math.max(1, Math.min(MAX_BATCH_ROW_OUTPUT_COUNT, Math.floor(rowOutputCount) || 1));
    const totalOutputCount = rowCount * outputCount;

    return (
        <AppModal
            open={open}
            onCancel={onClose}
            footer={null}
            centered
            destroyOnHidden
            width={480}
            title="批量生成设置"
        >
            <div className="flex flex-col gap-4 py-2">
                <div className="rounded-lg bg-black/5 px-3 py-2 text-sm dark:bg-white/[0.04]">
                    共 <span className="font-semibold">{rowCount}</span> 行 · 每行 <span className="font-semibold">{outputCount}</span> 张 · 合计 <span className="font-semibold">{totalOutputCount}</span> 张 · 并发上限 <span className="font-semibold">{concurrency}</span>
                    <div className="mt-1 text-xs opacity-75">确认后将提交生成任务，可能消耗积分或产生外部模型费用。</div>
                </div>

                <div className="space-y-2">
                    <div className="text-sm font-medium opacity-75">生成模型</div>
                    <ModelPicker
                        config={generationConfig}
                        value={imageModel}
                        capability="image"
                        fullWidth
                        showSelectedPrice={false}
                        onChange={handleModelChange}
                    />
                </div>

                <div className="border-t pt-3" style={{ borderColor: theme.node.stroke }}>
                    <ImageSettingsPanel
                        config={generationConfig}
                        onConfigChange={handleConfigChange}
                        theme={theme}
                        showTitle={false}
                        showCount={false}
                        quickCount={4}
                        maxCount={10}
                        className="w-full space-y-3"
                    />
                </div>

                <div className="space-y-2 border-t pt-3" style={{ borderColor: theme.node.stroke }}>
                    <div className="text-sm font-medium opacity-75">每行生成张数</div>
                    <ImageSettingsTheme theme={theme}>
                        <div className="grid grid-cols-4 gap-1.5">
                            {QUICK_ROW_OUTPUT_COUNTS.map((value) => (
                                <OptionPill key={value} selected={outputCount === value} theme={theme} onClick={() => setRowOutputCount(value)}>
                                    {value}
                                </OptionPill>
                            ))}
                            <CountInput
                                value={outputCount}
                                quickCount={QUICK_ROW_OUTPUT_COUNTS.length}
                                max={MAX_BATCH_ROW_OUTPUT_COUNT}
                                theme={theme}
                                onChange={(value) => setRowOutputCount(value || 1)}
                            />
                        </div>
                    </ImageSettingsTheme>
                    <div className="text-xs opacity-60">每行按张数展开成独立任务，与其他行一起排队并发执行。</div>
                </div>

                <div className="flex items-center justify-between border-t pt-3" style={{ borderColor: theme.node.stroke }}>
                    <div>
                        <div className="text-sm font-medium">摄像机控制</div>
                        <div className="mt-1 text-xs opacity-60">为本批次统一应用镜头、焦段和光圈设置</div>
                    </div>
                    <CanvasCameraControlPopover cameraControl={cameraControl} onCameraControlChange={setCameraControl} theme={theme} compact />
                </div>

                <div className="mt-2 flex justify-end gap-2">
                    <Button icon={<X className="size-4" />} onClick={onClose}>取消</Button>
                    <Button
                        type="primary"
                        icon={<WandSparkles className="size-4" />}
                        onClick={() => onConfirm({
                            model: generationConfig.model,
                            imageModel: generationConfig.imageModel,
                            quality: generationConfig.quality,
                            size: generationConfig.size,
                            transparentBackground: generationConfig.transparentBackground,
                            count: String(outputCount),
                            cameraControl,
                        })}
                    >
                        开始生成 {totalOutputCount} 张
                    </Button>
                </div>
            </div>
        </AppModal>
    );
}
