import type { CSSProperties } from "react";

import { nanoid } from "nanoid";

/**
 * 图片节点上的「文字图层」数据模型。
 *
 * 坐标一律使用原图像素空间（naturalWidth × naturalHeight），渲染时再按节点里图片的
 * 实际显示框做一次等比缩放。同一份图层数据因此在画布缩放、节点拉伸、原图导出三种
 * 场景下都落在同一位置，不需要为每种显示尺寸重新计算。
 *
 * 图层的绘制顺序就是数组顺序：下标越大越靠上（后画的盖住先画的）。
 */

export const CANVAS_TEXT_LAYER_ID_PREFIX = "text-layer";
export const CANVAS_TEXT_LAYER_DEFAULT_TEXT = "双击编辑文字";
export const CANVAS_TEXT_LAYER_DEFAULT_COLOR = "#ffffff";

export const CANVAS_TEXT_LAYER_LIMITS = {
    maxLayers: 200,
    maxCoordinate: 100000,
    minFontSize: 6,
    maxFontSize: 512,
    minLineHeight: 0.6,
    maxLineHeight: 4,
    minLetterSpacing: -20,
    maxLetterSpacing: 80,
    maxStrokeWidth: 24,
    maxShadowBlur: 96,
    minOpacity: 0.05,
    maxOpacity: 1,
} as const;

export type CanvasTextLayerAlign = "left" | "center" | "right";
export type CanvasTextLayerSource = "ocr" | "manual";

export type CanvasTextLayer = {
    id: string;
    text: string;
    /** 文本框左上角，原图像素。 */
    x: number;
    y: number;
    /** 文本框宽度（原图像素）；0 表示按内容自适应、不换行。 */
    width: number;
    /** 字号，原图像素。 */
    fontSize: number;
    fontFamily: string;
    /** CSS 字重，100–900。 */
    fontWeight: number;
    italic: boolean;
    align: CanvasTextLayerAlign;
    color: string;
    lineHeight: number;
    letterSpacing: number;
    /** 顺时针旋转角度。 */
    rotation: number;
    /** 0.05–1。 */
    opacity: number;
    visible: boolean;
    locked: boolean;
    strokeColor?: string;
    strokeWidth?: number;
    shadowColor?: string;
    shadowBlur?: number;
    source?: CanvasTextLayerSource;
};

export type CanvasTextLayerSize = { width: number; height: number };
export type CanvasTextLayerFrame = { left: number; top: number; width: number; height: number };

/** 字体下拉的候选项；value 直接写进 CSS font-family。 */
export const CANVAS_TEXT_LAYER_FONTS = [
    { label: "Bodoni MT", value: '"Bodoni MT", "Bodoni 72", Didot, "Times New Roman", serif' },
    { label: "Times New Roman", value: '"Times New Roman", Times, serif' },
    { label: "Georgia", value: 'Georgia, "Times New Roman", serif' },
    { label: "Inter", value: '"Inter Variable", Inter, "Helvetica Neue", Arial, sans-serif' },
    { label: "Helvetica", value: '"Helvetica Neue", Helvetica, Arial, sans-serif' },
    { label: "思源黑体", value: '"Source Han Sans SC", "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif' },
    { label: "思源宋体", value: '"Source Han Serif SC", "Noto Serif SC", "Songti SC", SimSun, serif' },
    { label: "等宽", value: '"JetBrains Mono Variable", "SFMono-Regular", Menlo, Consolas, monospace' },
] as const;

export const CANVAS_TEXT_LAYER_DEFAULT_FONT_FAMILY: string = CANVAS_TEXT_LAYER_FONTS[0].value;

const HEX_COLOR_PATTERN = /^#(?:[0-9a-f]{3}|[0-9a-f]{4}|[0-9a-f]{6}|[0-9a-f]{8})$/i;
const RGB_COLOR_PATTERN = /^rgba?\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*(?:,\s*(?:0|1|0?\.\d+)\s*)?\)$/i;
const FONT_FAMILY_PATTERN = /^[\w\s,'"-]+$/;

export function nextCanvasTextLayerId() {
    return `${CANVAS_TEXT_LAYER_ID_PREFIX}-${nanoid(8)}`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function clampNumber(value: unknown, min: number, max: number, fallback: number) {
    const parsed = typeof value === "number" ? value : Number(value);
    if (!Number.isFinite(parsed)) return fallback;
    return Math.min(Math.max(parsed, min), max);
}

function numberOrUndefined(value: unknown) {
    const parsed = typeof value === "number" ? value : Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
}

function clamp01(value: number) {
    return Math.min(Math.max(value, 0), 1);
}

export function clampCanvasTextLayerFontSize(value: unknown, fallback = 32) {
    return Math.round(clampNumber(value, CANVAS_TEXT_LAYER_LIMITS.minFontSize, CANVAS_TEXT_LAYER_LIMITS.maxFontSize, fallback));
}

export function normalizeCanvasTextLayerColor(value: unknown): string | undefined {
    if (typeof value !== "string") return undefined;
    const color = value.trim();
    if (!color) return undefined;
    if (color.toLowerCase() === "transparent") return "transparent";
    if (HEX_COLOR_PATTERN.test(color)) return color.toLowerCase();
    if (RGB_COLOR_PATTERN.test(color)) return color.replace(/\s+/g, " ");
    return undefined;
}

export function normalizeCanvasTextLayerFontFamily(value: unknown): string | undefined {
    if (typeof value !== "string") return undefined;
    const family = value.trim().slice(0, 160);
    if (!family || !FONT_FAMILY_PATTERN.test(family)) return undefined;
    return family;
}

function normalizeCanvasTextLayerAlign(value: unknown): CanvasTextLayerAlign {
    return value === "center" || value === "right" ? value : "left";
}

function normalizeCanvasTextLayerFontWeight(value: unknown) {
    const rounded = Math.round(clampNumber(value, 100, 900, 400) / 100) * 100;
    return Math.min(Math.max(rounded, 100), 900);
}

function normalizeCanvasTextLayerRotation(value: unknown) {
    return Math.round(clampNumber(value, -360, 360, 0) * 100) / 100;
}

/**
 * 唯一的图层构造入口：默认值、上下限、可选装饰字段都在这里收敛，
 * 编辑面板与 OCR 导入都不需要各自再写一遍校验。
 */
export function createCanvasTextLayer(patch: Partial<CanvasTextLayer> = {}): CanvasTextLayer {
    const limits = CANVAS_TEXT_LAYER_LIMITS;
    const strokeColor = normalizeCanvasTextLayerColor(patch.strokeColor);
    const strokeWidth = clampNumber(patch.strokeWidth, 0, limits.maxStrokeWidth, 0);
    const shadowColor = normalizeCanvasTextLayerColor(patch.shadowColor);
    const shadowBlur = clampNumber(patch.shadowBlur, 0, limits.maxShadowBlur, 0);
    const id = typeof patch.id === "string" && patch.id.trim() ? patch.id.trim() : nextCanvasTextLayerId();

    return {
        id,
        text: typeof patch.text === "string" ? patch.text : CANVAS_TEXT_LAYER_DEFAULT_TEXT,
        x: clampNumber(patch.x, 0, limits.maxCoordinate, 0),
        y: clampNumber(patch.y, 0, limits.maxCoordinate, 0),
        width: clampNumber(patch.width, 0, limits.maxCoordinate, 0),
        fontSize: clampCanvasTextLayerFontSize(patch.fontSize),
        fontFamily: normalizeCanvasTextLayerFontFamily(patch.fontFamily) || CANVAS_TEXT_LAYER_DEFAULT_FONT_FAMILY,
        fontWeight: normalizeCanvasTextLayerFontWeight(patch.fontWeight),
        italic: patch.italic === undefined ? false : Boolean(patch.italic),
        align: normalizeCanvasTextLayerAlign(patch.align),
        color: normalizeCanvasTextLayerColor(patch.color) || CANVAS_TEXT_LAYER_DEFAULT_COLOR,
        lineHeight: clampNumber(patch.lineHeight, limits.minLineHeight, limits.maxLineHeight, 1.2),
        letterSpacing: clampNumber(patch.letterSpacing, limits.minLetterSpacing, limits.maxLetterSpacing, 0),
        rotation: normalizeCanvasTextLayerRotation(patch.rotation),
        opacity: clampNumber(patch.opacity, limits.minOpacity, limits.maxOpacity, 1),
        visible: patch.visible === undefined ? true : Boolean(patch.visible),
        locked: Boolean(patch.locked),
        source: patch.source === "ocr" ? "ocr" : "manual",
        ...(strokeColor && strokeWidth > 0 ? { strokeColor, strokeWidth } : {}),
        ...(shadowColor && shadowBlur > 0 ? { shadowColor, shadowBlur } : {}),
    };
}

/**
 * 读取路径的兜底：落盘数据可能来自旧版本、其它端或手工改过的 JSON，
 * 坏一条只丢一条，不因为一条脏数据整块图层不显示。
 */
export function sanitizeCanvasTextLayer(value: unknown, index = 0): CanvasTextLayer | null {
    if (!isRecord(value)) return null;
    const text = typeof value.text === "string" ? value.text : "";
    if (!text.trim()) return null;

    const id = typeof value.id === "string" && value.id.trim() ? value.id.trim() : `${CANVAS_TEXT_LAYER_ID_PREFIX}-${index}`;
    const x = numberOrUndefined(value.x);
    const y = numberOrUndefined(value.y);
    const width = numberOrUndefined(value.width);
    const fontSize = numberOrUndefined(value.fontSize);
    const fontWeight = numberOrUndefined(value.fontWeight);
    const lineHeight = numberOrUndefined(value.lineHeight);
    const letterSpacing = numberOrUndefined(value.letterSpacing);
    const rotation = numberOrUndefined(value.rotation);
    const opacity = numberOrUndefined(value.opacity);
    const strokeWidth = numberOrUndefined(value.strokeWidth);
    const shadowBlur = numberOrUndefined(value.shadowBlur);

    return createCanvasTextLayer({
        id,
        text,
        ...(x === undefined ? {} : { x }),
        ...(y === undefined ? {} : { y }),
        ...(width === undefined ? {} : { width }),
        ...(fontSize === undefined ? {} : { fontSize }),
        ...(fontWeight === undefined ? {} : { fontWeight }),
        ...(lineHeight === undefined ? {} : { lineHeight }),
        ...(letterSpacing === undefined ? {} : { letterSpacing }),
        ...(rotation === undefined ? {} : { rotation }),
        ...(opacity === undefined ? {} : { opacity }),
        ...(strokeWidth === undefined ? {} : { strokeWidth }),
        ...(shadowBlur === undefined ? {} : { shadowBlur }),
        ...(normalizeCanvasTextLayerColor(value.color) ? { color: normalizeCanvasTextLayerColor(value.color) } : {}),
        ...(normalizeCanvasTextLayerFontFamily(value.fontFamily) ? { fontFamily: normalizeCanvasTextLayerFontFamily(value.fontFamily) } : {}),
        ...(normalizeCanvasTextLayerColor(value.strokeColor) ? { strokeColor: normalizeCanvasTextLayerColor(value.strokeColor) } : {}),
        ...(normalizeCanvasTextLayerColor(value.shadowColor) ? { shadowColor: normalizeCanvasTextLayerColor(value.shadowColor) } : {}),
        align: normalizeCanvasTextLayerAlign(value.align),
        italic: typeof value.italic === "boolean" ? value.italic : undefined,
        visible: typeof value.visible === "boolean" ? value.visible : undefined,
        locked: typeof value.locked === "boolean" ? value.locked : undefined,
        source: value.source === "ocr" ? "ocr" : "manual",
    });
}

export function readCanvasTextLayers(metadata: { textLayers?: unknown } | null | undefined): CanvasTextLayer[] {
    const raw = metadata?.textLayers;
    if (!Array.isArray(raw)) return [];
    return raw
        .slice(0, CANVAS_TEXT_LAYER_LIMITS.maxLayers)
        .map((item, index) => sanitizeCanvasTextLayer(item, index))
        .filter((item): item is CanvasTextLayer => item !== null);
}

/** object-contain 的留白计算：容器里的图片实际显示框。 */
export function containFitFrame(container: CanvasTextLayerSize, content: CanvasTextLayerSize): CanvasTextLayerFrame {
    const containerWidth = Math.max(1, numberOrUndefined(container.width) || 1);
    const containerHeight = Math.max(1, numberOrUndefined(container.height) || 1);
    const contentWidth = Math.max(1, numberOrUndefined(content.width) || 1);
    const contentHeight = Math.max(1, numberOrUndefined(content.height) || 1);
    const scale = Math.min(containerWidth / contentWidth, containerHeight / contentHeight);
    const width = contentWidth * scale;
    const height = contentHeight * scale;
    return { left: (containerWidth - width) / 2, top: (containerHeight - height) / 2, width, height };
}

/** 文字图层舞台框：节点里图片占据的那块区域。 */
export function canvasTextLayerStageFrame(options: {
    containerWidth: number;
    containerHeight: number;
    imageWidth: number;
    imageHeight: number;
    /** 节点被拉伸成自由比例（object-fill）时图片铺满内容框。 */
    fill?: boolean;
}): CanvasTextLayerFrame {
    const containerWidth = Math.max(1, numberOrUndefined(options.containerWidth) || 1);
    const containerHeight = Math.max(1, numberOrUndefined(options.containerHeight) || 1);
    if (options.fill) return { left: 0, top: 0, width: containerWidth, height: containerHeight };
    return containFitFrame({ width: containerWidth, height: containerHeight }, { width: options.imageWidth || containerWidth, height: options.imageHeight || containerHeight });
}

/** 图层样式只在这里生成一次，画布渲染与后续导出合成共用同一套数值。 */
export function buildCanvasTextLayerStyle(layer: CanvasTextLayer): CSSProperties {
    const hasFixedWidth = layer.width > 0;
    const strokeWidth = layer.strokeColor ? clampNumber(layer.strokeWidth, 0, CANVAS_TEXT_LAYER_LIMITS.maxStrokeWidth, 0) : 0;
    const shadowBlur = layer.shadowColor ? clampNumber(layer.shadowBlur, 0, CANVAS_TEXT_LAYER_LIMITS.maxShadowBlur, 0) : 0;

    return {
        position: "absolute",
        left: layer.x,
        top: layer.y,
        width: hasFixedWidth ? layer.width : "max-content",
        transform: layer.rotation ? `rotate(${layer.rotation}deg)` : undefined,
        transformOrigin: "top left",
        fontFamily: layer.fontFamily,
        fontSize: layer.fontSize,
        fontWeight: layer.fontWeight,
        fontStyle: layer.italic ? "italic" : "normal",
        lineHeight: layer.lineHeight,
        letterSpacing: layer.letterSpacing,
        color: layer.color,
        opacity: layer.opacity,
        textAlign: layer.align,
        whiteSpace: hasFixedWidth ? "pre-wrap" : "pre",
        overflowWrap: hasFixedWidth ? "break-word" : undefined,
        WebkitTextStroke: strokeWidth > 0 && layer.strokeColor ? `${strokeWidth}px ${layer.strokeColor}` : undefined,
        textShadow: shadowBlur > 0 && layer.shadowColor ? `0 0 ${shadowBlur}px ${layer.shadowColor}` : undefined,
    };
}

export type CanvasTextLayerDetectedLine = {
    original: string;
    text: string;
    location: string;
    /** 归一化（0–1）文字框；模型没给坐标时留空，走顺序排布兜底。 */
    bbox?: { x: number; y: number; width: number; height: number };
};

function detectedBoxFraction(value: number, unit: number, imageSize: number) {
    if (unit > 0) return value / unit;
    return imageSize > 0 ? value / imageSize : value;
}

/**
 * 文字模型给的坐标框有三种常见写法：归一化（0–1）、千分位（0–1000）、原图像素。
 * 这里统一换算成 0–1 并裁进画面；宽高非法一律当作「没有坐标」，走顺序排布兜底。
 */
function normalizeDetectedBoundingBox(value: unknown, imageWidth: number, imageHeight: number) {
    if (!isRecord(value)) return null;
    const x = numberOrUndefined(value.x);
    const y = numberOrUndefined(value.y);
    const width = numberOrUndefined(value.width);
    const height = numberOrUndefined(value.height);
    if (x === undefined || y === undefined || width === undefined || height === undefined) return null;
    if (width <= 0 || height <= 0) return null;

    const largest = Math.max(x, y, width, height);
    const unit = largest <= 1.0001 ? 1 : largest <= 1000 ? 1000 : 0;
    const left = clamp01(detectedBoxFraction(x, unit, imageWidth));
    const top = clamp01(detectedBoxFraction(y, unit, imageHeight));
    const right = clamp01(detectedBoxFraction(x + width, unit, imageWidth));
    const bottom = clamp01(detectedBoxFraction(y + height, unit, imageHeight));
    return {
        x: left,
        y: top,
        width: Math.max(0.01, right - left),
        height: Math.max(0.01, bottom - top),
    };
}

/**
 * 把「识别图片文字」的结果转成可排版图层：有坐标就用坐标，没有就按阅读顺序纵向排布，
 * 识别之后用户只改文字和样式，不再消耗生成额度。
 */
export function canvasTextLayersFromDetectedLines(lines: CanvasTextLayerDetectedLine[], options: { imageWidth: number; imageHeight: number; idPrefix?: string }): CanvasTextLayer[] {
    const imageWidth = Math.max(1, numberOrUndefined(options.imageWidth) || 1);
    const imageHeight = Math.max(1, numberOrUndefined(options.imageHeight) || 1);
    const idPrefix = options.idPrefix?.trim() || CANVAS_TEXT_LAYER_ID_PREFIX;
    const defaultFontSize = clampCanvasTextLayerFontSize(Math.round(imageHeight * 0.06));
    const marginX = imageWidth * 0.08;
    let fallbackTop = imageHeight * 0.08;
    const layers: CanvasTextLayer[] = [];

    lines.forEach((line, index) => {
        const text = (line.text || line.original || "").trim();
        if (!text) return;
        const box = normalizeDetectedBoundingBox(line.bbox, imageWidth, imageHeight);
        if (box) {
            layers.push(
                createCanvasTextLayer({
                    id: `${idPrefix}-${index}`,
                    text,
                    x: box.x * imageWidth,
                    y: box.y * imageHeight,
                    width: box.width * imageWidth,
                    fontSize: Math.max(defaultFontSize * 0.6, Math.round(box.height * imageHeight * 0.82)),
                    source: "ocr",
                }),
            );
            return;
        }
        layers.push(
            createCanvasTextLayer({
                id: `${idPrefix}-${index}`,
                text,
                x: marginX,
                y: fallbackTop,
                width: imageWidth - marginX * 2,
                fontSize: defaultFontSize,
                align: "center",
                source: "ocr",
            }),
        );
        fallbackTop += defaultFontSize * 1.5;
    });

    return layers.slice(0, CANVAS_TEXT_LAYER_LIMITS.maxLayers);
}

/** 合并式更新：仍然走 createCanvasTextLayer 收敛数值，避免面板直接把非法值写进 metadata。 */
export function updateCanvasTextLayer(layers: CanvasTextLayer[], layerId: string, patch: Partial<CanvasTextLayer>): CanvasTextLayer[] {
    return layers.map((layer) => (layer.id === layerId ? createCanvasTextLayer({ ...layer, ...patch, id: layer.id }) : layer));
}

export function removeCanvasTextLayer(layers: CanvasTextLayer[], layerId: string): CanvasTextLayer[] {
    return layers.filter((layer) => layer.id !== layerId);
}

/** 图层排序即绘制顺序，offset 为正表示往上层挪。 */
export function reorderCanvasTextLayer(layers: CanvasTextLayer[], layerId: string, offset: number): CanvasTextLayer[] {
    const index = layers.findIndex((layer) => layer.id === layerId);
    if (index < 0 || !Number.isFinite(offset)) return layers;
    const target = Math.min(Math.max(index + Math.trunc(offset), 0), layers.length - 1);
    if (target === index) return layers;
    const next = [...layers];
    const [moved] = next.splice(index, 1);
    next.splice(target, 0, moved);
    return next;
}

/** 画布编辑器拖动图层：写绝对位置，并裁进图片范围（屏幕位移 ÷ 舞台缩放由调用方换算）。 */
export function setCanvasTextLayerPosition(layers: CanvasTextLayer[], layerId: string, x: number, y: number, bounds?: { width: number; height: number }): CanvasTextLayer[] {
    const maxX = bounds && Number.isFinite(bounds.width) ? Math.max(0, bounds.width) : CANVAS_TEXT_LAYER_LIMITS.maxCoordinate;
    const maxY = bounds && Number.isFinite(bounds.height) ? Math.max(0, bounds.height) : CANVAS_TEXT_LAYER_LIMITS.maxCoordinate;
    return updateCanvasTextLayer(layers, layerId, {
        x: clampNumber(x, 0, maxX, 0),
        y: clampNumber(y, 0, maxY, 0),
    });
}

/**
 * 直接设置字号：固定宽度的文本框按同一比例缩放，排版比例不会在反复缩放中跑偏。
 * 拖拽缩放手柄用的是绝对目标字号，而不是相对倍率，避免拖动过程中误差累积。
 */
export function setCanvasTextLayerFontSize(layers: CanvasTextLayer[], layerId: string, fontSize: number): CanvasTextLayer[] {
    const layer = layers.find((item) => item.id === layerId);
    if (!layer) return layers;
    const next = clampCanvasTextLayerFontSize(fontSize, layer.fontSize);
    const ratio = layer.fontSize > 0 ? next / layer.fontSize : 1;
    return updateCanvasTextLayer(layers, layerId, {
        fontSize: next,
        ...(layer.width > 0 ? { width: Math.round(layer.width * ratio) } : {}),
    });
}

/** 按倍率缩放字号（面板上的 ± 按钮用）。 */
export function scaleCanvasTextLayerFont(layers: CanvasTextLayer[], layerId: string, factor: number): CanvasTextLayer[] {
    const layer = layers.find((item) => item.id === layerId);
    if (!layer || !Number.isFinite(factor) || factor <= 0) return layers;
    return setCanvasTextLayerFontSize(layers, layerId, layer.fontSize * factor);
}

/** 新建图层：默认落在画面中上部，多个图层依次下移，避免完全重叠。 */
export function createCanvasTextLayerInImage(layers: CanvasTextLayer[], options: { imageWidth: number; imageHeight: number; text?: string; id?: string }): CanvasTextLayer[] {
    if (layers.length >= CANVAS_TEXT_LAYER_LIMITS.maxLayers) return layers;
    const imageWidth = Math.max(1, numberOrUndefined(options.imageWidth) || 1);
    const imageHeight = Math.max(1, numberOrUndefined(options.imageHeight) || 1);
    const fontSize = clampCanvasTextLayerFontSize(Math.round(imageHeight * 0.08));
    const offset = (layers.length % 5) * fontSize * 1.6;
    return [
        ...layers,
        createCanvasTextLayer({
            id: options.id?.trim() || nextCanvasTextLayerId(),
            text: options.text ?? CANVAS_TEXT_LAYER_DEFAULT_TEXT,
            x: Math.round(imageWidth * 0.1),
            y: Math.round(imageHeight * 0.12 + offset),
            width: Math.round(imageWidth * 0.8),
            fontSize,
            align: "center",
            source: "manual",
        }),
    ];
}
