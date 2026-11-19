import { CONTENT_MODERATION_ERROR_CODE, isContentModerationError } from "@/lib/generation-error";
import type { WorkbenchTool } from "@/services/api/workbench";

/** 内容审核拒绝是终态：原样重新生成只会再次被拒，必须改描述或素材后再提交。 */
export function isModeratedFailure(task: { status: string; errorCode?: string; error?: string }) {
    return task.status === "failed" && (task.errorCode === CONTENT_MODERATION_ERROR_CODE || isContentModerationError(task.error));
}

/** 与后端 workbench.videoReferenceImageMinEdge 保持一致：视频模型拒绝宽或高小于该值的参考图。 */
export const VIDEO_REFERENCE_IMAGE_MIN_EDGE = 300;

export function videoReferenceImageSizeIssue(size: { width: number; height: number } | undefined) {
    if (!size || size.width <= 0 || size.height <= 0) return "";
    if (size.width >= VIDEO_REFERENCE_IMAGE_MIN_EDGE && size.height >= VIDEO_REFERENCE_IMAGE_MIN_EDGE) return "";
    return `图片为 ${size.width}×${size.height} 像素，视频模型要求宽高均不小于 ${VIDEO_REFERENCE_IMAGE_MIN_EDGE} 像素，请放大或补边后重新上传`;
}

export function workbenchVideoOperation(references: ReadonlyArray<{ kind: string; role?: string }>) {
    if (references.some((reference) => reference.kind === "video" || (reference.kind === "image" && reference.role !== "first_frame" && reference.role !== "last_frame"))) return "reference_to_video";
    if (references.some((reference) => reference.kind === "image")) return "image_to_video";
    if (references.some((reference) => reference.kind === "audio")) return "audio_to_video";
    return "text_to_video";
}

export function workbenchPromptRequired(tool: WorkbenchTool | null) {
    return !tool || tool.inputContract?.promptRequired !== false || (tool.inputContract?.minTextReferences || 0) > 0;
}

export function missingWorkbenchInput(tool: WorkbenchTool | null, references: Array<{ slotKey?: string; resourceId?: string; uploading?: boolean; error?: string }>) {
    return tool?.inputContract?.slots.find((slot) => references.filter((reference) => reference.slotKey === slot.key && reference.resourceId && !reference.uploading && !reference.error).length < Math.max(slot.minCount, slot.required ? 1 : 0));
}
