import { describe, expect, test } from "bun:test";
import { CREATION_TASKS, buildCreationTaskPrompt, creationTask, defaultModuleSelection, selectedModuleSummary } from "../src/pages/create/creation-task-catalog";

describe("creation task catalog", () => {
    test("tasks are unique, ecommerce-first, and every task has usable slots", () => {
        expect(new Set(CREATION_TASKS.map((task) => task.key)).size).toBe(CREATION_TASKS.length);
        expect(CREATION_TASKS.filter((task) => task.scenario === "ecommerce").length).toBeGreaterThanOrEqual(4);
        for (const task of CREATION_TASKS) {
            expect(task.slots.length).toBeGreaterThan(0);
            expect(task.slots.filter((slot) => slot.required).length).toBeGreaterThan(0);
            expect(task.notesHint?.length).toBeGreaterThan(4);
        }
        // 形态1：任务名不能是提示词，用户不该被要求描述画面。
        for (const task of CREATION_TASKS) expect(task.name).not.toMatch(/提示词|描述画面/);
    });

    test("module summary counts kinds and images from the selection", () => {
        const task = creationTask("ecommerce-image-set")!;
        expect(task.modules?.length).toBe(9);
        const summary = selectedModuleSummary(task, { hero: 1, scene: 1, "selling-point": 2 });
        expect(summary.kinds).toBe(3);
        expect(summary.images).toBe(4);
        // 商详套图的 9 模块名要跟参考站一致，收尾是「收藏图」不是「收尾图」。
        expect(task.modules?.map((module) => module.name)).toContain("收藏图");
        expect(task.modules?.map((module) => module.name)).not.toContain("收尾图");
    });

    test("default selection opens the panel with something already chosen", () => {
        const task = creationTask("ecommerce-hero")!;
        const selection = defaultModuleSelection(task);
        expect(selectedModuleSummary(task, selection).kinds).toBe(2);
        expect(selectedModuleSummary(task, {}).images).toBe(0);
    });

    test("prompt assembles scenario base + task directives + notes, with slot roles", () => {
        const task = creationTask("ecommerce-image-set")!;
        const prompt = buildCreationTaskPrompt({
            task,
            slotLabels: { product: ["图片1"], model: [], background: [] },
            moduleSelection: { hero: 1, "selling-point": 2 },
            choiceValues: { language: "中文", ratio: "3:4" },
            notes: "禁用元素：医疗器械字样",
            settings: { ratio: "3:4", seconds: "6" },
        });
        expect(prompt).toContain("电商美工");
        expect(prompt).toContain("@图片1");
        expect(prompt).toContain("本次共需产出 2 类、3 张图");
        expect(prompt).toContain("图文语言：中文");
        expect(prompt).toContain("图片比例：3:4");
        expect(prompt).toContain("禁用元素：医疗器械字样");
        // 视频任务不该混进图片比例文案。
        const videoTask = creationTask("ecommerce-spoken-video")!;
        const videoPrompt = buildCreationTaskPrompt({
            task: videoTask,
            slotLabels: { product: ["图片1"] },
            moduleSelection: {},
            choiceValues: {},
            notes: "",
            settings: { ratio: "16:9", seconds: "20" },
        });
        expect(videoPrompt).toContain("单段时长：20 秒");
        expect(videoPrompt).not.toContain("图片比例");
        // 必填槽位缺失时必须显式告诉模型不要编造主体，而不是静默放过。
        expect(buildCreationTaskPrompt({ task, slotLabels: {}, moduleSelection: {}, choiceValues: {}, notes: "", settings: { ratio: "3:4", seconds: "6" } }))
            .toContain("缺失，需在结果中避免编造商品主体");
    });
});