import { useEffect, useState } from "react";
import { Alert, Button, Select, Space } from "antd";
import { getActiveUserScope } from "@/lib/user-scope";
import { getSkillCuration, type SkillCuration } from "@/services/api/skill-curation";
import type { Skill } from "@/services/api/skills";
import { fallbackSkillCategories } from "@/pages/skills/skill-catalog";

export function curationQuery(data: SkillCuration | null, value: string) {
    if (!data?.enabled || !value) return {};
    return value === "__uncategorized__" ? { platformUncategorized: true } : { platformCategoryId: value };
}

export function useSkillCuration(active = true) {
    const scope = getActiveUserScope();
    const [result, setResult] = useState<{ scope: string; data: SkillCuration } | null>(null);
    const [error, setError] = useState("");
    const [reload, setReload] = useState(0);
    useEffect(() => {
        if (!active) return;
        let cancelled = false;
        setError("");
        getSkillCuration().then((data) => { if (!cancelled) setResult({ scope, data }); }).catch(() => { if (!cancelled) { setResult(null); setError("分类暂不可用，请重试"); } });
        const refresh = () => setReload((value) => value + 1);
        window.addEventListener("focus", refresh);
        window.addEventListener("canvas-skills-changed", refresh);
        return () => { cancelled = true; window.removeEventListener("focus", refresh); window.removeEventListener("canvas-skills-changed", refresh); };
    }, [active, scope, reload]);
    return { curation: result?.scope === scope ? result.data : null, error, retry: () => setReload((value) => value + 1) };
}

export function matchesCuration(skill: Skill, data: SkillCuration | null, category: string) {
    if (!data?.enabled || !category) return true;
    const ids = data.assignments.filter((item) => item.skillId === skill.skillId).map((item) => item.categoryId);
    return category === "__uncategorized__" ? ids.length === 0 : ids.includes(category);
}

export function SkillCurationBrowser({ data, value, onChange, error, retry }: { data: SkillCuration | null; value: string; onChange: (value: string) => void; error: string; retry: () => void }) {
    useEffect(() => {
        if ((!data?.enabled || (value && value !== "__uncategorized__" && !data.categories.some((item) => item.id === value))) && value) onChange("");
    }, [data, value, onChange]);
    if (error) return <Alert type="warning" title={error} action={<Button onClick={retry}>重试</Button>} />;
    if (!data?.enabled) return null;
    return <Space wrap><span>平台分类</span><Select aria-label="平台子分类" value={value} onChange={onChange} popupMatchSelectWidth={false} options={[
        { value: "", label: "全部子分类" }, { value: "__uncategorized__", label: "未细分" },
        ...data.categories.map((item) => ({ value: item.id, label: `${fallbackSkillCategories.find((root) => root.value === item.rootTag)?.label || item.rootTag} / ${item.name}` })),
    ]} /></Space>;
}
