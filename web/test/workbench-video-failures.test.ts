import { expect, test } from "bun:test";

import { isContentModerationError } from "../src/lib/generation-error";
import { isModeratedFailure, VIDEO_REFERENCE_IMAGE_MIN_EDGE, videoReferenceImageSizeIssue } from "../src/pages/workbench/workbench-input";

test("video reference images below the provider minimum are rejected before upload", () => {
    expect(VIDEO_REFERENCE_IMAGE_MIN_EDGE).toBe(300);
    expect(videoReferenceImageSizeIssue({ width: 466, height: 295 })).toContain("466×295");
    expect(videoReferenceImageSizeIssue({ width: 299, height: 1200 })).not.toBe("");
    expect(videoReferenceImageSizeIssue({ width: 300, height: 300 })).toBe("");
    expect(videoReferenceImageSizeIssue(undefined)).toBe("");
    expect(videoReferenceImageSizeIssue({ width: 0, height: 0 })).toBe("");
});

test("moderation failures are terminal and excluded from blind regeneration", () => {
    expect(isContentModerationError('{"code":"VIDEO_CONTENT_REVIEW"}')).toBe(true);
    expect(isModeratedFailure({ status: "failed", errorCode: "sensitive_words_detected" })).toBe(true);
    expect(isModeratedFailure({ status: "failed", error: "VIDEO_CONTENT_REVIEW: review rejected" })).toBe(true);
    expect(isModeratedFailure({ status: "failed", error: "模型服务响应超时，请稍后再试。" })).toBe(false);
    expect(isModeratedFailure({ status: "cancelled", errorCode: "sensitive_words_detected" })).toBe(false);
});
