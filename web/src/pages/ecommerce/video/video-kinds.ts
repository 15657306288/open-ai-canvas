import { translate } from "@/i18n";
import type { VideoReplicationAssetCategory, VideoReplicationParameters, VideoReplicationTarget } from "@/services/api/video-replication";

// 这里的文字只用于界面显示（不进入接口参数和提示词）。切换界面语言不刷新页面，显示文字写成 getter / 函数，读取时取当前语言。
const tv = (key: string) => translate(`ecommerceVideo.${key}`);

/** 电商 Agent「商品视频」：可切换的视频功能。只列出后端真实可用的功能。 */
export type EcommerceVideoFeature = "replace";

export type ReplaceKind = NonNullable<VideoReplicationParameters["replaceMode"]>;

export type TipKind = "model" | "face" | "background" | "product" | "video" | "voice";
export type CompareKind = "clothing" | "product";
/** modes：声音三选一；rhythm：参考音色并排放在素材里，下方只有「保留音乐和节奏」勾选（精准商品 / 精准人物）。 */
export type AudioUi = "modes" | "rhythm";
/** 框选锁定：在参考视频画面上框出要替换的商品 / 人物。 */
export type LockKind = "product" | "person";

/** 一个上传位：同一类别可以拆成多个位（如正面图 / 反面图），提交时按位顺序登记。 */
export type MaterialSlot = {
    key: string;
    label: string;
    category: Exclude<VideoReplicationAssetCategory, "reference_video">;
    /** 上传位的媒体类型，默认图片。 */
    media?: "image" | "audio";
    max: number;
    note: string;
    /** 需要先上传另一个位才能上传（反面图需要先有正面图）。 */
    requires?: string;
    requiresNote?: string;
    tip?: TipKind;
};

export type ReplaceKindConfig = {
    id: ReplaceKind;
    label: string;
    materialHint: string;
    slots: MaterialSlot[];
    compare?: { kind: CompareKind; title: string };
    audio: AudioUi;
    lock?: LockKind;
    /** 显示「是否口播视频」开关。 */
    talking?: boolean;
    /** ratio：显示「画布」（比例 · 清晰度）；resolution：比例跟随参考视频，只选清晰度。 */
    canvas: "ratio" | "resolution";
    notesLimit: number;
    subjectHint?: boolean;
    consent?: boolean;
    /** 后端替换目标；组合替换按实际上传的类别决定。 */
    targets: VideoReplicationTarget[];
};

/** 商品细节 / 拆解图 / 参数表（选填）：提供新商品结构和真实资料，不是独立的替换目标。 */
const PRODUCT_DETAIL_SLOT: MaterialSlot = {
    key: "detail",
    get label() {
        return tv("kinds.product.detailSlot");
    },
    category: "product_detail_image",
    max: 3,
    get note() {
        return tv("notes.optional3");
    },
    tip: "product",
};
/** 包装图（选填）：原视频出现包装时按这张图还原，不是独立的替换目标。 */
const PACKAGING_SLOT: MaterialSlot = {
    key: "packaging",
    get label() {
        return tv("kinds.product.packagingSlot");
    },
    category: "packaging_image",
    max: 1,
    get note() {
        return tv("notes.optional1");
    },
    tip: "product",
};

export const CATEGORY_TARGET: Partial<Record<MaterialSlot["category"], VideoReplicationTarget>> = {
    product_image: "product",
    model_image: "model",
    apparel_image: "apparel",
    face_image: "face",
    background_image: "background",
};

/** 辅助商品资料不能单独触发替换，也不能在移除商品图后被当作人物 / 背景替换的有效输入。 */
export function replacementMaterialError(kind: ReplaceKindConfig, materials: ReadonlyArray<{ category: MaterialSlot["category"]; resourceId?: string }>): string {
    const ready = materials.filter((material) => material.resourceId);
    const hasProductSupport = ready.some((material) => material.category === "product_detail_image" || material.category === "packaging_image");
    if (hasProductSupport && !ready.some((material) => material.category === "product_image")) return "请先上传商品图，再补充细节、参数表或包装图";
    if (!ready.some((material) => CATEGORY_TARGET[material.category])) return kind.id === "combination" ? "请至少上传商品、人物或背景中的一类素材" : `请上传${kind.slots[0].label}`;
    return "";
}

export const REPLACE_KINDS: ReplaceKindConfig[] = [
    {
        id: "model",
        get label() {
            return tv("kinds.model.label");
        },
        get materialHint() {
            return tv("kinds.model.hint");
        },
        slots: [
            {
                key: "model",
                get label() {
                    return tv("kinds.model.slot");
                },
                category: "model_image",
                max: 3,
                get note() {
                    return tv("notes.max3Angles");
                },
                tip: "model",
            },
        ],
        audio: "modes",
        canvas: "ratio",
        notesLimit: 500,
        targets: ["model"],
    },
    {
        id: "clothing",
        get label() {
            return tv("kinds.clothing.label");
        },
        get materialHint() {
            return tv("kinds.clothing.hint");
        },
        slots: [
            {
                key: "front",
                get label() {
                    return tv("kinds.clothing.front");
                },
                category: "apparel_image",
                max: 1,
                get note() {
                    return tv("notes.only1");
                },
                tip: "product",
            },
            {
                key: "back",
                get label() {
                    return tv("kinds.clothing.back");
                },
                category: "apparel_image",
                max: 1,
                get note() {
                    return tv("notes.only1");
                },
                requires: "front",
                get requiresNote() {
                    return tv("kinds.clothing.frontFirst");
                },
                tip: "product",
            },
        ],
        compare: {
            kind: "clothing",
            get title() {
                return tv("kinds.clothing.compare");
            },
        },
        audio: "modes",
        canvas: "ratio",
        notesLimit: 200,
        targets: ["apparel"],
    },
    {
        id: "face",
        get label() {
            return tv("kinds.face.label");
        },
        get materialHint() {
            return tv("kinds.face.hint");
        },
        slots: [
            {
                key: "face",
                get label() {
                    return tv("kinds.face.slot");
                },
                category: "face_image",
                max: 3,
                get note() {
                    return tv("notes.max3Angles");
                },
                tip: "face",
            },
        ],
        audio: "modes",
        canvas: "resolution",
        notesLimit: 500,
        consent: true,
        targets: ["face"],
    },
    {
        id: "background",
        get label() {
            return tv("kinds.background.label");
        },
        get materialHint() {
            return tv("kinds.background.hint");
        },
        slots: [
            {
                key: "background",
                get label() {
                    return tv("kinds.background.slot");
                },
                category: "background_image",
                max: 3,
                get note() {
                    return tv("notes.max3Angles");
                },
                tip: "background",
            },
        ],
        audio: "modes",
        canvas: "resolution",
        notesLimit: 500,
        targets: ["background"],
    },
    {
        id: "product",
        get label() {
            return tv("kinds.product.label");
        },
        get materialHint() {
            return tv("kinds.product.hint");
        },
        slots: [
            {
                key: "product",
                get label() {
                    return tv("kinds.product.slot");
                },
                category: "product_image",
                max: 4,
                get note() {
                    return tv("notes.max4Angles");
                },
                tip: "product",
            },
            PRODUCT_DETAIL_SLOT,
            PACKAGING_SLOT,
        ],
        compare: {
            kind: "product",
            get title() {
                return tv("kinds.product.compare");
            },
        },
        audio: "modes",
        canvas: "resolution",
        notesLimit: 500,
        subjectHint: true,
        targets: ["product"],
    },
    {
        id: "precise-product",
        get label() {
            return tv("kinds.preciseProduct.label");
        },
        get materialHint() {
            return tv("kinds.preciseProduct.hint");
        },
        slots: [
            {
                key: "product",
                get label() {
                    return tv("kinds.product.slot");
                },
                category: "product_image",
                max: 3,
                get note() {
                    return tv("notes.max3Angles");
                },
                tip: "product",
            },
            PRODUCT_DETAIL_SLOT,
            PACKAGING_SLOT,
        ],
        lock: "product",
        audio: "rhythm",
        canvas: "ratio",
        notesLimit: 200,
        targets: ["product"],
    },
    {
        id: "precise-person",
        get label() {
            return tv("kinds.precisePerson.label");
        },
        get materialHint() {
            return tv("kinds.model.hint");
        },
        slots: [
            {
                key: "model",
                get label() {
                    return tv("kinds.model.slot");
                },
                category: "model_image",
                max: 1,
                get note() {
                    return tv("notes.only1");
                },
                tip: "model",
            },
        ],
        lock: "person",
        audio: "rhythm",
        talking: true,
        canvas: "ratio",
        notesLimit: 200,
        targets: ["model"],
    },
    {
        id: "combination",
        get label() {
            return tv("kinds.combination.label");
        },
        get materialHint() {
            return tv("kinds.combination.hint");
        },
        slots: [
            {
                key: "product",
                get label() {
                    return tv("kinds.product.slot");
                },
                category: "product_image",
                max: 4,
                get note() {
                    return tv("notes.max4");
                },
                tip: "product",
            },
            PRODUCT_DETAIL_SLOT,
            PACKAGING_SLOT,
            {
                key: "model",
                get label() {
                    return tv("kinds.model.slot");
                },
                category: "model_image",
                max: 3,
                get note() {
                    return tv("notes.max3");
                },
                tip: "model",
            },
            {
                key: "background",
                get label() {
                    return tv("kinds.background.slot");
                },
                category: "background_image",
                max: 3,
                get note() {
                    return tv("notes.max3");
                },
                tip: "background",
            },
        ],
        audio: "modes",
        canvas: "resolution",
        notesLimit: 500,
        targets: [],
    },
];

export function replaceKindConfig(id: ReplaceKind | undefined) {
    return REPLACE_KINDS.find((item) => item.id === id) || REPLACE_KINDS[4];
}

/** 商品主体输入框的示例词，只作为可选填充建议。 */
export const subjectExamples = () => ["top", "pants", "skirt", "shoes", "bag", "hat", "lipstick", "cream", "bottle", "phone"].map((key) => tv(`subjects.${key}`));

/** 替换音频时的参考音色位：仅 1 段单人录音；换商品 / 服饰时改写台词，只换人物时沿用原台词。所有替换类型共用。 */
export const VOICE_SLOT: MaterialSlot = {
    key: "voice",
    get label() {
        return tv("kinds.voice");
    },
    category: "voice_audio",
    media: "audio",
    max: 1,
    get note() {
        return tv("notes.only1Clip");
    },
    tip: "voice",
};

export type AudioChoice = NonNullable<VideoReplicationParameters["audioMode"]>;

export const AUDIO_CHOICES: Array<{ value: AudioChoice; label: string; hint: string }> = [
    {
        value: "replace",
        get label() {
            return tv("audio.replace.label");
        },
        get hint() {
            return tv("audio.replace.hint");
        },
    },
    {
        value: "keep",
        get label() {
            return tv("audio.keep.label");
        },
        get hint() {
            return tv("audio.keep.hint");
        },
    },
    {
        value: "mute",
        get label() {
            return tv("audio.mute.label");
        },
        get hint() {
            return tv("audio.mute.hint");
        },
    },
];

export const FEATURES: Array<{ id: EcommerceVideoFeature; title: string; description: string }> = [
    {
        id: "replace",
        get title() {
            return tv("features.replace.title");
        },
        get description() {
            return tv("features.replace.description");
        },
    },
];

export const sharePlatforms = () => tv("sharePlatforms");
