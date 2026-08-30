import type { CanvasResourceReference } from "@/lib/canvas/canvas-resource-references";
import { resourceFileUrl, resourceIdFromStorageKey, resourceStorageKey } from "@/services/api/resources";
import type { ProjectAsset, ProjectDetail } from "@/services/api/projects";
import type { AssetCategory } from "@/stores/use-asset-store";
import type { ReferenceImage } from "@/types/image";
import type { ReferenceAudio } from "@/types/media";

export type ShotAssetReferenceContext = {
    mentionReferences: CanvasResourceReference[];
    referenceImages: ReferenceImage[];
    referenceAudios: ReferenceAudio[];
    assetReferences: ShotPromptAssetReference[];
    resolvedCharacterVersions: Array<{ assetId: string; versionId: string }>;
};

export type ShotPromptAssetReference = {
    assetId: string;
    title: string;
    category: string;
    imageIndex: number;
    audioIndex?: number;
    voiceDescription?: string;
};

const assetCategories = new Set<AssetCategory>(["character", "environment", "wardrobe", "prop", "weapon", "style", "other"]);

export function buildShotAssetReferenceContext(detail: ProjectDetail, shotId: string): ShotAssetReferenceContext {
    const assetByVersionId = new Map(detail.assets.filter((asset) => asset.primaryVersionId).map((asset) => [asset.primaryVersionId as string, asset]));
    const seenAssetIds = new Set<string>();
    const entries = (detail.shotReferences || []).flatMap((reference) => {
        if (reference.shotId !== shotId || reference.status !== "linked") return [];
        const asset = assetByVersionId.get(reference.assetVersionId);
        if (!asset || seenAssetIds.has(asset.id)) return [];
        const image = projectAssetReferenceImage(asset);
        if (!image) return [];
        seenAssetIds.add(asset.id);
        return [{ asset, image }];
    });

    const referenceAudios: ReferenceAudio[] = [];
    const audioIndexByResourceId = new Map<string, number>();
    const assetReferences = entries.map(({ asset }, index) => {
        const sampleResourceId = stringValue(asset.character?.voice?.profile.sampleResourceId);
        let audioIndex: number | undefined;
        if (sampleResourceId) {
            audioIndex = audioIndexByResourceId.get(sampleResourceId);
            if (!audioIndex) {
                audioIndex = referenceAudios.length + 1;
                audioIndexByResourceId.set(sampleResourceId, audioIndex);
                referenceAudios.push({
                    id: `project-character-voice:${asset.id}`,
                    name: `${asset.title}-声音样本`,
                    type: "audio/*",
                    url: resourceFileUrl(sampleResourceId),
                    storageKey: resourceStorageKey(sampleResourceId),
                });
            }
        }
        return {
            assetId: asset.id,
            title: asset.title,
            category: asset.category,
            imageIndex: index + 1,
            ...(audioIndex ? { audioIndex } : {}),
            ...(asset.character ? { voiceDescription: characterVoiceDescription(asset) } : {}),
        } satisfies ShotPromptAssetReference;
    });

    return {
        mentionReferences: entries.map(({ asset, image }) => ({
            id: `project-asset:${asset.id}`,
            nodeId: "",
            assetId: asset.id,
            kind: asset.character ? "character" : "image",
            label: asset.title,
            title: asset.title,
            previewUrl: image.url,
            storageKey: image.storageKey,
            active: true,
            category: projectAssetCategory(asset.category),
        })),
        referenceImages: entries.map(({ image }) => image),
        referenceAudios,
        assetReferences,
        resolvedCharacterVersions: entries.flatMap(({ asset }) => asset.character?.versionId ? [{ assetId: asset.id, versionId: asset.character.versionId }] : []),
    };
}

export function resolveShotAssetMentionPrompt(prompt: string, context: ShotAssetReferenceContext) {
    const imageLabelByAssetId = new Map(context.mentionReferences.map((reference, index) => [reference.assetId, `图片${index + 1}`]));
    const unresolved = new Set<string>();
    const resolved = prompt.replace(/@\[asset:([^\]]+)\]/g, (token, assetId: string) => {
        const label = imageLabelByAssetId.get(assetId);
        if (!label) {
            unresolved.add(token);
            return token;
        }
        return label;
    });
    if (unresolved.size) throw new Error(`提示词中的 ${Array.from(unresolved).join("、")} 未绑定到当前镜头，请重新选择资产或删除引用`);
    const assetBlock = compileShotAssetReferencePrompt(context.assetReferences);
    return [resolved.trim(), assetBlock].filter(Boolean).join("\n\n");
}

export function compileShotAssetReferencePrompt(references: ShotPromptAssetReference[]) {
    if (!references.length) return "";
    const lines = references.flatMap((reference) => {
        const visualLabel = reference.category === "character" ? "人物参考" : `${projectAssetCategoryLabel(reference.category)}参考`;
        const media = [`${visualLabel}：图片${reference.imageIndex}`, reference.audioIndex ? `声音参考：音频${reference.audioIndex}` : ""].filter(Boolean).join("；");
        return [
            `- ${reference.title}：${media}`,
            reference.voiceDescription ? `  声音说明：${reference.voiceDescription}` : "",
        ].filter(Boolean);
    });
    return ["【资产参考】", "", ...lines].join("\n");
}

function projectAssetReferenceImage(asset: ProjectAsset): ReferenceImage | undefined {
    const representation = asset.character
        ? asset.character.representations.find((item) => item.role === "turnaround_sheet")
            || asset.character.representations.find((item) => item.role === "primary")
            || asset.character.representations.find((item) => item.role === "front")
        : undefined;
    if (representation) {
        return {
            id: asset.id,
            name: asset.title,
            type: "image/*",
            dataUrl: "",
            url: resourceFileUrl(representation.resourceId),
            storageKey: resourceStorageKey(representation.resourceId),
        };
    }
    if (asset.mediaType !== "image" || !asset.storageKey) return undefined;
    const resourceId = resourceIdFromStorageKey(asset.storageKey);
    return {
        id: asset.id,
        name: asset.title,
        type: "image/*",
        dataUrl: "",
        ...(resourceId ? { url: resourceFileUrl(resourceId) } : {}),
        storageKey: asset.storageKey,
    };
}

function projectAssetCategory(value: string): AssetCategory {
    return assetCategories.has(value as AssetCategory) ? value as AssetCategory : "other";
}

function projectAssetCategoryLabel(value: string) {
    return ({ environment: "场景", wardrobe: "服装", prop: "道具", weapon: "武器", style: "风格", other: "资产" } as Record<string, string>)[value] || "资产";
}

function characterVoiceDescription(asset: ProjectAsset) {
    const definition = asset.character?.definition || {};
    const voice = asset.character?.voice;
    return [
        stringValue(definition.voiceLanguage) || stringValue(voice?.profile.language),
        stringValue(definition.voiceAge),
        stringValue(definition.voiceTimbre) || stringValue(voice?.profile.timbre),
        stringValue(voice?.instructions),
    ].filter(Boolean).join("；");
}

function stringValue(value: unknown) {
    return typeof value === "string" ? value.trim() : "";
}
