import type { CommerceScreen } from "@/types/commerce-workflow";

const visualSystemSchema = {
    artDirection: "适合该产品的整组视觉方向，不照搬产品原图场景",
    palette: "主色、辅助色、点缀色及用色比例",
    lighting: "统一的光影与材质表现原则",
    typography: "字体类别、标题正文层级、文字色彩与留白规则；纯图说明不排字",
};
const visualPlanSchema = {
    subject: "本屏产品的视角、比例、姿态、数量；如需模特说明动作与产品关系，不改变产品身份",
    composition: "具体镜头景别、机位、主体位置及占比、视觉焦点与留白位置",
    environment: "具体场景、前中后景、道具及其位置；服务本屏目标而不是泛称高级背景",
    lighting: "主辅光方向、软硬度、阴影、反射与透光如何塑造产品",
    colorAndMaterials: "本屏色彩分配、背景和道具材质、与产品的对比关系",
    typography: "精确文案的放置区域、大小层级与排版；无字屏明确无排版文字",
    visualEvidence: "本屏已确认卖点通过什么可见细节、使用动作或视觉证据表达，不编造检测结论",
    variation: "相对相邻屏在景别、场景或布局上的明确变化，以及保留的统一设计元素",
};

export const commerceDetailPlanSchema = {
    productAnalysis: { identity: "从产品图识别的产品身份与需要保留的外观", visibleFeatures: ["从产品图实际看见的形态、颜色与材质表现"], confirmedSellingPoints: ["区分图像可见事实与用户明确提供的事实"], unknowns: ["无法从素材确认的参数、功效或材质；不得猜测"] },
    styleAnalysis: { palette: "参考图配色与比例", composition: "参考图构图与留白规律", lighting: "光线方向、软硬与对比", typography: "字体与信息层级；无字则明确", transferableElements: "可转化到本产品的视觉规则", excludedElements: "不可复制的参考产品、人物身份、品牌、文字及不适用元素" },
    visualSystem: visualSystemSchema,
    screens: [{ title: "具体视觉主题而非泛化栏目名", sceneType: "本屏具体场景", sellingPoints: "本屏已确认事实，无依据留空", copy: "本屏完整成品文案，纯图留空", visualPlan: visualPlanSchema }],
};

function object(value: unknown, label: string): Record<string, unknown> {
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`详情策划缺少${label}，请检查原始响应；不会自动出图`);
    return value as Record<string, unknown>;
}

function text(value: unknown, label: string) {
    if (typeof value !== "string" || !value.trim() || value.length > 6000) throw new Error(`详情策划 ${label} 须为非空具体描述（不超过 6000 字符）`);
    return value.trim();
}

function fields(value: unknown, schema: Record<string, string>, label: string) {
    const source = object(value, label);
    return Object.fromEntries(Object.keys(schema).map((key) => [key, text(source[key], `${label}.${key}`)]));
}

/** Compile structured visual decisions into the existing editable prompt contract. */
export function compileCommerceDetailPlan(value: unknown, expected: CommerceScreen[]) {
    const plan = object(value, "产品与风格分析");
    const product = object(plan.productAnalysis, "产品分析 productAnalysis");
    text(product.identity, "产品身份");
    for (const key of ["visibleFeatures", "confirmedSellingPoints", "unknowns"]) {
        const list = product[key];
        if (!Array.isArray(list) || list.length > 40) throw new Error(`详情策划产品分析 ${key} 须为事实列表`);
        list.forEach((item) => text(item, `产品分析 ${key}`));
    }
    fields(plan.styleAnalysis, commerceDetailPlanSchema.styleAnalysis, "风格分析 styleAnalysis");
    const system = fields(plan.visualSystem, visualSystemSchema, "整组视觉 visualSystem");
    if (!Array.isArray(plan.screens) || plan.screens.length !== expected.length) throw new Error(`策划须包含 ${expected.length} 屏，请检查原始响应`);
    const seen = new Set<string>();
    return { screens: plan.screens.map((value, index) => {
        const row = object(value, `第 ${index + 1} 屏`);
        for (const [key, limit] of [["title", 200], ["copy", 8000], ["sceneType", 200], ["sellingPoints", 4000]] as const) {
            if (typeof row[key] !== "string" || (row[key] as string).length > limit || (["title", "sceneType"].includes(key) && !(row[key] as string).trim())) {
                throw new Error(`第 ${index + 1} 屏 ${key} 无效或超过 ${limit} 字符，不会截断执行`);
            }
        }
        const visual = fields(row.visualPlan, visualPlanSchema, `第 ${index + 1} 屏 visualPlan`);
        const fingerprint = JSON.stringify([visual.subject, visual.composition, visual.environment, visual.lighting, visual.colorAndMaterials, visual.visualEvidence]).replace(/\s/g, "");
        if (seen.has(fingerprint)) throw new Error(`第 ${index + 1} 屏视觉方案重复，请修改策划后再出图`);
        seen.add(fingerprint);
        const prompt = [
            `整组视觉方向：${system.artDirection}`, `统一配色：${system.palette}`, `统一光影：${system.lighting}`, `统一字体：${system.typography}`,
            `产品与主体：${visual.subject}`, `镜头与构图：${visual.composition}`, `环境与道具：${visual.environment}`, `光线与质感：${visual.lighting}`,
            `色彩与材质：${visual.colorAndMaterials}`, `文字与留白：${visual.typography}`, `卖点的视觉表达：${visual.visualEvidence}`, `本屏差异与连续性：${visual.variation}`,
        ].join("\n\n");
        if (prompt.length > 12000) throw new Error(`第 ${index + 1} 屏视觉方案过长，请精简后再生成，不会截断执行`);
        return { ...row, prompt };
    }) };
}
