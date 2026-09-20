import { describe, expect, test } from "bun:test";
import { applyCreativeAnswers, creativeBatchBarrier, normalizeCreativeQuestions, validateCreativeAnswers, type CreativeBrief } from "../src/lib/creation/creative-agent-contract";

const identity = { interactionId: "trusted", revision: 2 };
const question = (field: string) => ({ id: "model-id", field, title: field, type: "single", required: true, options: [{ id: "model-option", label: "选项" }] });
describe("创作交互合同", () => {
    test("跳过明确答案、未知字段和重复字段，最多三题且覆盖模型身份", () => {
        const brief: CreativeBrief = { genre: { value: "悬疑", source: "user", status: "confirmed" } };
        const request = normalizeCreativeQuestions([question("genre"), question("unknown"), question("seconds"), question("seconds"), question("style"), question("episodes"), question("scenes")], "short-film", brief, identity);
        expect(request.questions.map((q) => q.field)).toEqual(["seconds", "style", "episodes"]);
        expect(request.questions[0].id).toBe("trusted:2:1");
        expect(request.questions[0].options[0].id).not.toBe("model-option");
    });
    test("未解析和冲突不是已知事实，营销不询问短片字段", () => {
        const request = normalizeCreativeQuestions([question("assets"), question("style"), question("episodes")], "marketing", { assets: { value: "文件", source: "asset", status: "unparsed" }, style: { value: "写实", source: "user", status: "conflict" } }, identity);
        expect(request.questions.map((q) => q.field)).toEqual(["assets", "style"]);
    });
    test("必填答案与素材归属校验，成功才合并 brief", () => {
        const assets = [{ id: "real-asset", label: "真实商品图" }];
        const request = normalizeCreativeQuestions([{ ...question("assets"), type: "asset", options: [{ id: "invented", label: "模型虚构图" }] }], "general", {}, identity, assets);
        expect(request.questions[0].options).toEqual(assets);
        const id = request.questions[0].id;
        expect(validateCreativeAnswers(request, {}, assets)?.questionId).toBe(id);
        expect(() => applyCreativeAnswers(request, identity, { [id]: { selected: ["invented"], custom: "" } }, {}, assets)).toThrow("不可用");
        expect(applyCreativeAnswers(request, identity, { [id]: { selected: ["real-asset"], custom: "" } }, {}, assets).assets).toEqual({ value: "real-asset", source: "user", status: "confirmed" });
    });
    test("过期、已提交交互不能再次提交", () => {
        const request = normalizeCreativeQuestions([], "general", {}, identity);
        expect(() => applyCreativeAnswers(request, { ...identity, revision: 1 }, {}, {})).toThrow("过期");
        expect(() => applyCreativeAnswers({ ...request, status: "submitted" }, identity, {}, {})).toThrow("过期");
    });
    test("等待屏障保留前置读取与提问，取消后续写调用", () => {
        expect(creativeBatchBarrier(["read", "question", "create", "generate"], (call) => call === "question")).toEqual({ executable: ["read", "question"], cancelled: ["create", "generate"], waiting: true });
        expect(creativeBatchBarrier(["read"], () => false)).toEqual({ executable: ["read"], cancelled: [], waiting: false });
    });
});
