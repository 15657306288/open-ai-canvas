import type { CommerceScreen } from "../../src/types/commerce-workflow";

export function replicaPlanFixture(screens: CommerceScreen[]) {
    return {
        productAnalysis: { identity: "琥珀色宽口玻璃杯", visibleFeatures: ["波浪杯口", "透光杯壁"], preserve: ["杯口轮廓、杯身比例与琥珀色"], unknowns: ["未提供容量和耐热参数"] },
        screens: screens.map((screen) => ({ screenId: screen.id, templateNodeId: screen.templateNodeId, title: "琥珀玻璃的桌面光影", copy: "",
            templateAnalysis: { originalSubject: "模板中的高脚杯是待替换主体", composition: "主体在右侧占画面一半，左侧上方留标题", background: "浅灰石台与后方虚化窗框", lighting: "左后侧窗光，右前侧柔光补亮", palette: "暖灰、米白、少量深棕", textLayout: "左上两行细衬线标题，左下小号说明", preserveElements: "石台、窗光方向、左右留白和字级关系", removeElements: "模板高脚杯、原主体反射与包装标识" },
            replacementPlan: { placement: "以新杯实际宽高比例摆放在石台右侧，不能拉成长柄高脚杯", perspective: "根据石台俯视角调整杯口椭圆，保留新产品几何比例", contactAndLighting: "重建杯底接触阴影，窗光穿过琥珀杯壁形成暖色折射", backgroundAdaptation: "补全移除原主体后露出的台面，保持背景透视与纹理连续", identityConstraints: "只使用产品图的轮廓、颜色、纹理和标识，不混入模板杯脚", typography: "保持原文案字级与左侧留白；只应用用户指定的原文替换映射", execution: "先移除模板旧主体，再按新杯体积调整占比、遮挡、反射与光影，不改成另一套场景" },
        })),
    };
}
