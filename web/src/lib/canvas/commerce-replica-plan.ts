import type { CommerceScreen } from "@/types/commerce-workflow";

const templateSchema = {
    originalSubject: "识别模板中待替换的原主体及其与背景、道具、人物的关系",
    composition: "画面比例、景别、机位、主体位置与占比、视觉中心和留白位置",
    background: "前中后景、场景与道具的具体位置、材质和透视",
    lighting: "主辅光方向、软硬、色温、阴影、反射与透光",
    palette: "主辅色、点缀色、占比和对比关系",
    textLayout: "已有标题、说明、标签的位置、字体类别、字级与留白；无字明确无字",
    preserveElements: "本模板必须保留的具体构图、背景、道具、光影与排版元素",
    removeElements: "需去除的旧产品及关联包装、标识、遮挡、阴影或反射；不删除应保留的文案",
};
const replacementSchema = {
    placement: "新产品在模板中的位置、大小、姿态与数量，基于新产品真实比例调整，不能拉伸套旧轮廓",
    perspective: "新产品相机角度、透视与背景匹配的具体做法，保留自身几何结构",
    contactAndLighting: "重建新产品接触阴影、投影、环境反射与透光的方向和质感，不残留旧主体光影",
    backgroundAdaptation: "移除旧主体后的背景补全、遮挡和道具关系，保持模板布局而非重造场景",
    identityConstraints: "新产品轮廓、比例、颜色、纹理、标识等不可改变项，以及严禁混入的旧产品特征",
    typography: "保持原文案及版式，或按用户提供的原文替换映射执行；不自行翻译、扩写或新增卖点",
    execution: "将上述决策整合成这一张图可直接执行的具体替换步骤，不写同上或泛称参考模板",
};

export const commerceReplicaPlanSchema = {
    productAnalysis: { identity: "产品图中的真实产品身份", visibleFeatures: ["图像可确认的外观事实"], preserve: ["复刻时不可改变的产品特征"], unknowns: ["无法确认的属性，不得猜测"] },
    screens: [{ screenId: "严格使用输入的 screenId", templateNodeId: "严格使用该屏绑定的模板 ID", title: "具体模板主题", copy: "留空；文案沿用模板或用户原文替换映射，不生成新文案", templateAnalysis: templateSchema, replacementPlan: replacementSchema }],
};

function object(value: unknown, label: string): Record<string, unknown> {
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`复刻分析缺少${label}，不会自动出图`);
    return value as Record<string, unknown>;
}

function text(value: unknown, label: string, max = 6000) {
    if (typeof value !== "string" || !value.trim() || value.length > max) throw new Error(`复刻分析 ${label} 须为非空具体描述（不超过 ${max} 字符）`);
    return value.trim();
}

function fields(value: unknown, schema: Record<string, string>, label: string) {
    const source = object(value, label);
    return Object.fromEntries(Object.keys(schema).map((key) => [key, text(source[key], `${label}.${key}`)]));
}

/** Bind plans by stable screen/template identity, never by an untrusted response order. */
export function compileCommerceReplicaPlan(value: unknown, expected: CommerceScreen[]) {
    const plan = object(value, "产品与模板分析");
    const product = object(plan.productAnalysis, "产品分析 productAnalysis");
    const identity = text(product.identity, "产品身份");
    const facts: Record<string, string[]> = {};
    for (const key of ["visibleFeatures", "preserve", "unknowns"]) {
        const list = product[key];
        if (!Array.isArray(list) || list.length > 40 || key !== "unknowns" && !list.length) throw new Error(`复刻分析 ${key} 须为事实列表`);
        facts[key] = list.map((item) => text(item, `产品分析 ${key}`));
    }
    if (!Array.isArray(plan.screens) || plan.screens.length !== expected.length) throw new Error(`复刻分析须包含 ${expected.length} 张模板方案`);
    const byId = new Map<string, Record<string, unknown>>();
    for (const value of plan.screens) {
        const row = object(value, "逐模板方案");
        const id = text(row.screenId, "screenId", 200);
        const target = expected.find((screen) => screen.id === id);
        if (!target?.templateNodeId || target.templateNodeId !== row.templateNodeId || byId.has(id)) throw new Error("复刻分析的屏或模板对应关系不正确，不会自动出图");
        byId.set(id, row);
    }
    return { screens: expected.map((screen) => {
        const row = byId.get(screen.id)!;
        const title = text(row.title, "主题", 200);
        if (typeof row.copy !== "string" || row.copy.length > 8000) throw new Error("复刻分析 copy 须为不超过 8000 字符的文本");
        const template = fields(row.templateAnalysis, templateSchema, `${title} 模板分析`);
        const replacement = fields(row.replacementPlan, replacementSchema, `${title} 替换方案`);
        const prompt = [
            `新产品身份：${identity}`, `可见特征：${facts.visibleFeatures.join("；")}`, `必须保留：${facts.preserve.join("；")}`, `未确认属性（不得编造）：${facts.unknowns.join("；") || "无补充"}`,
            `模板原主体：${template.originalSubject}`, `模板构图：${template.composition}`, `模板背景：${template.background}`, `模板光影：${template.lighting}`, `模板配色：${template.palette}`, `模板文字布局：${template.textLayout}`,
            `保留元素：${template.preserveElements}`, `移除元素：${template.removeElements}`,
            `新产品摆放：${replacement.placement}`, `透视匹配：${replacement.perspective}`, `接触与光影：${replacement.contactAndLighting}`, `背景与遮挡修复：${replacement.backgroundAdaptation}`, `产品身份约束：${replacement.identityConstraints}`, `文字处理：${replacement.typography}`, `本屏执行方案：${replacement.execution}`,
        ].join("\n\n");
        if (prompt.length > 12000) throw new Error(`「${title}」复刻方案过长，请精简后再生成，不会截断执行`);
        return { title, copy: row.copy, prompt };
    }) };
}
