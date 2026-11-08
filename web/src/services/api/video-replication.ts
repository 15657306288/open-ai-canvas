import { ApiError, apiBaseURL, http } from "@/services/api/request";
import { consumeTaskTextStream, createTaskTextStreamParser, type TaskTextStreamEvent } from "@/services/api/task-text-stream";

/** 电商 Agent「商品视频」模特/商品替换：项目、三类素材、分析与分镜、逐镜头生成版本与成片版本。 */

export type VideoReplicationTarget = "product" | "model" | "apparel" | "face" | "background";
/** 逗号分隔的替换目标（可为空：只按创作描述复刻）。 */
export type VideoReplicationType = string;
export type VideoReplicationAssetCategory = "reference_video" | "product_image" | "model_image" | "apparel_image" | "face_image" | "background_image" | "product_detail_image" | "packaging_image" | "voice_audio";
export type VideoReplicationStatus = "draft" | "analyzing" | "analyzed" | "generating" | "composing" | "completed" | "failed";
export type VideoReplicationRunStatus = "submitting" | "queued" | "running" | "succeeded" | "failed" | "cancelled";

export type VideoReplicationRegion = { x: number; y: number; w: number; h: number };

export type VideoReplicationParameters = {
    model?: string;
    logicalModelId?: string;
    channelId?: string;
    modelLabel?: string;
    ratio?: string;
    resolution?: string;
    targetSeconds?: number;
    shotSeconds?: number[];
    generateAudio: boolean;
    watermark: boolean;
    camera?: string;
    rhythm?: string;
    lighting?: string;
    mood?: string;
    referenceMode?: "reference" | "first_frame" | "none";
    maxReferenceImages?: number;
    transition?: "cut" | "fade";
    subtitles: boolean;
    faceConsent: boolean;
    /** 保留参考视频原音轨（音乐与节奏），适合舞蹈、卡点、BGM 视频。 */
    keepOriginalAudio?: boolean;
    /** 由「一键生成」在服务端写入：分析完成后自动分段生成并合成。 */
    auto?: boolean;
    segmentSeconds?: number;
    autoEstimate?: number;
    maxVideoTasks?: number;
    /** 由服务端写入：先分析、再确认生成（分析完成后停在复刻方案）。 */
    confirmFirst?: boolean;
    /** 重做口播：按你的商品改写原视频台词；关闭时沿用原台词。 */
    rewriteVoice?: boolean;
    /** 电商 Agent 商品视频的替换方式。 */
    replaceMode?: "model" | "clothing" | "face" | "background" | "product" | "precise-product" | "precise-person" | "combination";
    /** replace 用参考音色按原台词重新配音 / keep 保留原声 / mute 静音 / generate 模型按新画面重新生成声音。 */
    audioMode?: "replace" | "keep" | "mute" | "generate";
    /** 裁剪视频：参考视频的起止时间（相对原视频，毫秒）；trimEndMs 为 0 表示不裁剪。 */
    trimStartMs?: number;
    trimEndMs?: number;
    /** 框选锁定：在原视频 regionFrameMs 处画面上框出的替换目标，坐标按画面宽高归一化到 0–1。 */
    regions?: VideoReplicationRegion[];
    regionFrameMs?: number;
    /** 口播视频：人物口型与原台词同步。 */
    talking?: boolean;
    /** 元素参考图用的系统图片模型：补充要求换画面元素时，每个新元素先出一张参考图再交给视频模型。 */
    keyframeModel?: string;
    keyframeLogicalModelId?: string;
    keyframeChannelId?: string;
    keyframeMaxImages?: number;
    keyframeParameters?: { size: string; quality?: string; imageResolution?: string; count: number };
    /** 商品替换时原视频里要被替换的商品主体（≤10 字）。 */
    subjectHint?: string;
    productName?: string;
    /** 所选模型可接收的参考音频数；替换音频时把参考音色作为参考音频发送。 */
    maxReferenceAudios?: number;
    /** 所选模型可接收的参考视频数与单段时长；大于 0 时按「原片编辑」只替换目标。 */
    maxReferenceVideos?: number;
    maxReferenceVideoSeconds?: number;
    /** 以下来自所选模型在后台的能力配置：参考音频时长上限、提示词字数上限。 */
    maxReferenceAudioSeconds?: number;
    promptMaxChars?: number;
};

export type VideoReplicationAsset = {
    id: string;
    projectId: string;
    category: VideoReplicationAssetCategory;
    resourceId: string;
    name: string;
    label: string;
    sortOrder: number;
    isPrimary: boolean;
    mimeType: string;
    sizeBytes: number;
    durationMs: number;
    width: number;
    height: number;
    fps: number;
    hasAudio: boolean;
    profile?: Record<string, unknown>;
};

export type VideoReplicationSourceShot = {
    index: number;
    startMs: number;
    endMs: number;
    keyframeResourceId?: string;
    shotSize?: string;
    camera?: string;
    scene?: string;
    action?: string;
    personAction?: string;
    productDisplay?: string;
    lighting?: string;
    composition?: string;
    subtitle?: string;
    voiceover?: string;
};

export type VideoReplicationVideoAnalysis = {
    assetId: string;
    name: string;
    primary: boolean;
    durationMs: number;
    width: number;
    height: number;
    fps: number;
    hasAudio: boolean;
    audioResourceId?: string;
    cutCount: number;
    shots: VideoReplicationSourceShot[];
};

export type VideoReplicationAnalysis = {
    videos: VideoReplicationVideoAnalysis[];
    /** 识图模型对用户框选目标的描述，以及画好红框的那一帧。 */
    regionTarget?: string;
    regionFrameResourceId?: string;
    summary: { structure?: string; rhythm?: string; editingPace?: string; style?: string; mood?: string; hook?: string; subtitles?: string; voiceover?: string };
    product?: Record<string, unknown> | null;
    model?: Record<string, unknown> | null;
    recommendation: { camera?: string; rhythm?: string; lighting?: string; mood?: string; ratio?: string; seconds?: number };
    voice?: VideoReplicationVoice;
    counts?: { shots: number; scenes?: string; people?: string; products?: string };
    marketing?: string;
    visionModel?: string;
    planningModel?: string;
    ffmpegSceneMode?: string;
    analyzedAt: string;
};

export type VideoReplicationShot = {
    id: string;
    index: number;
    durationSec: number;
    title: string;
    shotSize?: string;
    camera?: string;
    scene?: string;
    action?: string;
    productFocus?: string;
    lighting?: string;
    mood?: string;
    subtitle?: string;
    voiceover?: string;
    speaker?: string;
    sourceShot?: string;
    prompt: string;
};

export type VideoReplicationStoryboard = { shots: VideoReplicationShot[]; notes?: string; voiceAdapted?: boolean; subtitlesAdapted?: boolean; subtitles?: boolean };

export type VideoReplicationRun = {
    id: string;
    projectId: string;
    shotId: string;
    kind?: string;
    version: number;
    taskId: string;
    status: VideoReplicationRunStatus;
    outputResourceId: string;
    prompt: string;
    error: string;
    selected: boolean;
    createdAt: string;
    updatedAt: string;
};

export type VideoReplicationOutput = {
    mode?: VideoReplicationStrategy | "native_dubbed";
    quality?: VideoReplicationQuality;
    id: string;
    projectId: string;
    taskId: string;
    status: string;
    resourceId: string;
    subtitleSrt: string;
    durationMs: number;
    error: string;
    createdAt: string;
};

export type VideoReplicationMention = { assetId: string; category: VideoReplicationAssetCategory; label: string };

export type VideoReplicationProjectSummary = {
    id: string;
    title: string;
    videoType: VideoReplicationType;
    status: VideoReplicationStatus;
    stage: string;
    progress: number;
    error: string;
    createdAt: string;
    updatedAt: string;
};

export type VideoReplicationProject = VideoReplicationProjectSummary & {
    prompt: string;
    storyboardRevision: number;
    parameters: VideoReplicationParameters;
    analysis?: VideoReplicationAnalysis;
    storyboard?: VideoReplicationStoryboard;
    assets: VideoReplicationAsset[];
    mentions: VideoReplicationMention[];
    runs: VideoReplicationRun[];
    outputs: VideoReplicationOutput[];
    plan?: VideoReplicationPlan;
    planStale: boolean;
    spentCredits: number;
    paused: boolean;
    voiceCast: VideoReplicationVoiceCast;
    pauseReason: string;
    planConfirmedAt?: string;
    /** 已保存的分析与分段方案仍对应当前素材和要求，无需再次分析。 */
    analysisFresh: boolean;
};

export type VideoReplicationProjectPatch = {
    title?: string;
    videoType?: VideoReplicationType;
    prompt?: string;
    parameters?: VideoReplicationParameters;
};

const base = "/video-replication/projects";

export async function downloadVideoReplicationComparison(projectId: string, outputId: string, signal?: AbortSignal) {
    const response = await http.raw<Blob>({ method: "POST", url: `${base}/${encodeURIComponent(projectId)}/outputs/${encodeURIComponent(outputId)}/comparison-download`, responseType: "blob", timeout: 300_000, signal, validateStatus: () => true });
    if (response.status !== 200 || response.data.type.includes("json")) {
        let error: { msg?: string; reason?: string; code?: number } = {};
        try {
            error = JSON.parse(await response.data.text());
        } catch {
            /* Non-JSON proxy errors use the download-specific message. */
        }
        throw new ApiError(error.msg || "对比视频下载失败，请稍后重试", { status: response.status, code: error.code, reason: error.reason });
    }
    return response.data;
}

export function listVideoReplicationProjects(signal?: AbortSignal) {
    return http.get<{ projects: VideoReplicationProjectSummary[] }>(base, { params: { pageSize: 30 }, signal }).then((data) => data.projects || []);
}

export function createVideoReplicationProject(patch: VideoReplicationProjectPatch = {}) {
    return http.post<VideoReplicationProject>(base, patch);
}

export function getVideoReplicationProject(id: string, signal?: AbortSignal) {
    return http.get<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}`, { signal });
}

export function updateVideoReplicationProject(id: string, patch: VideoReplicationProjectPatch) {
    return http.patch<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}`, patch);
}

export function deleteVideoReplicationProject(id: string) {
    return http.delete<{ id: string }>(`${base}/${encodeURIComponent(id)}`);
}

export function addVideoReplicationAsset(id: string, input: { category: VideoReplicationAssetCategory; resourceId: string; name: string }) {
    return http.post<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/assets`, input);
}

/** 粘贴视频分享链接：后端解析下载后替换为该项目的参考视频（同步接口，新页面用下方带进度的版本）。 */
export function importVideoReplicationLink(id: string, url: string) {
    return http.post<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/link`, { url }, { timeout: 300_000 });
}

export type VideoLinkStage = "detecting" | "detected" | "resolving" | "parsing" | "parsed" | "downloading" | "merging" | "probing" | "uploading" | "reused" | "registering" | "done";
export type VideoLinkProgress = { stage: VideoLinkStage; platform?: string; platformLabel?: string; progress?: number; message?: string; title?: string; durationMs?: number };
export type VideoLinkSource = { platform: string; platformLabel: string; id: string; title: string; author?: string; sourceUrl: string; durationMs: number; width: number; height: number; fps: number; hasAudio: boolean; provider: string };
/** resourceId 是转存后的资源（云存储），assetId 是素材库素材，referenceAssetId 是项目里的参考视频素材。 */
export type VideoLinkImportResult = { project: VideoReplicationProject; source: VideoLinkSource; resourceId: string; assetId: string; referenceAssetId: string; reused: boolean };
type VideoLinkFailure = { code: string; message: string; retryable: boolean; platform?: string; status: number };
type VideoLinkStreamData = VideoLinkProgress & { result?: VideoLinkImportResult; error?: VideoLinkFailure };

/**
 * 带进度的链接导入（SSE）：服务端自动识别平台、展开短链、解析、下载（必要时合并音视频）、FFmpeg 校验、
 * 转存云存储并登记素材库，期间推送阶段事件，最后返回结果或带重试标记的错误。
 */
export async function importVideoReplicationLinkStream(id: string, text: string, onProgress: (progress: VideoLinkProgress) => void, signal?: AbortSignal): Promise<VideoLinkImportResult> {
    const response = await fetch(`${String(apiBaseURL).replace(/\/+$/, "")}${base}/${encodeURIComponent(id)}/link/stream`, {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
        body: JSON.stringify({ url: text }),
        signal,
    });
    if (!response.ok || !response.body || !response.headers.get("content-type")?.includes("text/event-stream")) {
        let message = `链接解析失败（${response.status}）`;
        let reason: string | undefined;
        try {
            const body = (await response.json()) as { msg?: string; reason?: string };
            if (body?.msg) message = body.msg;
            reason = body?.reason;
        } catch {
            // 非 JSON 响应保留默认说明。
        }
        throw new ApiError(message, { status: response.status, reason, retryable: response.status >= 500 || response.status === 429 });
    }
    const state: { result?: VideoLinkImportResult; failure?: VideoLinkFailure } = {};
    const onEvent = (event: TaskTextStreamEvent) => {
        const data = event.data as VideoLinkStreamData;
        if (event.event === "stage") onProgress(data);
        else if (event.event === "result" && data.result) state.result = data.result;
        else if (event.event === "error" && data.error) state.failure = data.error;
    };
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    const parser = createTaskTextStreamParser();
    for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        consumeTaskTextStream(parser, decoder.decode(value, { stream: true }), onEvent);
    }
    consumeTaskTextStream(parser, decoder.decode(), onEvent, true);
    if (state.failure) throw new ApiError(state.failure.message, { status: state.failure.status, reason: state.failure.code, retryable: state.failure.retryable, details: { platform: state.failure.platform } });
    if (!state.result) throw new ApiError("链接解析中断，请重试", { retryable: true });
    return state.result;
}

export function reorderVideoReplicationAssets(id: string, input: { category: VideoReplicationAssetCategory; ids: string[]; primaryId?: string }) {
    return http.put<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/assets/order`, input);
}

export function deleteVideoReplicationAsset(id: string, assetId: string) {
    return http.delete<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/assets/${encodeURIComponent(assetId)}`);
}

/**
 * 开始分析。confirmFirst 时只产出复刻方案（不调用付费视频模型），素材与要求未变时服务端直接复用已保存的分析；
 * force 丢弃已保存的分析重新理解参考视频。旧的一键项目（confirmFirst=false）分析完成后自动生成并合成。
 */
export function startVideoReplication(id: string, input: { segmentSeconds: number; keepOriginalAudio: boolean; estimatedCredits: number; maxVideoTasks?: number; confirmFirst?: boolean; force?: boolean }) {
    return http.post<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/start`, input);
}

export function analyzeVideoReplication(id: string) {
    return http.post<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/analyze`, {});
}

export function saveVideoReplicationStoryboard(id: string, revision: number, storyboard: VideoReplicationStoryboard) {
    return http.put<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/storyboard`, { revision, storyboard });
}

export function generateVideoReplication(id: string, input: { idempotencyKey: string; shotIds?: string[]; regenerate?: boolean }) {
    return http.post<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/generate`, input);
}

export function selectVideoReplicationRun(id: string, runId: string) {
    return http.post<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/runs/${encodeURIComponent(runId)}/select`, {});
}

export function cancelVideoReplication(id: string) {
    return http.post<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/cancel`, {});
}

export function composeVideoReplication(id: string, idempotencyKey: string) {
    return http.post<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/compose`, { idempotencyKey });
}

export type VideoReplicationStrategy = "native" | "segmented";
export type VideoReplicationAudioMode = "model_native" | "keep_original" | "mute" | "tts_voiceover";

export type VideoReplicationPlan = {
    strategy: VideoReplicationStrategy;
    model: string;
    modelLabel?: string;
    targetSeconds: number;
    taskCount: number;
    segmentMaxSeconds?: number;
    segments: Array<{ shotId: string; seconds: number }>;
    continuity: "single_task" | "tail_frame" | "shared_references";
    audioMode: VideoReplicationAudioMode;
    voiceAuthorized: boolean;
    usesFfmpegCompose: boolean;
    usesTts: boolean;
    usesDigitalHuman: boolean;
    lipSyncRequired: boolean;
    lipSyncSupported: boolean;
    estimatedCredits: number;
    budgetCredits: number;
    storyboardRevision: number;
    noSplice?: boolean;
    confirmedAt: string;
    notes?: string[];
};

export type VideoReplicationPlanInput = {
    strategy: VideoReplicationStrategy;
    audioMode: VideoReplicationAudioMode;
    voiceAuthorized: boolean;
    lipSyncAcknowledged: boolean;
    estimatedCredits: number;
    budgetCredits: number;
    /** 禁止剪辑拼接：只接受原生直出，模型做不到时服务端拒绝，不会改成智能分段。 */
    noSplice: boolean;
};

export type VideoReplicationVoice = {
    type: "on_camera" | "narration" | "dialogue" | "music" | "silent" | "unknown";
    typeLabel: string;
    hasAudio: boolean;
    asrAvailable: boolean;
    asrError?: string;
    language?: string;
    speechMs: number;
    segments?: Array<{ startMs: number; endMs: number; text: string }>;
    diarization: boolean;
    music?: string;
    soundEffects?: string;
    speakerCount?: string;
    charsPerSecond?: number;
    lipSyncNeeded: boolean;
    speakers?: Array<{ id: string; label: string; gender?: string; age?: string; tone?: string; onCamera: boolean }>;
};

export type VideoReplicationQuality = {
    passed: boolean;
    durationMs: number;
    expectedMs: number;
    width: number;
    height: number;
    hasAudio: boolean;
    blackMs: number;
    checks: Array<{ name: string; status: "pass" | "warn" | "fail" | "skipped"; detail?: string }>;
};

export function confirmVideoReplicationPlan(id: string, input: VideoReplicationPlanInput) {
    return http.post<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/plan`, input);
}

export function pauseVideoReplication(id: string, paused: boolean) {
    return http.post<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/${paused ? "pause" : "resume"}`, {});
}

export type VideoReplicationModelRef = { logicalModelId?: string; channelModelId?: string; channelId?: string; modelKey?: string };

export type VideoReplicationVideoModel = VideoReplicationModelRef & {
    label?: string;
    enabled: boolean;
    ratios?: string[];
    resolutions?: string[];
    defaultRatio?: string;
    defaultResolution?: string;
    generateAudio: boolean;
    allowAudioToggle: boolean;
};

export type VideoReplicationSetting = {
    vision?: VideoReplicationModelRef;
    planning?: VideoReplicationModelRef;
    videoModels: VideoReplicationVideoModel[];
    restrictVideoModels?: boolean;
    maxReferenceSeconds?: number;
    disabledKinds?: string[];
    asr?: { mode: "env" | "model" | "vision"; model: VideoReplicationModelRef; language?: string };
    tts?: { model: VideoReplicationModelRef; voices: VideoReplicationVoiceOption[]; format?: string };
    /** Seedance / 可灵模型提示词知识库的开关与自定义内容（content 为空 = 内置默认内容）。 */
    knowledge?: VideoReplicationKnowledgeBase[];
    updatedAt?: string;
};

export type VideoReplicationKnowledgeBase = { id: string; enabled: boolean; content?: string };

export type VideoReplicationKnowledgeView = {
    id: string;
    name: string;
    description: string;
    models: string[];
    enabled: boolean;
    content: string;
    customized: boolean;
    defaultContent: string;
};

export type VideoReplicationPublicConfig = {
    videoModels: VideoReplicationVideoModel[];
    restrictVideoModels: boolean;
    maxReferenceSeconds: number;
    /** 后台关闭的替换类型。 */
    disabledKinds?: string[];
    asrConfigured: boolean;
    ttsConfigured: boolean;
    voices: VideoReplicationVoiceOption[];
};

export type VideoReplicationAdminStatus = {
    setting: VideoReplicationSetting;
    checks: Array<{ name: string; ok: boolean; detail?: string }>;
    knowledge: VideoReplicationKnowledgeView[];
};

export type VideoReplicationAdminProject = {
    id: string;
    userId: string;
    username: string;
    displayName: string;
    title: string;
    videoType: VideoReplicationType;
    status: VideoReplicationStatus;
    stage: string;
    error: string;
    paused: boolean;
    strategy: "" | VideoReplicationStrategy;
    runs: number;
    failedRuns: number;
    activeRuns: number;
    outputs: number;
    updatedAt: string;
    createdAt?: string;
    replaceMode?: string;
    audioMode?: string;
};

export function getVideoReplicationConfig(signal?: AbortSignal) {
    return http.get<VideoReplicationPublicConfig>("/video-replication/config", { signal });
}

export function getVideoReplicationAdminStatus() {
    return http.get<VideoReplicationAdminStatus>("/admin/video-replication");
}

export function updateVideoReplicationSetting(setting: VideoReplicationSetting) {
    return http.put<VideoReplicationAdminStatus>("/admin/video-replication", setting);
}

export type VideoReplicationOverview = {
    projects: number;
    projectsInRange: number;
    users: number;
    byStatus: Record<string, number>;
    runs: Record<string, number>;
    outputs: number;
    byMode: Record<string, number>;
    recentErrors: Array<{ error: string; count: number }> | null;
};

export function getVideoReplicationOverview(days: number) {
    return http.get<VideoReplicationOverview>("/admin/video-replication/overview", { params: { days } });
}

export function getAdminVideoReplicationProject(id: string) {
    return http.get<VideoReplicationProject>(`/admin/video-replication/projects/${encodeURIComponent(id)}`);
}

export function listVideoReplicationAdminProjects() {
    return http.get<{ projects: VideoReplicationAdminProject[] }>("/admin/video-replication/projects", { params: { pageSize: 50 } }).then((data) => data.projects || []);
}

export type VideoReplicationVoiceOption = { key: string; name: string; gender?: string; age?: string; style?: string; language?: string };

export type VideoReplicationVoiceRole = {
    speakerId: string;
    label: string;
    gender?: string;
    age?: string;
    tone?: string;
    onCamera: boolean;
    voiceKey?: string;
    voiceName?: string;
    reason?: string;
    modelAssetId?: string;
    instructions?: string;
};

export type VideoReplicationVoiceCast = { roles: VideoReplicationVoiceRole[] };

export function updateVideoReplicationVoiceCast(id: string, cast: VideoReplicationVoiceCast) {
    return http.put<VideoReplicationProject>(`${base}/${encodeURIComponent(id)}/voice-cast`, cast);
}

/** 试听：后端用后台配音模型创建一个普通音频任务，返回任务；前台轮询任务拿到音频。 */
export function previewVideoReplicationVoice(id: string, input: { speakerId: string; voiceKey: string; text?: string }) {
    return http.post<{ id: string; status: string }>(`${base}/${encodeURIComponent(id)}/voice-preview`, input);
}
