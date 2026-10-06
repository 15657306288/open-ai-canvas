import { imagePresetValue, imageSizePresets } from "@/lib/image-size-presets";
import type { ImageCapabilityConfig } from "@/lib/model-capabilities";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";
import type { CommerceScreen } from "@/types/commerce-workflow";

function dimensions(value: string) {
    const match = value.match(/^(\d+(?:\.\d+)?)[:x](\d+(?:\.\d+)?)$/);
    return match && Number(match[1]) > 0 && Number(match[2]) > 0 ? { width: Number(match[1]), height: Number(match[2]), pixels: value.includes("x") } : null;
}

/** Map per-screen/template ratios to supported model sizes while retaining the chosen resolution tier. */
export function commerceImageSize(profile: ImageCapabilityConfig | undefined, currentSize: string, requestedRatio?: string) {
    if (!profile || !requestedRatio || profile.size.parameter === "none") return currentSize;
    const target = dimensions(requestedRatio);
    if (!target) throw new Error("单屏比例无效");
    const current = dimensions(currentSize);
    const presets = imageSizePresets(profile);
    const candidates = [...new Set([...profile.size.values, ...presets.map((preset) => imagePresetValue(profile, preset))])].flatMap((value) => {
        const size = dimensions(value);
        if (!size) return [];
        const ratioDistance = Math.abs(Math.log(size.width / size.height / (target.width / target.height)));
        const resolutionDistance = current?.pixels && size.pixels ? Math.abs(Math.log(size.width * size.height / (current.width * current.height))) : 0;
        return [{ value, ratioDistance, resolutionDistance }];
    });
    candidates.sort((a, b) => Math.round(a.ratioDistance * 1000) - Math.round(b.ratioDistance * 1000) || a.resolutionDistance - b.resolutionDistance);
    if (!candidates.length) throw new Error("所选模型没有可用的比例尺寸，请在本组设置中选择受支持的尺寸");
    return candidates[0].value;
}

/** Pricing and submission must resolve each template/screen to the same model size. */
export function commerceScreenImageSize(profile: ImageCapabilityConfig | undefined, currentSize: string, source: CanvasNodeData, screen: CommerceScreen | undefined, nodes: CanvasNodeData[]) {
    let size = screen?.size ? commerceImageSize(profile, currentSize, screen.size) : currentSize;
    if (source.type === CanvasNodeType.ProductReplica && source.metadata?.commerceWorkflow?.followTemplate !== false) {
        const template = nodes.find((node) => node.id === screen?.templateNodeId);
        const width = template?.metadata?.naturalWidth, height = template?.metadata?.naturalHeight;
        if (width && height) size = commerceImageSize(profile, size, `${width}:${height}`);
    }
    return size;
}
