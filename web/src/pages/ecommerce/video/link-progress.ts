import type { VideoLinkProgress, VideoLinkStage } from "@/services/api/video-replication";

/** 各阶段在整体进度条上的起点（%）；下载阶段在 35%–80% 之间按已下载比例推进。 */
const STAGE_PERCENT: Record<VideoLinkStage, number> = {
    detecting: 4,
    detected: 10,
    resolving: 14,
    parsing: 22,
    parsed: 32,
    downloading: 35,
    merging: 82,
    probing: 88,
    uploading: 92,
    reused: 92,
    registering: 96,
    done: 100,
};

const STAGE_TEXT: Record<VideoLinkStage, string> = {
    detecting: "正在识别平台",
    detected: "已识别平台",
    resolving: "正在展开链接",
    parsing: "正在解析作品信息",
    parsed: "已解析作品信息",
    downloading: "正在下载视频",
    merging: "正在整理音视频",
    probing: "正在校验视频",
    uploading: "正在转存到云存储",
    reused: "已在素材库中找到这条视频",
    registering: "正在登记到素材库",
    done: "已完成",
};

export function linkProgressPercent(progress: VideoLinkProgress | null): number {
    if (!progress) return 0;
    const start = STAGE_PERCENT[progress.stage] ?? 0;
    if (progress.stage === "downloading" && progress.progress) return Math.round(start + (STAGE_PERCENT.merging - start) * Math.min(1, Math.max(0, progress.progress)));
    return start;
}

/** 服务端给了更具体的说明（如“正在下载视频 12.3 MB”）时优先使用。 */
export function linkProgressText(progress: VideoLinkProgress | null): string {
    if (!progress) return "";
    return progress.message || STAGE_TEXT[progress.stage] || "正在解析";
}
