import { Alert, App, Button, Card, Drawer, Input, InputNumber, Segmented, Select, Space, Switch, Table, Tag, Typography } from "antd";
import { EmptyState } from "@/components/ui/product/empty-state";
import { ArrowUpRight, CheckCircle2, Plus, RefreshCw, Sparkles, Trash2, X, XCircle } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Navigate, useNavigate, useParams } from "react-router";

import { ModelPicker } from "@/components/model-picker";
import { modelCatalogChannels, systemChannelModelChannels } from "@/lib/user-session";
import { modelCapabilityConfigFor } from "@/lib/model-capabilities";
import { getAdminFeatureAvailability } from "@/services/api/auth";
import { getModelCatalog, type ModelCatalogResponse } from "@/services/api/logical-models";
import { resourceFileUrl } from "@/services/api/resources";
import {
    getAdminVideoReplicationProject,
    getVideoReplicationAdminStatus,
    getVideoReplicationOverview,
    listVideoReplicationAdminProjects,
    updateVideoReplicationSetting,
    type VideoReplicationAdminProject,
    type VideoReplicationAdminStatus,
    type VideoReplicationKnowledgeBase,
    type VideoReplicationModelRef,
    type VideoReplicationOverview,
    type VideoReplicationProject,
    type VideoReplicationSetting,
    type VideoReplicationVideoModel,
    type VideoReplicationVoiceOption,
} from "@/services/api/video-replication";
import { encodeChannelModel } from "@/stores/config-model-options";
import { defaultConfig, type AiConfig } from "@/stores/use-config-store";
import type { FeatureAvailability } from "@/stores/use-user-store";

import { AdminPageFrame } from "./components/admin-shell";
import { AdminStatTile, AdminStatusBadge } from "./components/admin-ui";
import "./video-replication-page.css";
import { audioModelOptions, OPENAI_COMPATIBLE_VOICES, planningModelOptions, recommended, visionModelOptions, type ReplicationModelOption, type ReplicationTextModelOption } from "./video-replication-models";

/*
 * 电商视频 Agent 后台：所有页面读写的都是前台「电商 Agent → 商品视频」真正使用的配置与数据
 * （/api/admin/video-replication*）。保存后新提交的任务立即按新配置执行。
 */

export type VideoAgentSection = "overview" | "features" | "models" | "video-models" | "knowledge" | "tasks";

const SECTIONS: Record<VideoAgentSection, { title: string; description: string; editable: boolean }> = {
    overview: { title: "运行总览", description: "商品视频的真实运行数据：提交量、成片、失败原因与运行环境检查。", editable: false },
    features: { title: "功能开放", description: "控制前台「商品视频」开放哪些替换类型，以及参考视频时长上限。关闭后前台隐藏该类型，后端也拒绝提交。", editable: true },
    models: { title: "分析与声音模型", description: "视频理解、分镜策划、语音识别与配音模型。前台不展示这些模型。", editable: true },
    "video-models": { title: "视频生成模型", description: "设置商品视频可选的模型、比例、清晰度和音频选项。", editable: true },
    knowledge: { title: "模型知识库", description: "按用户选择的视频模型调用对应的提示词知识库：Seedance 系列（2.0 mini、2.0 等）调用 Seedance 2.5 支持库，可灵系列调用可灵 4.0 支持库。关闭后 Agent 不调用该知识库，按通用写法规划。", editable: true },
    tasks: { title: "任务管理", description: "所有用户提交的商品视频任务，点击查看素材、子任务（逐段生成）与成片。", editable: false },
};

export const VIDEO_KIND_SWITCHES: Array<{ id: string; label: string; description: string }> = [
    { id: "model", label: "模特", description: "把视频里的人物换成用户的模特" },
    { id: "clothing", label: "服饰", description: "人物换穿用户的服装，可附参考音色" },
    { id: "face", label: "人脸", description: "替换人物面部（需用户确认肖像授权）" },
    { id: "background", label: "换背景", description: "保留人物和商品，只换场景" },
    { id: "product", label: "商品", description: "把视频里的物品换成用户商品" },
    { id: "precise-product", label: "精准商品", description: "按多角度图严格还原商品细节" },
    { id: "precise-person", label: "精准人物", description: "按多角度照片严格还原人物" },
    { id: "combination", label: "组合替换", description: "商品、模特、背景同时替换" },
];

const MODE_LABEL: Record<string, string> = Object.fromEntries([...VIDEO_KIND_SWITCHES.map((item) => [item.id, item.label]), ["legacy", "旧版复刻"]]);
const STATUS_LABEL: Record<string, string> = { draft: "草稿", analyzing: "分析中", analyzed: "已分析", generating: "生成中", composing: "合成中", completed: "已完成", failed: "失败" };
const STATUS_COLOR: Record<string, string> = { completed: "green", failed: "red", generating: "blue", composing: "blue", analyzing: "blue", analyzed: "default", draft: "default" };
const RUN_LABEL: Record<string, string> = { submitting: "提交中", queued: "排队中", running: "生成中", succeeded: "成功", failed: "失败", cancelled: "已取消" };
const AUDIO_LABEL: Record<string, string> = { replace: "替换音频", keep: "保留原声", mute: "静音", generate: "AI 重新生成" };

/**
 * 后台可选的视频模型及其真实能力（与前台 / 后端准入同一份能力配置）。
 * pickerValue 是首页同款 ModelPicker 里的选择值（channelId::modelKey），value 仍是保存用的 channel:/logical: 身份。
 */
type VideoModelOption = { value: string; pickerValue: string; label: string; ref: VideoReplicationModelRef; ratios: string[]; resolutions: string[]; maxSeconds: number; audio: boolean };

const refValue = (ref?: VideoReplicationModelRef) => (ref?.channelModelId ? `channel:${ref.channelModelId}` : ref?.logicalModelId ? `logical:${ref.logicalModelId}` : "");
const refFromValue = (value?: string): VideoReplicationModelRef | undefined => (value?.startsWith("channel:") ? { channelModelId: value.slice(8) } : value?.startsWith("logical:") ? { logicalModelId: value.slice(8) } : undefined);

function videoModelOptions(catalog: ModelCatalogResponse, picker: AiConfig): VideoModelOption[] {
    const result: VideoModelOption[] = [];
    const channels = systemChannelModelChannels(catalog.channels || []);
    for (const channel of catalog.channels || []) {
        for (const model of channel.models || []) {
            const option = `${channel.id}::${model.modelKey}`;
            const configured = channels.find((item) => item.id === channel.id);
            if (!configured?.models.includes(model.modelKey)) continue;
            const profile = modelCapabilityConfigFor({ ...defaultConfig, channels }, option).video;
            if (!profile || !(model.capability === "video" || (profile.operations || []).length)) continue;
            if (model.capability && model.capability !== "video") continue;
            const durations = profile.duration.selection === "enum" ? profile.duration.values || [] : [profile.duration.max || 0];
            result.push({
                value: `channel:${model.id}`,
                pickerValue: encodeChannelModel(channel.id, model.modelKey),
                label: `${model.displayName && model.displayName !== model.modelKey ? `${model.displayName} · ` : ""}${model.modelKey} · ${channel.displayName || channel.name}`,
                ref: { channelModelId: model.id, channelId: channel.id, modelKey: model.modelKey },
                ratios: profile.ratios,
                resolutions: profile.resolutions,
                maxSeconds: Math.max(0, ...durations),
                audio: profile.generateAudio.supported,
            });
        }
    }
    for (const group of catalog.models || []) {
        if (group.capability !== "video" || !group.available) continue;
        const options = group.capabilitySpec?.options || {};
        const values = (key: string) => (Array.isArray(options[key]?.values) ? (options[key]!.values as unknown[]).map(String) : []);
        const seconds = options.videoSeconds || options.seconds || options.duration;
        const maxSeconds = Math.max(0, Number(seconds?.max) || 0, ...(Array.isArray(seconds?.values) ? seconds!.values!.map(Number) : []));
        // 逻辑模型在下拉里挂在「智能路由」渠道下，按 logicalModelId 找回它的选择值。
        const pickerValue = picker.channels.flatMap((channel) => (channel.modelCosts || []).filter((cost) => cost.logicalModelId === group.id).map((cost) => encodeChannelModel(channel.id, cost.model)))[0] || "";
        result.push({
            value: `logical:${group.id}`,
            pickerValue,
            label: `${group.name} · ${group.code}（逻辑模型）`,
            ref: { logicalModelId: group.id },
            ratios: values("size").length ? values("size") : values("ratio"),
            resolutions: values("vquality").length ? values("vquality") : values("resolution"),
            maxSeconds,
            audio: true,
        });
    }
    return result;
}

function timeText(value?: string) {
    return value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";
}

/** 配置类页面共用：读取当前设置与模型目录，编辑后整体保存。 */
function useReplicationSetting(withCatalog: boolean) {
    const { message } = App.useApp();
    const [status, setStatus] = useState<VideoReplicationAdminStatus>();
    const [setting, setSetting] = useState<VideoReplicationSetting>({ videoModels: [] });
    const [options, setOptions] = useState<{ vision: ReplicationTextModelOption[]; planning: ReplicationTextModelOption[]; tts: ReplicationModelOption[]; asr: ReplicationModelOption[] }>({ vision: [], planning: [], tts: [], asr: [] });
    const [videoModels, setVideoModels] = useState<VideoModelOption[]>([]);
    // 首页同款模型选择器使用同一份目录转换（系统渠道 + 智能路由）。
    const [pickerConfig, setPickerConfig] = useState<AiConfig>(() => ({ ...defaultConfig, channels: [] }));
    const [loading, setLoading] = useState(false);
    const [saving, setSaving] = useState(false);
    const [dirty, setDirty] = useState(false);
    const [error, setError] = useState("");
    const load = useCallback(async () => {
        setLoading(true);
        try {
            const [nextStatus, catalog] = await Promise.all([getVideoReplicationAdminStatus(), withCatalog ? getModelCatalog() : Promise.resolve(null)]);
            setStatus(nextStatus);
            setSetting({ ...nextStatus.setting, videoModels: nextStatus.setting.videoModels || [], knowledge: knowledgeFromStatus(nextStatus) });
            if (catalog) {
                const picker: AiConfig = { ...defaultConfig, channels: modelCatalogChannels(catalog) };
                setOptions({ vision: visionModelOptions(catalog), planning: planningModelOptions(catalog), tts: audioModelOptions(catalog), asr: audioModelOptions(catalog, true) });
                setPickerConfig(picker);
                setVideoModels(videoModelOptions(catalog, picker));
            }
            setDirty(false);
            setError("");
        } catch (cause) {
            setError(cause instanceof Error ? cause.message : String(cause));
        } finally {
            setLoading(false);
        }
    }, [withCatalog]);
    useEffect(() => {
        void load();
    }, [load]);
    const patch = (next: Partial<VideoReplicationSetting>) => {
        setSetting((current) => ({ ...current, ...next }));
        setDirty(true);
    };
    const save = async () => {
        setSaving(true);
        try {
            const payload = { ...setting, ...(setting.tts && !refValue(setting.tts.model) ? { tts: undefined } : {}) };
            const next = await updateVideoReplicationSetting(payload);
            setStatus(next);
            setSetting({ ...next.setting, videoModels: next.setting.videoModels || [], knowledge: knowledgeFromStatus(next) });
            setDirty(false);
            message.success("已保存，新提交的商品视频任务立即生效");
        } catch (cause) {
            message.error(cause instanceof Error ? cause.message : String(cause));
        } finally {
            setSaving(false);
        }
    };
    return { status, setting, patch, options, videoModels, pickerConfig, loading, saving, dirty, error, load, save };
}

type SettingState = ReturnType<typeof useReplicationSetting>;

/** 后台状态里的知识库视图转成保存用的列表：未改过默认内容的不带 content。 */
function knowledgeFromStatus(status: VideoReplicationAdminStatus): VideoReplicationKnowledgeBase[] {
    return (status.knowledge || []).map((item) => ({ id: item.id, enabled: item.enabled, content: item.customized ? item.content : "" }));
}

function KnowledgeSection({ state }: { state: SettingState }) {
    const views = state.status?.knowledge || [];
    const items = state.setting.knowledge || [];
    const update = (id: string, next: Partial<VideoReplicationKnowledgeBase>) => {
        state.patch({ knowledge: views.map((view) => ({ ...(items.find((item) => item.id === view.id) || { id: view.id, enabled: view.enabled, content: "" }), ...(view.id === id ? next : {}) })) });
    };
    if (!views.length) return <Alert type="info" showIcon message={state.loading ? "正在读取知识库…" : "后端暂未返回知识库，请确认后端已更新"} />;
    return (
        <>
            <Alert
                type="info"
                showIcon
                message="知识库如何生效"
                description="用户在商品视频里选择 Seedance 系列模型时，分镜策划会按 Seedance 支持库组织每段提示词（公式、镜头词表、产品一致性与文字保护锁、反提示词）；选择可灵系列时按可灵支持库。参考视频有口播、字幕时，Agent 会按用户商品与补充说明改写口播，字幕跟随新口播，用原视频音色（或用户上传的音色）说出并要求口型逐字同步。"
            />
            {views.map((view) => {
                const item = items.find((entry) => entry.id === view.id) || { id: view.id, enabled: view.enabled, content: "" };
                const content = item.content || view.defaultContent;
                const customized = Boolean(item.content) && item.content !== view.defaultContent;
                return (
                    <Card
                        key={view.id}
                        size="small"
                        title={
                            <Space wrap>
                                <strong>{view.name}</strong>
                                <Tag color={item.enabled ? "green" : "default"}>{item.enabled ? "已开启" : "已关闭"}</Tag>
                                {customized ? <Tag color="blue">已自定义</Tag> : <Tag>内置内容</Tag>}
                            </Space>
                        }
                        extra={<Switch checked={item.enabled} onChange={(enabled) => update(view.id, { enabled })} checkedChildren="开启" unCheckedChildren="关闭" />}
                    >
                        <Space direction="vertical" size={10} style={{ width: "100%" }}>
                            <Typography.Text type="secondary">{view.description}</Typography.Text>
                            <Space wrap size={6}>
                                <Typography.Text type="secondary">适用模型：</Typography.Text>
                                {view.models.map((name) => (
                                    <Tag key={name}>{name}</Tag>
                                ))}
                            </Space>
                            <Input.TextArea
                                value={content}
                                onChange={(event) => update(view.id, { content: event.target.value === view.defaultContent ? "" : event.target.value })}
                                autoSize={{ minRows: 10, maxRows: 28 }}
                                maxLength={30000}
                                showCount
                                disabled={!item.enabled}
                            />
                            <Space>
                                <Button size="small" disabled={!customized} onClick={() => update(view.id, { content: "" })}>
                                    恢复内置内容
                                </Button>
                                {!item.enabled ? <Typography.Text type="secondary">已关闭：用户选择该系列模型时 Agent 不调用此知识库。</Typography.Text> : null}
                            </Space>
                        </Space>
                    </Card>
                );
            })}
        </>
    );
}

function ChecksCard({ status }: { status?: VideoReplicationAdminStatus }) {
    return (
        <Card size="small" title="运行环境检查">
            <ul className="vr-admin-checks">
                {(status?.checks || []).map((check) => (
                    <li key={check.name}>
                        {check.ok ? <CheckCircle2 className="is-ok" /> : <XCircle className="is-bad" />}
                        <strong>{check.name}</strong>
                        <span>{check.detail}</span>
                    </li>
                ))}
            </ul>
        </Card>
    );
}

function OverviewSection() {
    const [days, setDays] = useState(7);
    const [overview, setOverview] = useState<VideoReplicationOverview>();
    const [status, setStatus] = useState<VideoReplicationAdminStatus>();
    const [features, setFeatures] = useState<FeatureAvailability>();
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState("");
    const navigate = useNavigate();
    const load = useCallback(async () => {
        setLoading(true);
        try {
            // 三块数据各自加载：某一块失败只提示那一块，不让开放状态、环境检查一起变成空白。
            const [next, nextStatus, nextFeatures] = await Promise.allSettled([getVideoReplicationOverview(days), getVideoReplicationAdminStatus(), getAdminFeatureAvailability().then((result) => result.features)]);
            const failures: string[] = [];
            const reason = (label: string, result: PromiseRejectedResult) => failures.push(`${label}读取失败：${result.reason instanceof Error ? result.reason.message : String(result.reason)}`);
            if (next.status === "fulfilled") setOverview(next.value);
            else reason("运行统计", next);
            if (nextStatus.status === "fulfilled") setStatus(nextStatus.value);
            else reason("运行环境", nextStatus);
            if (nextFeatures.status === "fulfilled") setFeatures(nextFeatures.value);
            else reason("开放状态", nextFeatures);
            setError(failures.join("；"));
        } finally {
            setLoading(false);
        }
    }, [days]);
    useEffect(() => {
        void load();
    }, [load]);
    const by = overview?.byStatus || {};
    const runs = overview?.runs || {};
    const active = (by.analyzing || 0) + (by.generating || 0) + (by.composing || 0);
    const runTotal = Object.values(runs).reduce((sum, value) => sum + value, 0);
    const runFailed = (runs.failed || 0) + (runs.cancelled || 0);
    const open = Boolean(features?.ecommerceVideoEnabled && features?.videoWorkbenchEnabled);
    return (
        <>
            <div className="vr-admin-toolbar">
                <Segmented
                    value={days}
                    onChange={(value) => setDays(Number(value))}
                    options={[
                        { label: "近 24 小时", value: 1 },
                        { label: "近 7 天", value: 7 },
                        { label: "近 30 天", value: 30 },
                        { label: "近 90 天", value: 90 },
                    ]}
                />
                <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void load()}>
                    刷新
                </Button>
            </div>
            {error ? <Alert type="error" showIcon message={error} /> : null}
            <Card size="small">
                <div className="vr-admin-open">
                    <div>
                        <strong>前台开放状态</strong>
                        <span>「商品视频」需要「电商视频」和「视频生成」两个开关同时开放。</span>
                    </div>
                    <Space wrap>
                        <AdminStatusBadge label={features ? (features.ecommerceVideoEnabled ? "电商视频 已开放" : "电商视频 未开放") : "读取中"} tone={features?.ecommerceVideoEnabled ? "success" : "warning"} />
                        <AdminStatusBadge label={features ? (features.videoWorkbenchEnabled ? "视频生成 已开放" : "视频生成 未开放") : "读取中"} tone={features?.videoWorkbenchEnabled ? "success" : "warning"} />
                        {features ? <AdminStatusBadge label={open ? "用户可用" : "用户不可用"} tone={open ? "success" : "error"} /> : null}
                        <Button size="small" icon={<ArrowUpRight size={13} />} iconPlacement="end" onClick={() => navigate("/admin/settings/features")}>
                            去功能开放调整
                        </Button>
                    </Space>
                </div>
            </Card>
            <div className="vr-admin-stats">
                <AdminStatTile label="提交任务" value={overview?.projectsInRange ?? "—"} detail={`累计 ${overview?.projects ?? "—"} 个`} />
                <AdminStatTile label="使用用户" value={overview?.users ?? "—"} detail="提交过任务的用户数" />
                <AdminStatTile label="成片" value={overview?.outputs ?? "—"} detail={`已完成任务 ${by.completed || 0} 个`} />
                <AdminStatTile label="进行中" value={active} detail={`分析 ${by.analyzing || 0} · 生成 ${by.generating || 0} · 合成 ${by.composing || 0}`} />
                <AdminStatTile label="失败任务" value={by.failed || 0} trend={overview?.projectsInRange ? { value: `${(((by.failed || 0) / overview.projectsInRange) * 100).toFixed(1)}%`, tone: (by.failed || 0) > 0 ? "warning" : "success" } : undefined} />
                <AdminStatTile label="子任务（逐段生成）" value={runTotal} detail={`成功 ${runs.succeeded || 0} · 失败 ${runFailed}`} />
            </div>
            <div className="vr-admin-columns">
                <Card size="small" title="按替换类型">
                    {overview && Object.keys(overview.byMode).length ? (
                        <ul className="vr-admin-bars">
                            {Object.entries(overview.byMode)
                                .sort((a, b) => b[1] - a[1])
                                .map(([mode, count]) => (
                                    <li key={mode}>
                                        <span>{MODE_LABEL[mode] || mode}</span>
                                        <i style={{ width: `${Math.max(4, (count / Math.max(1, overview.projectsInRange)) * 100)}%` }} />
                                        <strong>{count}</strong>
                                    </li>
                                ))}
                        </ul>
                    ) : (
                        <EmptyState title="这段时间没有提交" />
                    )}
                </Card>
                <Card size="small" title="主要失败原因">
                    {overview?.recentErrors?.length ? (
                        <ul className="vr-admin-errors">
                            {overview.recentErrors.map((item) => (
                                <li key={item.error}>
                                    <Tag color="red">{item.count} 次</Tag>
                                    <span>{item.error}</span>
                                </li>
                            ))}
                        </ul>
                    ) : (
                        <EmptyState title="这段时间没有失败任务" />
                    )}
                </Card>
            </div>
            <ChecksCard status={status} />
        </>
    );
}

function FeaturesSection({ state }: { state: SettingState }) {
    const disabled = new Set(state.setting.disabledKinds || []);
    const toggle = (id: string, open: boolean) => {
        const next = new Set(disabled);
        if (open) next.delete(id);
        else next.add(id);
        state.patch({ disabledKinds: VIDEO_KIND_SWITCHES.map((item) => item.id).filter((item) => next.has(item)) });
    };
    return (
        <>
            <Card
                size="small"
                title="替换类型"
                extra={
                    <Typography.Text type="secondary">
                        已开放 {VIDEO_KIND_SWITCHES.filter((item) => !disabled.has(item.id)).length} / {VIDEO_KIND_SWITCHES.length}
                    </Typography.Text>
                }
            >
                <div className="vr-admin-kinds">
                    {VIDEO_KIND_SWITCHES.map((item) => (
                        <div key={item.id} className="vr-admin-kind">
                            <div>
                                <strong>{item.label}</strong>
                                <span>{item.description}</span>
                            </div>
                            <Switch checked={!disabled.has(item.id)} onChange={(open) => toggle(item.id, open)} checkedChildren="开放" unCheckedChildren="关闭" />
                        </div>
                    ))}
                </div>
            </Card>
            <Card size="small" title="参考视频">
                <label className="vr-admin-field is-inline">
                    <span>
                        <strong>参考视频时长上限</strong>
                        <small>管理员自由设置参考视频上限，支持 4–600 秒。</small>
                    </span>
                    <InputNumber min={4} max={600} value={state.setting.maxReferenceSeconds || 120} onChange={(value) => state.patch({ maxReferenceSeconds: Number(value) || 120 })} addonAfter="秒" />
                </label>
            </Card>
        </>
    );
}

function ModelsSection({ state }: { state: SettingState }) {
    const { setting, patch, options, pickerConfig } = state;
    const tts = setting.tts || { model: {}, voices: [] };
    const patchTTS = (next: Partial<NonNullable<VideoReplicationSetting["tts"]>>) => patch({ tts: { ...tts, ...next } });
    const patchVoice = (index: number, next: Partial<VideoReplicationVoiceOption>) => patchTTS({ voices: tts.voices.map((voice, voiceIndex) => (voiceIndex === index ? { ...voice, ...next } : voice)) });
    const modelRecommendation = (list: ReplicationModelOption[], current: string, onChange: (ref?: VideoReplicationModelRef) => void) => {
        const best = recommended(list);
        return best ? (
            <small className="vr-admin-recommend">
                <Sparkles size={12} />
                推荐 {best.label.split(" · ").slice(0, 2).join(" · ")}（{best.reason}）
                {best.value !== current ? (
                    <Button size="small" type="link" onClick={() => onChange(refFromValue(best.value))}>
                        使用推荐
                    </Button>
                ) : (
                    <Tag color="green">当前即推荐</Tag>
                )}
            </small>
        ) : (
            <small className="vr-admin-recommend">网站目前没有符合条件的模型，请先在渠道管理里接入</small>
        );
    };
    const analysisModelPicker = (label: string, value: VideoReplicationModelRef | undefined, onChange: (ref?: VideoReplicationModelRef) => void, hint: string, list: ReplicationTextModelOption[], placeholder: string) => {
        const current = refValue(value);
        const selected = list.find((item) => item.value === current);
        const byPickerValue = new Map(list.map((item) => [item.pickerValue, item]));
        return (
            <div className="vr-admin-field">
                <span>
                    <strong>{label}</strong>
                    <small>{hint}</small>
                </span>
                <div className="vr-admin-model-picker vr-admin-analysis-picker">
                    <ModelPicker
                        config={pickerConfig}
                        capability="text"
                        value={selected?.pickerValue}
                        fullWidth
                        className="creation-model-picker"
                        popoverClassName="vr-admin-analysis-popover"
                        showSelectedPrice={false}
                        placeholder={label}
                        triggerLabel={current ? (selected ? undefined : `${value?.modelKey || "已保存的模型"} · 当前不可选`) : placeholder}
                        modelFilter={(model) => byPickerValue.has(model)}
                        modelDisabledReason={(model) => (byPickerValue.get(model)?.disabled ? "需要支持图片输入" : "")}
                        onChange={(model) => {
                            const option = byPickerValue.get(model);
                            if (option && !option.disabled) onChange(refFromValue(option.value));
                        }}
                    />
                    {current ? <Button type="text" icon={<X size={14} />} aria-label={`清除${label}`} title="恢复默认模型" onClick={() => onChange(undefined)} /> : null}
                </div>
                {modelRecommendation(list, current, onChange)}
            </div>
        );
    };
    const modelSelect = (label: string, value: VideoReplicationModelRef | undefined, onChange: (ref?: VideoReplicationModelRef) => void, hint: string, list: ReplicationModelOption[], placeholder: string) => {
        const current = refValue(value);
        return (
            <label className="vr-admin-field">
                <span>
                    <strong>{label}</strong>
                    <small>{hint}</small>
                </span>
                <Select
                    showSearch
                    allowClear
                    optionFilterProp="label"
                    placeholder={placeholder}
                    value={current || undefined}
                    options={list.map((item) => ({ value: item.value, label: item.label, disabled: item.disabled }))}
                    onChange={(next?: string) => onChange(refFromValue(next))}
                    style={{ width: "100%" }}
                />
                {modelRecommendation(list, current, onChange)}
            </label>
        );
    };
    return (
        <div className="vr-admin-column">
            <Card size="small" title="分析模型" extra={<Typography.Text type="secondary">推荐按模型声明的能力与近 24 小时可用率计算</Typography.Text>}>
                <div className="vr-admin-grid vr-admin-analysis-grid">
                    {analysisModelPicker("视频理解模型", setting.vision, (vision) => patch({ vision }), "读取关键帧与台词，拆解镜头、运镜、口播、说话人与商品/模特档案（需支持图片输入）", options.vision, "未指定：沿用电商 Agent 识图模型")}
                    {analysisModelPicker("分镜策划模型", setting.planning, (planning) => patch({ planning }), "重构原创分镜、改写口播台词、推荐说话人音色", options.planning, "未指定：沿用电商 Agent 策划模型")}
                </div>
            </Card>

            <Card size="small" title="声音模型" extra={<Typography.Text type="secondary">前台不展示模型，只展示音色名称</Typography.Text>}>
                <div className="vr-admin-grid">
                    <label className="vr-admin-field">
                        <span>
                            <strong>语音识别（ASR）方式</strong>
                            <small>提取参考视频的口播 / 旁白台词与时间戳</small>
                        </span>
                        <Select
                            value={setting.asr?.mode || "env"}
                            options={[
                                { value: "vision", label: "视频理解模型听写（复用视频理解渠道，无需另配转写模型）" },
                                { value: "env", label: "服务器 whisper 服务（环境变量 CANVAS_WHISPER_BASE_URL）" },
                                { value: "model", label: "指定语音识别模型（OpenAI 兼容 /audio/transcriptions）" },
                            ]}
                            onChange={(mode: "env" | "model" | "vision") => patch({ asr: { model: setting.asr?.model || {}, language: setting.asr?.language, mode } })}
                        />
                    </label>
                    {setting.asr?.mode === "model"
                        ? modelSelect(
                              "语音识别模型",
                              setting.asr.model,
                              (model) => patch({ asr: { mode: model ? "model" : "env", model: model || {}, language: setting.asr?.language } }),
                              "例如 whisper-1、gpt-4o-transcribe 等转写模型",
                              options.asr,
                              "选择语音识别模型",
                          )
                        : null}
                </div>
                <div className="vr-admin-grid">
                    {modelSelect("配音模型（TTS）", tts.model, (model) => patchTTS({ model: model || {} }), "用户试听音色与生成 AI 配音；按该模型价格计费", options.tts, "选择语音合成模型")}
                    <label className="vr-admin-field">
                        <span>
                            <strong>配音格式</strong>
                        </span>
                        <Select
                            value={tts.format || "mp3"}
                            options={[
                                { value: "mp3", label: "mp3" },
                                { value: "wav", label: "wav" },
                            ]}
                            onChange={(format: string) => patchTTS({ format })}
                        />
                    </label>
                </div>
                <div className="vr-admin-voices">
                    <div className="vr-admin-model-meta">
                        <Typography.Text strong>音色库（{tts.voices.length}）</Typography.Text>
                        <Typography.Text type="secondary">音色 ID 是配音模型接口里的 voice 参数；Agent 会按说话人的性别、年龄、语气从这里推荐</Typography.Text>
                    </div>
                    {tts.voices.map((voice, index) => (
                        <div key={index} className="vr-admin-voice">
                            <Input size="small" placeholder="音色 ID（voice）" value={voice.key} onChange={(event) => patchVoice(index, { key: event.target.value })} />
                            <Input size="small" placeholder="显示名称" value={voice.name} onChange={(event) => patchVoice(index, { name: event.target.value })} />
                            <Select size="small" placeholder="性别" value={voice.gender || undefined} options={["女", "男", "中性", "童声"].map((value) => ({ value, label: value }))} onChange={(gender: string) => patchVoice(index, { gender })} />
                            <Input size="small" placeholder="年龄段，如 20-30" value={voice.age} onChange={(event) => patchVoice(index, { age: event.target.value })} />
                            <Input size="small" placeholder="风格，如 甜美带货 / 沉稳专业" value={voice.style} onChange={(event) => patchVoice(index, { style: event.target.value })} />
                            <Button size="small" type="text" danger icon={<Trash2 size={14} />} aria-label="删除音色" onClick={() => patchTTS({ voices: tts.voices.filter((_, voiceIndex) => voiceIndex !== index) })} />
                        </div>
                    ))}
                    <Space wrap>
                        <Button size="small" icon={<Plus size={14} />} onClick={() => patchTTS({ voices: [...tts.voices, { key: "", name: "" }] })}>
                            添加音色
                        </Button>
                        <Button
                            size="small"
                            onClick={() => {
                                const existing = new Set(tts.voices.map((voice) => voice.key));
                                patchTTS({ voices: [...tts.voices, ...OPENAI_COMPATIBLE_VOICES.filter((voice) => !existing.has(voice.key))] });
                            }}
                        >
                            导入 OpenAI 兼容内置音色
                        </Button>
                    </Space>
                </div>
            </Card>
        </div>
    );
}

function VideoModelsSection({ state }: { state: SettingState }) {
    const { setting, patch, videoModels, pickerConfig } = state;
    const patchModel = (index: number, next: Partial<VideoReplicationVideoModel>) => patch({ videoModels: setting.videoModels.map((item, itemIndex) => (itemIndex === index ? { ...item, ...next } : item)) });
    const optionFor = (ref: VideoReplicationModelRef) => videoModels.find((option) => option.value === refValue(ref));
    const usedValues = useMemo(() => new Set(setting.videoModels.map((item) => refValue(item))), [setting.videoModels]);
    // 下拉只列后台准入的视频模型；已被其他行添加的置灰。
    const listedPickerValues = useMemo(() => new Set(videoModels.map((option) => option.pickerValue).filter(Boolean)), [videoModels]);
    const usedPickerValues = useMemo(() => new Set(videoModels.filter((option) => usedValues.has(option.value)).map((option) => option.pickerValue)), [usedValues, videoModels]);
    const listedModel = useCallback((model: string) => listedPickerValues.has(model), [listedPickerValues]);
    const addModel = (value: string) => {
        const option = videoModels.find((item) => item.value === value);
        if (!option || usedValues.has(option.value)) return;
        patch({
            restrictVideoModels: true,
            videoModels: [{ ...option.ref, label: option.label.slice(0, 60), enabled: true, generateAudio: false, allowAudioToggle: true }, ...setting.videoModels],
        });
    };
    return (
        <div className="vr-admin-column">
            <Card
                size="small"
                title="视频生成模型"
                extra={
                    setting.videoModels.length >= 30 ? (
                        <Typography.Text type="secondary">已添加 30 个模型</Typography.Text>
                    ) : (
                        <Select<string | null>
                            size="small"
                            className="min-w-32"
                            classNames={{ placeholder: "vr-admin-add-model-label" }}
                            aria-label="添加模型"
                            placeholder="添加模型"
                            prefix={<Plus size={14} />}
                            value={null}
                            showSearch={{ optionFilterProp: "label" }}
                            placement="bottomRight"
                            popupMatchSelectWidth={false}
                            notFoundContent="没有可添加的视频模型"
                            options={videoModels.filter((item) => item.pickerValue && !usedValues.has(item.value)).map((item) => ({ value: item.value, label: item.label }))}
                            onChange={(value) => {
                                if (value) addModel(value);
                            }}
                        />
                    )
                }
            >
                <Typography.Paragraph type="secondary">添加后，用户只能选择名单中已开放且渠道可用的模型。</Typography.Paragraph>
                {setting.videoModels.length ? (
                    <div className="vr-admin-models">
                        {setting.videoModels.map((item, index) => {
                            const option = optionFor(item);
                            const ratios = option?.ratios || [];
                            const resolutions = option?.resolutions || [];
                            return (
                                <div key={refValue(item)} className="vr-admin-model">
                                    <div className="vr-admin-model-head">
                                        <div className="vr-admin-model-picker">
                                            <ModelPicker
                                                config={pickerConfig}
                                                capability="video"
                                                variant="creation"
                                                fullWidth
                                                className="creation-model-picker"
                                                showSelectedPrice={false}
                                                showOptionPrices
                                                placeholder="选择视频模型"
                                                value={option?.pickerValue}
                                                triggerLabel={!option ? item.label || refValue(item) : undefined}
                                                modelFilter={listedModel}
                                                modelDisabledReason={(model) => (model !== option?.pickerValue && usedPickerValues.has(model) ? "已在其他行添加" : "")}
                                                onChange={(model) => {
                                                    const next = videoModels.find((candidate) => candidate.pickerValue === model);
                                                    if (next)
                                                        patchModel(index, {
                                                            ...next.ref,
                                                            label: next.label.slice(0, 60),
                                                            logicalModelId: next.ref.logicalModelId,
                                                            channelModelId: next.ref.channelModelId,
                                                            ratios: [],
                                                            resolutions: [],
                                                            defaultRatio: undefined,
                                                            defaultResolution: undefined,
                                                            generateAudio: false,
                                                        });
                                                }}
                                            />
                                        </div>
                                        <Switch checked={item.enabled} onChange={(enabled) => patchModel(index, { enabled })} checkedChildren="开放" unCheckedChildren="关闭" />
                                        <Button type="text" danger icon={<Trash2 size={14} />} onClick={() => patch({ videoModels: setting.videoModels.filter((_, itemIndex) => itemIndex !== index) })} aria-label="移除模型" />
                                    </div>
                                    {option ? (
                                        <>
                                            <div className="vr-admin-model-meta">
                                                <Tag color="purple">单次上限 {option.maxSeconds || "?"} 秒</Tag>
                                                <Typography.Text type="secondary">参考视频 ≤{option.maxSeconds || "?"} 秒时原生直出；更长时自动智能分段</Typography.Text>
                                            </div>
                                            <div className="vr-admin-grid">
                                                <label className="vr-admin-field">
                                                    <span>
                                                        <strong>开放比例</strong>
                                                        <small>不选表示全部</small>
                                                    </span>
                                                    <Select
                                                        mode="multiple"
                                                        allowClear
                                                        value={item.ratios || []}
                                                        options={ratios.map((value) => ({ value, label: value }))}
                                                        onChange={(next: string[]) => patchModel(index, { ratios: next, defaultRatio: next.includes(item.defaultRatio || "") ? item.defaultRatio : next[0] })}
                                                    />
                                                </label>
                                                <label className="vr-admin-field">
                                                    <span>
                                                        <strong>默认比例</strong>
                                                    </span>
                                                    <Select
                                                        allowClear
                                                        value={item.defaultRatio}
                                                        options={(item.ratios?.length ? item.ratios : ratios).map((value) => ({ value, label: value }))}
                                                        onChange={(defaultRatio?: string) => patchModel(index, { defaultRatio })}
                                                    />
                                                </label>
                                                <label className="vr-admin-field">
                                                    <span>
                                                        <strong>开放清晰度</strong>
                                                        <small>不选表示全部</small>
                                                    </span>
                                                    <Select
                                                        mode="multiple"
                                                        allowClear
                                                        value={item.resolutions || []}
                                                        options={resolutions.map((value) => ({ value, label: value }))}
                                                        onChange={(next: string[]) => patchModel(index, { resolutions: next, defaultResolution: next.includes(item.defaultResolution || "") ? item.defaultResolution : next[0] })}
                                                    />
                                                </label>
                                                <label className="vr-admin-field">
                                                    <span>
                                                        <strong>默认清晰度</strong>
                                                    </span>
                                                    <Select
                                                        allowClear
                                                        value={item.defaultResolution}
                                                        options={(item.resolutions?.length ? item.resolutions : resolutions).map((value) => ({ value, label: value }))}
                                                        onChange={(defaultResolution?: string) => patchModel(index, { defaultResolution })}
                                                    />
                                                </label>
                                            </div>
                                            <Space wrap size={24}>
                                                <Space>
                                                    <Switch size="small" disabled={!option.audio} checked={item.generateAudio} onChange={(generateAudio) => patchModel(index, { generateAudio })} />
                                                    <Typography.Text>{option.audio ? "默认生成音频" : "该模型不支持原生音频"}</Typography.Text>
                                                </Space>
                                                <Space>
                                                    <Switch size="small" checked={item.allowAudioToggle} onChange={(allowAudioToggle) => patchModel(index, { allowAudioToggle })} />
                                                    <Typography.Text>允许用户开关音频</Typography.Text>
                                                </Space>
                                            </Space>
                                        </>
                                    ) : null}
                                </div>
                            );
                        })}
                    </div>
                ) : setting.restrictVideoModels ? (
                    <Space wrap>
                        <Typography.Text type="secondary">当前没有开放的视频模型。</Typography.Text>
                        <Button size="small" onClick={() => patch({ restrictVideoModels: false })}>
                            开放全部系统视频模型
                        </Button>
                    </Space>
                ) : (
                    <Typography.Text type="secondary">当前开放全部可用的系统视频模型。</Typography.Text>
                )}
            </Card>
        </div>
    );
}

function TasksSection() {
    const [projects, setProjects] = useState<VideoReplicationAdminProject[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState("");
    const [status, setStatus] = useState("all");
    const [mode, setMode] = useState("all");
    const [keyword, setKeyword] = useState("");
    const [detail, setDetail] = useState<VideoReplicationProject | null>(null);
    const [detailLoading, setDetailLoading] = useState("");
    const load = useCallback(async () => {
        setLoading(true);
        try {
            setProjects(await listVideoReplicationAdminProjects());
            setError("");
        } catch (cause) {
            setError(cause instanceof Error ? cause.message : String(cause));
        } finally {
            setLoading(false);
        }
    }, []);
    useEffect(() => {
        void load();
    }, [load]);
    const open = async (id: string) => {
        setDetailLoading(id);
        try {
            setDetail(await getAdminVideoReplicationProject(id));
        } catch (cause) {
            setError(cause instanceof Error ? cause.message : String(cause));
        } finally {
            setDetailLoading("");
        }
    };
    const query = keyword.trim().toLowerCase();
    const rows = projects
        .filter((row) => row.status !== "draft" || row.outputs > 0)
        .filter((row) => (status === "all" || row.status === status) && (mode === "all" || (row.replaceMode || "legacy") === mode) && (!query || [row.username, row.displayName, row.title, row.id].join(" ").toLowerCase().includes(query)));
    return (
        <>
            <div className="vr-admin-toolbar">
                <Space wrap>
                    <Input allowClear placeholder="搜索用户、标题或任务 ID" value={keyword} onChange={(event) => setKeyword(event.target.value)} style={{ width: 240 }} />
                    <Select value={status} onChange={setStatus} style={{ width: 130 }} options={[{ value: "all", label: "全部状态" }, ...Object.entries(STATUS_LABEL).map(([value, label]) => ({ value, label }))]} />
                    <Select value={mode} onChange={setMode} style={{ width: 140 }} options={[{ value: "all", label: "全部类型" }, ...Object.entries(MODE_LABEL).map(([value, label]) => ({ value, label }))]} />
                </Space>
                <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void load()}>
                    刷新
                </Button>
            </div>
            {error ? <Alert type="error" showIcon message={error} /> : null}
            <Card size="small" extra={<Typography.Text type="secondary">最近 50 个任务 · 显示 {rows.length} 个</Typography.Text>}>
                <Table
                    size="small"
                    rowKey="id"
                    pagination={{ pageSize: 20, hideOnSinglePage: true }}
                    loading={loading}
                    dataSource={rows}
                    scroll={{ x: 1100 }}
                    onRow={(row) => ({ onClick: () => void open(row.id), style: { cursor: "pointer" } })}
                    columns={[
                        { title: "用户", dataIndex: "username", width: 140, render: (_, row) => row.displayName || row.username || row.userId },
                        { title: "类型", dataIndex: "replaceMode", width: 100, render: (value?: string) => <Tag>{MODE_LABEL[value || "legacy"] || value}</Tag> },
                        { title: "声音", dataIndex: "audioMode", width: 90, render: (value?: string) => AUDIO_LABEL[value || ""] || "—" },
                        { title: "状态", dataIndex: "status", width: 90, render: (value: string, row) => <Tag color={STATUS_COLOR[value] || "default"}>{row.paused ? "已暂停" : STATUS_LABEL[value] || value}</Tag> },
                        { title: "子任务", width: 150, render: (_, row) => `${row.runs} 段（进行中 ${row.activeRuns} / 失败 ${row.failedRuns}）` },
                        { title: "成片", dataIndex: "outputs", width: 60 },
                        {
                            title: "说明",
                            render: (_, row) => (
                                <Typography.Text type={row.error ? "danger" : "secondary"} ellipsis style={{ maxWidth: 420 }}>
                                    {row.error || row.stage || "—"}
                                </Typography.Text>
                            ),
                        },
                        { title: "更新时间", dataIndex: "updatedAt", width: 170, render: (value: string) => timeText(value) },
                        {
                            title: "",
                            width: 70,
                            render: (_, row) => (
                                <Button size="small" type="link" loading={detailLoading === row.id}>
                                    详情
                                </Button>
                            ),
                        },
                    ]}
                />
            </Card>
            <Drawer open={Boolean(detail)} width={720} title={detail ? `任务详情 · ${detail.title}` : ""} onClose={() => setDetail(null)} destroyOnHidden>
                {detail ? <TaskDetail project={detail} /> : null}
            </Drawer>
        </>
    );
}

function clipClock(ms: number) {
    const seconds = ms / 1000;
    return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${(seconds % 60).toFixed(1).padStart(4, "0")}`;
}

function TaskDetail({ project }: { project: VideoReplicationProject }) {
    const params = project.parameters;
    const outputs = project.outputs.filter((item) => item.status === "succeeded" && item.resourceId);
    return (
        <div className="vr-admin-detail">
            <dl className="vr-admin-meta">
                <dt>任务 ID</dt>
                <dd>{project.id}</dd>
                <dt>状态</dt>
                <dd>
                    <Tag color={STATUS_COLOR[project.status] || "default"}>{STATUS_LABEL[project.status] || project.status}</Tag>
                    {project.stage}
                </dd>
                <dt>替换类型</dt>
                <dd>{MODE_LABEL[params.replaceMode || "legacy"] || params.replaceMode}</dd>
                <dt>声音</dt>
                <dd>{AUDIO_LABEL[params.audioMode || ""] || (params.keepOriginalAudio ? "保留原声" : params.generateAudio ? "模型配音" : "静音")}</dd>
                <dt>模型</dt>
                <dd>
                    {params.modelLabel || params.model || "—"} · {params.ratio || "—"} · {params.resolution || "—"}
                </dd>
                {params.trimEndMs ? (
                    <>
                        <dt>裁剪区间</dt>
                        <dd>
                            {clipClock(params.trimStartMs || 0)} – {clipClock(params.trimEndMs)}（{((params.trimEndMs - (params.trimStartMs || 0)) / 1000).toFixed(1)} 秒）
                        </dd>
                    </>
                ) : null}
                {params.talking ? (
                    <>
                        <dt>口播视频</dt>
                        <dd>是，口型与原台词同步</dd>
                    </>
                ) : null}
                {params.regions?.length ? (
                    <>
                        <dt>框选目标</dt>
                        <dd>
                            {params.regions.length} 个（原视频 {clipClock(params.regionFrameMs || 0)}）{project.analysis?.regionTarget ? `：${project.analysis.regionTarget}` : "，等待分析识别"}
                            {project.analysis?.regionFrameResourceId ? <img className="vr-admin-region" src={resourceFileUrl(project.analysis.regionFrameResourceId)} alt="框选画面" /> : null}
                        </dd>
                    </>
                ) : null}
                {params.subjectHint ? (
                    <>
                        <dt>商品主体</dt>
                        <dd>{params.subjectHint}</dd>
                    </>
                ) : null}
                <dt>补充说明</dt>
                <dd>{project.prompt || "—"}</dd>
                <dt>消耗</dt>
                <dd>{project.spentCredits ? `${project.spentCredits.toLocaleString("zh-CN", { maximumFractionDigits: 3 })} 积分` : "—"}</dd>
                <dt>创建 / 更新</dt>
                <dd>
                    {timeText(project.createdAt)} / {timeText(project.updatedAt)}
                </dd>
                {project.error ? (
                    <>
                        <dt>错误</dt>
                        <dd className="is-error">{project.error}</dd>
                    </>
                ) : null}
            </dl>
            <h4>成片</h4>
            {outputs.length ? (
                <div className="vr-admin-media">
                    {outputs.map((output) => (
                        <video key={output.id} src={resourceFileUrl(output.resourceId)} controls preload="metadata" />
                    ))}
                </div>
            ) : (
                <Typography.Text type="secondary">暂无成片</Typography.Text>
            )}
            <h4>素材（{project.assets.length}）</h4>
            <div className="vr-admin-assets">
                {project.assets.map((asset) => (
                    <figure key={asset.id}>
                        {asset.category === "reference_video" ? (
                            <video src={resourceFileUrl(asset.resourceId)} preload="metadata" controls />
                        ) : asset.category === "voice_audio" ? (
                            <audio src={resourceFileUrl(asset.resourceId)} controls preload="none" />
                        ) : (
                            <img src={resourceFileUrl(asset.resourceId)} alt={asset.name} />
                        )}
                        <figcaption>{asset.label || asset.name}</figcaption>
                    </figure>
                ))}
            </div>
            <h4>子任务（{project.runs.length}）</h4>
            <Table
                size="small"
                rowKey="id"
                pagination={false}
                dataSource={project.runs}
                columns={[
                    { title: "段", dataIndex: "shotId", width: 90, render: (value: string) => (value === "__full__" ? "完整视频" : value) },
                    { title: "版本", dataIndex: "version", width: 56 },
                    { title: "状态", dataIndex: "status", width: 80, render: (value: string) => <Tag color={value === "succeeded" ? "green" : value === "failed" || value === "cancelled" ? "red" : "blue"}>{RUN_LABEL[value] || value}</Tag> },
                    { title: "工作台任务", dataIndex: "taskId", width: 120, render: (value: string) => <Typography.Text copyable={value ? { text: value } : false}>{value ? `${value.slice(0, 8)}…` : "—"}</Typography.Text> },
                    { title: "说明", dataIndex: "error", render: (value: string, row) => <Typography.Text type={value ? "danger" : "secondary"}>{value || timeText(row.updatedAt)}</Typography.Text> },
                ]}
            />
        </div>
    );
}

export default function VideoAgentAdminPage() {
    const { section } = useParams();
    if (!section || !(section in SECTIONS)) return <Navigate to="/admin/video-agent/overview" replace />;
    return <VideoAgentSectionPage key={section} section={section as VideoAgentSection} />;
}

function VideoAgentSectionPage({ section }: { section: VideoAgentSection }) {
    const meta = SECTIONS[section];
    const state = useReplicationSetting(section === "models" || section === "video-models");
    return (
        <AdminPageFrame
            title={meta.title}
            description={meta.description}
            actions={
                meta.editable ? (
                    <Space>
                        <Button icon={<RefreshCw size={14} />} loading={state.loading} onClick={() => void state.load()}>
                            重新读取
                        </Button>
                        <Button type="primary" loading={state.saving} disabled={!state.dirty} onClick={() => void state.save()}>
                            保存修改
                        </Button>
                    </Space>
                ) : undefined
            }
            scroll
        >
            <div className="vr-admin">
                {meta.editable && state.error ? <Alert type="error" showIcon message={state.error} /> : null}
                {meta.editable && state.dirty ? <Alert type="warning" showIcon message="有未保存的修改" /> : null}
                {section === "overview" ? <OverviewSection /> : null}
                {section === "features" ? <FeaturesSection state={state} /> : null}
                {section === "models" ? <ModelsSection state={state} /> : null}
                {section === "video-models" ? <VideoModelsSection state={state} /> : null}
                {section === "knowledge" ? <KnowledgeSection state={state} /> : null}
                {section === "tasks" ? <TasksSection /> : null}
            </div>
        </AdminPageFrame>
    );
}
