import { formatCredits } from "@/constant/credits";
import { modelCapabilityConfigFor } from "@/lib/model-capabilities";
import { modelRequestOptions, type ModelRequirements } from "@/lib/model-selection";
import { priceTiersForCurrentSelection, requestCreditCost } from "@/lib/model-pricing";
import { modelOptionName, resolveModelChannel, type AiConfig } from "@/stores/use-config-store";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "@/types/canvas";
import type { CommerceScreen } from "@/types/commerce-workflow";
import { batchGenerationRows, batchRunningRowIds } from "./canvas-batch-table";
import { buildGenerationConfig } from "./canvas-project-generation";
import { commerceScreenImageSize } from "./commerce-image-size";
import { commerceBatchTable, commerceInputs, commercePlanningImageIds } from "./commerce-workflow";

export type CommercePrice = { microcredits: number | null; description: string };
export type CommerceImagePrice = CommercePrice & { id: string; size: string };
export type CommercePricing = { images: CommerceImagePrice[]; remaining: CommerceImagePrice[]; text: CommercePrice; ocr: CommercePrice };

const unavailable = (): CommercePrice => ({ microcredits: null, description: "当前规格暂无可用标价" });

function modelPrice(config: AiConfig, capability: "image" | "text", requirements: ModelRequirements): CommercePrice {
    const channel = resolveModelChannel(config, config.model);
    if (channel.scope !== "system") return { microcredits: null, description: "自定义渠道费用另计" };
    const cost = channel.modelCosts?.find((item) => item.model === modelOptionName(config.model));
    if (!cost) return unavailable();
    const tiers = cost.pricePolicy === "channel" ? priceTiersForCurrentSelection(cost.logicalPriceTiers || [], capability, config, requirements) : [cost];
    if (!tiers.length) return unavailable();
    const first = tiers[0];
    if (first.billingMode === "token") {
        const input = first.inputTokenPriceMicrocredits, output = first.outputTokenPriceMicrocredits;
        const known = [input, output].every((value) => typeof value === "number" && Number.isFinite(value) && value >= 0)
            && tiers.every((tier) => tier.billingMode === "token" && tier.inputTokenPriceMicrocredits === input && tier.outputTokenPriceMicrocredits === output);
        return { microcredits: null, description: known
            ? `按实际 Token 另计（输入 ${formatCredits(input!)} / 输出 ${formatCredits(output!)} 积分/百万 Token）`
            : "按实际 Token 另计，当前线路单价待确认" };
    }
    if (first.billingMode !== "fixed_request") return unavailable();
    const amount = requestCreditCost({ channelMode: "remote", modelCosts: channel.modelCosts, model: modelOptionName(config.model), count: 1, capability, config, requirements });
    if (amount === null || !Number.isFinite(amount) || amount < 0) return unavailable();
    const microcredits = Math.round(amount * 1_000_000);
    return { microcredits, description: `${formatCredits(microcredits)} 积分/${capability === "image" ? "张" : "次"}` };
}

/** Read the published model prices only; never submit quotes, tasks, or billing writes. */
export function commercePricing(node: CanvasNodeData, nodes: CanvasNodeData[], connections: CanvasConnection[], config: AiConfig, planningScreenId?: string): CommercePricing {
    const data = node.metadata!.commerceWorkflow!;
    const inputs = commerceInputs(node, nodes, connections);
    const replica = node.type === CanvasNodeType.ProductReplica;
    const screens: CommerceScreen[] = data.role === "result" ? data.screens : replica
        ? inputs.references.map((template) => ({ id: template.id, templateNodeId: template.id, title: "", copy: "", prompt: "" }))
        : Array.from({ length: Number.isInteger(data.screenCount) && data.screenCount >= 1 && data.screenCount <= 30 ? data.screenCount : 0 }, (_, index) => ({ id: `planned-${index}`, title: "", copy: "", prompt: "" }));
    const model = node.metadata?.model || config.imageModel || config.model;
    const imageConfig = { ...config, model, imageModel: model, size: node.metadata?.size || config.size, quality: node.metadata?.quality || config.quality, count: "1" };
    const profile = modelCapabilityConfigFor(imageConfig, model).image;
    const pricesBySpec = new Map<string, Omit<CommerceImagePrice, "id">>();
    const images = screens.map((screen): CommerceImagePrice => {
        try {
            const size = commerceScreenImageSize(profile, imageConfig.size, node, screen, nodes);
            const ids = new Set([...inputs.products.map((input) => input.id), ...(replica ? [screen.templateNodeId!, ...(screen.referenceNodeIds || [])] : [])]);
            // Commerce always edits product images; drafts may not have their references connected yet.
            const requirements: ModelRequirements = { capability: "image", input: { imageCount: Math.max(1, ids.size), textCount: 0, videoCount: 0, audioCount: 0, characterCount: 0 } };
            const specKey = JSON.stringify([size, requirements.input?.imageCount]);
            const cached = pricesBySpec.get(specKey);
            if (cached) return { id: screen.id, ...cached };
            const initialModel = buildGenerationConfig(imageConfig, undefined, "image").model;
            const resolved = buildGenerationConfig(imageConfig, { ...node, type: CanvasNodeType.Image, metadata: { model: initialModel, size, quality: imageConfig.quality, count: 1 } }, "image", requirements);
            const price = { size: resolved.size, ...modelPrice(resolved, "image", { ...requirements, options: modelRequestOptions(resolved, "image") }) };
            pricesBySpec.set(specKey, price);
            return { id: screen.id, ...price };
        } catch {
            return { id: screen.id, size: screen.size || imageConfig.size, ...unavailable() };
        }
    });
    let remaining = images;
    if (data.role === "result") {
        try {
            // Rebuild only in memory: edited prompts may invalidate an old completed output.
            const batchTable = commerceBatchTable(node, nodes, connections);
            const ids = new Set(batchGenerationRows({ ...node, metadata: { ...node.metadata, batchTable } }, nodes).map((row) => row.id));
            remaining = images.filter((image) => ids.has(image.id));
        } catch {
            // 策划未完成时重建会抛错（缺提示词）。此时仍需排除在途行与已出成品的屏，
            // 但结果组未必已持久化 batchTable，因此逐屏按 batch-output 归属边找结果卡。
            const running = batchRunningRowIds(node, nodes);
            remaining = images.filter((image) => {
                const outputId = node.metadata?.batchTable?.rows.find((row) => row.id === image.id)?.outputNodeId
                    ?? connections.find((connection) => connection.relation === "batch-output" && connection.storyboardRowId === image.id)?.toNodeId;
                const output = nodes.find((item) => item.id === outputId);
                return !running.has(image.id) && !(output?.metadata?.content || output?.metadata?.storageKey);
            });
        }
    }
    const textPrice = (imageCount: number, textCount: number) => {
        try {
            const requirements: ModelRequirements = { capability: "text", input: { imageCount, textCount, videoCount: 0, audioCount: 0, characterCount: 0 } };
            const resolved = buildGenerationConfig(config, { ...node, type: CanvasNodeType.Text, metadata: { model: data.textModel || config.textModel } }, "text", requirements);
            return modelPrice(resolved, "text", requirements);
        } catch { return unavailable(); }
    };
    const planningScreens = screens.filter((screen) => !planningScreenId || screen.id === planningScreenId);
    const text = textPrice(commercePlanningImageIds(node, nodes, connections, planningScreens).length, inputs.texts.length);
    return { images, remaining, text, ocr: textPrice(1, 0) };
}

export function commerceImageTotal(images: CommerceImagePrice[]) {
    return images.every((image) => image.microcredits !== null) ? images.reduce((total, image) => total + image.microcredits!, 0) : null;
}

export function commerceImagePriceLabel(images: CommerceImagePrice[]) {
    if (!images.length) return "0 张 · 0 积分";
    const total = commerceImageTotal(images);
    if (total === null) {
        const reasons = [...new Set(images.filter((image) => image.microcredits === null).map((image) => image.description))];
        return `${images.length} 张 · ${reasons.join("；")}（不显示不完整合计）`;
    }
    const uniform = images.every((image) => image.microcredits === images[0].microcredits);
    return uniform ? `${images.length} 张 × ${formatCredits(images[0].microcredits!)} 积分/张 = ${formatCredits(total)} 积分`
        : `${images.length} 张 · 按各屏规格合计 ${formatCredits(total)} 积分`;
}

export function commerceActionPriceLabel(pricing: CommercePricing, action: string, screenId?: string) {
    if (action === "manual") return "自定义策划免费建组，出图另计";
    if (["plan", "replan", "ocr"].includes(action)) return `${action === "ocr" ? "文字识别" : "AI 策划"}：${(action === "ocr" ? pricing.ocr : pricing.text).description}`;
    const images = screenId ? pricing.images.filter((image) => image.id === screenId) : pricing.remaining;
    const imageLabel = `预计图片费用：${commerceImagePriceLabel(images)}`;
    if (action !== "direct") return imageLabel;
    const total = commerceImageTotal(images);
    return `${imageLabel}；AI 策划：${pricing.text.description}${total !== null && pricing.text.microcredits !== null ? `；两阶段预计合计 ${formatCredits(total + pricing.text.microcredits)} 积分` : "；图片合计不包含策划费用"}`;
}
