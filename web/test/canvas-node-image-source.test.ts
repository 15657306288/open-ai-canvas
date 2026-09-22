import { describe, expect, test } from "bun:test";

// 弹窗底图解析单测：来源优先级、候选去重、真实像素解码。

import { canvasDialogImageInput, canvasNodeImageCandidates, canvasNodeImageSource, decodeCanvasNodeImage, loadCanvasNodeImage } from "@/lib/canvas/canvas-node-image-source";

/** 用假的 Image 走真实的 onload/onerror 分支，验证解码结果确实来自解码而非节点宽高。 */
function stubImage(size: { width: number; height: number } | null) {
    const original = globalThis.Image;
    globalThis.Image = class {
        onload: (() => void) | null = null;
        onerror: (() => void) | null = null;
        decoding = "";
        naturalWidth = size?.width ?? 0;
        naturalHeight = size?.height ?? 0;
        set src(_value: string) {
            queueMicrotask(() => (size ? this.onload?.() : this.onerror?.()));
        }
    } as unknown as typeof Image;
    return () => {
        globalThis.Image = original;
    };
}

describe("canvas node image source", () => {
    test("只有 storageKey 的节点也能作为底图来源打开，并优先用记录的原始像素尺寸", () => {
        const source = canvasNodeImageSource({
            width: 480,
            height: 320,
            metadata: { storageKey: "resource:abc", naturalWidth: 1600, naturalHeight: 900 },
        });
        expect(source).toEqual({ hasSource: true, url: "", storageKey: "resource:abc", width: 1600, height: 900 });
    });

    test("content 作为兜底地址保留，缺少原始尺寸时退回节点尺寸", () => {
        const source = canvasNodeImageSource({
            width: 720,
            height: 405,
            metadata: { content: " blob:https://yingce/expired ", storageKey: "resource:abc" },
        });
        expect(source).toEqual({ hasSource: true, url: "blob:https://yingce/expired", storageKey: "resource:abc", width: 720, height: 405 });
    });

    test("两者皆无时判据为 false，previewContent 只作地址兜底", () => {
        expect(canvasNodeImageSource(null).hasSource).toBe(false);
        expect(canvasNodeImageSource({ metadata: { content: "   " } }).hasSource).toBe(false);
        const previewOnly = canvasNodeImageSource({ metadata: { previewContent: "/api/preview.png" } });
        expect(previewOnly.hasSource).toBe(false);
        expect(previewOnly.url).toBe("/api/preview.png");
    });

    test("候选地址按 storageKey 解析结果优先并去重", () => {
        expect(canvasNodeImageCandidates({ url: "/api/fallback.png", storageKey: "resource:abc" }, "https://cdn.example.com/abc.png")).toEqual(["https://cdn.example.com/abc.png", "/api/fallback.png"]);
        expect(canvasNodeImageCandidates({ url: "https://cdn.example.com/abc.png", storageKey: "resource:abc" }, "https://cdn.example.com/abc.png")).toEqual(["https://cdn.example.com/abc.png"]);
        expect(canvasNodeImageCandidates({ url: "", storageKey: "" }, "")).toEqual([]);
    });

    test("弹窗入参兼容老 dataUrl 直传，优先用新的解析结果", () => {
        expect(canvasDialogImageInput("data:image/png;base64,AAAA")).toEqual({ url: "data:image/png;base64,AAAA", storageKey: "", width: 0, height: 0 });
        expect(canvasDialogImageInput("data:image/png;base64,AAAA", { url: "", storageKey: "resource:abc", width: 1200, height: 800 })).toEqual({ url: "", storageKey: "resource:abc", width: 1200, height: 800 });
        expect(canvasDialogImageInput()).toBeNull();
    });

    test("解码拿真实像素尺寸，解码失败给出错误", async () => {
        const restore = stubImage({ width: 1024, height: 768 });
        try {
            expect(await decodeCanvasNodeImage("https://cdn.example.com/abc.png")).toEqual({ width: 1024, height: 768 });
        } finally {
            restore();
        }
        const restoreFailure = stubImage(null);
        try {
            await expect(decodeCanvasNodeImage("broken://image")).rejects.toThrow("图片加载失败");
        } finally {
            restoreFailure();
        }
    });

    test("storageKey-only 节点解析成资源地址后按真实像素返回", async () => {
        const restore = stubImage({ width: 1600, height: 900 });
        try {
            const loaded = await loadCanvasNodeImage({ url: "", storageKey: "resource:abc" });
            expect(loaded.url).toContain("/resources/abc/file");
            expect(loaded.width).toBe(1600);
            expect(loaded.height).toBe(900);
        } finally {
            restore();
        }
    });

    test("storageKey 地址解码失败时退回 content 兜底地址", async () => {
        const restore = stubImage({ width: 800, height: 600 });
        try {
            const loaded = await loadCanvasNodeImage({ url: "https://cdn.example.com/fallback.png", storageKey: "image:local:missing" });
            expect(loaded.url).toBe("https://cdn.example.com/fallback.png");
            expect(loaded.width).toBe(800);
        } finally {
            restore();
        }
    });

    test("没有任何可用地址时明确报错，而不是留一块空背景", async () => {
        await expect(loadCanvasNodeImage({ url: "", storageKey: "" })).rejects.toThrow("底图加载失败");
    });
});
