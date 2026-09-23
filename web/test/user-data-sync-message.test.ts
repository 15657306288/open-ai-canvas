import { describe, expect, test } from "bun:test";

import { formatLocalSavedRemotePending } from "@/services/user-data-sync";

describe("本地已保存、云端同步失败的文案", () => {
    test("带上原始错误并声明会自动重试", () => {
        expect(formatLocalSavedRemotePending("素材已在本地保存", new Error("网络中断"))).toBe("素材已在本地保存，云端同步失败：网络中断。将自动重试。");
    });

    test("没有 Error.message 时使用未知错误", () => {
        expect(formatLocalSavedRemotePending("已在本地还原", "x")).toBe("已在本地还原，云端同步失败：未知错误。将自动重试。");
    });
});
