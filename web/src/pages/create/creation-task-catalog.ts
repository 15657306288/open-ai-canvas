// 任务卡目录：首页「你想做什么？」入口的参数化配置。
//
// 复刻成熟电商 AI 平台的工具箱表单契约（槽位 + 模块勾选 + 下拉选项），
// 但最终结论直接喂给已有 submit() 链路：用户全程不接触提示词，提示词由 buildCreationTaskPrompt 组装。
// 字段命名对齐对方的 reference_solt_config / input_contract 两套结构，不自造第三种命名。

import { CREATIVE_SCENARIOS, type CreativeScenarioId } from "@/lib/creation/creative-scenarios";
import type { CreationMode } from "./creation-assets";

export type CreationTaskMediaKind = "image" | "video" | "audio";

export type CreationTaskSlot = {
    key: string;
    name: string;
    /** 该槽位接受的媒体类型；为空表示按当前模式放宽。 */
    media: CreationTaskMediaKind[];
    required: boolean;
    hint: string;
};

export type CreationTaskModule = {
    key: string;
    name: string;
    /** 单模块默认出图张数，面板里可改。 */
    count: number;
    /** 产出规格：这张图凭什么合格，写进提示词，用户看不到。 */
    spec: string;
};

export type CreationTaskOption = { value: string; label: string };

export type CreationTaskChoice = { key: string; label: string; options: CreationTaskOption[] };

export type CreationTask = {
    key: string;
    name: string;
    /** 一句话说明，直接放卡片上。 */
    detail: string;
    mode: CreationMode;
    scenario: CreativeScenarioId;
    slots: CreationTaskSlot[];
    /** 商详套图这类「一次出多张」的任务才需要模块勾选。 */
    modules?: CreationTaskModule[];
    choices?: CreationTaskChoice[];
    /** 补充说明输入框占位文案；undefined 表示该任务不提供这个入口。 */
    notesHint?: string;
    /**
     * 可选技能配方（backend seed/presets.json 的 presetId），按任务媒体类型圈定。
     * 全站只有几十个配方，后端的 scene 只区分内容领域（短剧/电商）不区分媒体，
     * 所以视频配方不得混进图片任务，反之亦然；第一个是默认值。
     */
    presetIds: string[];
};

// 店面拆解的材料全是图，但产出是结构化文本，因此走文本模式的多模态分析。
const teardownSlots = (): CreationTaskSlot[] => [
    { key: "storefront", name: "店面截图", media: ["image"], required: true, hint: "店铺首页或主图区截图，你想对标的那家。" },
    { key: "samples", name: "竞品图片", media: ["image"], required: false, hint: "主图、详情页或模特图，多张越多结论越稳，可选。" },
    { key: "notes", name: "补充样本", media: ["image"], required: false, hint: "其他视觉参考图，可留空。" },
];

const imageSlots = (): CreationTaskSlot[] => [
    { key: "product", name: "添加商品", media: ["image"], required: true, hint: "商品主体图，必填。外观、颜色与 logo 需保持一致。" },
    { key: "model", name: "添加人物", media: ["image"], required: false, hint: "模特或人物参考，可留空。" },
    { key: "background", name: "添加背景", media: ["image"], required: false, hint: "背景或场景参考，可留空。" },
];

const videoSlots = (): CreationTaskSlot[] => [
    { key: "product", name: "添加商品", media: ["image", "video"], required: true, hint: "商品素材，必填。" },
    { key: "model", name: "添加人物", media: ["image", "video"], required: false, hint: "出镜人物或模特，可留空。" },
    { key: "scene", name: "添加场景", media: ["image", "video"], required: false, hint: "场景参考，可留空。" },
    { key: "sound", name: "添加声音", media: ["audio"], required: false, hint: "配音或参考音频，可留空。" },
];

const languageChoice: CreationTaskChoice = { key: "language", label: "图片文案", options: [{ value: "中文", label: "中文" }, { value: "英文", label: "英文" }] };

// 平台选项不只是下拉：它决定比例、最小尺寸与套图顺序，会写进内部提示词供技能参考。
const platformChoice: CreationTaskChoice = {
    key: "platform",
    label: "目标平台",
    options: [
        { value: "淘宝/天猫", label: "淘宝/天猫" },
        { value: "京东", label: "京东" },
        { value: "拼多多", label: "拼多多" },
        { value: "抖音电商", label: "抖音电商" },
        { value: "小红书", label: "小红书" },
        { value: "Amazon", label: "Amazon" },
        { value: "独立站", label: "独立站" },
    ],
};

// 品类选项决定品类打法是否适用：皮草不能用食品口碵，生鲜不能用皮草绘图逻辑。
const categoryChoice: CreationTaskChoice = {
    key: "category",
    label: "商品类目",
    options: [
        { value: "女装皮草", label: "女装皮草" },
        { value: "食品三农生鲜", label: "食品三农生鲜" },
        { value: "其他品类", label: "其他品类" },
    ],
};

// 图片比例不放在 choices 里：面板直接用当前模型真实支持的比例，
// 避免出现一个和生成参数脱钩的假下拉。
const imageChoices = () => [platformChoice, categoryChoice, languageChoice];

export const CREATION_TASKS: CreationTask[] = [
    {
        key: "ecommerce-visual-teardown",
        name: "店面视觉拆解",
        detail: "反推竞品配色、版式与光影",
        mode: "text",
        scenario: "ecommerce",
        slots: teardownSlots(),
        notesHint: "店铺名称、主打品类、想学这家的哪一点、不想学的部分",
        presetIds: ["ecom-visual-teardown"],
    },
    {
        key: "ecommerce-image-set",
        presetIds: ["ecom-shelf-ready", "ecom-image"],
        name: "商详套图",
        detail: "一次生成完整详情页套图",
        mode: "image",
        scenario: "ecommerce",
        slots: imageSlots(),
        choices: imageChoices(),
        notesHint: "卖点方向、目标人群、禁用元素、希望呈现的风格",
        modules: [
            { key: "hero", name: "首图 / 主视觉", count: 1, spec: "主体占画面 55–65%，纯色或极简背景，无文字或仅含 1 个核心卖点短语。" },
            { key: "scene", name: "场景图 / 使用", count: 1, spec: "商品处于真实使用场景，有环境光与生活痕迹，允许虚化背景。" },
            { key: "selling-point", name: "卖点图 / 功效", count: 2, spec: "1 图 1 卖点，文案不超过 8 个汉字，图形化标注指向商品对应部位。" },
            { key: "detail", name: "细节图 / 材质", count: 1, spec: "微距级特写（材质纹理、缝线、工艺），需体现触感。" },
            { key: "package", name: "包装图 / 到手", count: 1, spec: "完整呈现外包装 + 内衬，体现礼盒感。" },
            { key: "favorite", name: "收藏图", count: 1, spec: "强调加购或收藏利益点，含明确利益文案。" },
            { key: "ingredient", name: "成分图 / 配料", count: 1, spec: "原料或配方可视化，需呈现原料实物而非纯文字罗列。" },
            { key: "structure", name: "结构图 / 功能", count: 1, spec: "爆炸图或剖面图，标注关键结构名称。" },
            { key: "compare", name: "对比图 / 痛点", count: 1, spec: "本品与同类产品并列，用视觉差异说明优势，禁止贬损性文字。" },
        ],
    },
    {
        key: "ecommerce-hero",
        presetIds: ["ecom-shelf-ready", "ecom-image"],
        name: "商品主图",
        detail: "白底、场景与卖点图",
        mode: "image",
        scenario: "ecommerce",
        slots: imageSlots(),
        choices: imageChoices(),
        notesHint: "卖点方向、目标人群、禁用元素、希望呈现的风格",
        modules: [
            { key: "white", name: "白底图", count: 1, spec: "纯白背景，主体居中，柔和投影，无多余装饰。" },
            { key: "hero", name: "主视觉", count: 1, spec: "主体占画面 55–65%，纯色或极简背景，无文字或仅含 1 个核心卖点短语。" },
            { key: "scene", name: "场景图", count: 1, spec: "商品处于真实使用场景，有环境光与生活痕迹，允许虚化背景。" },
            { key: "selling-point", name: "卖点图", count: 2, spec: "1 图 1 卖点，文案不超过 8 个汉字，图形化标注指向商品对应部位。" },
        ],
    },
    {
        key: "ecommerce-scene",
        presetIds: ["ecom-shelf-ready", "ecom-image"],
        name: "使用场景图",
        detail: "把商品放进真实使用环境",
        mode: "image",
        scenario: "ecommerce",
        slots: imageSlots(),
        choices: imageChoices(),
        notesHint: "目标人群、使用场合、禁用元素、希望呈现的风格",
        modules: [
            { key: "scene", name: "生活场景", count: 2, spec: "商品处于真实使用场景，有环境光与生活痕迹，允许虚化背景。" },
            { key: "detail", name: "细节特写", count: 1, spec: "微距级特写（材质纹理、缝线、工艺），需体现触感。" },
            { key: "structure", name: "功能结构", count: 1, spec: "爆炸图或剖面图，标注关键结构名称。" },
        ],
    },
    {
        key: "ecommerce-spoken-video",
        presetIds: ["ad-full-chain", "h3-video", "seedance-video"],
        name: "电商带货视频",
        detail: "商品展示与带货短视频",
        mode: "video",
        scenario: "ecommerce",
        slots: videoSlots(),
        notesHint: "卖点方向、目标人群、口播风格、禁用元素",
    },
    {
        key: "ecommerce-brand-video",
        presetIds: ["ad-full-chain", "h3-video", "seedance-video"],
        name: "品牌广告",
        detail: "视听统一的品牌宣传片",
        mode: "video",
        scenario: "marketing",
        slots: videoSlots(),
        notesHint: "品牌名、主张、目标人群、禁用元素",
    },
    {
        key: "ecommerce-drama-video",
        presetIds: ["short-drama-starter", "short-drama-pilot", "ai-performer"],
        name: "剧情短片",
        detail: "情节紧凑的剧情短片",
        mode: "video",
        scenario: "short-film",
        slots: videoSlots(),
        notesHint: "故事梗概、角色、时长、禁用元素",
    },
];

export const CREATION_TASK_BY_KEY = new Map(CREATION_TASKS.map((task) => [task.key, task]));

export function creationTask(key: string) {
    return CREATION_TASK_BY_KEY.get(key);
}

export type CreationTaskModuleSelection = Record<string, number>;

export function defaultModuleSelection(task: CreationTask): CreationTaskModuleSelection {
    // 默认勾选前两类：面板一打开就有结果，而不是空态逼用户先做决定。
    return Object.fromEntries((task.modules || []).slice(0, 2).map((module) => [module.key, module.count]));
}

export function selectedModuleSummary(task: CreationTask, selection: CreationTaskModuleSelection) {
    const modules = (task.modules || []).filter((module) => (selection[module.key] || 0) > 0);
    return {
        kinds: modules.length,
        images: modules.reduce((sum, module) => sum + Math.max(1, Math.floor(selection[module.key] || module.count)), 0),
        modules,
    };
}
export type CreationTaskPromptInput = {
    task: CreationTask;
    /** 每个槽位当前拥有的素材标签，例如 { product: ["图片1"] }。 */
    slotLabels: Record<string, string[]>;
    moduleSelection: CreationTaskModuleSelection;
    choiceValues: Record<string, string>;
    notes: string;
    /** 已回填的真实规格，例如 3:4、中文、10 秒。 */
    settings: { ratio: string; seconds: string };
};

/**
 * 三层组装：场景基座 → 任务专项指令 → 用户输入块。
 * 用户界面上没有提示词框，这段文本是内部产物。
 */
export function buildCreationTaskPrompt(input: CreationTaskPromptInput) {
    const { task } = input;
    const blocks = [CREATIVE_SCENARIOS[task.scenario].instruction];

    const directives = [`任务：${task.name}（${task.detail}）。`];
    for (const slot of task.slots) {
        const labels = input.slotLabels[slot.key] || [];
        const role = labels.length ? labels.map((label) => `@${label}`).join("、") : slot.required ? "（缺失，需在结果中避免编造商品主体）" : "未提供";
        directives.push(`- ${slot.name}：${role}。${slot.hint}`);
    }
    const summary = selectedModuleSummary(task, input.moduleSelection);
    if (summary.modules.length) {
        directives.push(`本次共需产出 ${summary.kinds} 类、${summary.images} 张图，每类按下列规格执行：`);
        summary.modules.forEach((module) => directives.push(`- ${module.name}（${Math.max(1, Math.floor(input.moduleSelection[module.key] || module.count))} 张）：${module.spec}`));
    }
    const specs = [];
    const platform = input.choiceValues.platform;
    if (platform) specs.push(`目标平台：${platform}（请沿用该平台的图片尺寸、白底要求与套图顺序）`);
    const category = input.choiceValues.category;
    if (category) specs.push(`商品类目：${category}（请沿用该品类的视觉重点、卖点结构与合规红线）`);
    const language = input.choiceValues.language;
    if (language) specs.push(`图文语言：${language}`);
    if (task.mode === "image" && input.settings.ratio) specs.push(`图片比例：${input.settings.ratio}`);
    if (task.mode === "video" && input.settings.seconds) specs.push(`单段时长：${input.settings.seconds} 秒`);
    if (specs.length) directives.push(`${specs.join("；")}。`);
    blocks.push(directives.join("\n"));

    const notes = input.notes.trim();
    if (notes) blocks.push(`用户补充说明：\n${notes}`);
    return blocks.join("\n\n");
}
