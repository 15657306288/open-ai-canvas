import { runBackendCanvasGenerationTask } from "@/lib/canvas/canvas-project-generation";
import type { AiConfig } from "@/stores/use-config-store";

type ProjectStylePrompt = { id: string; title: string; prompt: string };

export async function generateCharacterTurnaround(input: { projectId: string; assetId: string; versionId: string; name: string; definition: Record<string, unknown>; projectStyle?: ProjectStylePrompt; config: AiConfig }) {
    const prompt = characterTurnaroundPrompt(input.name, input.definition, input.projectStyle);
    await runBackendCanvasGenerationTask({
        projectId: input.projectId,
        nodeId: `character-turnaround:${input.assetId}`,
        mode: "image",
        prompt,
        config: { ...input.config, model: input.config.imageModel || input.config.model, count: "1" },
        metadata: { operation: "character_turnaround", characterAssetId: input.assetId, stylePresetId: input.projectStyle?.id, resolvedCharacterVersions: [{ assetId: input.assetId, versionId: input.versionId }] },
    });
}

export function characterTurnaroundPrompt(name: string, definition: Record<string, unknown>, projectStyle?: ProjectStylePrompt) {
    const visual = [definition.role, definition.appearance, definition.physique, definition.clothing, definition.personality, definition.props, definition.consistencyPrompt, definition.multiViewPrompt]
        .map((value) => String(value || "").trim())
        .filter(Boolean)
        .join("；");
    if (!visual) throw new Error("请先填写剧情定位、角色外貌、体型或服装，再初始化三视图");
    return [
        `为角色“${name}”制作专业人物三视图设定表。`,
        "画面严格分成三个等宽竖向区域，从左到右依次为正面全身、右侧面全身、背面全身。",
        "三个视角必须是同一个人、同一服装、同一发型、同一体型和同一比例，站立中性姿势，完整显示头顶到脚底。",
        "使用纯净中性浅色背景和均匀设定稿光线；背景只负责分离轮廓，不得改变项目画风的绘画或渲染媒介。",
        "不添加文字、边框、道具说明、表情变化或额外人物。",
        characterStyleConstraint(projectStyle),
        `角色设定：${visual}`,
    ].filter(Boolean).join("\n");
}

function characterStyleConstraint(projectStyle?: ProjectStylePrompt) {
    if (!projectStyle) return "";
    // 三视图只继承角色资产相关规范，避免把建筑、运镜等项目规则错误塞进静态设定表。
    const characterSections = new Set(["项目定位", "项目色彩系统", "角色设计系统", "服饰与材质系统", "资产一致性", "全局禁用"]);
    const rules = projectStyle.prompt.split("\n").filter((line) => {
        const title = line.match(/^【([^】]+)】/)?.[1];
        return title ? characterSections.has(title) : false;
    });
    return [
        `项目画风：${projectStyle.title}。角色造型、配色、服装材质与最终渲染媒介必须遵循以下规范：`,
        ...rules,
    ].join("\n");
}
