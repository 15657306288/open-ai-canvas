import { expect, spyOn, test } from "bun:test";
import { commerceCompletedImages, createCommerceImageAssembly } from "@/lib/canvas/commerce-image-assembly";
import { newCommerceWorkflow } from "@/lib/canvas/commerce-workflow";
import { isolateCopiedNodeMetadata } from "@/lib/canvas/canvas-node-copy";
import { ensureMediaNodeMinimumSize } from "@/lib/canvas/canvas-node-size";
import { removeCanvasNodes } from "@/lib/canvas/canvas-project-domain";
import { getFrameChildren, isNodeHiddenByCollapsedFrame } from "@/lib/canvas/canvas-frame";
import { ensureRemoteResourceReferences } from "@/services/user-data-sync-media";
import * as resources from "@/services/api/resources";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";

function fixture() {
    const source: CanvasNodeData = { id: "result", type: CanvasNodeType.ProductDetail, title: "策划结果", width: 972, height: 900, position: { x: 0, y: 0 }, metadata: {
        commerceWorkflow: { ...newCommerceWorkflow(), productName: "产品", role: "result", screens: ["second", "first"].map((id) => ({ id, title: id, copy: "", prompt: "" })) },
        batchTable: { operation: "creative", concurrency: 2, rows: ["first", "second"].map((id) => ({ id, prompt: "", enabled: true, inputNodeIds: [], outputNodeId: `out-${id}` })) },
    } };
    const outputs: CanvasNodeData[] = ["first", "second"].map((id, index) => ({ id: `out-${id}`, type: CanvasNodeType.Image, title: id,
        position: { x: 1200, y: index * 400 }, width: 320, height: 200, metadata: {
            status: "success", storageKey: `resource:${id}`, content: `https://example.invalid/${id}.png?signature=temporary`,
            naturalWidth: 1200, naturalHeight: index ? 2400 : 600, taskId: `task-${id}`, taskStatus: "succeeded", batchRootId: "old-root", generationSpec: { prompt: "old" } as never,
        } }));
    return { source, outputs, nodes: [source, ...outputs] };
}

test("引用拼合按卡片顺序、等宽原比例无缝排列，保留原结果和资源身份", () => {
    const { source, nodes } = fixture(); const before = JSON.stringify(nodes);
    const { frame, images } = createCommerceImageAssembly(source, nodes);
    expect(images.map((image) => image.metadata?.storageKey)).toEqual(["resource:second", "resource:first"]);
    expect(images.map((image) => image.metadata?.content)).toEqual(["resource:second", "resource:first"]);
    expect(images.map((image) => [image.width, image.height])).toEqual([[420, 840], [420, 210]]);
    expect(images[1].position.y).toBe(images[0].position.y + images[0].height);
    expect(images[0].position.x).toBe(images[1].position.x);
    expect(frame.type).toBe(CanvasNodeType.Frame); expect(frame.metadata?.imageAssembly).toBe("vertical");
    expect(frame.metadata?.referenceAssetNodeIds).toEqual(images.map((image) => image.id));
    for (const image of images) {
        expect(image.parentId).toBe(frame.id); expect(image.metadata?.referenceSetId).toBe(frame.id);
        expect(image.metadata?.taskId).toBeUndefined(); expect(image.metadata?.batchRootId).toBeUndefined(); expect(image.metadata?.generationSpec).toBeUndefined();
        expect(image.metadata?.manualSize).toBe(true); expect(ensureMediaNodeMinimumSize(image)).toBe(image);
    }
    expect(JSON.stringify(nodes)).toBe(before);
});

test("保存与刷新保留原资源、顺序和组尺寸，不调用资源上传", async () => {
    const { source, nodes } = fixture(); const { frame, images } = createCommerceImageAssembly(source, nodes);
    const upload = spyOn(resources, "uploadResourceFile").mockRejectedValue(new Error("不应上传"));
    try {
        const saved = await ensureRemoteResourceReferences([frame, ...images]);
        const restored = JSON.parse(JSON.stringify(saved)) as CanvasNodeData[];
        expect(upload).not.toHaveBeenCalled(); expect(getFrameChildren(frame.id, restored)).toHaveLength(2);
        expect(restored[0].metadata?.imageAssembly).toBe("vertical");
        expect(restored.slice(1).map((image) => image.metadata?.storageKey)).toEqual(images.map((image) => image.metadata?.storageKey));
        expect(restored.slice(1).map((image) => [image.position, image.width, image.height])).toEqual(images.map((image) => [image.position, image.width, image.height]));
        expect(JSON.stringify(restored)).not.toContain("signature=temporary");
    } finally { upload.mockRestore(); }
});

test("原节点删除或资源替换不改变已创建的拼合快照", () => {
    const { source, nodes, outputs } = fixture(); const { frame, images } = createCommerceImageAssembly(source, nodes);
    outputs[0].metadata!.storageKey = "resource:replacement";
    const removed = removeCanvasNodes([...nodes, frame, ...images], new Set(outputs.map((image) => image.id)));
    expect(getFrameChildren(frame.id, removed.nodes).map((image) => image.metadata?.storageKey)).toEqual(["resource:second", "resource:first"]);
});

test("复制和折叠采用原生分组规则，复制资源引用不继承任务", () => {
    const { source, nodes } = fixture(); const { frame, images } = createCommerceImageAssembly(source, nodes);
    const all = [frame, ...images]; const ids = new Map(all.map((node) => [node.id, `copy-${node.id}`]));
    const copies = all.map((node) => ({ ...node, id: ids.get(node.id)!, parentId: node.parentId ? ids.get(node.parentId) : undefined, metadata: isolateCopiedNodeMetadata(node, ids) }));
    expect(copies[0].metadata.imageAssembly).toBe("vertical");
    expect(copies[0].metadata.referenceAssetNodeIds).toEqual(copies.slice(1).map((node) => node.id));
    expect(getFrameChildren(copies[0].id, copies)).toHaveLength(2);
    copies[0].metadata.frame!.collapsed = true;
    expect(isNodeHiddenByCollapsedFrame(copies[1], copies)).toBe(true);
    expect(frame.metadata?.frame?.collapsed).toBe(false);
});

test("重复拼合保留旧组并避让同列内容", () => {
    const { source, nodes } = fixture(); const first = createCommerceImageAssembly(source, nodes);
    const second = createCommerceImageAssembly(source, [...nodes, first.frame, ...first.images]);
    expect(second.frame.id).not.toBe(first.frame.id);
    expect(second.frame.position.y).toBeGreaterThanOrEqual(first.frame.position.y + first.frame.height + 80);
});

test("没有原始尺寸时采用已有显示比例，不下载原图测量", () => {
    const { source, nodes, outputs } = fixture();
    for (const output of outputs) { delete output.metadata!.naturalWidth; delete output.metadata!.naturalHeight; }
    expect(createCommerceImageAssembly(source, nodes).images.map((image) => image.height)).toEqual([262.5, 262.5]);
});

test("缺失、未成功、生成中、非图片、空资源或空结果整组拒绝，不截断拼合", () => {
    const mutations: Array<(source: CanvasNodeData, outputs: CanvasNodeData[]) => void> = [
        (_source, outputs) => { outputs[0].id = "missing"; },
        (_source, outputs) => { outputs[0].metadata!.status = "error"; },
        (_source, outputs) => { outputs[0].metadata!.taskStatus = "running"; },
        (_source, outputs) => { outputs[0].type = CanvasNodeType.Video; },
        (_source, outputs) => { outputs[0].metadata!.storageKey = ""; outputs[0].metadata!.content = ""; },
        (_source, outputs) => { outputs[0].metadata!.fileUpload = "uploading"; },
        (source) => { source.metadata!.commerceWorkflow!.screens = []; },
        (source) => { source.metadata!.commerceWorkflow!.role = "source"; },
    ];
    for (const mutate of mutations) {
        const { source, outputs, nodes } = fixture(); mutate(source, outputs);
        expect(() => commerceCompletedImages(source, nodes)).toThrow();
    }
});

test("异常比例和过长拼合显式失败", () => {
    const { source, outputs, nodes } = fixture();
    outputs[0].metadata!.naturalWidth = 1; outputs[0].metadata!.naturalHeight = 10000;
    expect(() => createCommerceImageAssembly(source, nodes)).toThrow("过长");
    delete outputs[0].metadata!.naturalWidth; delete outputs[0].metadata!.naturalHeight; outputs[0].width = NaN;
    expect(() => createCommerceImageAssembly(source, nodes)).toThrow("比例未知");
});
