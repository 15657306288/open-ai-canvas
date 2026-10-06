export function detailPlanFixture(titles: string[]) {
    return {
        productAnalysis: { identity: "琥珀色玻璃杯", visibleFeatures: ["波浪杯口", "透光杯壁"], confirmedSellingPoints: ["杯壁呈琥珀色"], unknowns: ["容量未提供", "耐热温度未确认"] },
        styleAnalysis: { palette: "暖白与灰绿，琥珀点缀", composition: "偏心主体与疏密留白", lighting: "侧向漫射光", typography: "细衬线标题与克制的小字", transferableElements: "低饱和背景和柔和投影", excludedElements: "不复制参考图人物、产品或品牌文字" },
        visualSystem: { artDirection: "温暖的日常器物摄影", palette: "暖白、灰绿、琥珀", lighting: "柔和侧逆光突出玻璃透光", typography: "深灰细衬线标题，正文无衬线" },
        screens: titles.map((title, index) => ({ title, copy: index === 0 ? "温润日常" : "", sceneType: index === 0 ? "器物静物" : "生活场景", sellingPoints: "琥珀色透光杯壁",
            visualPlan: { subject: "保留产品杯口轮廓、杯壁纹理和原始颜色，不改变材质", composition: `第 ${index + 1} 屏采用${index === 0 ? "左下低机位近景，主体占画面三分之二" : "右侧俯拍中景，主体占画面三分之一"}`,
                environment: index === 0 ? "灰绿哑光纸台与暖白背景" : "临窗早餐桌，亚麻餐垫和淡色陶盘形成层次",
                lighting: "左后方柔光穿过杯壁，右侧白卡补光，保留通透折射和接触阴影", colorAndMaterials: "琥珀玻璃与灰绿哑光台面形成材质对比，不改变产品色相",
                typography: index === 0 ? "标题放右上留白，深灰细衬线，正文与主体不重叠" : "纯图无排版文字",
                visualEvidence: "通过逆光透射而不是功能口号表达杯壁透光", variation: `与相邻屏区分：${index === 0 ? "器物近景" : "餐桌环境中景"}，保留相同色系和光线方向` } })),
    };
}
