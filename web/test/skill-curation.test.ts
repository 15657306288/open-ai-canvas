import { describe, expect, test } from "bun:test";
import { curationQuery, matchesCuration } from "../src/components/skills/skill-curation-browser";
import type { SkillCuration } from "../src/services/api/skill-curation";
import type { Skill } from "../src/services/api/skills";

describe("platform curation", () => {
    const data: SkillCuration = { enabled: false, revision: 0, categories: [], assignments: [{ skillId: "one", categoryId: "child" }] };
    const skill = { skillId: "one", tag: "drama" } as Skill;
    test("disabled mode preserves existing requests and visible skills", () => {
        expect(curationQuery(data, "child")).toEqual({});
        expect(matchesCuration(skill, data, "missing")).toBe(true);
    });
    test("enabled mode filters by assignment and includes unassigned skills", () => {
        const enabled = { ...data, enabled: true };
        expect(curationQuery(enabled, "child")).toEqual({ platformCategoryId: "child" });
        expect(matchesCuration(skill, enabled, "child")).toBe(true);
        expect(matchesCuration(skill, enabled, "__uncategorized__")).toBe(false);
        expect(matchesCuration({ ...skill, skillId: "two" }, enabled, "__uncategorized__")).toBe(true);
    });
});
