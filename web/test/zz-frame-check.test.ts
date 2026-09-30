import { test, expect } from "bun:test";
import { applyFrameDrop, buildCanvasFrameDropIndex, findFrameDropTargetFromIndex } from "@/lib/canvas/canvas-frame";
import { CanvasNodeType } from "@/types/canvas";
const frame = { id: "f", type: CanvasNodeType.Frame, title: "f", position: { x: 0, y: 0 }, width: 760, height: 520, metadata: { frame: { collapsed: false, expandedWidth: 760, expandedHeight: 520 } } } as any;
for (const t of [CanvasNodeType.Text, CanvasNodeType.Markdown, CanvasNodeType.Audio, CanvasNodeType.Image]) {
    test(t, () => {
        const n = { id: "n", type: t, title: "n", position: { x: 100, y: 100 }, width: 340, height: 240, metadata: {} } as any;
        const target = findFrameDropTargetFromIndex(buildCanvasFrameDropIndex([frame, n]), [n], new Set(["n"]), { x: 0, y: 0 });
        console.log(t, target, applyFrameDrop([frame, n], new Set(["n"]), target).find((x) => x.id === "n")?.parentId);
    });
}
