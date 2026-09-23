import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import { App, Button, ColorPicker, Input, InputNumber, Modal, Select, Slider, Tooltip } from "antd";
import { AlignCenter, AlignLeft, AlignRight, Bold, ChevronDown, ChevronUp, Eye, EyeOff, ImageOff, Italic, LoaderCircle, Lock, LockOpen, Plus, RefreshCw, Save, ScanText, Trash2, X } from "lucide-react";

import { loadCanvasNodeImage } from "@/lib/canvas/canvas-node-image-source";

import {
    CANVAS_TEXT_LAYER_FONTS,
    CANVAS_TEXT_LAYER_LIMITS,
    buildCanvasTextLayerStyle,
    canvasTextLayersFromDetectedLines,
    containFitFrame,
    createCanvasTextLayerInImage,
    readCanvasTextLayers,
    removeCanvasTextLayer,
    reorderCanvasTextLayer,
    setCanvasTextLayerFontSize,
    setCanvasTextLayerPosition,
    updateCanvasTextLayer,
    type CanvasTextLayer,
} from "@/lib/canvas/canvas-text-layer";
import type { CanvasImageTextLine } from "./canvas-node-text-edit-dialog";

type CanvasTextLayerLoadedImage = { url: string; width: number; height: number };

type CanvasTextLayerEditorImage = {
    /** 兜底图片地址；storageKey 存在时优先解析 storageKey，与画布节点显示一致。 */
    url: string;
    storageKey: string;
    /** 解码出真实尺寸之前的兜底像素尺寸。 */
    width: number;
    height: number;
};

type CanvasTextLayerEditorProps = {
    image: CanvasTextLayerEditorImage;
    layers: CanvasTextLayer[];
    /** 识别图中文字并追加为图层；不传时隐藏识别入口。 */
    onDetectText?: () => Promise<CanvasImageTextLine[]>;
    onSave: (layers: CanvasTextLayer[], imageSize: { width: number; height: number }) => void;
    onClose: () => void;
};

/** 缩放手柄在屏幕上的命中尺寸；画布按 scale 反向换算，缩放画布时手感不变。 */
const HANDLE_SCREEN_SIZE = 12;

/**
 * 文字图层编辑器：底图不动，只在上面排版可拖拽、可缩放的文字图层。
 *
 * 与「文字编辑」的区别：这里不调用图片模型。识别一次只用来生成图层初稿，
 * 之后改文案、字体、位置全部本地生效，保存后写回 node.metadata.textLayers。
 */
export function CanvasTextLayerEditor({ image, layers, onDetectText, onSave, onClose }: CanvasTextLayerEditorProps) {
    const { message } = App.useApp();
    const boardRef = useRef<HTMLDivElement>(null);
    const dragRef = useRef<{ id: string; pointerX: number; pointerY: number; originX: number; originY: number } | null>(null);
    const resizeRef = useRef<{ id: string; pointerY: number; fontSize: number } | null>(null);
    const [draft, setDraft] = useState<CanvasTextLayer[]>(() => readCanvasTextLayers({ textLayers: layers }));
    const [selectedId, setSelectedId] = useState<string | null>(() => draft[0]?.id ?? null);
    const [detecting, setDetecting] = useState(false);
    const [boardSize, setBoardSize] = useState({ width: 0, height: 0 });
    const [loadedImage, setLoadedImage] = useState<CanvasTextLayerLoadedImage | null>(null);
    const [imageLoading, setImageLoading] = useState(true);
    const [imageError, setImageError] = useState("");
    const [reloadToken, setReloadToken] = useState(0);

    useEffect(() => {
        const element = boardRef.current;
        if (!element) return;
        const measure = () => setBoardSize({ width: element.clientWidth, height: element.clientHeight });
        measure();
        if (typeof ResizeObserver === "undefined") return;
        const observer = new ResizeObserver(measure);
        observer.observe(element);
        return () => observer.disconnect();
    }, []);

    // 底图按画布节点同一优先级读取：storageKey 解析结果 > content 兜底 > 字节读取 data URL。
    useEffect(() => {
        let cancelled = false;
        setImageLoading(true);
        setImageError("");
        void loadCanvasNodeImage({ url: image.url, storageKey: image.storageKey })
            .then((loaded) => {
                if (cancelled) return;
                setLoadedImage(loaded);
                setImageLoading(false);
            })
            .catch((reason: unknown) => {
                if (cancelled) return;
                setLoadedImage(null);
                setImageError(reason instanceof Error ? reason.message : "底图加载失败");
                setImageLoading(false);
            });
        return () => { cancelled = true; };
    }, [image.storageKey, image.url, reloadToken]);

    const imagePixelSize = {
        width: loadedImage?.width || (image.width > 0 ? image.width : Math.max(1, boardSize.width)),
        height: loadedImage?.height || (image.height > 0 ? image.height : Math.max(1, boardSize.height)),
    };
    const frame = boardSize.width > 0 && boardSize.height > 0 ? containFitFrame(boardSize, imagePixelSize) : { left: 0, top: 0, width: 0, height: 0 };
    const stageScale = frame.width > 0 ? frame.width / Math.max(1, imagePixelSize.width) : 1;
    const selected = draft.find((layer) => layer.id === selectedId) ?? null;

    const patchSelected = (patch: Partial<CanvasTextLayer>) => {
        if (!selected) return;
        setDraft((current) => updateCanvasTextLayer(current, selected.id, patch));
    };

    const beginDrag = (event: ReactPointerEvent<HTMLElement>, layer: CanvasTextLayer) => {
        event.stopPropagation();
        setSelectedId(layer.id);
        if (layer.locked) return;
        event.currentTarget.setPointerCapture(event.pointerId);
        dragRef.current = { id: layer.id, pointerX: event.clientX, pointerY: event.clientY, originX: layer.x, originY: layer.y };
    };

    const moveDrag = (event: ReactPointerEvent<HTMLElement>) => {
        const drag = dragRef.current;
        if (!drag) return;
        const scale = stageScale > 0 ? stageScale : 1;
        const x = drag.originX + (event.clientX - drag.pointerX) / scale;
        const y = drag.originY + (event.clientY - drag.pointerY) / scale;
        setDraft((current) => setCanvasTextLayerPosition(current, drag.id, Math.round(x), Math.round(y), imagePixelSize));
    };

    const endDrag = (event: ReactPointerEvent<HTMLElement>) => {
        if (!dragRef.current) return;
        dragRef.current = null;
        if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
    };

    const beginResize = (event: ReactPointerEvent<HTMLElement>, layer: CanvasTextLayer) => {
        event.stopPropagation();
        if (layer.locked) return;
        event.currentTarget.setPointerCapture(event.pointerId);
        resizeRef.current = { id: layer.id, pointerY: event.clientY, fontSize: layer.fontSize };
    };

    const moveResize = (event: ReactPointerEvent<HTMLElement>) => {
        const resize = resizeRef.current;
        if (!resize) return;
        const scale = stageScale > 0 ? stageScale : 1;
        const target = resize.fontSize + (event.clientY - resize.pointerY) / scale;
        setDraft((current) => setCanvasTextLayerFontSize(current, resize.id, Math.round(target)));
    };

    const endResize = (event: ReactPointerEvent<HTMLElement>) => {
        if (!resizeRef.current) return;
        resizeRef.current = null;
        if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
    };

    const addLayer = () => {
        if (!loadedImage) {
            message.warning("底图还没加载好，暂时不能添加文字图层");
            return;
        }
        if (draft.length >= CANVAS_TEXT_LAYER_LIMITS.maxLayers) {
            message.warning("图层数量已达上限");
            return;
        }
        // 新建一次性算好再入库：避免在 setState 更新函数里做有副作用的 id 生成。
        const next = createCanvasTextLayerInImage(draft, { imageWidth: imagePixelSize.width, imageHeight: imagePixelSize.height });
        setDraft(next);
        setSelectedId(next[next.length - 1].id);
    };

    const detectText = async () => {
        if (!onDetectText || detecting) return;
        setDetecting(true);
        try {
            const lines = await onDetectText();
            const detected = canvasTextLayersFromDetectedLines(lines, {
                imageWidth: imagePixelSize.width,
                imageHeight: imagePixelSize.height,
                idPrefix: `text-layer-ocr-${Date.now().toString(36)}`,
            });
            if (!detected.length) {
                message.warning("没有识别到可添加的文字");
                return;
            }
            setDraft((current) => [...current, ...detected].slice(0, CANVAS_TEXT_LAYER_LIMITS.maxLayers));
            setSelectedId(detected[0].id);
            message.success(`已添加 ${detected.length} 个文字图层，可继续调整排版`);
        } catch (reason) {
            message.error(reason instanceof Error ? reason.message : "文字识别失败");
        } finally {
            setDetecting(false);
        }
    };

    const save = () => {
        const kept = draft.filter((layer) => layer.text.trim().length > 0);
        // 图层坐标是原图像素：把解码到的真实尺寸一起写回，避免节点宽高被当成原图尺寸。
        onSave(kept, { width: loadedImage?.width || 0, height: loadedImage?.height || 0 });
    };

    const rows = [...draft].reverse();

    return (
        <Modal open onCancel={onClose} footer={null} centered destroyOnHidden width={1120} title="文字图层">
            <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_360px]">
                <div ref={boardRef} className="relative h-[58vh] min-h-[320px] w-full overflow-hidden rounded-xl bg-black/75" onPointerDown={() => setSelectedId(null)}>
                    {loadedImage && frame.width > 0 ? (
                        <img
                            src={loadedImage.url}
                            alt="文字图层底图"
                            draggable={false}
                            className="pointer-events-none absolute select-none"
                            style={{ left: frame.left, top: frame.top, width: frame.width, height: frame.height }}
                            onError={() => {
                                // 解码成功但展示阶段被拦（鉴权/跨域）时同样给出可重试的错误态，而不是留一块空背景。
                                setLoadedImage(null);
                                setImageError("底图加载失败：图片资源不可用或已失效");
                            }}
                        />
                    ) : null}
                    {loadedImage && frame.width > 0 ? (
                        <div className="absolute" style={{ left: frame.left, top: frame.top, width: frame.width, height: frame.height }}>
                            <div className="relative select-none origin-top-left" style={{ width: imagePixelSize.width, height: imagePixelSize.height, transform: `scale(${stageScale})` }}>
                                {draft.map((layer) =>
                                    layer.visible ? (
                                        <div
                                            key={layer.id}
                                            onPointerDown={(event) => beginDrag(event, layer)}
                                            onPointerMove={moveDrag}
                                            onPointerUp={endDrag}
                                            onPointerCancel={endDrag}
                                            style={{
                                                ...buildCanvasTextLayerStyle(layer),
                                                cursor: layer.locked ? "default" : "move",
                                                outline: layer.id === selectedId ? "2px solid #1677ff" : undefined,
                                                outlineOffset: layer.id === selectedId ? 3 : undefined,
                                                touchAction: "none",
                                            }}
                                        >
                                            {layer.text}
                                            {layer.id === selectedId && !layer.locked ? (
                                                <span
                                                    role="presentation"
                                                    aria-label="拖动缩放文字"
                                                    onPointerDown={(event) => beginResize(event, layer)}
                                                    onPointerMove={moveResize}
                                                    onPointerUp={endResize}
                                                    onPointerCancel={endResize}
                                                    style={{
                                                        position: "absolute",
                                                        right: -HANDLE_SCREEN_SIZE / 2 / stageScale,
                                                        bottom: -HANDLE_SCREEN_SIZE / 2 / stageScale,
                                                        width: HANDLE_SCREEN_SIZE / stageScale,
                                                        height: HANDLE_SCREEN_SIZE / stageScale,
                                                        borderRadius: 2,
                                                        background: "#1677ff",
                                                        cursor: "nwse-resize",
                                                        touchAction: "none",
                                                    }}
                                                />
                                            ) : null}
                                        </div>
                                    ) : null,
                                )}
                            </div>
                        </div>
                    ) : null}
                    {imageLoading ? (
                        <div className="pointer-events-none absolute inset-0 grid place-items-center text-xs text-white/70">
                            <span className="flex items-center gap-2">
                                <LoaderCircle className="size-4 animate-spin" />
                                正在读取底图...
                            </span>
                        </div>
                    ) : null}
                    {!imageLoading && imageError ? (
                        <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 px-6 text-center text-xs text-white/70">
                            <ImageOff className="size-5 opacity-70" />
                            <span>{imageError}</span>
                            <Button size="small" icon={<RefreshCw className="size-3.5" />} onClick={() => setReloadToken((current) => current + 1)}>
                                重新加载
                            </Button>
                        </div>
                    ) : null}
                    {loadedImage && !draft.length ? <div className="pointer-events-none absolute inset-x-0 bottom-4 text-center text-xs text-white/55">还没有文字图层</div> : null}
                </div>

                <div className="flex min-h-[320px] flex-col gap-4">
                    <div className="flex items-center justify-between gap-2">
                        <div className="flex items-center gap-2 text-sm font-medium">
                            文字图层
                            <span className="text-xs opacity-55">
                                {draft.length}/{CANVAS_TEXT_LAYER_LIMITS.maxLayers}
                            </span>
                        </div>
                        <div className="flex items-center gap-2">
                            {onDetectText ? (
                                <Button size="small" icon={detecting ? <LoaderCircle className="size-3.5 animate-spin" /> : <ScanText className="size-3.5" />} onClick={() => void detectText()} disabled={detecting || !loadedImage}>
                                    识别图中文字
                                </Button>
                            ) : null}
                            <Tooltip title="添加文字图层">
                                <Button size="small" icon={<Plus className="size-3.5" />} onClick={addLayer} disabled={!loadedImage} />
                            </Tooltip>
                        </div>
                    </div>

                    <div className="max-h-40 space-y-1.5 overflow-y-auto pr-1">
                        {rows.map((layer) => (
                            <div key={layer.id} className={`flex items-center gap-1.5 rounded-lg border px-2 py-1.5 text-xs ${layer.id === selectedId ? "border-blue-500/70 bg-blue-500/10" : "border-transparent bg-black/5 dark:bg-white/[0.04]"}`}>
                                <button
                                    type="button"
                                    className="shrink-0 opacity-70 hover:opacity-100"
                                    aria-label={layer.visible ? "隐藏图层" : "显示图层"}
                                    onClick={() => setDraft((current) => updateCanvasTextLayer(current, layer.id, { visible: !layer.visible }))}
                                >
                                    {layer.visible ? <Eye className="size-3.5" /> : <EyeOff className="size-3.5" />}
                                </button>
                                <button type="button" className="min-w-0 flex-1 truncate text-left" onClick={() => setSelectedId(layer.id)} title={layer.text || "空图层"}>
                                    {layer.text.trim() || "空图层"}
                                </button>
                                <button
                                    type="button"
                                    className="shrink-0 opacity-70 hover:opacity-100"
                                    aria-label={layer.locked ? "解锁图层" : "锁定图层"}
                                    onClick={() => setDraft((current) => updateCanvasTextLayer(current, layer.id, { locked: !layer.locked }))}
                                >
                                    {layer.locked ? <Lock className="size-3.5" /> : <LockOpen className="size-3.5" />}
                                </button>
                                <button type="button" className="shrink-0 opacity-70 hover:opacity-100" aria-label="上移图层" onClick={() => setDraft((current) => reorderCanvasTextLayer(current, layer.id, 1))}>
                                    <ChevronUp className="size-3.5" />
                                </button>
                                <button type="button" className="shrink-0 opacity-70 hover:opacity-100" aria-label="下移图层" onClick={() => setDraft((current) => reorderCanvasTextLayer(current, layer.id, -1))}>
                                    <ChevronDown className="size-3.5" />
                                </button>
                                <button
                                    type="button"
                                    className="shrink-0 opacity-70 hover:opacity-100"
                                    aria-label="删除图层"
                                    onClick={() => {
                                        setDraft((current) => removeCanvasTextLayer(current, layer.id));
                                        if (selectedId === layer.id) setSelectedId(null);
                                    }}
                                >
                                    <Trash2 className="size-3.5" />
                                </button>
                            </div>
                        ))}
                    </div>

                    {selected ? (
                        <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto pr-1">
                            <Input.TextArea value={selected.text} autoSize={{ minRows: 2, maxRows: 5 }} placeholder="输入文字内容" onChange={(event) => patchSelected({ text: event.target.value })} />
                            <div className="grid grid-cols-2 gap-2">
                                <Select size="small" value={selected.fontFamily} onChange={(value) => patchSelected({ fontFamily: value })} options={CANVAS_TEXT_LAYER_FONTS.map((font) => ({ value: font.value, label: font.label }))} />
                                <InputNumber
                                    size="small"
                                    min={CANVAS_TEXT_LAYER_LIMITS.minFontSize}
                                    max={CANVAS_TEXT_LAYER_LIMITS.maxFontSize}
                                    value={selected.fontSize}
                                    onChange={(value) => {
                                        if (typeof value === "number") setDraft((current) => setCanvasTextLayerFontSize(current, selected.id, value));
                                    }}
                                />
                            </div>
                            <div className="flex items-center gap-2">
                                <ColorPicker size="small" format="hex" value={selected.color} onChange={(value) => patchSelected({ color: value.toHexString() })} />
                                <button
                                    type="button"
                                    aria-label="加粗"
                                    className={`grid size-7 place-items-center rounded-md border ${selected.fontWeight >= 600 ? "border-blue-500/70 bg-blue-500/10" : "border-transparent bg-black/5 dark:bg-white/[0.06]"}`}
                                    onClick={() => patchSelected({ fontWeight: selected.fontWeight >= 600 ? 400 : 700 })}
                                >
                                    <Bold className="size-3.5" />
                                </button>
                                <button
                                    type="button"
                                    aria-label="斜体"
                                    className={`grid size-7 place-items-center rounded-md border ${selected.italic ? "border-blue-500/70 bg-blue-500/10" : "border-transparent bg-black/5 dark:bg-white/[0.06]"}`}
                                    onClick={() => patchSelected({ italic: !selected.italic })}
                                >
                                    <Italic className="size-3.5" />
                                </button>
                                <div className="ml-auto flex items-center gap-1">
                                    {(
                                        [
                                            ["left", <AlignLeft key="left" className="size-3.5" />],
                                            ["center", <AlignCenter key="center" className="size-3.5" />],
                                            ["right", <AlignRight key="right" className="size-3.5" />],
                                        ] as const
                                    ).map(([align, icon]) => (
                                        <button
                                            key={align}
                                            type="button"
                                            aria-label={`对齐：${align}`}
                                            className={`grid size-7 place-items-center rounded-md border ${selected.align === align ? "border-blue-500/70 bg-blue-500/10" : "border-transparent bg-black/5 dark:bg-white/[0.06]"}`}
                                            onClick={() => patchSelected({ align })}
                                        >
                                            {icon}
                                        </button>
                                    ))}
                                </div>
                            </div>
                            <div className="flex items-center gap-3 text-xs opacity-75">
                                <span className="shrink-0">不透明度</span>
                                <Slider className="flex-1" min={5} max={100} value={Math.round(selected.opacity * 100)} onChange={(value) => patchSelected({ opacity: value / 100 })} />
                            </div>
                            <div className="flex items-center gap-3 text-xs opacity-75">
                                <span className="shrink-0">旋转</span>
                                <Slider className="flex-1" min={-45} max={45} value={selected.rotation} onChange={(value) => patchSelected({ rotation: value })} />
                            </div>
                        </div>
                    ) : (
                        <div className="flex flex-1 items-center justify-center text-xs opacity-55">选中一个图层后可调整排版</div>
                    )}

                    <div className="mt-auto flex items-center justify-between gap-2 border-t pt-3">
                        <Button
                            size="small"
                            danger
                            disabled={!draft.length}
                            onClick={() => {
                                setDraft([]);
                                setSelectedId(null);
                            }}
                        >
                            清空
                        </Button>
                        <div className="flex items-center gap-2">
                            <Button icon={<X className="size-4" />} onClick={onClose}>
                                取消
                            </Button>
                            <Button type="primary" icon={<Save className="size-4" />} onClick={save}>
                                保存图层
                            </Button>
                        </div>
                    </div>
                </div>
            </div>
        </Modal>
    );
}
