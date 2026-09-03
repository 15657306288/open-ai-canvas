import { describe, expect, test } from "bun:test";

import type { Skill, SkillPackageFile, SkillPackageFileContent } from "../src/services/api/skills";
import { createSkillRuntime, resolveSkillMentions, composeSkillsForTurn, isFirstPartyDefaultSkill, firstPartyDefaultSkillIdsForProfile, getBenchmarkSkillMode } from "../src/services/skill-runtime";

function skill(overrides: Partial<Skill> = {}): Skill {
    return {
        skill_id: "director",
        skill_name: "AI导演",
        description: "导演工作流",
        version_id: "version-2",
        version: "2.0.0",
        content_hash: "hash",
        file_count: 5,
        total_bytes: 1024,
        source_type: "zip",
        source_url: "",
        source_ref: "",
        source_subdir: "",
        source_commit: "",
        sync_status: "synced",
        auto_update: false,
        last_checked_at: 0,
        last_synced_at: 0,
        status: 1,
        markdown_url: "",
        create_time: 0,
        update_time: 0,
        source: 0,
        tag: "影视",
        sort_weight: 0,
        is_private: true,
        like_count: 0,
        is_like: false,
        owner_uid: "user",
        effective_user: { name: "用户", avatar_url: "", uid: "user" },
        original_skill_id: null,
        showcase_media: [],
        added_count: 1,
        is_test: false,
        extra_info: "",
        is_added: true,
        is_owner: true,
        ...overrides,
    };
}

function file(path: string, content: string, kind: SkillPackageFile["kind"] = "markdown"): SkillPackageFileContent {
    return {
        file: { path, kind, mime_type: "text/markdown", size: content.length, sha256: `sha-${path}` },
        content,
        binary: false,
    };
}

describe("skill runtime", () => {
    test("技能引用解析由统一规则同时支持稳定 token 和自然提及", () => {
        const director = skill();
        const storyboard = skill({ skill_id: "storyboard", skill_name: "小说转分镜" });

        expect(resolveSkillMentions("用 @[skill:director] 处理", [director, storyboard]).map((item) => item.skill_id)).toEqual(["director"]);
        expect(resolveSkillMentions("请用 @小说转分镜。", [director, storyboard]).map((item) => item.skill_id)).toEqual(["storyboard"]);
        expect(resolveSkillMentions("@AI导演增强版", [director])).toEqual([]);
    });

    test("普通生成只加载入口和与当前任务最相关的直接引用文本", async () => {
        const entry = [
            "# AI导演",
            "视频提示词读取 `references/prompt_templates.md`。",
            "角色资产读取 [角色规则](references/character_assets.md)。",
            "维护脚本见 `scripts/audit_skill.py`。",
            "项目模板见 `assets/project-ledger-template.md`。",
        ].join("\n");
        const files: SkillPackageFile[] = [
            file("SKILL.md", entry).file,
            file("references/prompt_templates.md", "视频提示词模板正文").file,
            file("references/character_assets.md", "角色资产正文").file,
            file("scripts/audit_skill.py", "print('audit')", "code").file,
            file("assets/project-ledger-template.md", "台账模板").file,
        ];
        const readPaths: string[] = [];
        const runtime = createSkillRuntime({
            getFile: async (_id, path) => {
                readPaths.push(path);
                if (path === "SKILL.md") return { file: file(path, entry) };
                return { file: file(path, path.includes("prompt_templates") ? "视频提示词模板正文" : "角色资产正文") };
            },
            listFiles: async () => ({ files }),
            searchFiles: async () => ({ results: [] }),
            getBundle: async () => { throw new Error("不应读取完整包"); },
        });

        const result = await runtime.prepare({ profile: "canvas", prompt: "@[skill:director] 帮我生成视频提示词", skills: [skill()] });

        expect(result.prompt).toContain('<skill-file path="SKILL.md">');
        expect(result.prompt).toContain('<skill-file path="references/prompt_templates.md">');
        expect(readPaths).not.toContain("references/character_assets.md");
        expect(readPaths).not.toContain("scripts/audit_skill.py");
        expect(readPaths).not.toContain("assets/project-ledger-template.md");
        expect(result.prompt).toContain("【用户任务】\n@AI导演 帮我生成视频提示词");
        expect(result.metadata.skillIds).toEqual(["director"]);
    });

    test("本地 Agent 通过同一 Runtime 投递完整原生技能包", async () => {
        const runtime = createSkillRuntime({
            getFile: async () => { throw new Error("不应读取单文件"); },
            listFiles: async () => ({ files: [] }),
            searchFiles: async () => ({ results: [] }),
            getBundle: async () => ({
                bundle: {
                    skill_id: "director",
                    name: "AI导演",
                    description: "导演工作流",
                    version_id: "version-2",
                    version: "2.0.0",
                    content_hash: "hash",
                    files: [{ path: "SKILL.md", mime_type: "text/markdown", content_base64: "IyBBSuWvv+a8lA==" }],
                },
            }),
        });

        const result = await runtime.prepare({ profile: "localAgent", prompt: "@[skill:director] 开始", skills: [skill()] });

        expect(result.prompt).toBe("@AI导演 开始");
        expect(result.skills).toEqual([{ skillId: "director", name: "AI导演", description: "导演工作流", version: "2.0.0", files: [{ path: "SKILL.md", mimeType: "text/markdown", contentBase64: "IyBBSuWvv+a8lA==" }] }]);
    });

    test("在线 Agent 的技能工具由 Runtime 注册表统一执行", async () => {
        const runtime = createSkillRuntime({
            getFile: async () => ({ file: file("SKILL.md", "# AI导演") }),
            listFiles: async () => ({ files: [] }),
            searchFiles: async () => ({ results: [] }),
            getBundle: async () => { throw new Error("不应读取完整包"); },
        });

        expect(runtime.agentToolNames("onlineAgent").has("canvas_get_skill")).toBe(true);
        const result = await runtime.executeAgentTool("onlineAgent", "canvas_get_skill", { skillId: "director" }, [skill()]);
        expect(result?.ok).toBe(true);
        expect(result && "data" in result ? result.data : null).toMatchObject({ skillId: "director", version: "2.0.0" });
    });
});

describe("第一方默认技能组合（按profile资格边界）", () => {
    const storyboardDirector = skill({ skill_id: "storyboard-director", skill_name: "分镜导演", is_added: true });
    const userSkill1 = skill({ skill_id: "user-skill-1", skill_name: "用户技能1", is_added: true });
    const userSkill2 = skill({ skill_id: "user-skill-2", skill_name: "用户技能2", is_added: true });
    const userSkill3 = skill({ skill_id: "user-skill-3", skill_name: "用户技能3", is_added: true });
    const userSkill4 = skill({ skill_id: "user-skill-4", skill_name: "用户技能4", is_added: true });
    const userSkill5 = skill({ skill_id: "user-skill-5", skill_name: "用户技能5", is_added: true });
    const notAdded = skill({ skill_id: "not-added", skill_name: "未添加技能", is_added: false });

    const allProfiles = ["canvas", "creation", "shortDrama", "director", "onlineAgent", "localAgent"] as const;
    const eligibleProfiles = ["localAgent"] as const;
    const nonEligibleProfiles = ["canvas", "creation", "shortDrama", "director", "onlineAgent"] as const;

    test("isFirstPartyDefaultSkill 按profile正确识别", () => {
        expect(isFirstPartyDefaultSkill("storyboard-director", "localAgent")).toBe(true);
        expect(isFirstPartyDefaultSkill("storyboard-director", "canvas")).toBe(false);
        expect(isFirstPartyDefaultSkill("storyboard-director", "creation")).toBe(false);
        expect(isFirstPartyDefaultSkill("storyboard-director", "shortDrama")).toBe(false);
        expect(isFirstPartyDefaultSkill("storyboard-director", "director")).toBe(false);
        expect(isFirstPartyDefaultSkill("storyboard-director", "onlineAgent")).toBe(false);
        expect(isFirstPartyDefaultSkill("user-skill-1", "localAgent")).toBe(false);
        // 不传profile时返回全局判断（兼容旧代码）
        expect(isFirstPartyDefaultSkill("storyboard-director")).toBe(true);
    });

    test("firstPartyDefaultSkillIdsForProfile 返回正确列表", () => {
        expect(firstPartyDefaultSkillIdsForProfile("localAgent")).toEqual(["storyboard-director"]);
        for (const profile of nonEligibleProfiles) {
            expect(firstPartyDefaultSkillIdsForProfile(profile)).toEqual([]);
        }
    });

    test.each(eligibleProfiles)("eligible profile %s: 无用户技能时默认技能可用且仅一次", (profile) => {
        const result = composeSkillsForTurn({ profile, prompt: "随便说点什么", skills: [storyboardDirector], maxSkills: 4 });
        expect(result).toHaveLength(1);
        expect(result[0].skill_id).toBe("storyboard-director");
    });

    test.each(eligibleProfiles)("eligible profile %s: 1用户技能+默认技能两者都可用", (profile) => {
        const result = composeSkillsForTurn({ profile, prompt: "@用户技能1 测试", skills: [storyboardDirector, userSkill1], maxSkills: 4 });
        expect(result).toHaveLength(2);
        expect(result.map((s) => s.skill_id)).toContain("user-skill-1");
        expect(result.map((s) => s.skill_id)).toContain("storyboard-director");
    });

    test.each(eligibleProfiles)("eligible profile %s: maxSkills用户技能时默认技能仍可用（不占容量）", (profile) => {
        const allSkills = [storyboardDirector, userSkill1, userSkill2, userSkill3, userSkill4, userSkill5];
        const prompt = "@用户技能1 @用户技能2 @用户技能3 @用户技能4 @用户技能5 测试";
        const result = composeSkillsForTurn({ profile, prompt, skills: allSkills, maxSkills: 4 });
        expect(result).toHaveLength(5);
        expect(result.map((s) => s.skill_id)).toContain("storyboard-director");
        const userSkillsInResult = result.filter((s) => s.skill_id !== "storyboard-director");
        expect(userSkillsInResult).toHaveLength(4);
    });

    test.each(eligibleProfiles)("eligible profile %s: 用户已提及默认技能时不重复", (profile) => {
        const result = composeSkillsForTurn({ profile, prompt: "@分镜导演 测试", skills: [storyboardDirector], maxSkills: 4 });
        expect(result).toHaveLength(1);
        expect(result[0].skill_id).toBe("storyboard-director");
    });

    test.each(nonEligibleProfiles)("non-eligible profile %s: 无用户提及时默认技能不自动注入", (profile) => {
        const result = composeSkillsForTurn({ profile, prompt: "随便说点什么", skills: [storyboardDirector], maxSkills: 4 });
        expect(result).toHaveLength(0);
        expect(result.map((s) => s.skill_id)).not.toContain("storyboard-director");
    });

    test.each(nonEligibleProfiles)("non-eligible profile %s: 用户技能行为不变（提及则包含）", (profile) => {
        const result = composeSkillsForTurn({ profile, prompt: "@用户技能1 测试", skills: [storyboardDirector, userSkill1], maxSkills: 4 });
        expect(result).toHaveLength(1);
        expect(result[0].skill_id).toBe("user-skill-1");
        expect(result.map((s) => s.skill_id)).not.toContain("storyboard-director");
    });

    test.each(nonEligibleProfiles)("non-eligible profile %s: 用户显式选择默认技能时保留（如架构允许）", (profile) => {
        const result = composeSkillsForTurn({ profile, prompt: "测试", skills: [storyboardDirector, userSkill1], maxSkills: 4, selectedSkillIds: ["storyboard-director"] });
        // selectedSkillIds路径下，resolveSkillMentions只返回显式选择的技能
        expect(result.map((s) => s.skill_id)).toContain("storyboard-director");
    });

    test("所有profile覆盖完整性", () => {
        expect(allProfiles).toHaveLength(6);
        expect(eligibleProfiles).toHaveLength(1);
        expect(nonEligibleProfiles).toHaveLength(5);
        // 验证eligible + nonEligible = all
        const allSet = new Set([...eligibleProfiles, ...nonEligibleProfiles]);
        expect(allSet.size).toBe(allProfiles.length);
    });

    test("eligible profile: 未添加（is_added=false）的技能不出现在结果中", () => {
        const result = composeSkillsForTurn({ profile: "localAgent", prompt: "测试", skills: [storyboardDirector, notAdded], maxSkills: 4 });
        expect(result).toHaveLength(1);
        expect(result[0].skill_id).toBe("storyboard-director");
    });

    test("eligible profile: 显式选择技能优先级最高，默认技能追加不覆盖", () => {
        const result = composeSkillsForTurn({ profile: "localAgent", prompt: "测试", skills: [storyboardDirector, userSkill1, userSkill2], maxSkills: 4, selectedSkillIds: ["user-skill-1"] });
        expect(result).toHaveLength(2);
        expect(result[0].skill_id).toBe("user-skill-1");
        expect(result[1].skill_id).toBe("storyboard-director");
    });

    test("eligible profile: 用户技能顺序确定性，默认技能始终在末尾", () => {
        const result = composeSkillsForTurn({ profile: "localAgent", prompt: "@用户技能2 @用户技能1 测试", skills: [storyboardDirector, userSkill1, userSkill2], maxSkills: 4 });
        const ids = result.map((s) => s.skill_id);
        expect(ids[ids.length - 1]).toBe("storyboard-director");
    });

    test("eligible profile: 空prompt时仅默认技能可用（用户技能不被自动选择）", () => {
        const result = composeSkillsForTurn({ profile: "localAgent", prompt: "   ", skills: [storyboardDirector, userSkill1], maxSkills: 4 });
        expect(result).toHaveLength(1);
        expect(result[0].skill_id).toBe("storyboard-director");
    });

    test("负例：普通画布编辑命令不强制激活storyboard-director（AVAILABLE != FORCED）", () => {
        // storyboard-director作为可用的第一方技能存在于skills列表中
        // 但composeSkillsForTurn只是将其放入effectiveSkills，不强制LLM使用
        const result = composeSkillsForTurn({ profile: "localAgent", prompt: "把当前选中的节点往左移动一点", skills: [storyboardDirector], maxSkills: 4 });
        // 默认技能被包含在effective skills中（AVAILABLE）
        expect(result).toHaveLength(1);
        expect(result[0].skill_id).toBe("storyboard-director");
        // 但composeSkillsForTurn不做意图分类，不强制激活（FORCED）
        // LLM/Codex根据技能描述自行决定是否使用
        // 这里验证的是组合层不做意图判断，只是正确地包含可用技能
    });
});

describe("Benchmark Skill Mode", () => {
    const storyboardDirector = skill({ skill_id: "storyboard-director", skill_name: "分镜导演", is_added: true });
    const userSkill1 = skill({ skill_id: "user-skill-1", skill_name: "用户技能1", is_added: true });

    test("1. normal模式仍为localAgent注入storyboard-director", () => {
        const result = composeSkillsForTurn({ profile: "localAgent", prompt: "测试", skills: [storyboardDirector], maxSkills: 4, benchmarkMode: "normal" });
        expect(result).toHaveLength(1);
        expect(result[0].skill_id).toBe("storyboard-director");
    });

    test("2. baseline模式不注入storyboard-director", () => {
        const result = composeSkillsForTurn({ profile: "localAgent", prompt: "测试", skills: [storyboardDirector], maxSkills: 4, benchmarkMode: "baseline" });
        expect(result).toHaveLength(0);
        expect(result.map((s) => s.skill_id)).not.toContain("storyboard-director");
    });

    test("3. director模式包含storyboard-director恰好一次", () => {
        const result = composeSkillsForTurn({ profile: "localAgent", prompt: "测试", skills: [storyboardDirector], maxSkills: 4, benchmarkMode: "director" });
        expect(result).toHaveLength(1);
        expect(result[0].skill_id).toBe("storyboard-director");
        // 去重验证
        const ids = result.map((s) => s.skill_id);
        expect(ids.filter((id) => id === "storyboard-director")).toHaveLength(1);
    });

    test("4. 普通用户技能在所有模式下正常工作", () => {
        for (const mode of ["normal", "baseline", "director"] as const) {
            const result = composeSkillsForTurn({ profile: "localAgent", prompt: "@用户技能1 测试", skills: [storyboardDirector, userSkill1], maxSkills: 4, benchmarkMode: mode });
            expect(result.map((s) => s.skill_id)).toContain("user-skill-1");
        }
    });

    test("5. 非localAgent profile行为不变（benchmarkMode不影响）", () => {
        for (const profile of ["canvas", "creation", "shortDrama", "director", "onlineAgent"] as const) {
            const result = composeSkillsForTurn({ profile, prompt: "测试", skills: [storyboardDirector], maxSkills: 4, benchmarkMode: "director" });
            // 非eligible profile不注入默认技能
            expect(result.map((s) => s.skill_id)).not.toContain("storyboard-director");
        }
    });

    test("默认benchmarkMode为normal", () => {
        const result = composeSkillsForTurn({ profile: "localAgent", prompt: "测试", skills: [storyboardDirector], maxSkills: 4 });
        expect(result).toHaveLength(1);
        expect(result[0].skill_id).toBe("storyboard-director");
    });

    test("baseline模式下用户提及storyboard-director仍可包含（显式选择）", () => {
        const result = composeSkillsForTurn({ profile: "localAgent", prompt: "@分镜导演 测试", skills: [storyboardDirector], maxSkills: 4, benchmarkMode: "baseline" });
        // baseline模式不自动注入，但用户显式提及的技能仍包含
        expect(result).toHaveLength(1);
        expect(result[0].skill_id).toBe("storyboard-director");
    });
});
