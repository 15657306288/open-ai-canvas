import { App } from "antd";
import { useBlocker } from "react-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { createClientUUID } from "@/lib/client-id";
import { modelCapabilityConfigFor, normalizeVideoValue } from "@/lib/model-capabilities";
import { resourceFileUrl, uploadResourceFile } from "@/services/api/resources";
import {
    addVideoReplicationAsset,
    createVideoReplicationProject,
    deleteVideoReplicationAsset,
    deleteVideoReplicationProject,
    getVideoReplicationConfig,
    getVideoReplicationProject,
    importVideoReplicationLinkStream,
    listVideoReplicationProjects,
    startVideoReplication,
    updateVideoReplicationProject,
    type VideoReplicationAsset,
    type VideoReplicationParameters,
    type VideoReplicationRegion,
    type VideoReplicationProject,
    type VideoReplicationPublicConfig,
    type VideoReplicationTarget,
    type VideoLinkProgress,
} from "@/services/api/video-replication";
import { ApiError } from "@/services/api/request";
import { logicalModelIDForConfig, modelDisplayName, modelOptionName, resolveModelChannel, selectableModelsByCapability, useConfigStore, useEffectiveConfig } from "@/stores/use-config-store";
import { useUserStore } from "@/stores/use-user-store";

import { quoteReplicationTasks } from "./replication-quote";
import { draftImages, draftVideo, loadVideoDraft, saveVideoDraft } from "./video-draft";
import { durationValues, estimateSegments, replicationReferenceMode, segmentSecondOptions } from "./replication-strategy";
import { commerceImageParameters } from "../workflow";
import { CATEGORY_TARGET, REPLACE_KINDS, replaceKindConfig, replacementMaterialError, VOICE_SLOT, type AudioChoice, type EcommerceVideoFeature, type MaterialSlot, type ReplaceKind, type ReplaceKindConfig } from "./video-kinds";

export type DraftImage = { id: string; slot: string; category: MaterialSlot["category"]; resourceId: string; name: string; previewUrl: string; progress?: number; error?: string; durationMs?: number };
export type VideoTrim = { startMs: number; endMs: number };
export type VideoRegions = { frameMs: number; boxes: VideoReplicationRegion[] };
export type DraftVideo = { resourceId: string; name: string; previewUrl: string; durationMs: number; width: number; height: number; projectId?: string; uploading?: boolean; progress?: number; sourceLabel?: string };
/** 链接解析失败的说明；retryable 为 true 时显示「重试」。 */
export type LinkError = { message: string; retryable: boolean };

const ACTIVE = new Set(["analyzing", "generating", "composing"]);
/** 一键生成分析完成后等待提交生成任务的宽限期：替换音频时要先分离原片伴奏，服务端最长等 20 分钟。 */
const AUTO_SUBMIT_GRACE_MS = 30 * 60 * 1000;
const VIDEO_MAX_BYTES = 100 * 1024 * 1024;
/** 可以导入后再裁剪的参考视频原始时长上限（与后端一致）。 */
const VIDEO_SOURCE_MAX_MS = 10 * 60 * 1000;
const IMAGE_MAX_BYTES = 30 * 1024 * 1024;

function errorText(error: unknown, fallback: string) {
    return error instanceof Error && error.message ? error.message : fallback;
}

type ProjectProgress = Pick<VideoReplicationProject, "status" | "parameters" | "paused" | "updatedAt" | "analysis" | "outputs"> & Partial<Pick<VideoReplicationProject, "error">>;

/**
 * 一键生成在分析完成后、本次分析的成片出来之前，项目会暂时停在 analyzed（分离伴奏、提交生成任务、等待合成），
 * 再次生成时还会暂时显示上一版成片的 completed。服务端没有后台轮询，项目进度靠读取项目时推进，
 * 这段时间必须继续轮询：否则卡片停在「尚未开始生成」或上一版成片，成片也要等刷新页面才会合成。
 * 超过宽限期仍没有新成片（自动提交被中断）时不再算进行中，卡片恢复「开始生成」按钮。
 */
export function isAutoRunPending(project: ProjectProgress) {
    const params = project.parameters;
    if (!params?.auto || params.confirmFirst || project.paused) return false;
    // 后台已经报错（自动提交失败等）：不再等待，卡片直接显示失败原因。
    if (project.error) return false;
    if (project.status !== "analyzed" && project.status !== "completed") return false;
    const analyzedAt = Date.parse(project.analysis?.analyzedAt || "");
    if (!Number.isFinite(analyzedAt)) return false;
    // 本次分析之后已经出过成片（成功或失败）：这一轮已经结束，按项目原状态展示。
    if (project.outputs.some((output) => Date.parse(output.createdAt) >= analyzedAt)) return false;
    const updatedAt = Date.parse(project.updatedAt);
    return Date.now() - Math.max(analyzedAt, Number.isFinite(updatedAt) ? updatedAt : 0) < AUTO_SUBMIT_GRACE_MS;
}

export function isActiveProject(project: ProjectProgress) {
    return ACTIVE.has(project.status) || isAutoRunPending(project);
}

function readAudioDuration(file: File): Promise<number> {
    return new Promise((resolve) => {
        const url = URL.createObjectURL(file);
        const audio = document.createElement("audio");
        let done = false;
        const finish = (value: number) => {
            if (done) return;
            done = true;
            URL.revokeObjectURL(url);
            resolve(value);
        };
        audio.preload = "metadata";
        audio.onloadedmetadata = () => finish(Number.isFinite(audio.duration) ? Math.round(audio.duration * 1000) : 0);
        audio.onerror = () => finish(0);
        window.setTimeout(() => finish(0), 8000);
        audio.src = url;
    });
}

function readVideoMeta(file: File): Promise<{ durationMs: number; width: number; height: number }> {
    return new Promise((resolve) => {
        const url = URL.createObjectURL(file);
        const video = document.createElement("video");
        let done = false;
        const finish = (value: { durationMs: number; width: number; height: number }) => {
            if (done) return;
            done = true;
            URL.revokeObjectURL(url);
            resolve(value);
        };
        video.preload = "metadata";
        video.muted = true;
        video.onloadedmetadata = () => finish({ durationMs: Number.isFinite(video.duration) ? Math.round(video.duration * 1000) : 0, width: video.videoWidth, height: video.videoHeight });
        video.onerror = () => finish({ durationMs: 0, width: 0, height: 0 });
        window.setTimeout(() => finish({ durationMs: 0, width: 0, height: 0 }), 8000);
        video.src = url;
    });
}

/** 按参考视频的宽高比选最接近的可用比例；没有视频时用模型默认比例。 */
function nearestRatio(width: number, height: number, ratios: string[], fallback: string) {
    if (!width || !height || !ratios.length) return fallback;
    const target = width / height;
    let best = fallback;
    let bestDiff = Infinity;
    for (const ratio of ratios) {
        const [w, h] = ratio.split(":").map(Number);
        if (!w || !h) continue;
        const diff = Math.abs(Math.log(w / h / target));
        if (diff < bestDiff) {
            bestDiff = diff;
            best = ratio;
        }
    }
    return best;
}

export function useEcommerceVideo() {
    const { message, modal } = App.useApp();
    const config = useEffectiveConfig();
    const updateConfig = useConfigStore((store) => store.updateConfig);
    const creditsEnabled = useUserStore((store) => store.features.creditsEnabled);

    const userId = useUserStore((store) => store.user?.id || "");
    // 刷新 / 切换页面后恢复草稿：已上传好的素材、参考视频与填写内容（只在首次挂载时读取一次）。
    const [initialDraft] = useState(() => loadVideoDraft(userId));
    const feature: EcommerceVideoFeature = "replace";
    const [kind, setKind] = useState<ReplaceKind>(initialDraft?.kind || "product");
    const [images, setImages] = useState<DraftImage[]>(() => draftImages(initialDraft, createClientUUID));
    const [video, setVideo] = useState<DraftVideo | null>(() => draftVideo(initialDraft));
    const [linkText, setLinkText] = useState(initialDraft?.linkText || "");
    const [linkBusy, setLinkBusy] = useState(false);
    const [linkProgress, setLinkProgress] = useState<VideoLinkProgress | null>(null);
    const [linkError, setLinkError] = useState<LinkError | null>(null);
    const linkAbortRef = useRef<AbortController | null>(null);
    const [notes, setNotes] = useState(initialDraft?.notes || "");
    const [subjectHint, setSubjectHint] = useState(initialDraft?.subjectHint || "");
    const [productName, setProductName] = useState(initialDraft?.productName || "");
    const [audio, setAudio] = useState<AudioChoice>(initialDraft?.audio || "keep");
    const [faceConsent, setFaceConsent] = useState(Boolean(initialDraft?.faceConsent));
    // 精准商品 / 精准人物：勾选「保留音乐和节奏」、口播开关；所有类型共用裁剪区间，精准类型可框选替换目标。
    const [keepMusic, setKeepMusic] = useState(initialDraft?.keepMusic ?? true);
    const [talking, setTalking] = useState(Boolean(initialDraft?.talking));
    const [trim, setTrim] = useState<VideoTrim | null>(initialDraft?.trim || null);
    const [regions, setRegions] = useState<VideoRegions | null>(initialDraft?.regions || null);
    const [ratio, setRatio] = useState(initialDraft?.ratio || "");
    const [resolution, setResolution] = useState(initialDraft?.resolution || "");
    const [submitting, setSubmitting] = useState(false);
    const [projects, setProjects] = useState<VideoReplicationProject[]>([]);
    const [loadingProjects, setLoadingProjects] = useState(true);
    const [publicConfig, setPublicConfig] = useState<VideoReplicationPublicConfig | null>(null);
    const [quoted, setEstimate] = useState<{ key: string; credits: number | null; label: string }>({ key: "", credits: null, label: "" });
    const submitInFlight = useRef(false);
    const draftProjectRef = useRef<Promise<string> | null>(null);

    const kindConfig: ReplaceKindConfig = replaceKindConfig(kind);
    // 替换音频时多一个参考音色位（放在「声音」里），提交时与素材一起登记到项目。
    const rhythmUi = kindConfig.audio === "rhythm";
    const activeSlots = useMemo<MaterialSlot[]>(
        () =>
            rhythmUi || audio === "replace" ? [...kindConfig.slots, VOICE_SLOT] : kindConfig.slots,
        [audio, kindConfig.slots, rhythmUi],
    );

    // ---------- 草稿：输入变化后写入本地，刷新后恢复 ----------
    // 挂载时用户信息还没加载好：等拿到用户 ID 后先恢复草稿，再开始写入，避免空面板覆盖已有草稿。
    const [draftUser, setDraftUser] = useState(initialDraft || userId ? userId : "");
    useEffect(() => {
        if (!userId || draftUser === userId) return;
        const draft = loadVideoDraft(userId);
        if (draft) {
            setKind(draft.kind || "product");
            setImages(draftImages(draft, createClientUUID));
            setVideo(draftVideo(draft));
            setLinkText(draft.linkText || "");
            setNotes(draft.notes || "");
            setSubjectHint(draft.subjectHint || "");
            setProductName(draft.productName || "");
            setAudio(draft.audio || "keep");
            setKeepMusic(draft.keepMusic ?? true);
            setTalking(Boolean(draft.talking));
            setFaceConsent(Boolean(draft.faceConsent));
            setTrim(draft.trim || null);
            setRegions(draft.regions || null);
            if (draft.ratio) setRatio(draft.ratio);
            if (draft.resolution) setResolution(draft.resolution);
        }
        setDraftUser(userId);
    }, [draftUser, userId]);
    useEffect(() => {
        if (!userId || draftUser !== userId) return;
        const timer = window.setTimeout(() => {
            saveVideoDraft(userId, {
                feature,
                kind,
                images: images.filter((image) => image.resourceId && !image.error).map(({ slot, category, resourceId, name, durationMs }) => ({ slot, category, resourceId, name, durationMs })),
                video: video?.resourceId ? { resourceId: video.resourceId, name: video.name, durationMs: video.durationMs, width: video.width, height: video.height, projectId: video.projectId } : null,
                linkText,
                notes,
                subjectHint,
                productName,
                audio,
                keepMusic,
                talking,
                faceConsent,
                trim,
                regions,
                ratio,
                resolution,
            });
        }, 300);
        return () => window.clearTimeout(timer);
    }, [audio, draftUser, faceConsent, feature, images, keepMusic, kind, linkText, notes, productName, ratio, regions, resolution, subjectHint, talking, trim, userId, video]);

    // 上传或提交进行中：刷新 / 关闭标签页弹出浏览器确认，站内跳转先确认，避免把上传和提交打断。
    const uploading = Boolean(video?.uploading) || images.some((image) => !image.resourceId && !image.error) || linkBusy;
    const [submittingGuard, setSubmittingGuard] = useState(false);
    const guarded = uploading || submittingGuard;
    useEffect(() => {
        if (!guarded) return;
        const beforeUnload = (event: BeforeUnloadEvent) => {
            event.preventDefault();
            event.returnValue = "";
        };
        window.addEventListener("beforeunload", beforeUnload);
        return () => window.removeEventListener("beforeunload", beforeUnload);
    }, [guarded]);
    const blocker = useBlocker(({ currentLocation, nextLocation }) => guarded && currentLocation.pathname !== nextLocation.pathname);
    useEffect(() => {
        if (blocker.state !== "blocked") return;
        modal.confirm({
            title: uploading ? "素材还在上传" : "正在提交生成",
            content: uploading ? "离开这个页面会中断正在上传的素材，已上传好的会保留在草稿里。确定离开？" : "生成请求还没提交完，离开可能导致这次生成没有开始。确定离开？",
            okText: "仍然离开",
            cancelText: "留在这里",
            onOk: () => blocker.proceed(),
            onCancel: () => blocker.reset(),
        });
    }, [blocker, modal, uploading]);

    // ---------- 模型：只用系统模型（服务端逐段提交），后台限定时只显示开放的模型 ----------
    useEffect(() => {
        const controller = new AbortController();
        const refresh = () =>
            getVideoReplicationConfig(controller.signal)
                .then(setPublicConfig)
                .catch(() => undefined);
        void refresh();
        window.addEventListener("focus", refresh);
        return () => {
            controller.abort();
            window.removeEventListener("focus", refresh);
        };
    }, []);
    // 后台关闭的替换类型：当前选中的被关闭时切到第一个开放的类型。
    const disabledKinds = publicConfig?.disabledKinds;
    useEffect(() => {
        if (!disabledKinds?.length) return;
        if (disabledKinds.includes(kind)) {
            const next = REPLACE_KINDS.find((item) => !disabledKinds.includes(item.id));
            if (next) setKind(next.id);
        }
    }, [disabledKinds, kind]);
    const adminEntryFor = useCallback(
        (model: string) => {
            if (!publicConfig?.restrictVideoModels) return null;
            const logicalModelId = logicalModelIDForConfig({ ...config, model }) || "";
            const channelId = resolveModelChannel(config, model)?.id || "";
            const key = modelOptionName(model).replace(/^models\//, "");
            return publicConfig.videoModels.find((item) => (item.logicalModelId ? item.logicalModelId === logicalModelId : !logicalModelId && item.channelId === channelId && (item.modelKey || "").replace(/^models\//, "") === key)) || undefined;
        },
        [config, publicConfig],
    );
    const systemModel = useCallback((model: string) => (Boolean(logicalModelIDForConfig({ ...config, model })) || resolveModelChannel(config, model)?.scope === "system") && adminEntryFor(model) !== undefined, [adminEntryFor, config]);
    const firstModel = useMemo(() => selectableModelsByCapability(config, "video").find(systemModel) || "", [config, systemModel]);
    const configured = config.videoModel || "";
    const selectedModel = configured && systemModel(configured) ? configured : firstModel;
    const adminEntry = selectedModel ? adminEntryFor(selectedModel) : null;
    const profile = useMemo(() => (selectedModel ? modelCapabilityConfigFor(config, selectedModel).video : undefined), [config, selectedModel]);
    const referenceMode = replicationReferenceMode(profile);
    const allowedSeconds = useMemo(() => durationValues(profile), [profile]);
    const ratios = useMemo(() => (profile ? (adminEntry?.ratios?.length ? profile.ratios.filter((value) => adminEntry.ratios!.includes(value)) : profile.ratios) : []), [adminEntry, profile]);
    const resolutions = useMemo(() => (profile ? (adminEntry?.resolutions?.length ? profile.resolutions.filter((value) => adminEntry.resolutions!.includes(value)) : profile.resolutions) : []), [adminEntry, profile]);
    const audioLocked = Boolean(adminEntry && !adminEntry.allowAudioToggle);
    // 替换音频需要：模型能生成声音（且后台没有锁定关闭）+ 参考图生视频时能附带参考音频。
    const canGenerateAudio = Boolean(profile?.generateAudio.supported) && !(audioLocked && adminEntry && !adminEntry.generateAudio);
    const voiceSupported = referenceMode.mode === "reference" && (profile?.references.maxAudios || 0) > 0;
    // 原片编辑：模型能接收参考视频时，每段把原视频片段发给模型，只替换目标，场景与动作沿用原片。
    const editMode = referenceMode.mode === "reference" && (profile?.references.maxVideos || 0) > 0;
    const editSeconds = editMode ? Math.max(0, Math.floor(profile?.references.maxVideoDurationSeconds || 0)) : 0;
    const maxVoiceSeconds = profile?.references.maxAudioDurationSeconds || 15;
    const maxVoiceAudios = Math.min(VOICE_SLOT.max, profile?.references.maxAudios || 0);
    const uploadedVoices = images.filter((image) => image.slot === VOICE_SLOT.key && image.category === VOICE_SLOT.category && image.resourceId).length;
    // 上传参考音色时优先使用它；商品 / 服饰台词另按实际替换目标改写，保留音乐不等于保留旧商品台词。
    const effectiveAudio: AudioChoice = !rhythmUi ? audio : uploadedVoices > 0 ? "replace" : keepMusic ? "keep" : canGenerateAudio ? "generate" : "mute";
    const setModel = useCallback(
        (value: string) => {
            updateConfig("videoModel", value);
        },
        [updateConfig],
    );

    // 比例 / 清晰度不在当前模型的开放范围时回落到默认值；「清晰度」模式下比例跟随参考视频。
    useEffect(() => {
        if (!profile) return;
        const defaultRatio = adminEntry?.defaultRatio && ratios.includes(adminEntry.defaultRatio) ? adminEntry.defaultRatio : ratios.includes(profile.defaultRatio) ? profile.defaultRatio : ratios[0] || "";
        if (kindConfig.canvas === "resolution") {
            const next = nearestRatio(video?.width || 0, video?.height || 0, ratios, defaultRatio);
            if (next !== ratio) setRatio(next);
        } else if (!ratios.includes(ratio)) setRatio(ratios.includes("9:16") ? "9:16" : defaultRatio);
        if (!resolutions.includes(resolution)) {
            const defaultResolution = adminEntry?.defaultResolution && resolutions.includes(adminEntry.defaultResolution) ? adminEntry.defaultResolution : resolutions.includes(profile.defaultResolution) ? profile.defaultResolution : resolutions[0] || "";
            setResolution(defaultResolution);
        }
    }, [adminEntry, kindConfig.canvas, profile, ratio, ratios, resolution, resolutions, video?.height, video?.width]);

    const segmentSeconds = useMemo(() => {
        const options = segmentSecondOptions(allowedSeconds);
        // 原片编辑：单段不超过模型接收的参考视频时长。
        if (editSeconds >= 4) {
            const fit = options.filter((value) => value <= editSeconds);
            if (fit.length) return fit[fit.length - 1];
        }
        return options[options.length - 1];
    }, [allowedSeconds, editSeconds]);

    // ---------- 素材 ----------
    const slotImages = useCallback((slot: string) => images.filter((image) => image.slot === slot), [images]);
    const visibleImages = useMemo(() => images.filter((image) => activeSlots.some((slot) => slot.key === image.slot && slot.category === image.category)), [images, activeSlots]);
    const hasProductImage = visibleImages.some((image) => image.category === "product_image" && image.resourceId && !image.error);

    const addImageFiles = useCallback(
        async (slot: MaterialSlot, files: File[]) => {
            const room = slot.max - images.filter((image) => image.slot === slot.key && image.category === slot.category).length;
            const audio = slot.media === "audio";
            const accepted = files.filter((file) => file.type.startsWith(audio ? "audio/" : "image/")).slice(0, Math.max(0, room));
            let uploaded = 0;
            let usedVoiceMs = audio ? images.filter((image) => image.slot === slot.key && image.category === slot.category && !image.error).reduce((sum, image) => sum + (image.durationMs || 0), 0) : 0;
            if (files.length > accepted.length) message.warning(room <= 0 ? `${slot.label}最多 ${slot.max} ${audio ? "段" : "张"}` : audio ? "只能上传音频，超出的部分未添加" : "只能上传图片，超出的部分未添加");
            for (const file of accepted) {
                if (file.size > (audio ? 20 * 1024 * 1024 : IMAGE_MAX_BYTES)) {
                    message.error(`${file.name} 超过 ${audio ? 20 : 30}MB`);
                    continue;
                }
                const durationMs = audio ? await readAudioDuration(file) : 0;
                if (audio && durationMs > maxVoiceSeconds * 1000 + 500) {
                    message.error(`参考音色最长 ${maxVoiceSeconds} 秒，请剪成一段清晰的单人说话片段`);
                    continue;
                }
                // 多段参考音色一起发给模型，按合计时长限制。
                const voiceTotal = audio ? usedVoiceMs + durationMs : 0;
                if (audio && voiceTotal > maxVoiceSeconds * 1000 + 500) {
                    message.error(`参考音色多段合计最长 ${maxVoiceSeconds} 秒，加上 ${file.name} 共 ${(voiceTotal / 1000).toFixed(1)} 秒`);
                    continue;
                }
                if (audio) usedVoiceMs = voiceTotal;
                const id = createClientUUID();
                const previewUrl = audio ? "" : URL.createObjectURL(file);
                setImages((current) => [...current, { id, slot: slot.key, category: slot.category, resourceId: "", name: file.name, previewUrl, progress: 0, durationMs: durationMs || undefined }]);
                try {
                    const resource = await uploadResourceFile(file, audio ? "audio" : "image", { fileName: file.name, durationMs: durationMs || undefined }, (done, total) =>
                        setImages((current) => current.map((item) => (item.id === id ? { ...item, progress: total ? done / total : 0 } : item))),
                    );
                    setImages((current) => current.map((item) => (item.id === id ? { ...item, resourceId: resource.id, progress: undefined } : item)));
                    uploaded++;
                } catch (error) {
                    setImages((current) => current.map((item) => (item.id === id ? { ...item, error: errorText(error, "上传失败"), progress: undefined } : item)));
                }
            }
            return uploaded;
        },
        [images, maxVoiceSeconds, message],
    );
    const addPickedImages = useCallback((slot: MaterialSlot, picked: Array<{ resourceId: string; name: string }>) => {
        setImages((current) => {
            const room = slot.max - current.filter((image) => image.slot === slot.key && image.category === slot.category).length;
            const next = picked
                .slice(0, Math.max(0, room))
                .map((item) => ({ id: createClientUUID(), slot: slot.key, category: slot.category, resourceId: item.resourceId, name: item.name, previewUrl: slot.media === "audio" ? "" : resourceFileUrl(item.resourceId) }));
            return [...current, ...next];
        });
    }, []);
    const removeImage = useCallback(
        (id: string) => {
            setImages((current) => {
                const target = current.find((item) => item.id === id);
                if (target?.previewUrl.startsWith("blob:")) URL.revokeObjectURL(target.previewUrl);
                const rest = current.filter((item) => item.id !== id);
                // 依赖的位（如反面图依赖正面图）随主位一起清空。
                if (target && !rest.some((item) => item.slot === target.slot && item.category === target.category)) {
                    const dependents = kindConfig.slots.filter((slot) => slot.requires === target.slot).map((slot) => slot.key);
                    return rest.filter((item) => !(dependents.includes(item.slot) && item.category === target.category));
                }
                return rest;
            });
        },
        [kindConfig.slots],
    );

    const addVideoFile = useCallback(
        async (file: File) => {
            if (!(file.type === "video/mp4" || file.type === "video/quicktime" || /\.(mp4|mov)$/i.test(file.name))) {
                message.error("参考视频只支持 MP4、MOV");
                return;
            }
            if (file.size > VIDEO_MAX_BYTES) {
                message.error("参考视频不能超过 100MB");
                return;
            }
            const meta = await readVideoMeta(file);
            if (meta.durationMs && meta.durationMs < 3950) {
                message.error("参考视频至少 4 秒");
                return;
            }
            if (meta.durationMs > VIDEO_SOURCE_MAX_MS + 50) {
                message.error("参考视频最长 10 分钟，请先在本地剪短");
                return;
            }
            const limit = publicConfig?.maxReferenceSeconds || 120;
            if (meta.durationMs > limit * 1000 + 50) message.info(`视频超过 ${limit} 秒，上传后请点「裁剪视频」截取其中一段`);
            setTrim(null);
            setRegions(null);
            const previewUrl = URL.createObjectURL(file);
            setVideo({ resourceId: "", name: file.name, previewUrl, ...meta, uploading: true, progress: 0 });
            try {
                const resource = await uploadResourceFile(file, "video", { fileName: file.name, durationMs: meta.durationMs || undefined, width: meta.width || undefined, height: meta.height || undefined }, (done, total) =>
                    setVideo((current) => (current && current.previewUrl === previewUrl ? { ...current, progress: total ? done / total : 0 } : current)),
                );
                setVideo((current) => (current && current.previewUrl === previewUrl ? { ...current, resourceId: resource.id, uploading: false, progress: undefined } : current));
            } catch (error) {
                message.error(errorText(error, "参考视频上传失败"));
                setVideo((current) => (current && current.previewUrl === previewUrl ? null : current));
            }
        },
        [message, publicConfig?.maxReferenceSeconds],
    );
    const pickVideo = useCallback((picked: { resourceId: string; name: string }) => {
        setVideo({ resourceId: picked.resourceId, name: picked.name, previewUrl: resourceFileUrl(picked.resourceId), durationMs: 0, width: 0, height: 0 });
        setTrim(null);
        setRegions(null);
    }, []);
    const removeVideo = useCallback(() => {
        setVideo(null);
        setTrim(null);
        setRegions(null);
    }, []);
    /** 裁剪 / 框选弹窗读到视频真实元数据后回填（素材库选的视频没有时长与尺寸）。 */
    const setVideoMeta = useCallback((meta: { durationMs: number; width: number; height: number }) => {
        setVideo((current) => (current && (!current.durationMs || !current.width) ? { ...current, durationMs: current.durationMs || meta.durationMs, width: current.width || meta.width, height: current.height || meta.height } : current));
    }, []);

    const ensureDraftProject = useCallback(() => {
        if (!draftProjectRef.current) {
            draftProjectRef.current = createVideoReplicationProject({ title: "商品视频" })
                .then((project) => project.id)
                .catch((error) => {
                    draftProjectRef.current = null;
                    throw error;
                });
        }
        return draftProjectRef.current;
    }, []);

    // 粘贴的链接或分享文案交给后端：自动识别平台、解析、转存并登记素材库；过程中展示阶段进度，失败时就地显示原因与重试。
    // 成功后替换当前参考视频（本地上传的也会被替换），失败时保留原视频；override 供一键粘贴 / 粘贴即解析使用。
    const parseLink = useCallback(
        async (override?: string) => {
            const text = (typeof override === "string" ? override : linkText).trim();
            if (!text) {
                message.info("先粘贴视频分享链接或分享文案");
                return;
            }
            if (text !== linkText) setLinkText(text);
            linkAbortRef.current?.abort();
            const controller = new AbortController();
            linkAbortRef.current = controller;
            setLinkBusy(true);
            setLinkError(null);
            setLinkProgress({ stage: "detecting", message: "正在识别平台" });
            try {
                const projectId = await ensureDraftProject();
                const result = await importVideoReplicationLinkStream(
                    projectId,
                    text,
                    (progress) => setLinkProgress((current) => ({ ...progress, platformLabel: progress.platformLabel || current?.platformLabel, title: progress.title || current?.title })),
                    controller.signal,
                );
                const asset = result.project.assets.find((item) => item.id === result.referenceAssetId) || result.project.assets.find((item) => item.category === "reference_video");
                if (!asset) throw new Error("没有解析到视频");
                const label = result.source.platformLabel;
                setVideo({ resourceId: asset.resourceId, name: result.source.title || asset.name, previewUrl: resourceFileUrl(asset.resourceId), durationMs: asset.durationMs, width: asset.width, height: asset.height, projectId, sourceLabel: label });
                setTrim(null);
                setRegions(null);
                setLinkText("");
                message.success(result.reused ? `已使用素材库中的${label}视频` : `${label}视频已解析并存入素材库`);
            } catch (error) {
                if (controller.signal.aborted) return;
                setLinkError({ message: errorText(error, "链接解析失败"), retryable: error instanceof ApiError ? error.retryable !== false : true });
            } finally {
                if (linkAbortRef.current === controller) {
                    linkAbortRef.current = null;
                    setLinkBusy(false);
                    setLinkProgress(null);
                }
            }
        },
        [ensureDraftProject, linkText, message],
    );
    useEffect(() => () => linkAbortRef.current?.abort(), []);
    const changeLinkText = useCallback((value: string) => {
        setLinkText(value);
        setLinkError(null);
    }, []);

    const pasteLink = useCallback(async () => {
        try {
            const text = (await navigator.clipboard.readText()).trim();
            if (!text) {
                message.info("剪贴板是空的");
                return;
            }
            // 一键粘贴后直接解析，不用再点「解析视频」。
            void parseLink(text);
        } catch {
            message.info("浏览器不允许读取剪贴板，请手动粘贴");
        }
    }, [message, parseLink]);

    // ---------- 项目列表 ----------
    const refreshProjects = useCallback(async () => {
        try {
            const summaries = await listVideoReplicationProjects();
            const details = await Promise.all(summaries.slice(0, 24).map((item) => getVideoReplicationProject(item.id).catch(() => null)));
            // 只展示真正提交过的项目：还停在草稿、没有参考视频的解析草稿不进作品区。
            setProjects(details.filter((item): item is VideoReplicationProject => Boolean(item && (item.status !== "draft" || item.outputs.length))));
        } catch {
            // 列表刷新失败时保留现有结果，下次轮询再试。
        } finally {
            setLoadingProjects(false);
        }
    }, []);
    useEffect(() => {
        void refreshProjects();
    }, [refreshProjects]);
    const hasActive = projects.some(isActiveProject);
    useEffect(() => {
        if (!hasActive) return;
        const timer = window.setInterval(() => {
            const active = projects.filter(isActiveProject);
            void Promise.all(active.map((item) => getVideoReplicationProject(item.id).catch(() => null))).then((updated) => {
                const byId = new Map(updated.filter((item): item is VideoReplicationProject => Boolean(item)).map((item) => [item.id, item]));
                setProjects((current) => current.map((item) => byId.get(item.id) || item));
            });
        }, 4000);
        return () => window.clearInterval(timer);
    }, [hasActive, projects]);

    // ---------- 预估积分 ----------
    const referenceCount = Math.min(visibleImages.filter((image) => image.resourceId && image.category !== "voice_audio").length, referenceMode.max || 0);
    const voiceCount = effectiveAudio === "replace" ? visibleImages.filter((image) => image.category === "voice_audio" && image.resourceId).length : 0;
    const clipMs = trim ? trim.endMs - trim.startMs : video?.durationMs || 0;
    const voiceSegmentSeconds = useMemo(() => {
        if (!voiceCount || !editMode || editSeconds <= 0) return segmentSeconds;
        const limit = Math.min(segmentSeconds, editSeconds - Math.max(5, Math.ceil((voiceCount * 2000 + 300) / 1000)));
        const usable = allowedSeconds.filter((value) => value >= 4 && value <= limit);
        return usable.length ? Math.max(...usable) : allowedSeconds.length ? 0 : Math.max(0, limit);
    }, [allowedSeconds, editMode, editSeconds, segmentSeconds, voiceCount]);
    const planSeconds = useMemo(() => (clipMs > 0 && voiceSegmentSeconds >= 4 ? estimateSegments(clipMs, voiceSegmentSeconds, allowedSeconds) : []), [allowedSeconds, clipMs, voiceSegmentSeconds]);
    const quoteKey = JSON.stringify([selectedModel, ratio, resolution, planSeconds, referenceCount, referenceMode.mode, editMode, voiceCount]);
    const estimate = creditsEnabled && quoted.key === quoteKey ? quoted : { credits: null, label: "" };
    useEffect(() => {
        if (!creditsEnabled || !selectedModel || !profile || !planSeconds.length) {
            return;
        }
        const controller = new AbortController();
        quoteReplicationTasks({ config, model: selectedModel, ratio, resolution, seconds: planSeconds, referenceCount, referenceMode: referenceMode.mode, referenceVideo: editMode, audioCount: voiceCount, signal: controller.signal })
            .then((quote) => {
                if (controller.signal.aborted) return;
                setEstimate({ key: quoteKey, credits: quote.credits, label: quote.usage ? "按实际用量结算" : quote.credits === null ? "暂时无法估算积分" : "" });
            })
            .catch(() => {
                if (!controller.signal.aborted) setEstimate({ key: quoteKey, credits: null, label: "暂时无法估算积分" });
            });
        return () => controller.abort();
    }, [config, creditsEnabled, editMode, planSeconds, profile, quoteKey, ratio, referenceCount, referenceMode.mode, resolution, selectedModel, voiceCount]);

    // ---------- 提交 ----------
    const targets = useMemo<VideoReplicationTarget[]>(() => {
        if (kindConfig.targets.length) return kindConfig.targets;
        return [...new Set(visibleImages.flatMap((image) => CATEGORY_TARGET[image.category] || []))];
    }, [kindConfig.targets, visibleImages]);
    const adaptProductSpeech = targets.some((target) => target === "product" || target === "apparel");

    const missing = useMemo(() => {
        if (!selectedModel) return "暂无可用的视频模型";
        if (!video) return "请上传参考视频";
        if (video.uploading) return "参考视频上传中";
        if (!clipMs) return "正在读取参考视频时长";
        if (voiceSegmentSeconds < 4) return "模型参考时长不足，请减少音色或更换模型";
        if (visibleImages.some((image) => !image.resourceId && !image.error)) return "素材上传中";
        const materialError = replacementMaterialError(kindConfig, visibleImages);
        if (materialError) return materialError;
        for (const slot of kindConfig.slots) {
            if (slot.requires) continue;
            if (kindConfig.id !== "combination" && slot.note.startsWith("限") && !slotImages(slot.key).some((image) => image.category === slot.category && image.resourceId)) return `请上传${slot.label}`;
        }
        if (referenceMode.mode === "none") return "所选模型不支持参考图，请换一个模型";
        const limitSeconds = publicConfig?.maxReferenceSeconds || 120;
        if (clipMs > limitSeconds * 1000 + 50) return `参考视频超过 ${limitSeconds} 秒，请点「裁剪视频」截取一段`;
        if (clipMs && clipMs < 3950) return "裁剪后的视频至少 4 秒";
        if (effectiveAudio === "replace") {
            if (!canGenerateAudio) return "所选模型不能生成声音，请换模型或改选保留原声 / 静音";
            if (!voiceSupported) return "所选模型不支持参考音色，请换用支持参考音频的模型（如 Seedance 2.0）";
            if (!voiceCount) return "请上传至少 1 段参考音色";
            if (rhythmUi && !voiceSupported) return "所选模型不支持参考音色，请换用 Seedance 2.0 等模型，或删掉参考音色沿用原视频声音";
            if (voiceCount > maxVoiceAudios) return `所选模型最多接收 ${maxVoiceAudios} 段参考音频，请删掉多余的参考音色`;
        }
        if (kindConfig.consent && !faceConsent) return "请确认已取得肖像授权";
        if (Array.from(notes).length > kindConfig.notesLimit) return `补充说明最多 ${kindConfig.notesLimit} 字`;
        if (creditsEnabled && estimate.credits === null && estimate.label !== "按实际用量结算") return estimate.label || "正在估算积分";
        return "";
    }, [canGenerateAudio, clipMs, creditsEnabled, effectiveAudio, estimate.credits, estimate.label, faceConsent, kindConfig, maxVoiceAudios, notes, publicConfig?.maxReferenceSeconds, referenceMode.mode, rhythmUi, selectedModel, slotImages, video, visibleImages, voiceCount, voiceSegmentSeconds, voiceSupported]);

    const audioMode: AudioChoice = effectiveAudio;
    // 保留原声时参考音色用不上，不登记到项目。
    const submitSlots = useMemo(() => (audioMode === "replace" ? activeSlots : activeSlots.filter((slot) => slot.key !== VOICE_SLOT.key)), [activeSlots, audioMode]);

    // 元素参考图：补充要求换画面元素（梨→香蕉）时，后端给每个新元素先出一张参考图，视频模型以原片为底按图替换。
    // 只靠文字时原片编辑会把没有参考图的元素原样保留。自动选能收参考图的系统图片模型，优先用户默认图片模型。
    const keyframe = useMemo((): Partial<VideoReplicationParameters> => {
        const systemImage = (model: string) => Boolean(logicalModelIDForConfig({ ...config, model })) || resolveModelChannel(config, model)?.scope === "system";
        const models = selectableModelsByCapability(config, "image").filter(systemImage);
        const withReference = (model: string) => (modelCapabilityConfigFor(config, model).image?.references.maxImages || 0) >= 1;
        const model = config.imageModel && models.includes(config.imageModel) && withReference(config.imageModel) ? config.imageModel : models.find(withReference) || "";
        const imageProfile = model ? modelCapabilityConfigFor(config, model).image : undefined;
        if (!model || !imageProfile) return {};
        const sizes = imageProfile.size.values.length ? imageProfile.size.values : [imageProfile.size.default];
        const size = sizes.includes("1:1") ? "1:1" : imageProfile.size.default || sizes[0];
        const logicalModelId = logicalModelIDForConfig({ ...config, model }) || "";
        return {
            keyframeModel: modelOptionName(model),
            keyframeLogicalModelId: logicalModelId || undefined,
            keyframeChannelId: logicalModelId ? undefined : resolveModelChannel(config, model)?.id,
            keyframeMaxImages: imageProfile.references.maxImages,
            keyframeParameters: commerceImageParameters(imageProfile, size, imageProfile.resolution.supported ? imageProfile.resolution.default : imageProfile.quality.default || "auto"),
        };
    }, [config]);

    const buildParameters = useCallback((): VideoReplicationParameters => {
        const logicalModelId = logicalModelIDForConfig({ ...config, model: selectedModel }) || "";
        const channel = resolveModelChannel(config, selectedModel);
        const normalized = profile ? normalizeVideoValue(profile, { seconds: String(segmentSeconds), ratio, resolution }) : { ratio, resolution };
        // 保留音乐和节奏时也请求出声：换了商品后口播要按新商品改写，后端用原片伴奏 + 新人声合成；不需要改写时仍沿用原音轨。
        let generateAudio = audioMode === "replace" || audioMode === "generate" || (audioMode === "keep" && canGenerateAudio);
        if (audioLocked && adminEntry) generateAudio = adminEntry.generateAudio;
        return {
            model: modelOptionName(selectedModel),
            logicalModelId: logicalModelId || undefined,
            channelId: logicalModelId ? undefined : channel?.id,
            modelLabel: modelDisplayName(config, selectedModel),
            ratio: normalized.ratio,
            resolution: normalized.resolution,
            shotSeconds: allowedSeconds,
            referenceMode: referenceMode.mode,
            // 参考图 / 音频 / 视频数量、时长和提示词字数都按后台给所选模型配的能力，不另设上限（后端开始生成时还会按模型配置重写）。
            maxReferenceImages: referenceMode.max,
            maxReferenceAudios: Math.max(0, profile?.references.maxAudios || 0),
            maxReferenceAudioSeconds: Math.max(0, profile?.references.maxAudioDurationSeconds || 0),
            maxReferenceVideos: editMode ? Math.max(0, profile?.references.maxVideos || 0) : 0,
            maxReferenceVideoSeconds: editMode ? editSeconds : 0,
            promptMaxChars: Math.max(0, profile?.references.promptMaxChars || 0),
            generateAudio,
            keepOriginalAudio: audioMode === "keep",
            watermark: false,
            subtitles: false,
            faceConsent: Boolean(kindConfig.consent && faceConsent),
            transition: "cut",
            rhythm: "跟随参考视频",
            rewriteVoice: adaptProductSpeech && audioMode !== "mute",
            replaceMode: kindConfig.id,
            audioMode,
            subjectHint: kindConfig.subjectHint ? subjectHint.trim() : "",
            productName: hasProductImage ? productName.trim() : "",
            trimStartMs: trim ? Math.round(trim.startMs) : 0,
            trimEndMs: trim ? Math.round(trim.endMs) : 0,
            regions: kindConfig.lock && regions?.boxes.length ? regions.boxes : [],
            regionFrameMs: kindConfig.lock && regions?.boxes.length ? Math.round(regions.frameMs) : 0,
            talking: Boolean(kindConfig.talking && talking),
            ...(editMode ? keyframe : {}),
        };
    }, [
        keyframe,
        canGenerateAudio,
        regions,
        talking,
        trim,
        adminEntry,
        allowedSeconds,
        audioLocked,
        audioMode,
        adaptProductSpeech,
        config,
        faceConsent,
        hasProductImage,
        feature,
        kindConfig,
        profile,
        productName,
        ratio,
        referenceMode.max,
        referenceMode.mode,
        resolution,
        segmentSeconds,
        selectedModel,
        subjectHint,
    ]);

    const generate = useCallback(async () => {
        if (submitInFlight.current) return;
        if (missing || !video) {
            if (missing) message.warning(missing);
            return;
        }
        submitInFlight.current = true;
        setSubmitting(true);
        setSubmittingGuard(true);
        try {
            const projectId = video.projectId || (await createVideoReplicationProject({ title: "商品视频" })).id;
            const title = `${kindConfig.label}替换 · ${new Date().toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}`;
            let project = await updateVideoReplicationProject(projectId, { title, videoType: targets.join(","), prompt: notes.trim(), parameters: buildParameters() });
            // 项目里的素材与当前面板对齐：参考视频保留同一资源，图片按上传位顺序重新登记。
            for (const asset of project.assets) {
                if (asset.category === "reference_video" ? asset.resourceId !== video.resourceId : true) project = await deleteVideoReplicationAsset(projectId, asset.id);
            }
            if (!project.assets.some((asset) => asset.category === "reference_video")) project = await addVideoReplicationAsset(projectId, { category: "reference_video", resourceId: video.resourceId, name: video.name });
            for (const slot of submitSlots) {
                const items = visibleImages.filter((image) => image.slot === slot.key && image.category === slot.category && image.resourceId);
                for (const [index, image] of items.entries()) {
                    project = await addVideoReplicationAsset(projectId, { category: slot.category, resourceId: image.resourceId, name: items.length > 1 ? `${slot.label}${index + 1}` : slot.label });
                }
            }
            project = await startVideoReplication(projectId, { segmentSeconds: voiceSegmentSeconds, keepOriginalAudio: audioMode === "keep", estimatedCredits: estimate.credits || 0, maxVideoTasks: planSeconds.length, confirmFirst: false });
            if (draftProjectRef.current) {
                const draftId = await draftProjectRef.current.catch(() => "");
                if (draftId === projectId) draftProjectRef.current = null;
            }
            // 已提交生成：清空参考框里的素材与参考视频（草稿随之清空），提交失败时保留以便重试；模型、比例、说明等设置保留。
            setImages((current) => {
                for (const item of current) if (item.previewUrl.startsWith("blob:")) URL.revokeObjectURL(item.previewUrl);
                return [];
            });
            setVideo((current) => {
                if (current?.previewUrl.startsWith("blob:")) URL.revokeObjectURL(current.previewUrl);
                return null;
            });
            setTrim(null);
            setRegions(null);
            setProductName("");
            setLinkText("");
            setProjects((current) => [project, ...current.filter((item) => item.id !== project.id)]);
            message.success("已开始生成，AI 正在分析参考视频");
        } catch (error) {
            message.error(errorText(error, "提交失败，请稍后重试"));
        } finally {
            submitInFlight.current = false;
            setSubmitting(false);
            setSubmittingGuard(false);
        }
    }, [submitSlots, audioMode, buildParameters, estimate.credits, feature, kindConfig, message, missing, notes, planSeconds, voiceSegmentSeconds, targets, video, visibleImages]);

    /** 再次生成：用项目原来的素材和参数重新分析并生成，生成新的成片版本。 */
    const regenerate = useCallback(
        async (project: VideoReplicationProject) => {
            const segmentLimit = project.parameters.segmentSeconds || segmentSeconds;
            const sourceMs = project.assets.find((asset) => asset.category === "reference_video" && asset.isPrimary)?.durationMs || 0;
            const trimEnd = project.parameters.trimEndMs;
            const durationMs = trimEnd ? Math.min(trimEnd, sourceMs || trimEnd) - (project.parameters.trimStartMs || 0) : sourceMs;
            const maxVideoTasks = project.parameters.maxVideoTasks || (durationMs > 0 ? estimateSegments(durationMs, segmentLimit).length : 1);
            const confirmed = await new Promise<boolean>((resolve) => {
                modal.confirm({
                    title: "再次生成视频？",
                    content: `这次最多提交 ${maxVideoTasks} 次付费视频生成，按当前模型价格计费。`,
                    okText: "再次生成",
                    cancelText: "取消",
                    onOk: () => resolve(true),
                    onCancel: () => resolve(false),
                });
            });
            if (!confirmed) return;
            try {
                const next = await startVideoReplication(project.id, {
                    segmentSeconds: segmentLimit,
                    keepOriginalAudio: Boolean(project.parameters.keepOriginalAudio),
                    estimatedCredits: project.parameters.autoEstimate || 0,
                    maxVideoTasks,
                    confirmFirst: false,
                });
                setProjects((current) => [next, ...current.filter((item) => item.id !== next.id)]);
                message.success("已重新开始生成");
            } catch (error) {
                message.error(errorText(error, "重新生成失败"));
            }
        },
        [message, modal, segmentSeconds],
    );

    /** 编辑：把项目的素材与参数载入左侧面板，修改后生成为新作品，原作品保留。 */
    const edit = useCallback(
        (project: VideoReplicationProject) => {
            const params = project.parameters;
            const mode = params.replaceMode || "product";
            const config = replaceKindConfig(mode);
            setKind(config.id);
            const byCategory = new Map<string, VideoReplicationAsset[]>();
            for (const asset of [...project.assets].sort((a, b) => a.sortOrder - b.sortOrder)) byCategory.set(asset.category, [...(byCategory.get(asset.category) || []), asset]);
            const nextImages: DraftImage[] = [];
            for (const slot of [...config.slots, VOICE_SLOT]) {
                const pool = byCategory.get(slot.category) || [];
                pool.splice(0, slot.max).forEach((asset) =>
                    nextImages.push({
                        id: createClientUUID(),
                        slot: slot.key,
                        category: slot.category,
                        resourceId: asset.resourceId,
                        name: asset.name,
                        previewUrl: slot.media === "audio" ? "" : resourceFileUrl(asset.resourceId),
                        durationMs: asset.durationMs || undefined,
                    }),
                );
            }
            setImages(nextImages);
            const reference = project.assets.find((asset) => asset.category === "reference_video");
            setVideo(reference ? { resourceId: reference.resourceId, name: reference.name, previewUrl: resourceFileUrl(reference.resourceId), durationMs: reference.durationMs, width: reference.width, height: reference.height } : null);
            setNotes(project.prompt || "");
            setSubjectHint(params.subjectHint || "");
            setProductName(params.productName || "");
            const mode2 = params.audioMode || (params.keepOriginalAudio ? "keep" : params.generateAudio ? "replace" : "mute");
            setAudio(mode2 === "generate" ? "keep" : mode2);
            setKeepMusic(mode2 === "keep");
            setTalking(Boolean(params.talking));
            setTrim(params.trimEndMs && params.trimEndMs > (params.trimStartMs || 0) ? { startMs: params.trimStartMs || 0, endMs: params.trimEndMs } : null);
            setRegions(params.regions?.length ? { frameMs: params.regionFrameMs || 0, boxes: params.regions } : null);
            setFaceConsent(Boolean(params.faceConsent));
            if (params.ratio) setRatio(params.ratio);
            if (params.resolution) setResolution(params.resolution);
            message.info("已载入到左侧面板，修改后点击「开始生成」会生成新作品");
        },
        [message],
    );

    const remove = useCallback(
        (project: VideoReplicationProject) => {
            modal.confirm({
                title: "删除这个视频作品？",
                content: "作品记录会被删除，上传过的素材仍保留在素材库。",
                okText: "删除",
                okButtonProps: { danger: true },
                cancelText: "取消",
                onOk: async () => {
                    try {
                        await deleteVideoReplicationProject(project.id);
                        setProjects((current) => current.filter((item) => item.id !== project.id));
                    } catch (error) {
                        message.error(errorText(error, "删除失败"));
                        throw error;
                    }
                },
            });
        },
        [message, modal],
    );

    const reset = useCallback(() => {
        setImages([]);
        setVideo(null);
        setNotes("");
        setSubjectHint("");
        setProductName("");
        setFaceConsent(false);
        setLinkText("");
        setTrim(null);
        setRegions(null);
        setTalking(false);
    }, []);

    return {
        setSubmittingGuard,
        profile,
        publicConfig,
        buildParameters,
        feature,
        kind,
        setKind,
        kindConfig,
        images: visibleImages,
        slotImages,
        addImageFiles,
        addPickedImages,
        removeImage,
        video,
        addVideoFile,
        pickVideo,
        removeVideo,
        setVideoMeta,
        trim,
        setTrim,
        regions,
        setRegions,
        keepMusic,
        setKeepMusic,
        talking,
        setTalking,
        effectiveAudio,
        adaptProductSpeech,
        uploadedVoices,
        clipMs,
        linkText,
        setLinkText: changeLinkText,
        linkBusy,
        linkProgress,
        linkError,
        parseLink,
        pasteLink,
        notes,
        setNotes,
        subjectHint,
        hasProductImage,
        productName,
        setProductName,
        setSubjectHint,
        audio,
        setAudio,
        canGenerateAudio,
        voiceSupported,
        editMode,
        maxVoiceSeconds,
        maxVoiceAudios,
        faceConsent,
        setFaceConsent,
        audioLocked,
        config,
        selectedModel,
        setModel,
        systemModel,
        ratio,
        setRatio,
        ratios,
        resolution,
        setResolution,
        resolutions,
        estimate,
        creditsEnabled,
        estimatedVideoTasks: planSeconds.length,
        missing,
        submitting,
        generate,
        projects,
        loadingProjects,
        refreshProjects,
        regenerate,
        edit,
        remove,
        reset,
        maxReferenceSeconds: publicConfig?.maxReferenceSeconds || 120,
        disabledKinds: publicConfig?.disabledKinds || [],
    };
}

export type EcommerceVideoState = ReturnType<typeof useEcommerceVideo>;
