import { App, Button, Checkbox, Input, Popconfirm, Segmented, Tag, Tooltip } from "antd";
import { Ban, Check, CircleCheck, Clock3, Gauge, Globe, KeyRound, ListChecks, Loader2, MonitorSmartphone, PencilLine, Plus, QrCode, RotateCcw, Snowflake, Tag as TagIcon, TimerReset, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { PageHeader, WorkspacePage } from "@/components/layout/workspace-page";
import { WorkspaceState } from "@/components/layout/workspace-state";
import { AppModal } from "@/components/ui/product/app-modal/app-modal";
import { readAxiosError } from "@/services/api/image-response";
import { cancelDoubaoQrLogin, fetchDoubaoQrStatus, startDoubaoQrLogin, POOL_SITE_META, type DoubaoQrSession, type PoolSite } from "@/services/api/doubao-accounts";
import { cn } from "@/lib/utils";
import {
    DOUBAO_COOLDOWN_MINUTES,
    DOUBAO_STATUS_META,
    effectiveStatus,
    useDoubaoAccountStore,
    type DoubaoAccount,
} from "@/stores/use-doubao-account-store";

type StatusFilter = DoubaoAccount["state"] | "all";

/** 顶部站点切换只暴露已接通生成链路的站点；即梦仍在类型与后端中保留，但不再作为入口展示。 */
const VISIBLE_POOL_SITES: PoolSite[] = ["doubao", "dola"];

/** 账号池健康度统计卡：点击即按该状态筛选（选中态用描边+角标，不只靠颜色）。 */
function FilterStatCard({ label, value, tone, active, onClick }: { label: string; value: number; tone: "neutral" | "success" | "warning" | "error" | "muted"; active: boolean; onClick: () => void }) {
    const toneClass = {
        neutral: "text-foreground",
        success: "text-emerald-500 dark:text-emerald-400",
        warning: "text-amber-500 dark:text-amber-400",
        error: "text-red-500 dark:text-red-400",
        muted: "text-foreground/45",
    }[tone];
    return (
        <button
            type="button"
            onClick={onClick}
            aria-pressed={active}
            className={cn(
                "relative min-h-11 rounded-lg border bg-surface px-4 py-3 text-left transition-[border-color,background-color,box-shadow] duration-150 select-none",
                "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary",
                active ? "border-primary/60 bg-primary/[.06] ring-1 ring-primary/30" : "border-border/60 hover:border-border hover:bg-surface-hover",
            )}
        >
            <span className={cn("block text-xl font-semibold leading-7 tabular-nums", toneClass)}>{value}</span>
            <span className={cn("mt-0.5 block text-xs", active ? "font-medium text-foreground/80" : "text-foreground/55")}>{label}</span>
            {active ? <Check className="absolute top-2.5 right-2.5 size-3.5 text-primary" aria-hidden /> : null}
        </button>
    );
}

export default function AccountsPage() {
    const { message } = App.useApp();
    const site = useDoubaoAccountStore((state) => state.site);
    const setSite = useDoubaoAccountStore((state) => state.setSite);
    const accounts = useDoubaoAccountStore((state) => state.accounts);
    const stats = useDoubaoAccountStore((state) => state.stats);
    const loading = useDoubaoAccountStore((state) => state.loading);
    const loaded = useDoubaoAccountStore((state) => state.loaded);
    const refresh = useDoubaoAccountStore((state) => state.refresh);
    const addAccount = useDoubaoAccountStore((state) => state.addAccount);
    const bulkImport = useDoubaoAccountStore((state) => state.bulkImport);
    const updateAccount = useDoubaoAccountStore((state) => state.updateAccount);
    const removeAccounts = useDoubaoAccountStore((state) => state.removeAccounts);
    const batch = useDoubaoAccountStore((state) => state.batch);
    const clearCooldown = useDoubaoAccountStore((state) => state.clearCooldown);
    const clearAllCooldowns = useDoubaoAccountStore((state) => state.clearAllCooldowns);
    const cooldown = useDoubaoAccountStore((state) => state.cooldown);

    const [filter, setFilter] = useState<StatusFilter>("all");
    const [selected, setSelected] = useState<Set<string>>(new Set());
    const [pasteOpen, setPasteOpen] = useState(false);
    const [bulkOpen, setBulkOpen] = useState(false);
    const [qrOpen, setQrOpen] = useState(false);
    const [qrSite, setQrSite] = useState<PoolSite>("doubao");
    const [qrSession, setQrSession] = useState<DoubaoQrSession | null>(null);
    const [qrBusy, setQrBusy] = useState(false);
    const qrPollRef = useRef<ReturnType<typeof setInterval> | null>(null);
    const [editing, setEditing] = useState<DoubaoAccount | null>(null);
    const [taggingIds, setTaggingIds] = useState<string[] | null>(null);
    const [busy, setBusy] = useState(false);

    // 弹窗表单临时值（受控），随弹窗开关重置。
    const [pasteName, setPasteName] = useState("");
    const [pasteCookie, setPasteCookie] = useState("");
    const [bulkTags, setBulkTags] = useState("");
    const [bulkText, setBulkText] = useState("");
    const [editName, setEditName] = useState("");
    const [editTags, setEditTags] = useState("");
    const [editNote, setEditNote] = useState("");
    const [editEnabled, setEditEnabled] = useState(true);
    const [tagInput, setTagInput] = useState("");

    useEffect(() => {
        refresh().catch((error) => message.error(readAxiosError(error, "账号池加载失败")));
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    const counters = useMemo(
        () => ({
            total: stats?.accountCount ?? accounts.length,
            ready: stats?.availableCount ?? 0,
            cooling: stats?.coolingCount ?? 0,
            expired: stats?.expiredCount ?? 0,
            disabled: stats?.disabledCount ?? 0,
        }),
        [stats, accounts],
    );

    const filtered = useMemo(
        () => (filter === "all" ? accounts : accounts.filter((account) => effectiveStatus(account) === filter)),
        [accounts, filter],
    );

    const toggleSelected = (id: string, checked: boolean) => {
        setSelected((prev) => {
            const next = new Set(prev);
            if (checked) next.add(id);
            else next.delete(id);
            return next;
        });
    };

    const selectedIds = useMemo(() => Array.from(selected), [selected]);

    const withBusy = async (task: () => Promise<unknown>, success?: string) => {
        setBusy(true);
        try {
            await task();
            if (success) message.success(success);
        } catch (error) {
            message.error(readAxiosError(error, "操作失败"));
        } finally {
            setBusy(false);
        }
    };

    const openEdit = (account: DoubaoAccount) => {
        setEditing(account);
        setEditName(account.label);
        setEditTags(account.tags.join(","));
        setEditNote(account.note);
        setEditEnabled(account.enabled);
    };

    const saveEdit = () => {
        if (!editing) return;
        void withBusy(async () => {
            await updateAccount(editing.id, {
                label: editName.trim() || editing.label,
                tags: editTags.split(/[,，]/).map((tag) => tag.trim()).filter(Boolean),
                note: editNote.trim(),
                enabled: editEnabled,
            });
            setEditing(null);
        }, "账号已更新");
    };

    const savePaste = () => {
        if (!pasteCookie.trim()) {
            message.warning("请粘贴完整 Cookie 或 sessionid");
            return;
        }
        void withBusy(async () => {
            await addAccount({ displayName: pasteName, cookieText: pasteCookie, setAsCurrent: accounts.length === 0 });
            setPasteOpen(false);
            setPasteName("");
            setPasteCookie("");
        }, "账号已加入账号池");
    };

    const saveBulk = () => {
        if (!bulkText.trim()) {
            message.warning("请粘贴账号列表：每行一个 Cookie / sessionid，# 开头为注释");
            return;
        }
        void withBusy(async () => {
            const unifiedTags = bulkTags.split(/[,，]/).map((tag) => tag.trim()).filter(Boolean);
            const result = await bulkImport({ text: bulkText, tags: unifiedTags });
            setBulkOpen(false);
            setBulkText("");
            setBulkTags("");
            const failedNote = result.failed.length ? `，${result.failed.length} 行失败` : "";
            message.success(`已导入 ${result.added} 个新账号、更新 ${result.updated} 个${failedNote}`);
        });
    };

    const saveTag = () => {
        if (!taggingIds?.length) return;
        const tags = tagInput.split(/[,，]/).map((tag) => tag.trim()).filter(Boolean);
        if (!tags.length) {
            message.warning("请输入至少一个标签");
            return;
        }
        void withBusy(async () => {
            await batch("tag", taggingIds, tags);
            setTaggingIds(null);
            setTagInput("");
        }, "标签已追加");
    };

    // 扫码登录：打开弹窗 → 启动后端浏览器会话 → 轮询状态直到终态。
    const stopQrPolling = useCallback(() => {
        if (qrPollRef.current) {
            clearInterval(qrPollRef.current);
            qrPollRef.current = null;
        }
    }, []);

    const openQrLogin = () => {
        const currentSite = site;
        setQrSite(currentSite);
        setQrOpen(true);
        setQrSession(null);
        setQrBusy(true);
        stopQrPolling();
        startDoubaoQrLogin(currentSite)
            .then(({ session }) => setQrSession(session))
            .catch((error) => {
                message.error(readAxiosError(error, "扫码登录启动失败"));
                setQrSession({ state: "failed", message: "启动失败", hasBrowser: false, elapsedText: "" });
            })
            .finally(() => setQrBusy(false));
    };

    useEffect(() => {
        if (!qrOpen) {
            stopQrPolling();
            return;
        }
        const state = qrSession?.state;
        if (state === "success") {
            stopQrPolling();
            message.success(`登录成功，${qrSession?.masked ?? ""} 已加入账号池`);
            refresh().catch(() => {});
            const timer = setTimeout(() => setQrOpen(false), 1600);
            return () => clearTimeout(timer);
        }
        if (state && state !== "waiting" && state !== "idle") {
            stopQrPolling();
            return;
        }
        stopQrPolling();
        qrPollRef.current = setInterval(() => {
            fetchDoubaoQrStatus(qrSite)
                .then(({ session }) => setQrSession(session))
                .catch(() => {});
        }, 1500);
        return stopQrPolling;
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [qrOpen, qrSite, qrSession?.state, stopQrPolling]);

    useEffect(() => stopQrPolling, [stopQrPolling]);

    const qrCancel = () => {
        void withBusy(async () => {
            await cancelDoubaoQrLogin(qrSite).catch(() => {});
            setQrOpen(false);
        });
    };

    const batchAction = (action: "enable" | "disable" | "clearCooldown" | "resetQuota" | "tag" | "remove") => {        if (!selectedIds.length) return;
        switch (action) {
            case "enable":
                void withBusy(async () => { await batch("enable", selectedIds); }, "已启用所选账号（额度已恢复）");
                break;
            case "disable":
                void withBusy(async () => { await batch("disable", selectedIds); }, "已停用所选账号");
                break;
            case "clearCooldown":
                void withBusy(async () => { await clearCooldown(selectedIds); }, "已清除冷却");
                break;
            case "resetQuota":
                void withBusy(async () => { await batch("reset-quota", selectedIds); }, "已重置所选账号的视频额度");
                break;
            case "tag":
                setTaggingIds(selectedIds);
                setTagInput("");
                break;
            case "remove":
                void withBusy(async () => {
                    await removeAccounts(selectedIds);
                    setSelected(new Set());
                }, `已删除 ${selectedIds.length} 个账号`);
                break;
        }
    };

    return (
        <WorkspacePage>
            <PageHeader
                title="账号池"
                actions={
                    <>
                        <Segmented
                            value={site}
                            onChange={(value) => void setSite(value as PoolSite)}
                            options={VISIBLE_POOL_SITES.map((key) => ({
                                value: key,
                                label: (
                                    <span className="flex items-center gap-1.5">
                                        <Globe className="size-3.5" aria-hidden />
                                        {POOL_SITE_META[key].label}
                                    </span>
                                ),
                            }))}
                        />
                        <Tooltip title={site === "dola" ? "打开 Dola 登录窗口（自动带入已有登录态），操作后自动保存完整 Cookie" : `打开${POOL_SITE_META[site].label}登录窗口，登录后自动保存`}>
                            <Button type="primary" icon={<QrCode className="size-4" />} onClick={openQrLogin}>
                                扫码登录
                            </Button>
                        </Tooltip>
                        <Button icon={<KeyRound className="size-4" />} onClick={() => { setPasteName(""); setPasteCookie(""); setPasteOpen(true); }}>
                            粘贴 Cookie
                        </Button>
                        <Button icon={<Plus className="size-4" />} onClick={() => { setBulkTags(""); setBulkText(""); setBulkOpen(true); }}>
                            批量导入
                        </Button>
                        <Button
                            icon={<TimerReset className="size-4" />}
                            disabled={!counters.cooling || busy}
                            onClick={() => void withBusy(() => clearAllCooldowns(), "已清除全部冷却")}
                        >
                            清除全部冷却
                        </Button>
                    </>
                }
            />

            {/* 站点说明常驻固定槽位：切换站点只换文案不增删块，避免整页跳动。 */}
            <div className="mt-4 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg border border-border/60 bg-surface px-3 py-2 text-xs text-foreground/60">
                {site === "doubao" ? (
                    <span>支持扫码登录或粘贴 Cookie；生成链路已接通，账号被风控时自动冷却并换号。视频按池限额扣额度（单账号 5 条），失败不扣，用完自动停用并切换下一账号。</span>
                ) : null}
                {site === "jimeng" ? (
                    <span>支持扫码登录或粘贴 Cookie；生成链路暂未接通，当前用于保存登录态。额度按池限额记账（seedance1.0 Fast 累计 33 秒）。</span>
                ) : null}
                {site === "dola" ? (
                    <span>
                        支持登录窗口：点上方「扫码登录」会弹出浏览器并自动带入已有登录态，在窗口中发一条消息（如遇滑块请完成验证）后自动保存完整 Cookie。视频按池限额扣额度（单账号 2 条），失败不扣，用完自动停用并切换下一账号。
                    </span>
                ) : null}
            </div>

            <div className="mt-4 grid grid-cols-2 gap-3 sm:grid-cols-5">
                <FilterStatCard label="全部账号" value={counters.total} tone="neutral" active={filter === "all"} onClick={() => setFilter("all")} />
                <FilterStatCard label="可用" value={counters.ready} tone="success" active={filter === "ready"} onClick={() => setFilter("ready")} />
                <FilterStatCard label="冷却中" value={counters.cooling} tone="warning" active={filter === "cooling"} onClick={() => setFilter("cooling")} />
                <FilterStatCard label="登录失效" value={counters.expired} tone="error" active={filter === "expired"} onClick={() => setFilter("expired")} />
                <FilterStatCard label="已停用" value={counters.disabled} tone="muted" active={filter === "disabled"} onClick={() => setFilter("disabled")} />
            </div>

            {/* 批量操作条常驻显示；未选中账号时按钮整体置灰。 */}
            <div className="mt-3 flex flex-wrap items-center gap-2 rounded-lg border border-primary/25 bg-primary/[.06] px-3 py-2">
                <span className="mr-1 flex items-center gap-1.5 text-xs text-foreground/70">
                    <ListChecks className="size-3.5" aria-hidden />
                    已选 <b className="tabular-nums">{selected.size}</b> 个
                </span>
                <Button size="small" disabled={busy || !selected.size} onClick={() => batchAction("enable")}>启用</Button>
                <Button size="small" disabled={busy || !selected.size} onClick={() => batchAction("disable")}>停用</Button>
                <Button size="small" disabled={busy || !selected.size} icon={<Snowflake className="size-3.5" />} onClick={() => batchAction("clearCooldown")}>清除冷却</Button>
                <Tooltip title="清零视频额度用量并重新启用（额度用完被自动停用的账号由此恢复）">
                    <Button size="small" disabled={busy || !selected.size} icon={<Gauge className="size-3.5" />} onClick={() => batchAction("resetQuota")}>重置额度</Button>
                </Tooltip>
                <Button size="small" disabled={busy || !selected.size} icon={<TagIcon className="size-3.5" />} onClick={() => batchAction("tag")}>打标签</Button>
                <Popconfirm title={`删除所选 ${selected.size} 个账号？`} okButtonProps={{ danger: true }} onConfirm={() => batchAction("remove")} disabled={!selected.size}>
                    <Button size="small" danger disabled={busy || !selected.size} icon={<Trash2 className="size-3.5" />}>删除</Button>
                </Popconfirm>
                <Button size="small" type="text" disabled={!selected.size} onClick={() => setSelected(new Set())}>取消选择</Button>
            </div>

            <div className="mt-3 flex flex-col gap-2.5">
                {filtered.map((account) => {
                    const status = effectiveStatus(account);
                    const meta = DOUBAO_STATUS_META[status];
                    const isSelected = selected.has(account.id);
                    return (
                        <div
                            key={account.id}
                            className={cn(
                                "flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg border bg-surface px-4 py-3 transition-colors",
                                isSelected ? "border-primary/45" : "border-border/60 hover:border-border",
                                account.active && "ring-1 ring-primary/35",
                            )}
                        >
                            <Checkbox
                                checked={isSelected}
                                onChange={(event) => toggleSelected(account.id, event.target.checked)}
                                aria-label={`选择 ${account.label}`}
                            />
                            <div className="flex min-w-40 flex-col">
                                <span className="flex items-center gap-1.5 text-sm font-medium leading-5">
                                    {account.label}
                                    {account.active ? <Tag color="blue" className="m-0">当前</Tag> : null}
                                </span>
                                <span className="text-xs text-foreground/45">
                                    {account.hasFullCookie ? `完整 Cookie ${account.masked}` : `sessionid ${account.masked}`}
                                    {account.tags.length ? ` · ${account.tags.join(" / ")}` : ""}
                                </span>
                            </div>
                            <Tag color={meta.color} className="m-0">{account.statusText}</Tag>
                            {status === "cooling" && account.cooldownUntil ? (
                                <span className="flex items-center gap-1 text-xs text-amber-500 dark:text-amber-400">
                                    <Clock3 className="size-3.5" aria-hidden />
                                    至 {new Date(account.cooldownUntil).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })}
                                </span>
                            ) : null}
                            {account.note ? <span className="min-w-0 max-w-56 truncate text-xs text-foreground/55" title={account.note}>{account.note}</span> : null}
                            <div className="ml-auto flex items-center gap-3 text-xs tabular-nums text-foreground/50">
                                <Tooltip title="可生成视频额度：生成成功扣减、失败不扣，用完自动停用并切换下一账号">
                                    <span className={cn("cursor-help", account.state === "ready" && account.videoQuotaText !== "不限" && "text-amber-500 dark:text-amber-400")}>
                                        额 {account.videoQuotaText}
                                    </span>
                                </Tooltip>
                                <span title="取用次数">取 {account.useCount}</span>
                                <span className="text-emerald-500 dark:text-emerald-400" title="成功次数">成 {account.successCount}</span>
                                <span className="text-red-500 dark:text-red-400" title="失败次数">败 {account.failCount}</span>
                                {account.lastError ? (
                                    <Tooltip title={account.lastError}>
                                        <span className="cursor-help text-red-500/80 dark:text-red-400/80">最近错误</span>
                                    </Tooltip>
                                ) : null}
                            </div>
                            <div className="flex shrink-0 items-center gap-1">
                                {status === "cooling" ? (
                                    <Button size="small" type="text" disabled={busy} icon={<RotateCcw className="size-3.5" />} onClick={() => void withBusy(() => clearCooldown([account.id]), "已解除冷却")}>解除冷却</Button>
                                ) : status !== "expired" ? (
                                    <Tooltip title={`冷却 ${DOUBAO_COOLDOWN_MINUTES} 分钟`}>
                                        <Button size="small" type="text" disabled={busy} icon={<Snowflake className="size-3.5" />} aria-label="进入冷却" onClick={() => void withBusy(() => cooldown(account.id), `已进入冷却（${DOUBAO_COOLDOWN_MINUTES} 分钟）`)} />
                                    </Tooltip>
                                ) : null}
                                {status === "disabled" ? (
                                    <Button size="small" type="text" disabled={busy} icon={<CircleCheck className="size-3.5" />} onClick={() => void withBusy(() => batch("enable", [account.id]), "已启用")}>启用</Button>
                                ) : (
                                    <Button size="small" type="text" disabled={busy} icon={<Ban className="size-3.5" />} onClick={() => void withBusy(() => batch("disable", [account.id]), "已停用")}>停用</Button>
                                )}
                                <Button size="small" type="text" icon={<PencilLine className="size-3.5" />} aria-label="编辑账号" onClick={() => openEdit(account)} />
                                <Popconfirm title="删除该账号？" okButtonProps={{ danger: true }} onConfirm={() => { void withBusy(() => removeAccounts([account.id]), "账号已删除"); setSelected((prev) => { const next = new Set(prev); next.delete(account.id); return next; }); }}>
                                    <Button size="small" type="text" danger icon={<Trash2 className="size-3.5" />} aria-label="删除账号" />
                                </Popconfirm>
                            </div>
                        </div>
                    );
                })}
            </div>

            {loaded && !accounts.length ? (
                <WorkspaceState
                    icon="empty"
                    title={`${POOL_SITE_META[site].label}账号池为空`}
                    description={
                        site === "doubao"
                            ? "扫码登录或粘贴 Cookie 开始管理账号；完整 Cookie 只保存在本地后端账号池服务，不会写入浏览器存储。"
                            : `登录 ${POOL_SITE_META[site].label} 网页版后复制整段 Cookie 导入；完整凭据只保存在本地后端账号池服务，不会写入浏览器存储。`
                    }
                    action={
                        <div className="flex items-center gap-2">
                            <Tooltip title={site === "dola" ? "打开 Dola 登录窗口（自动带入已有登录态），操作后自动保存完整 Cookie" : `打开${POOL_SITE_META[site].label}登录窗口，登录后自动保存`}>
                                <Button type="primary" icon={<QrCode className="size-4" />} onClick={openQrLogin}>
                                    扫码登录
                                </Button>
                            </Tooltip>
                            <Button icon={<KeyRound className="size-4" />} onClick={() => { setPasteName(""); setPasteCookie(""); setPasteOpen(true); }}>
                                粘贴 Cookie 添加
                            </Button>
                        </div>
                    }
                />
            ) : null}
            {accounts.length && !filtered.length ? (
                <WorkspaceState compact icon="empty" title="没有符合筛选条件的账号" description="换个状态筛选，或清除筛选查看全部账号。" />
            ) : null}
            {loading && !loaded ? <WorkspaceState compact icon="empty" title="正在加载账号池…" description="正在从本地后端读取账号池状态。" /> : null}

            {/* 粘贴 Cookie 添加 */}
            <AppModal
                open={pasteOpen}
                title={site === "doubao" ? "粘贴 Cookie / sessionid" : `粘贴 ${POOL_SITE_META[site].label} Cookie`}
                onCancel={() => setPasteOpen(false)}
                footer={[
                    <Button key="cancel" onClick={() => setPasteOpen(false)}>取消</Button>,
                    <Button key="save" type="primary" loading={busy} onClick={savePaste}>保存</Button>,
                ]}
            >
                <div className="flex flex-col gap-4 px-6 pb-5 pt-4">
                    <label className="flex flex-col gap-1.5">
                        <span className="text-xs text-foreground/60">显示名（可选）</span>
                        <Input value={pasteName} onChange={(event) => setPasteName(event.target.value)} placeholder="如：主号 / 备用 1" />
                    </label>
                    <label className="flex flex-col gap-1.5">
                        <span className="text-xs text-foreground/60">{site === "doubao" ? "完整 Cookie 或纯 sessionid" : "整段 Cookie（在浏览器登录后复制）"}</span>
                        <Input.TextArea rows={6} value={pasteCookie} onChange={(event) => setPasteCookie(event.target.value)} placeholder={site === "doubao" ? "粘贴浏览器复制的整段 Cookie（推荐），或单独的 sessionid" : "粘贴浏览器复制的整段 Cookie，如：a=1; b=2; c=3"} />
                    </label>
                    <p className="text-xs leading-5 text-foreground/45">
                        {site === "doubao"
                            ? "只带 sessionid 极易被风控，推荐粘贴完整 Cookie（含 ttwid / passport_csrf_token 等）。"
                            : `推荐在 ${POOL_SITE_META[site].label} 网页版（${POOL_SITE_META[site].url}）登录后，从开发者工具或地址栏锁图标复制完整 Cookie。`}
                        浏览器端只保留掩码预览，完整凭据保存在本地后端账号池服务。
                    </p>
                </div>
            </AppModal>

            {/* 批量导入 */}
            <AppModal
                open={bulkOpen}
                title="批量导入账号"
                onCancel={() => setBulkOpen(false)}
                footer={[
                    <Button key="cancel" onClick={() => setBulkOpen(false)}>取消</Button>,
                    <Button key="save" type="primary" loading={busy} onClick={saveBulk}>导入</Button>,
                ]}
            >
                <div className="flex flex-col gap-4 px-6 pb-5 pt-4">
                    <label className="flex flex-col gap-1.5">
                        <span className="text-xs text-foreground/60">统一标签（可选）</span>
                        <Input value={bulkTags} onChange={(event) => setBulkTags(event.target.value)} placeholder="逗号分隔，如：分组A,备用" />
                    </label>
                    <label className="flex flex-col gap-1.5">
                        <span className="text-xs text-foreground/60">账号列表（每行一个）</span>
                        <Input.TextArea rows={9} value={bulkText} onChange={(event) => setBulkText(event.target.value)} placeholder={"# 注释行会被忽略\nCookie 或 sessionid | 分组A,备用 | 备注文字\n纯 sessionid 也可以"} />
                    </label>
                </div>
            </AppModal>

            {/* 扫码登录：后端弹出本机浏览器，用户在窗口内扫码/登录 */}
            <AppModal
                open={qrOpen}
                title={`扫码登录${POOL_SITE_META[qrSite].label}账号`}
                onCancel={() => setQrOpen(false)}
                footer={
                    qrSession?.state === "waiting" || qrBusy
                        ? [
                              <Button key="cancel" danger loading={qrBusy} onClick={qrCancel}>取消登录</Button>,
                              <Button key="hide" type="text" onClick={() => setQrOpen(false)}>最小化</Button>,
                          ]
                        : [
                              <Button key="close" onClick={() => setQrOpen(false)}>关闭</Button>,
                              <Button key="retry" type="primary" onClick={openQrLogin}>重新登录</Button>,
                          ]
                }
            >
                <div className="flex flex-col items-center gap-4 px-6 pb-6 pt-6 text-center">
                    {qrBusy || qrSession?.state === "waiting" ? (
                        <>
                            <Loader2 className="size-10 animate-spin text-primary" aria-hidden />
                            <p className="text-sm font-medium">{qrSession?.message || "正在启动浏览器…"}</p>
                            <p className="flex items-center gap-1.5 text-xs text-foreground/55">
                                <MonitorSmartphone className="size-3.5" aria-hidden />
                                本机浏览器窗口已打开{POOL_SITE_META[qrSite].label}（{qrSession?.elapsedText || "0 秒"}），用手机 App 扫码或直接登录，成功后自动保存
                            </p>
                            <p className="text-xs leading-5 text-foreground/45">
                                登录窗口与工作台相互独立；检测到登录态后自动收集完整 Cookie 并设为当前账号。超时 5 分钟自动取消。
                            </p>
                        </>
                    ) : null}
                    {qrSession?.state === "success" ? (
                        <>
                            <span className="grid size-12 place-items-center rounded-full bg-emerald-500/12">
                                <CircleCheck className="size-7 text-emerald-500 dark:text-emerald-400" aria-hidden />
                            </span>
                            <p className="text-sm font-medium">登录成功，已写入账号池并设为当前账号</p>
                            <p className="text-xs text-foreground/55">账号 {qrSession.masked}</p>
                        </>
                    ) : null}
                    {qrSession && ["expired", "canceled", "failed"].includes(qrSession.state) ? (
                        <>
                            <span className="grid size-12 place-items-center rounded-full bg-red-500/12">
                                <Ban className="size-7 text-red-500 dark:text-red-400" aria-hidden />
                            </span>
                            <p className="text-sm font-medium">{qrSession.state === "expired" ? "登录超时" : qrSession.state === "canceled" ? "已取消登录" : "登录失败"}</p>
                            <p className="max-w-90 text-xs leading-5 text-foreground/55">{qrSession.message}</p>
                        </>
                    ) : null}
                    {!qrSession && !qrBusy ? <p className="text-sm text-foreground/55">正在准备登录会话…</p> : null}
                </div>
            </AppModal>

            {/* 编辑账号 */}
            <AppModal
                open={Boolean(editing)}
                title="编辑账号"
                onCancel={() => setEditing(null)}
                footer={[
                    <Button key="cancel" onClick={() => setEditing(null)}>取消</Button>,
                    <Button key="save" type="primary" loading={busy} onClick={saveEdit}>保存</Button>,
                ]}
            >
                <div className="flex flex-col gap-4 px-6 pb-5 pt-4">
                    <label className="flex flex-col gap-1.5">
                        <span className="text-xs text-foreground/60">显示名</span>
                        <Input value={editName} onChange={(event) => setEditName(event.target.value)} />
                    </label>
                    <label className="flex flex-col gap-1.5">
                        <span className="text-xs text-foreground/60">标签（逗号分隔）</span>
                        <Input value={editTags} onChange={(event) => setEditTags(event.target.value)} />
                    </label>
                    <label className="flex flex-col gap-1.5">
                        <span className="text-xs text-foreground/60">备注</span>
                        <Input value={editNote} onChange={(event) => setEditNote(event.target.value)} />
                    </label>
                    <Checkbox checked={editEnabled} onChange={(event) => setEditEnabled(event.target.checked)}>启用（参与取号）</Checkbox>
                </div>
            </AppModal>

            {/* 打标签 */}
            <AppModal
                open={Boolean(taggingIds?.length)}
                title="为选中账号追加标签"
                onCancel={() => setTaggingIds(null)}
                footer={[
                    <Button key="cancel" onClick={() => setTaggingIds(null)}>取消</Button>,
                    <Button key="save" type="primary" loading={busy} onClick={saveTag}>追加</Button>,
                ]}
            >
                <div className="flex flex-col gap-4 px-6 pb-5 pt-4">
                    <label className="flex flex-col gap-1.5">
                        <span className="text-xs text-foreground/60">标签（逗号分隔）</span>
                        <Input value={tagInput} onChange={(event) => setTagInput(event.target.value)} placeholder="如：分组A,高额度" />
                    </label>
                </div>
            </AppModal>
        </WorkspacePage>
    );
}


