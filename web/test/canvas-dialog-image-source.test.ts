import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

function readSource(relativePath: string) {
    return readFileSync(resolve(import.meta.dir, relativePath), "utf8");
}

const mediaDialogs = readSource("../src/pages/canvas/canvas-project-media-dialogs.tsx");
const mediaTools = readSource("../src/pages/canvas/use-canvas-media-tools.ts");

const dialogNames = [
    "CanvasNodeCropDialog",
    "CanvasNodeMaskEditDialog",
    "CanvasNodeUpscaleDialog",
    "CanvasNodeImageEditDialog",
    "CanvasNodeLayerDecompositionDialog",
    "CanvasNodeTextEditDialog",
];

describe("canvas media dialogs share one image source", () => {
    test("六个图片工具弹窗都收到 storageKey 优先的解析结果，不再直塞 content", () => {
        for (const name of dialogNames) {
            expect(mediaDialogs).toContain(`<${name} image=`);
        }
        expect(mediaDialogs).not.toContain("dataUrl={");
        expect(mediaDialogs).not.toContain("metadata?.content ?");
    });

    test("弹窗打开判据统一成 hasSource，只有 storageKey 的云端节点也能打开", () => {
        expect(mediaDialogs).toContain("function dialogImage(node: CanvasNodeData | null): CanvasNodeImageSource | null");
        expect(mediaDialogs).toContain("return source.hasSource ? source : null;");
    });

    test("本地裁剪与放大在只有 storageKey 时也能取到字节", () => {
        expect(mediaTools).toContain("cropDataUrl(await readCanvasNodeImageBytes(source), crop)");
        expect(mediaTools).toContain("upscaleDataUrl(await readCanvasNodeImageBytes(source), params)");
    });

    test("生成类工具（蒙版/图片编辑/图层拆分/文字编辑/标记）的入口判据同样放宽到 storageKey", () => {
        // 蒙版、文字编辑、标记走无参数判据；图片编辑与图层拆分还要校验提示词。
        expect(mediaTools.match(/canvasNodeImageSource\(node\)\.hasSource/g)?.length ?? 0).toBeGreaterThanOrEqual(5);
        expect(mediaTools).toContain("if (!canvasNodeImageSource(node).hasSource || !payload.prompt.trim()) return;");
        expect(mediaTools).toContain("if (!canvasNodeImageSource(node).hasSource) {");
    });
});
