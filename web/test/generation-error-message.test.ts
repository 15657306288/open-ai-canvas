import { describe, expect, test } from "bun:test";

import { generationErrorMessage } from "@/lib/generation-error";

// 上传失败的文案会直接决定用户下一步动作：
// 网关瞬时故障要提示「稍后重试」，而对象存储配置问题才应该提示「检查配置」。
describe("生成错误文案：参考素材上传", () => {
    test("网关 520 提示稍后重试，而不是让用户去改对象存储配置", () => {
        const message = generationErrorMessage(new Error("参考图片上传失败：云端网关暂时不可用（HTTP 520），请稍后重试"));
        expect(message).toContain("云端网关");
        expect(message).not.toContain("对象存储配置");
    });

    test("502 网关错误同样归为瞬时故障", () => {
        const message = generationErrorMessage(new Error("参考媒体上传失败：后端服务暂时不可用，请稍后重试"));
        expect(message).not.toContain("对象存储配置");
    });

    test("真实的对象存储故障仍提示检查配置", () => {
        const message = generationErrorMessage(new Error("参考图片上传失败：OSS 上传失败，请检查 bucket 权限"));
        expect(message).toBe("参考素材上传到对象存储失败，请检查对象存储配置后重试。");
    });

    test("对象存储账号停用保留专门的提示", () => {
        const message = generationErrorMessage(new Error("参考图片上传失败：UserDisable"));
        expect(message).toContain("对象存储账号已停用");
    });
});
