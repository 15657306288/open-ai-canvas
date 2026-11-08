import { resourceFileUrl } from "@/services/api/resources";

import type { DraftImage, DraftVideo, VideoRegions, VideoTrim } from "./use-ecommerce-video";
import type { AudioChoice, EcommerceVideoFeature, ReplaceKind } from "./video-kinds";

/*
 * 商品视频左侧面板的草稿：刷新或切换页面后恢复已经上传好的素材、参考视频和填写的内容。
 * 只保存资源 ID 与文字（几 KB），不保存文件本身；还在上传中的素材不会写入草稿。
 * 按用户分开存，换账号不会看到别人的草稿。
 */

export type VideoDraft = {
    feature: EcommerceVideoFeature;
    kind: ReplaceKind;
    images: Array<Pick<DraftImage, "slot" | "category" | "resourceId" | "name" | "durationMs">>;
    video: Pick<DraftVideo, "resourceId" | "name" | "durationMs" | "width" | "height" | "projectId"> | null;
    linkText: string;
    notes: string;
    subjectHint: string;
    productName?: string;
    audio: AudioChoice;
    keepMusic: boolean;
    talking: boolean;
    faceConsent: boolean;
    trim: VideoTrim | null;
    regions: VideoRegions | null;
    ratio: string;
    resolution: string;
};

const KEY = "ecv-draft:v1:";

export function loadVideoDraft(userId: string): VideoDraft | null {
    if (!userId) return null;
    try {
        const raw = window.localStorage.getItem(KEY + userId);
        if (!raw) return null;
        const draft = JSON.parse(raw) as VideoDraft;
        return draft && typeof draft === "object" && Array.isArray(draft.images) ? draft : null;
    } catch {
        return null;
    }
}

export function saveVideoDraft(userId: string, draft: VideoDraft) {
    if (!userId) return;
    try {
        window.localStorage.setItem(KEY + userId, JSON.stringify(draft));
    } catch {
        // 存储已满或被禁用：草稿只在本次页面内保留。
    }
}

export function draftImages(draft: VideoDraft | null, createId: () => string): DraftImage[] {
    return (draft?.images || []).filter((item) => item.resourceId).map((item) => ({ ...item, id: createId(), previewUrl: item.category === "voice_audio" ? "" : resourceFileUrl(item.resourceId) }));
}

export function draftVideo(draft: VideoDraft | null): DraftVideo | null {
    const video = draft?.video;
    return video?.resourceId ? { ...video, previewUrl: resourceFileUrl(video.resourceId) } : null;
}
