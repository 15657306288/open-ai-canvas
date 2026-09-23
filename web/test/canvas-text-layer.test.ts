import { describe, expect, test } from "bun:test";

import {
    CANVAS_TEXT_LAYER_LIMITS,
    canvasTextLayerStageFrame,
    canvasTextLayersFromDetectedLines,
    containFitFrame,
    createCanvasTextLayer,
    createCanvasTextLayerInImage,
    readCanvasTextLayers,
    removeCanvasTextLayer,
    reorderCanvasTextLayer,
    scaleCanvasTextLayerFont,
    setCanvasTextLayerPosition,
    updateCanvasTextLayer,
} from "@/lib/canvas/canvas-text-layer";
import { canvasTextLayerImageCandidates, canvasTextLayerImageSource } from "@/lib/canvas/canvas-text-layer-image";

describe("canvas text layers", () => {
    test("contain fit keeps letterbox offsets for portrait images in landscape boxes", () => {
        expect(containFitFrame({ width: 720, height: 520 }, { width: 900, height: 1600 })).toEqual({
            left: 213.75,
            top: 0,
            width: 292.5,
            height: 520,
        });
    });

    test("stage frame falls back to the container when the image size is unknown", () => {
        expect(canvasTextLayerStageFrame({ containerWidth: 640, containerHeight: 480, imageWidth: 0, imageHeight: 0 })).toEqual({
            left: 0,
            top: 0,
            width: 640,
            height: 480,
        });
        expect(canvasTextLayerStageFrame({ containerWidth: 640, containerHeight: 480, imageWidth: 1000, imageHeight: 1000, fill: true })).toEqual({
            left: 0,
            top: 0,
            width: 640,
            height: 480,
        });
    });

    test("createCanvasTextLayer clamps unsafe style values", () => {
        const layer = createCanvasTextLayer({
            text: "标题",
            x: -20,
            fontSize: 9999,
            fontWeight: 450,
            opacity: 0,
            width: -5,
            align: "justify" as never,
            color: "red; background: url(https://example.com)",
        });

        expect(layer.x).toBe(0);
        expect(layer.width).toBe(0);
        expect(layer.fontSize).toBe(512);
        expect(layer.fontWeight).toBe(500);
        expect(layer.opacity).toBe(0.05);
        expect(layer.align).toBe("left");
        expect(layer.color).toBe("#ffffff");
        expect(layer.visible).toBe(true);
        expect(layer.strokeWidth).toBeUndefined();
    });

    test("decorations require both a colour and a size", () => {
        expect(createCanvasTextLayer({ strokeWidth: 4, shadowBlur: 12 }).strokeColor).toBeUndefined();
        expect(createCanvasTextLayer({ strokeColor: "#000000", strokeWidth: 4, shadowColor: "#000000", shadowBlur: 200 })).toMatchObject({
            strokeColor: "#000000",
            strokeWidth: 4,
            shadowColor: "#000000",
            shadowBlur: 96,
        });
    });

    test("readCanvasTextLayers drops malformed entries but keeps valid ones", () => {
        const layers = readCanvasTextLayers({
            textLayers: [{ id: "a", text: "标题", x: 10, y: 20, fontSize: 48, color: "#FF0000", align: "center", visible: false }, { text: "   " }, "nope", null, { text: "第二行" }],
        });

        expect(layers).toHaveLength(2);
        expect(layers[0]).toMatchObject({ id: "a", x: 10, y: 20, fontSize: 48, color: "#ff0000", align: "center", visible: false });
        expect(layers[1].id).toBe("text-layer-4");
        expect(layers[1].visible).toBe(true);
        expect(readCanvasTextLayers({ textLayers: "nope" })).toEqual([]);
        expect(readCanvasTextLayers(undefined)).toEqual([]);
    });

    test("detected lines become positioned layers when a bounding box is present", () => {
        const layers = canvasTextLayersFromDetectedLines([{ original: "限时特惠", text: "限时特惠", location: "左上方", bbox: { x: 0.1, y: 0.2, width: 0.5, height: 0.06 } }], { imageWidth: 1000, imageHeight: 2000 });

        expect(layers).toHaveLength(1);
        expect(layers[0]).toMatchObject({ id: "text-layer-0", x: 100, y: 400, width: 500, fontSize: 98, source: "ocr" });
    });

    test("detected lines without coordinates fall back to stacked placement", () => {
        const layers = canvasTextLayersFromDetectedLines(
            [
                { original: "第一行", text: "第一行", location: "顶部" },
                { original: "第二行", text: "", location: "中部" },
                { original: "   ", text: "", location: "底部" },
            ],
            { imageWidth: 1000, imageHeight: 2000 },
        );

        expect(layers).toHaveLength(2);
        expect(layers[0]).toMatchObject({ x: 80, y: 160, width: 840, fontSize: 120, align: "center", text: "第一行" });
        expect(layers[1]).toMatchObject({ x: 80, y: 340, text: "第二行" });
    });

    test("update keeps the identity and clamps through the same factory", () => {
        const [layer] = canvasTextLayersFromDetectedLines([{ original: "标题", text: "标题", location: "顶部" }], { imageWidth: 800, imageHeight: 600 });
        const updated = updateCanvasTextLayer([layer], layer.id, { fontSize: 4, color: "not-a-colour" });

        expect(updated).toHaveLength(1);
        expect(updated[0].id).toBe(layer.id);
        expect(updated[0].fontSize).toBe(6);
        expect(updated[0].color).toBe("#ffffff");
    });

    test("layer order follows the array and stays stable on reorder and remove", () => {
        const layers = ["a", "b", "c"].map((id) => createCanvasTextLayer({ id, text: id }));
        expect(reorderCanvasTextLayer(layers, "a", 2).map((layer) => layer.id)).toEqual(["b", "c", "a"]);
        expect(reorderCanvasTextLayer(layers, "c", 9).map((layer) => layer.id)).toEqual(["a", "b", "c"]);
        expect(reorderCanvasTextLayer(layers, "missing", 1)).toBe(layers);
        expect(removeCanvasTextLayer(layers, "b").map((layer) => layer.id)).toEqual(["a", "c"]);
    });

    test("bounding boxes in per-mille and pixel units are converted into image pixels", () => {
        const perMille = canvasTextLayersFromDetectedLines([{ original: "限时", text: "限时", location: "顶部", bbox: { x: 100, y: 200, width: 500, height: 60 } }], { imageWidth: 1000, imageHeight: 2000 });
        expect(perMille[0]).toMatchObject({ x: 100, y: 400, width: 500, fontSize: 98 });

        const pixels = canvasTextLayersFromDetectedLines([{ original: "标语", text: "标语", location: "左侧", bbox: { x: 800, y: 600, width: 1200, height: 300 } }], { imageWidth: 4000, imageHeight: 3000 });
        expect(pixels[0]).toMatchObject({ x: 800, y: 600, width: 1200, fontSize: 246 });

        expect(canvasTextLayersFromDetectedLines([{ original: "无效", text: "无效", location: "未知", bbox: { x: 5, y: 5, width: 0, height: 0 } }], { imageWidth: 1000, imageHeight: 1000 })[0]).toMatchObject({ x: 80, y: 80, align: "center" });
    });

    test("dragging clamps the layer inside the image bounds", () => {
        const layer = createCanvasTextLayer({ id: "drag", text: "拖拽" });
        expect(setCanvasTextLayerPosition([layer], "drag", 1200, -80, { width: 1000, height: 2000 })[0]).toMatchObject({ x: 1000, y: 0 });
        expect(setCanvasTextLayerPosition([layer], "drag", 300.4, 180.6)[0]).toMatchObject({ x: 300.4, y: 180.6 });
        expect(setCanvasTextLayerPosition([layer], "missing", 10, 10)).toEqual([layer]);
    });

    test("resize handle scales font size and fixed width together", () => {
        const layer = createCanvasTextLayer({ id: "scale", text: "缩放", fontSize: 100, width: 800 });
        expect(scaleCanvasTextLayerFont([layer], "scale", 1.2)[0]).toMatchObject({ fontSize: 120, width: 960 });
        expect(scaleCanvasTextLayerFont([layer], "scale", 99)[0].fontSize).toBe(CANVAS_TEXT_LAYER_LIMITS.maxFontSize);
        const list = [layer];
        expect(scaleCanvasTextLayerFont(list, "scale", 0)).toBe(list);
    });

    test("adding a layer stacks it below the previous ones and respects the cap", () => {
        const first = createCanvasTextLayerInImage([], { imageWidth: 1000, imageHeight: 2000, id: "manual-0" });
        expect(first).toHaveLength(1);
        expect(first[0]).toMatchObject({ id: "manual-0", x: 100, y: 240, width: 800, fontSize: 160, align: "center", source: "manual" });

        const second = createCanvasTextLayerInImage(first, { imageWidth: 1000, imageHeight: 2000, id: "manual-1" });
        expect(second).toHaveLength(2);
        expect(second[1].y).toBeGreaterThan(second[0].y);

        const full = Array.from({ length: CANVAS_TEXT_LAYER_LIMITS.maxLayers }, (_, index) => createCanvasTextLayer({ id: `full-${index}`, text: "x" }));
        expect(createCanvasTextLayerInImage(full, { imageWidth: 1000, imageHeight: 2000 })).toBe(full);
    });

    test("image source opens for storageKey-only nodes and keeps content as fallback url", () => {
        const storageOnly = canvasTextLayerImageSource({
            width: 480,
            height: 320,
            metadata: { storageKey: "resource:abc", naturalWidth: 1600, naturalHeight: 900 },
        });
        expect(storageOnly).toEqual({ hasSource: true, url: "", storageKey: "resource:abc", width: 1600, height: 900 });
    });

    test("image source prefers content and falls back to node size when natural size is missing", () => {
        const source = canvasTextLayerImageSource({
            width: 720,
            height: 405,
            metadata: { content: " blob:https://yingce/expired ", storageKey: "resource:abc" },
        });
        expect(source).toEqual({ hasSource: true, url: "blob:https://yingce/expired", storageKey: "resource:abc", width: 720, height: 405 });
    });

    test("image source stays closed without storageKey or content", () => {
        expect(canvasTextLayerImageSource(null).hasSource).toBe(false);
        expect(canvasTextLayerImageSource({ metadata: { content: "   " } }).hasSource).toBe(false);
        const previewOnly = canvasTextLayerImageSource({ metadata: { previewContent: "/api/preview.png" } });
        expect(previewOnly.hasSource).toBe(false);
        expect(previewOnly.url).toBe("/api/preview.png");
    });

    test("image candidates try the storageKey address first and drop duplicates", () => {
        expect(canvasTextLayerImageCandidates({ url: "/api/fallback.png", storageKey: "resource:abc" }, "https://cdn.example.com/abc.png")).toEqual(["https://cdn.example.com/abc.png", "/api/fallback.png"]);
        expect(canvasTextLayerImageCandidates({ url: "https://cdn.example.com/abc.png", storageKey: "resource:abc" }, "https://cdn.example.com/abc.png")).toEqual(["https://cdn.example.com/abc.png"]);
        expect(canvasTextLayerImageCandidates({ url: "/api/fallback.png", storageKey: "" }, "")).toEqual(["/api/fallback.png"]);
        expect(canvasTextLayerImageCandidates({ url: "", storageKey: "resource:abc" }, "blob:https://yingce/local")).toEqual(["blob:https://yingce/local"]);
        expect(canvasTextLayerImageCandidates({ url: "", storageKey: "" }, "")).toEqual([]);
    });
});
