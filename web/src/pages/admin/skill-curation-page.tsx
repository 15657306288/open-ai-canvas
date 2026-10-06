import { useEffect, useState } from "react";
import { Alert, App, Button, Input, InputNumber, Select, Space } from "antd";
import { AdminPageFrame } from "./components/admin-shell";
import { AdminDataTable } from "./components/admin-ui";
import { AdminSwitch } from "./ui/controls";
import { getSkillCuration, updateSkillCuration, type CurationCategory, type CurationChange, type SkillCuration, type CurationRoot } from "@/services/api/skill-curation";
import { curationIcon, curationIconKeys, curationIconLabels } from "@/components/skills/skill-curation-browser";
import { listSkills, type Skill } from "@/services/api/skills";

export default function SkillCurationPage() {
    const { message } = App.useApp();
    const [data, setData] = useState<SkillCuration | null>(null);
    const [error, setError] = useState("");
    const [busy, setBusy] = useState(false);
    const [category, setCategory] = useState<CurationCategory>({ id: "", rootTag: "drama", name: "", sortOrder: 0, enabled: true });
    const [search, setSearch] = useState("");
    const [skills, setSkills] = useState<Skill[]>([]);
    const [skillId, setSkillId] = useState("");
    const [categoryIds, setCategoryIds] = useState<string[]>([]);
    const [root, setRoot] = useState<CurationRoot>({ id: "", name: "", iconKey: "shapes", sortOrder: 0, enabled: true });
    const [assignedRoot, setAssignedRoot] = useState("");
    const reload = async () => { try { setData(await getSkillCuration(true)); setError(""); } catch (e) { setError(String(e)); } };
    useEffect(() => { void reload(); }, []);
    useEffect(() => {
        let cancelled = false;
        const timer = setTimeout(() => { listSkills({ scope: "public", search, pageSize: 50 }).then((result) => { if (!cancelled) setSkills(result.skills); }).catch((e) => { if (!cancelled) setError(String(e)); }); }, 250);
        return () => { cancelled = true; clearTimeout(timer); };
    }, [search]);
    const save = async (change: CurationChange) => {
        if (!data) return;
        setBusy(true);
        try { setData(await updateSkillCuration(data.revision, change)); setError(""); message.success("已保存"); window.dispatchEvent(new Event("canvas-skills-changed")); }
        catch (e) { setError(`${String(e)}；请重新加载后核对修改`); }
        finally { setBusy(false); }
    };
    const selectedSkill = skills.find((skill) => skill.skillId === skillId);
    return <AdminPageFrame title="技能分类" description="平台策展模式默认关闭；开启后使用后台维护的分类。" scroll actions={<Button onClick={() => void reload()}>重新加载</Button>}>
        <Space orientation="vertical" className="w-full" size="large">
            {error && <Alert type="error" title={error} />}
            <Space><AdminSwitch aria-label="平台策展模式" checked={data?.enabled ?? false} disabled={!data || busy} onChange={(enabled) => void save({ enabled })} /><span>平台策展模式</span></Space>
            <AdminDataTable<CurationRoot> table={{ rowKey: "id", dataSource: data?.roots || [], pagination: false, loading: !data, columns: [
                { title: "一级分类", dataIndex: "name" }, { title: "图示", render: (_, item) => { const Icon = curationIcon(item.iconKey); return <Icon size={18} />; } },
                { title: "排序", dataIndex: "sortOrder" }, { title: "状态", render: (_, item) => item.enabled ? "启用" : "停用" },
                { title: "操作", render: (_, item) => <Button onClick={() => setRoot({ ...item })}>编辑</Button> },
            ] }} />
            <Space wrap>
                <Input aria-label="一级分类名称" placeholder="一级分类名称" maxLength={64} value={root.name} onChange={(e) => setRoot({ ...root, name: e.target.value })} />
                <Select aria-label="一级分类图示" value={root.iconKey || "shapes"} options={curationIconKeys.map((key) => { const Icon = curationIcon(key); return { value: key, label: <Space><Icon size={16} />{curationIconLabels[key]}</Space> }; })} onChange={(iconKey) => setRoot({ ...root, iconKey })} />
                <InputNumber aria-label="一级分类排序" precision={0} value={root.sortOrder} onChange={(sortOrder) => setRoot({ ...root, sortOrder: sortOrder ?? 0 })} />
                <AdminSwitch aria-label="一级分类启用" checked={root.enabled} onChange={(enabled) => setRoot({ ...root, enabled })} />
                <Button type="primary" loading={busy} disabled={!data || !root.name.trim()} onClick={() => void save({ root })}>{root.id ? "保存一级分类" : "新增一级分类"}</Button>
                <Button onClick={() => setRoot({ id: "", name: "", iconKey: "shapes", sortOrder: 0, enabled: true })}>新建一级分类</Button>
            </Space>
            <AdminDataTable<CurationCategory> table={{ rowKey: "id", dataSource: data?.categories || [], pagination: false, loading: !data, columns: [
                { title: "一级分类", render: (_, row) => data?.roots?.find((item) => item.id === row.rootTag)?.name || "未归入启用分类" }, { title: "子分类", dataIndex: "name" }, { title: "排序", dataIndex: "sortOrder" },
                { title: "状态", render: (_, row) => row.enabled ? "启用" : "停用" }, { title: "操作", render: (_, row) => <Button onClick={() => setCategory({ ...row })}>编辑</Button> },
            ] }} />
            <Space wrap>
                <Select aria-label="所属一级分类" value={category.rootTag} disabled={Boolean(category.id)} options={data?.roots?.map((item) => ({ value: item.id, label: item.name, disabled: !item.enabled }))} onChange={(rootTag) => setCategory({ ...category, rootTag })} />
                <Input aria-label="分类名称" placeholder="分类名称" value={category.name} maxLength={64} onChange={(e) => setCategory({ ...category, name: e.target.value })} />
                <InputNumber aria-label="排序" value={category.sortOrder} precision={0} onChange={(sortOrder) => setCategory({ ...category, sortOrder: sortOrder ?? 0 })} />
                <AdminSwitch aria-label="分类启用" checked={category.enabled} onChange={(enabled) => setCategory({ ...category, enabled })} />
                <Button type="primary" loading={busy} disabled={!data || !category.name.trim()} onClick={() => void save({ category })}>{category.id ? "保存分类" : "新增子分类"}</Button>
                <Button onClick={() => setCategory({ id: "", rootTag: "drama", name: "", sortOrder: 0, enabled: true })}>新建</Button>
            </Space>
            <Space wrap>
                <Input aria-label="搜索公开技能" placeholder="搜索公开技能（最多50条）" value={search} onChange={(e) => setSearch(e.target.value)} />
                <Select aria-label="选择技能" value={skillId || undefined} placeholder="选择技能" options={skills.map((skill) => ({ value: skill.skillId, label: skill.skillName }))} onChange={(id) => { setSkillId(id); setAssignedRoot(data?.rootAssignments?.find((item) => item.skillId === id)?.rootId || ""); setCategoryIds(data?.assignments.filter((item) => item.skillId === id).map((item) => item.categoryId) || []); }} />
                <Select aria-label="技能一级分类" value={assignedRoot} options={[{ value: "", label: "按原始分类" }, ...(data?.roots || []).map((item) => ({ value: item.id, label: item.name, disabled: !item.enabled }))]} onChange={(id) => { setAssignedRoot(id); setCategoryIds([]); }} />
                <Select mode="multiple" aria-label="技能子分类" placeholder="选择子分类，留空清除" value={categoryIds} onChange={setCategoryIds} options={data?.categories.filter((item) => item.enabled && item.rootTag === (assignedRoot || selectedSkill?.tag)).map((item) => ({ value: item.id, label: item.name }))} />
                <Button loading={busy} disabled={!skillId || !data} onClick={() => void save({ assignment: { skillId, categoryIds, rootId: assignedRoot } })}>保存技能归类</Button>
            </Space>
        </Space>
    </AdminPageFrame>;
}
