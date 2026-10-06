import { nanoid } from "nanoid";
import { createCanvasNode } from "./canvas-project-domain";
import { commerceInputIdentity, manualCommerceScreens, validateCommerceInputs } from "./commerce-workflow";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "@/types/canvas";
import type { CommerceWorkflow } from "@/types/commerce-workflow";
import { commerceResultLayout } from "./commerce-result-layout";

/** Result groups are independent runs; changing the source never overwrites previous plans. */
export function createCommerceResult(source: CanvasNodeData, nodes: CanvasNodeData[], connections: CanvasConnection[], mode: NonNullable<CommerceWorkflow["runMode"]>) {
    const inputs = validateCommerceInputs(source, nodes, connections);
    const inputNodes = [...inputs.products, ...inputs.references, ...inputs.texts];
    const previous = nodes.filter((node) => node.metadata?.commerceWorkflow?.sourceNodeId === source.id);
    const y = Math.max(source.position.y, ...previous.map((node) => node.position.y + node.height + 80));
    const data: CommerceWorkflow = { ...structuredClone(source.metadata!.commerceWorkflow!), role: "result", sourceNodeId: source.id, runMode: mode,
        inputNodeIds: inputNodes.map((node) => node.id), inputIdentities: Object.fromEntries(inputNodes.map((node) => [node.id, commerceInputIdentity(node)])),
        screens: manualCommerceScreens(source, nodes, connections), pending: undefined, rawOutput: undefined, extraReferenceNodeIds: [], extraReferenceIdentities: {},
        autoGenerate: mode === "direct" ? "awaiting-plan" : undefined };
    const result = createCanvasNode(source.type, { x: 0, y: 0 }, { commerceWorkflow: data, model: source.metadata?.model,
        size: source.metadata?.size, quality: source.metadata?.quality, status: "idle" });
    result.position = { x: source.position.x + source.width + 120, y };
    const layout = commerceResultLayout(result, Math.min(3, data.screens.length));
    result.width = layout.width;
    result.height = layout.height;
    result.title = `${source.type === CanvasNodeType.ProductReplica ? "复刻结果" : "详情策划"} · ${data.productName || "产品"} · ${previous.length + 1}`;
    const edges: CanvasConnection[] = inputNodes.map((input) => ({ id: nanoid(), fromNodeId: input.id, toNodeId: result.id }));
    edges.push({ id: nanoid(), fromNodeId: source.id, toNodeId: result.id, relation: "batch-output" });
    return { result, edges };
}
