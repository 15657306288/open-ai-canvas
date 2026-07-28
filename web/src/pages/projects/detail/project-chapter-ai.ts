import { runBackendCanvasGenerationTask } from "@/lib/canvas/canvas-project-generation";
import { parseCharacterBreakdown, type CharacterBreakdown } from "@/lib/canvas/canvas-character-reference";
import type { AiConfig } from "@/stores/use-config-store";

type ChapterAnalysisInput = {
    projectId: string;
    projectName: string;
    chapterId: string;
    chapterTitle: string;
    sourceText: string;
    projectStyle: string;
    config: AiConfig;
};

export async function extractChapterCharacters(input: ChapterAnalysisInput): Promise<CharacterBreakdown[]> {
    const prompt = [
        `请从短剧项目《${input.projectName}》的章节“${input.chapterTitle}”中提取需要建立可复用视觉资产的角色。`,
        "提取本章实际出场、发言或对剧情产生明确作用，并且后续制作中需要保持视觉或声音一致的角色；忽略系统播报、纯物件、无身份群众、不影响剧情的一次性路人，以及只在单个镜头出现且没有持续角色价值的匿名录像或历史影像人物。合并同一角色的姓名、专属称谓和别名。",
        "每个角色必须填写剧情定位，以及至少三项可执行的稳定设定；正文未明确的信息要明确写“正文未明确”，不得留空，也不能改变人物关系和时代背景。角色名称必须来自正文中的姓名、昵称或稳定称谓，不得自行编造编号式名称。",
        `【项目画风】\n${input.projectStyle || "项目尚未指定画风，保持视觉描述中性、可执行。"}`,
        "只返回 JSON，不要 Markdown 或解释。JSON 结构必须严格为：",
        '{"characters":[{"name":"角色名","aliases":["唯一别名"],"role":"剧情定位与人物关系","appearance":"年龄、脸型、五官、肤色、发型等稳定外貌","clothing":"固定服装版型、颜色、纹样和材质","physique":"身高、头身、体型和体态","personality":"稳定气质与表演基线","props":"固定道具及佩戴位置，没有则为空字符串","consistencyPrompt":"跨图片和镜头必须保持不变的角色约束","multiViewPrompt":"正面、侧面、背面转面展示需要强调的结构细节","voiceLanguage":"语言、口音和表达习惯","voiceAge":"适合选角的声音年龄感","voiceTimbre":"音色、语速、力度和声音气质"}]}',
        "【章节正文】",
        input.sourceText,
    ].join("\n\n");
    const result = await runProjectTextTask(input, "chapter_character_breakdown", prompt);
    return parseCharacterBreakdown(result);
}

async function runProjectTextTask(input: ChapterAnalysisInput, operation: string, prompt: string) {
    const model = input.config.textModel || input.config.model;
    const result = await runBackendCanvasGenerationTask({
        projectId: input.projectId,
        nodeId: `${operation}:${input.chapterId}`,
        mode: "text",
        prompt,
        config: { ...input.config, model },
        metadata: { domainProjectId: input.projectId, chapterId: input.chapterId, operation },
    });
    if (!result.text?.trim()) throw new Error("模型没有返回可用结果");
    return result.text;
}
