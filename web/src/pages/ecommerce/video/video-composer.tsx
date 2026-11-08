import { AutoComplete, Button, Input, Popover, Select, Tooltip } from "antd";
import { ArrowUp, Check, ChevronDown, Scissors, ClipboardPaste, FolderOpen, Link2, LoaderCircle, Music2, Play, Plus, Sparkles, Volume2, VolumeX, X } from "lucide-react";
import { useEffect, useRef, useState, type CSSProperties, type DragEvent, type ReactNode } from "react";

import { ModelPicker } from "@/components/model-picker";
import { RollingCreditAmount } from "@/components/rolling-credit-text";
import { CreditStar } from "@/components/ui/credit-star";
import { AppModal } from "@/components/ui/product/app-modal";
import { resourceFileUrl } from "@/services/api/resources";
import { WorkbenchAssetPicker } from "@/pages/workbench/workbench-asset-picker";

import { linkProgressPercent, linkProgressText } from "./link-progress";
import { FeatureGlyph, KIND_DETAILS, KindGlyph } from "./video-glyphs";
import "./video-chrome.css";
import { formatClock, LockCard, RegionSelectModal, TrimVideoModal } from "./video-editors";
import { CompareCard, UploadTipCard } from "./video-illustrations";
import { AUDIO_CHOICES, FEATURES, REPLACE_KINDS, sharePlatforms, subjectExamples, VOICE_SLOT, type MaterialSlot, type ReplaceKind, type TipKind } from "./video-kinds";
import type { EcommerceVideoState } from "./use-ecommerce-video";

const TIP_STORAGE = "ecv-tip-dismissed:";

function tipDismissed(kind: TipKind) {
    try {
        return window.localStorage.getItem(TIP_STORAGE + kind) === "1";
    } catch {
        return false;
    }
}

/** 悬停上传位时弹出「注意事项」；点「不再提醒」后这一类不再弹出。 */
function UploadTip({ kind, children }: { kind: TipKind; children: ReactNode }) {
    const [open, setOpen] = useState(false);
    const [dismissed, setDismissed] = useState(() => tipDismissed(kind));
    useEffect(() => setDismissed(tipDismissed(kind)), [kind]);
    if (dismissed) return <>{children}</>;
    return (
        <Popover
            open={open}
            onOpenChange={setOpen}
            trigger={["hover"]}
            mouseEnterDelay={0.2}
            mouseLeaveDelay={0.15}
            placement="bottomLeft"
            arrow={false}
            classNames={{ root: "ecv-tip-popover" }}
            content={
                <UploadTipCard
                    kind={kind}
                    onClose={() => setOpen(false)}
                    onDismiss={() => {
                        try {
                            window.localStorage.setItem(TIP_STORAGE + kind, "1");
                        } catch {
                            // 浏览器禁用存储时只在本次页面内不再提醒。
                        }
                        setDismissed(true);
                        setOpen(false);
                    }}
                />
            }
        >
            <span className="ecv-tip-anchor">{children}</span>
        </Popover>
    );
}

function Section({ title, hint, extra, children }: { title: string; hint?: ReactNode; extra?: ReactNode; children: ReactNode }) {
    return (
        <section className="ecs-block ecv-block" aria-label={title || "模型与参数"}>
            <header className="ecv-block-head" hidden={!title && !hint && !extra}>
                <div>
                    <h3>{title}</h3>
                    {hint ? <p>{hint}</p> : null}
                </div>
                {extra}
            </header>
            {children}
        </section>
    );
}

function stop(event: DragEvent) {
    event.preventDefault();
    event.stopPropagation();
}

/** 替换类型按钮：单色图标与文字，悬浮显示说明卡。 */
function KindButton({ id, label, pressed, onPick }: { id: ReplaceKind; label: string; pressed: boolean; onPick: () => void }) {
    const detail = KIND_DETAILS[id];
    return (
        <Tooltip
            placement="top"
            mouseEnterDelay={0.35}
            arrow={false}
            classNames={{ root: "ecv-kind-tip" }}
            title={
                <span className="ecv-kind-tip-body">
                    <span className={`ecv-glyph-tile is-kind is-${id}`}>
                        <KindGlyph id={id} />
                    </span>
                    <span>
                        <strong>{detail.title}</strong>
                        <small>{detail.description}</small>
                    </span>
                </span>
            }
        >
            <button type="button" className="ecv-kind" aria-pressed={pressed} aria-describedby={undefined} onClick={onPick}>
                <span className={`ecv-kind-icon is-${id}`}>
                    <KindGlyph id={id} />
                </span>
                <span className="ecv-kind-label">{label}</span>
            </button>
        </Tooltip>
    );
}

/** 悬浮素材时在旁边弹出大图 / 自动播放的视频小窗 / 音频试听。 */
export function MediaHoverPreview({
    kind,
    src,
    name,
    startMs,
    endMs,
    showHoverPreview = true,
    children,
}: {
    kind: "image" | "video" | "audio";
    src: string;
    name: string;
    startMs?: number;
    endMs?: number;
    showHoverPreview?: boolean;
    children: ReactNode;
}) {
    const [open, setOpen] = useState(false);
    const [expanded, setExpanded] = useState(false);
    useEffect(() => {
        setOpen(false);
        setExpanded(false);
    }, [src]);
    if (!src) return <>{children}</>;
    return (
        <>
            <Popover
                open={showHoverPreview && open && !expanded}
                onOpenChange={setOpen}
                trigger={showHoverPreview ? "hover" : []}
                placement="rightTop"
                mouseEnterDelay={0.25}
                mouseLeaveDelay={0.1}
                arrow={false}
                destroyOnHidden
                classNames={{ root: "ecv-hover-preview" }}
                content={
                    <div className={`ecv-hover-card is-${kind}`}>
                        {kind === "image" ? <img src={src} alt={name} /> : null}
                        {kind === "video" ? <HoverVideo src={src} startMs={startMs} endMs={endMs} /> : null}
                        {kind === "audio" ? (
                            <div className="ecv-hover-audio">
                                <Music2 size={18} />
                                <audio src={src} controls autoPlay preload="metadata" />
                            </div>
                        ) : null}
                        <footer>
                            <span title={name}>{name}</span>
                            {kind === "video" && endMs ? (
                                <small>
                                    选区 {formatClock(startMs || 0)}–{formatClock(endMs)}
                                </small>
                            ) : null}
                        </footer>
                    </div>
                }
            >
                {kind === "video" ? (
                    <button
                        type="button"
                        className="ecv-hover-anchor ecv-video-preview-trigger"
                        aria-label={`放大播放参考视频：${name}`}
                        title="放大播放（含原声）"
                        onClick={() => {
                            setOpen(false);
                            setExpanded(true);
                        }}
                    >
                        {children}
                    </button>
                ) : (
                    <span className="ecv-hover-anchor">{children}</span>
                )}
            </Popover>
            {kind === "video" ? (
                <AppModal open={expanded} title="参考视频" width={800} centered footer={null} onCancel={() => setExpanded(false)}>
                    {expanded ? <HoverVideo src={src} startMs={startMs} endMs={endMs} controls /> : null}
                </AppModal>
            ) : null}
        </>
    );
}

function HoverVideo({ src, startMs = 0, endMs, controls = false }: { src: string; startMs?: number; endMs?: number; controls?: boolean }) {
    const ref = useRef<HTMLVideoElement>(null);
    const [failed, setFailed] = useState(false);
    if (failed) return <p role="alert">视频加载失败，请关闭预览后重试，或重新添加参考视频。</p>;
    return (
        <video
            ref={ref}
            src={src}
            muted={!controls}
            controls={controls}
            aria-label="参考视频播放"
            className={controls ? "ecv-reference-player" : undefined}
            autoPlay
            loop={!endMs}
            playsInline
            preload="metadata"
            onError={() => setFailed(true)}
            onLoadedMetadata={(event) => {
                if (startMs) event.currentTarget.currentTime = startMs / 1000;
            }}
            onTimeUpdate={(event) => {
                // 裁剪过的视频只循环播放选区。
                if (endMs && !event.currentTarget.paused && event.currentTarget.currentTime * 1000 >= endMs) {
                    event.currentTarget.currentTime = startMs / 1000;
                }
            }}
            onSeeking={(event) => {
                const video = event.currentTarget;
                if (video.currentTime * 1000 < startMs || (endMs && video.currentTime * 1000 > endMs)) video.currentTime = startMs / 1000;
            }}
            onEnded={(event) => {
                if (!endMs) return;
                const video = event.currentTarget;
                video.currentTime = startMs / 1000;
                void video.play().catch(() => {
                    // 浏览器拒绝自动播放时，保留控件供用户手动播放。
                });
            }}
        />
    );
}

/** 当前视频功能：本模块只提供「模特 / 商品替换」。 */
export function FeatureSwitcher(_props: { state: EcommerceVideoState }) {
    const current = FEATURES[0];
    return (
        <div className="ecv-feature">
            <div className="ecv-feature-card">
                <span className={`ecv-glyph-tile is-${current.id}`}>
                    <FeatureGlyph id={current.id} />
                </span>
                <span className="ecv-feature-copy">
                    <small>当前视频功能</small>
                    <strong>{current.title}</strong>
                    <span>{current.description}</span>
                </span>
            </div>
        </div>
    );
}

export function SlotTile({
    state,
    slot,
    onLibrary,
    required = false,
    note,
    renderLibrary,
}: {
    state: EcommerceVideoState;
    slot: MaterialSlot;
    onLibrary: (slot: MaterialSlot) => void;
    required?: boolean;
    note?: string;
    renderLibrary?: (button: ReactNode) => ReactNode;
}) {
    const input = useRef<HTMLInputElement>(null);
    const [dragging, setDragging] = useState(false);
    const items = state.slotImages(slot.key).filter((image) => image.category === slot.category);
    const blocked = Boolean(slot.requires && !state.slotImages(slot.requires).some((image) => image.category === slot.category));
    const full = items.length >= slot.max;
    const libraryButton = (
        <button type="button" className="ecv-library" disabled={blocked || (full && !renderLibrary)} onClick={() => onLibrary(slot)}>
            <FolderOpen size={13} /> 素材库
        </button>
    );
    const tile = (
        <button
            type="button"
            className={`ecv-slot-add${dragging ? " is-dragging" : ""}`}
            disabled={blocked || full}
            onClick={() => input.current?.click()}
            onDragEnter={(event) => {
                stop(event);
                if (!blocked && !full) setDragging(true);
            }}
            onDragOver={stop}
            onDragLeave={(event) => {
                stop(event);
                setDragging(false);
            }}
            onDrop={(event) => {
                stop(event);
                setDragging(false);
                if (!blocked && !full) void state.addImageFiles(slot, Array.from(event.dataTransfer.files));
            }}
        >
            <span className={`ecv-badge${required ? " is-required" : ""}`}>{required ? "必填" : "可选"}</span>
            <Plus size={20} />
            <strong>{slot.label}</strong>
        </button>
    );
    return (
        <div className="ecv-slot">
            <div className="ecv-slot-row">
                {items.map((image) => (
                    <figure key={image.id} className={`ecv-thumb${image.error ? " is-error" : ""}`} title={image.error || undefined}>
                        <MediaHoverPreview kind={slot.media === "audio" ? "audio" : "image"} src={slot.media === "audio" ? (image.resourceId ? resourceFileUrl(image.resourceId) : "") : image.previewUrl} name={image.name}>
                            {slot.media === "audio" ? (
                                <span className="ecv-thumb-audio">
                                    <Music2 size={18} />
                                    <small>{image.name}</small>
                                </span>
                            ) : (
                                <img src={image.previewUrl} alt={image.name} />
                            )}
                        </MediaHoverPreview>
                        {image.progress !== undefined ? <span className="ecv-thumb-progress">{Math.round(image.progress * 100)}%</span> : null}
                        {image.error ? <span className="ecv-thumb-progress">失败</span> : null}
                        <button type="button" aria-label={`移除 ${image.name}`} onClick={() => state.removeImage(image.id)}>
                            <X size={12} />
                        </button>
                    </figure>
                ))}
                {!full ? slot.tip && !blocked ? <UploadTip kind={slot.tip}>{tile}</UploadTip> : tile : null}
            </div>
            <input
                ref={input}
                hidden
                type="file"
                accept={slot.media === "audio" ? "audio/*,.mp3,.wav,.m4a" : "image/*"}
                multiple={slot.max > 1}
                onChange={(event) => {
                    void state.addImageFiles(slot, Array.from(event.target.files || []));
                    event.target.value = "";
                }}
            />
            <p className={`ecv-slot-note${blocked ? " is-blocked" : ""}`}>{blocked ? slot.requiresNote : slot.note}</p>
            {renderLibrary ? renderLibrary(libraryButton) : libraryButton}
            {note ? <p className="ecv-slot-note">{note}</p> : null}
        </div>
    );
}

function formatSeconds(ms: number) {
    if (!ms) return "";
    const seconds = Math.round(ms / 1000);
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

export function ReferenceVideo({ state, onLibrary, onTrim }: { state: EcommerceVideoState; onLibrary: () => void; onTrim?: () => void }) {
    const input = useRef<HTMLInputElement>(null);
    const [dragging, setDragging] = useState(false);
    const { video } = state;
    // 已有参考视频：左侧换成视频缩略卡（右上角 × 删除），右侧链接输入保持可用，解析成功后直接替换当前视频。
    const picked = video ? (
        <figure className="ecv-video-thumb">
            <MediaHoverPreview kind="video" src={video.previewUrl} name={video.name} startMs={state.trim?.startMs} endMs={state.trim?.endMs}>
                <video src={video.previewUrl} preload="metadata" muted playsInline />
            </MediaHoverPreview>
            <span className="ecv-badge is-filled">1/1 个</span>
            <button type="button" className="ecv-video-thumb-remove" aria-label="移除参考视频" title="移除后可重新上传" onClick={state.removeVideo}>
                <X size={13} />
            </button>
            {video.uploading ? null : (
                <span className="ecv-video-thumb-play" aria-hidden>
                    <Play size={14} fill="currentColor" />
                </span>
            )}
            {video.uploading ? (
                <span className="ecv-video-thumb-progress">上传中 {Math.round((video.progress || 0) * 100)}%</span>
            ) : onTrim ? (
                <button type="button" className={`ecv-video-thumb-trim${state.trim ? " is-trimmed" : ""}`} disabled={!video.resourceId} onClick={onTrim}>
                    <Scissors size={11} /> {state.trim ? `${formatClock(state.trim.startMs)}–${formatClock(state.trim.endMs)}` : "裁剪"}
                </button>
            ) : null}
            <figcaption title={video.name}>
                {video.sourceLabel ? `${video.sourceLabel} · ` : ""}
                {video.name}
                {!state.trim && video.durationMs ? ` · ${formatSeconds(video.durationMs)}` : ""}
            </figcaption>
        </figure>
    ) : null;
    const tile = (
        <button
            type="button"
            className={`ecv-video-add${dragging ? " is-dragging" : ""}`}
            onClick={() => input.current?.click()}
            onDragEnter={(event) => {
                stop(event);
                setDragging(true);
            }}
            onDragOver={stop}
            onDragLeave={(event) => {
                stop(event);
                setDragging(false);
            }}
            onDrop={(event) => {
                stop(event);
                setDragging(false);
                const file = event.dataTransfer.files[0];
                if (file) void state.addVideoFile(file);
            }}
        >
            <span className="ecv-badge">限 1 个</span>
            <Plus size={20} />
            <strong>本地上传</strong>
            <small>MP4 / MOV · 限 1 个</small>
        </button>
    );
    return (
        <div className="ecv-video-box">
            <div className="ecv-video-upload">
                {picked ?? <UploadTip kind="video">{tile}</UploadTip>}
                {picked ? null : (
                    <button type="button" className="ecv-library" onClick={onLibrary}>
                        <FolderOpen size={13} /> 素材库
                    </button>
                )}
            </div>
            <input
                ref={input}
                hidden
                type="file"
                accept="video/mp4,video/quicktime,.mp4,.mov"
                onChange={(event) => {
                    const file = event.target.files?.[0];
                    if (file) void state.addVideoFile(file);
                    event.target.value = "";
                }}
            />
            <span className="ecv-or">或</span>
            <div className="ecv-link">
                <Input.TextArea
                    variant="filled"
                    value={state.linkText}
                    autoSize={{ minRows: 3, maxRows: 4 }}
                    placeholder={video ? "粘贴新的短视频链接，解析后替换当前视频" : `粘贴${sharePlatforms()}等短视频链接`}
                    disabled={state.linkBusy}
                    onChange={(event) => state.setLinkText(event.target.value)}
                    onPaste={(event) => {
                        // 直接粘贴进输入框也自动解析（输入框原本为空时）。
                        const text = event.clipboardData.getData("text").trim();
                        if (text && !state.linkText.trim()) {
                            event.preventDefault();
                            void state.parseLink(text);
                        }
                    }}
                />
                {video && !state.linkBusy ? <small className="ecv-link-hint">已上传视频；粘贴新链接解析后会替换当前视频</small> : null}
                <div className="ecv-link-actions">
                    <Button size="small" icon={<ClipboardPaste size={13} />} disabled={state.linkBusy} onClick={() => void state.pasteLink()}>
                        一键粘贴
                    </Button>
                    <Button size="small" type="primary" className="ecv-parse" disabled={!state.linkText.trim()} loading={state.linkBusy} icon={<Link2 size={13} />} onClick={() => void state.parseLink()}>
                        {state.linkBusy ? "解析中" : "解析视频"}
                    </Button>
                </div>
                {state.linkBusy && state.linkProgress ? (
                    <div className="ecv-link-progress" role="status" aria-live="polite">
                        <div className="ecv-link-progress-head">
                            <span>{linkProgressText(state.linkProgress)}</span>
                            {state.linkProgress.platformLabel ? <span className="ecv-link-platform">{state.linkProgress.platformLabel}</span> : null}
                        </div>
                        {state.linkProgress.title ? <small className="ecv-link-title">{state.linkProgress.title}</small> : null}
                        <div className="ecv-link-progress-bar">
                            <span style={{ width: `${linkProgressPercent(state.linkProgress)}%` }} />
                        </div>
                    </div>
                ) : null}
                {!state.linkBusy && state.linkError ? (
                    <div className="ecv-link-error" role="alert">
                        <span>{state.linkError.message}</span>
                        {state.linkError.retryable ? (
                            <Button size="small" type="link" onClick={() => void state.parseLink()}>
                                重试
                            </Button>
                        ) : null}
                    </div>
                ) : null}
            </div>
        </div>
    );
}

export function EcommerceVideoComposer({ state }: { state: EcommerceVideoState }) {
    const { kindConfig } = state;
    const [compareOpen, setCompareOpen] = useState(true);
    const [lockOpen, setLockOpen] = useState(true);
    const [trimOpen, setTrimOpen] = useState(false);
    const [regionOpen, setRegionOpen] = useState(false);
    const rhythm = kindConfig.audio === "rhythm";
    const videoReady = Boolean(state.video && state.video.resourceId && !state.video.uploading);
    // 参考视频超过时长上限且还没裁剪：上传完成后直接打开裁剪弹窗。
    const overLimit = Boolean(state.video && state.video.durationMs > state.maxReferenceSeconds * 1000 + 50 && !state.trim);
    const overLimitKey = overLimit && videoReady ? state.video!.previewUrl : "";
    useEffect(() => {
        if (overLimitKey) setTrimOpen(true);
    }, [overLimitKey]);
    const [library, setLibrary] = useState<{ slot?: MaterialSlot; video?: boolean } | null>(null);
    const canvasOptions =
        kindConfig.canvas === "ratio"
            ? state.ratios.flatMap((ratio) => state.resolutions.map((resolution) => ({ value: `${ratio}|${resolution}`, label: `${ratio} · ${resolution}` })))
            : state.resolutions.map((resolution) => ({ value: resolution, label: resolution }));
    const canvasValue = kindConfig.canvas === "ratio" ? `${state.ratio}|${state.resolution}` : state.resolution;
    const notesCount = Array.from(state.notes).length;
    const productKind = kindConfig.targets.includes("product");
    const busy = state.submitting;
    const disabledReason = state.missing;
    return (
        <>
            <div className="ecs-composer-scroll ecv-scroll" data-canvas-wheel-scroll>
                <FeatureSwitcher state={state} />
                <Section title="替换类型" hint={<span className="ecv-picked">已选择：{kindConfig.label}</span>}>
                    <div className="ecv-kinds" role="group" aria-label="替换类型">
                        {REPLACE_KINDS.filter((item) => !state.disabledKinds.includes(item.id)).map((item) => (
                            <KindButton key={item.id} id={item.id} label={item.label} pressed={state.kind === item.id} onPick={() => state.setKind(item.id)} />
                        ))}
                    </div>
                </Section>
                <Section title="素材" hint={kindConfig.materialHint}>
                    <div className="ecv-slots">
                        {kindConfig.slots.map((slot) => (
                            <SlotTile key={`${kindConfig.id}-${slot.key}`} state={state} slot={slot} onLibrary={(value) => setLibrary({ slot: value })} />
                        ))}
                        {rhythm ? <SlotTile key={`${kindConfig.id}-voice`} state={state} slot={VOICE_SLOT} onLibrary={(value) => setLibrary({ slot: value })} /> : null}
                    </div>
                    {state.hasProductImage ? (
                        <label className="ecv-subject">
                            <span>商品名称 <small>选填</small></span>
                            <Input variant="filled" maxLength={60} placeholder="例如：海盐曲奇饼干" value={state.productName} onChange={(event) => state.setProductName(event.target.value)} />
                        </label>
                    ) : null}
                    {rhythm && state.uploadedVoices > 0 ? (
                        <p className={`ecv-slot-note${!state.voiceSupported ? " is-blocked" : ""}`}>
                            {!state.voiceSupported
                                ? "所选模型不支持参考音频，请换用 Seedance 2.0 等模型"
                                : state.adaptProductSpeech
                                  ? `用这段音色讲新商品，原有字幕同步改写（≤${state.maxVoiceSeconds} 秒）`
                                  : `成片人物用这段参考音色说原视频台词，原视频背景音乐保留（≤${state.maxVoiceSeconds} 秒）`}
                        </p>
                    ) : null}
                    {kindConfig.lock ? (
                        <LockCard
                            kind={kindConfig.lock}
                            open={lockOpen}
                            onToggle={() => setLockOpen((value) => !value)}
                            hasVideo={videoReady}
                            regions={state.regions}
                            videoSrc={state.video?.previewUrl || ""}
                            videoAspect={state.video?.width && state.video.height ? state.video.width / state.video.height : 9 / 16}
                            onSelect={() => setRegionOpen(true)}
                            onClear={() => state.setRegions(null)}
                        />
                    ) : null}
                    {kindConfig.subjectHint ? (
                        <label className="ecv-subject">
                            <span>
                                商品主体 <small>选填 · 10 字内</small>
                            </span>
                            <AutoComplete
                                value={state.subjectHint}
                                options={subjectExamples().map((value) => ({ value }))}
                                onChange={(value) => state.setSubjectHint(Array.from(value).slice(0, 10).join(""))}
                                filterOption={(input, option) => String(option?.value || "").includes(input)}
                            >
                                <Input variant="filled" maxLength={10} placeholder="例如：上衣、裤子、裙子" suffix={<ChevronDown size={14} />} />
                            </AutoComplete>
                        </label>
                    ) : null}
                    {kindConfig.compare ? <CompareCard kind={kindConfig.compare.kind} title={kindConfig.compare.title} open={compareOpen} onToggle={() => setCompareOpen((value) => !value)} /> : null}
                    {kindConfig.consent ? (
                        <label className="ecv-check">
                            <input type="checkbox" checked={state.faceConsent} onChange={(event) => state.setFaceConsent(event.target.checked)} />
                            <span>我确认已取得照片中人物的肖像授权</span>
                        </label>
                    ) : null}
                </Section>
                <Section
                    title="参考视频"
                    hint={`若视频镜头较多，部分镜头编辑效果不佳，建议分镜后单独编辑 · 4–${state.maxReferenceSeconds} 秒`}
                    extra={
                        <button type="button" className={`ecv-trim-button${overLimit ? " is-alert" : ""}`} disabled={!videoReady} title={videoReady ? undefined : "先上传参考视频"} onClick={() => setTrimOpen(true)}>
                            <Scissors size={13} /> {state.trim ? "重新裁剪" : "裁剪视频"}
                        </button>
                    }
                >
                    <ReferenceVideo state={state} onLibrary={() => setLibrary({ video: true })} onTrim={videoReady ? () => setTrimOpen(true) : undefined} />
                </Section>
                {rhythm ? (
                    <div className="ecv-rhythm">
                        <label className="ecv-check ecv-check-strong">
                            <input type="checkbox" checked={state.keepMusic} onChange={(event) => state.setKeepMusic(event.target.checked)} />
                            <span>
                                {state.adaptProductSpeech ? "保留参考视频里的音乐和节奏" : "保留参考视频里的音乐和节奏，适合舞蹈、卡点、BGM 视频。"}
                                <small>
                                    {state.adaptProductSpeech
                                        ? state.effectiveAudio === "mute"
                                            ? "所选模型不能生成声音，成片为静音。"
                                            : state.uploadedVoices > 0
                                              ? "用上传音色讲新商品，原有字幕同步改写。"
                                              : "沿用原视频音色讲新商品，原有字幕同步改写。"
                                        : state.effectiveAudio === "replace"
                                          ? "已上传参考音色：人物换成你的音色说原视频台词，背景音乐沿用参考视频，动作和镜头切换卡准原节拍。"
                                          : state.keepMusic
                                            ? "勾选：成片直接用参考视频的原音轨，人物动作和镜头切换卡准原音乐节拍。"
                                            : state.effectiveAudio === "generate"
                                              ? "不勾选：AI 按新画面重新生成声音和配乐，不带原视频的声音；上传参考音色可指定人声。"
                                              : "不勾选：所选模型不能生成声音，成片为静音。"}
                                </small>
                            </span>
                        </label>
                        {kindConfig.talking ? (
                            <label className="ecv-check ecv-check-strong">
                                <input type="checkbox" checked={state.talking} onChange={(event) => state.setTalking(event.target.checked)} />
                                <span>
                                    是否口播视频
                                    <small>{state.talking ? "人物面对镜头说话：AI 识别原台词，成片口型与台词同步。" : "不勾也会自动识别：参考视频里有人对镜头说话时，成片照样对口型；走秀、展示类等无人说话的视频不受影响。"}</small>
                                </span>
                            </label>
                        ) : null}
                    </div>
                ) : kindConfig.audio ? (
                    <Section title="声音">
                        <div className="ecv-audio" role="radiogroup" aria-label="声音">
                            {AUDIO_CHOICES.map((item) => (
                                <button
                                    key={item.value}
                                    type="button"
                                    role="radio"
                                    aria-checked={state.audio === item.value}
                                    disabled={item.value === "replace" && !state.canGenerateAudio && state.audio !== "replace"}
                                    title={item.value === "replace" && !state.canGenerateAudio ? "所选模型不能生成声音" : undefined}
                                    onClick={() => state.setAudio(item.value)}
                                >
                                    <strong>
                                        {item.value === "replace" ? <Sparkles size={13} /> : item.value === "keep" ? <Volume2 size={13} /> : <VolumeX size={13} />}
                                        {state.adaptProductSpeech && item.value === "keep" ? "沿用音色和音乐" : item.label}
                                    </strong>
                                    <small>{state.adaptProductSpeech && item.value !== "mute" ? (item.value === "keep" ? "用原视频音色讲新商品，原有字幕同步改写" : "用上传音色讲新商品，原有字幕同步改写") : item.hint}</small>
                                </button>
                            ))}
                        </div>
                        {state.audio === "replace" ? (
                            <div className="ecv-voice">
                                <div className="ecv-voice-head">
                                    <strong>参考音色</strong>
                                    <small>必填 · 仅 1 段 · 上传后台词用这个声音说</small>
                                </div>
                                <SlotTile state={state} slot={VOICE_SLOT} required onLibrary={(value) => setLibrary({ slot: value })} note="重新生成时，请上传 1 段参考音色" />
                                {!state.voiceSupported ? <p className="ecv-slot-note is-blocked">所选模型不支持参考音频，请换用 Seedance 2.0 等支持参考音频的模型</p> : null}
                            </div>
                        ) : null}
                    </Section>
                ) : null}
                <Section title="模型与参数">
                    <div className="ecv-params">
                        <div className="ecv-param">
                            <small>模型</small>
                            <ModelPicker
                                config={state.config}
                                value={state.selectedModel}
                                capability="video"
                                variant="creation"
                                fullWidth
                                className="creation-model-picker ecv-model-picker"
                                showSelectedPrice={false}
                                showOptionPrices
                                modelFilter={state.systemModel}
                                placeholder="暂无可用视频模型"
                                onChange={state.setModel}
                            />
                        </div>
                        <label className="ecv-param">
                            <small>{kindConfig.canvas === "ratio" ? "画布" : "清晰度"}</small>
                            <Select
                                variant="filled"
                                value={canvasOptions.some((item) => item.value === canvasValue) ? canvasValue : undefined}
                                placeholder="选择"
                                options={canvasOptions}
                                onChange={(value: string) => {
                                    if (kindConfig.canvas === "ratio") {
                                        const [ratio, resolution] = value.split("|");
                                        state.setRatio(ratio);
                                        state.setResolution(resolution);
                                    } else state.setResolution(value);
                                }}
                            />
                        </label>
                    </div>
                    {kindConfig.canvas === "resolution" && state.ratio ? <p className="ecv-hint">画面比例跟随参考视频：{state.ratio}</p> : null}
                    {state.selectedModel ? (
                        state.editMode ? (
                            <p className="ecv-mode-note is-edit">原片编辑：以参考视频为底片，场景、手部动作和运镜保持原样，只替换你的商品 / 人物</p>
                        ) : (
                            <p className="ecv-mode-note">当前模型不能读取参考视频，只能按文字描述重新生成画面，场景和动作会与原视频不同；想保留原视频画面只换商品，请换用 Seedance 2.0 等支持参考视频的模型</p>
                        )
                    ) : null}
                </Section>
                <Section title="补充说明" hint={productKind ? `选填 · 最多 ${kindConfig.notesLimit} 字；商品和原视频的结构不同时，写清楚部件位置和拆装方式` : `选填 · 最多 ${kindConfig.notesLimit} 字，用来补充商品 / 人物细节`}>
                    <Input.TextArea
                        variant="filled"
                        value={state.notes}
                        autoSize={{ minRows: kindConfig.notesLimit > 50 ? 2 : 1, maxRows: 6 }}
                        placeholder={productKind ? "例如：我的吹风机滤网在机身尾部，旋转拧下；出风口是格栅，没有中空圆环" : `补充说明（选填，最多 ${kindConfig.notesLimit} 字）`}
                        status={notesCount > kindConfig.notesLimit ? "error" : undefined}
                        onChange={(event) => state.setNotes(event.target.value)}
                    />
                    <span className={`ecv-count${notesCount > kindConfig.notesLimit ? " is-over" : ""}`}>
                        {notesCount}/{kindConfig.notesLimit}
                    </span>
                </Section>
            </div>
            <footer className="ecs-footer">
                <div className="ecs-estimate" aria-live="polite">
                    {state.estimate.credits === null ? (
                        <strong className="is-pending">{state.estimate.label || (state.estimatedVideoTasks > 0 && state.creditsEnabled ? "正在估算积分" : "")}</strong>
                    ) : (
                        <strong>
                            <CreditStar />
                            <RollingCreditAmount value={state.estimate.credits} maximumFractionDigits={3} />
                            <span>预计积分</span>
                        </strong>
                    )}
                    <span>{disabledReason || (state.estimatedVideoTasks ? `预计生成 ${state.estimatedVideoTasks} 次` : "成片时长跟随参考视频")}</span>
                </div>
                <Button
                    type="primary"
                    className="ecs-submit ecv-submit"
                    disabled={Boolean(disabledReason) || busy}
                    aria-busy={busy}
                    icon={busy ? <LoaderCircle className="commerce-planning-spin" size={15} /> : <ArrowUp size={15} />}
                    onClick={() => void state.generate()}
                >
                    开始生成
                </Button>
            </footer>
            {state.video ? (
                <TrimVideoModal
                    open={trimOpen}
                    src={state.video.previewUrl}
                    value={state.trim}
                    maxSeconds={state.maxReferenceSeconds}
                    onMeta={state.setVideoMeta}
                    onCancel={() => setTrimOpen(false)}
                    onApply={(trim) => {
                        state.setTrim(trim);
                        // 裁剪区间变了，框选画面不在区间里时清空重框。
                        if (state.regions && trim && (state.regions.frameMs < trim.startMs || state.regions.frameMs > trim.endMs)) state.setRegions(null);
                        setTrimOpen(false);
                    }}
                />
            ) : null}
            {state.video && kindConfig.lock ? (
                <RegionSelectModal
                    open={regionOpen}
                    kind={kindConfig.lock}
                    src={state.video.previewUrl}
                    trim={state.trim}
                    value={state.regions}
                    onMeta={state.setVideoMeta}
                    onCancel={() => setRegionOpen(false)}
                    onApply={(regions) => {
                        state.setRegions(regions);
                        setRegionOpen(false);
                    }}
                />
            ) : null}
            {library ? (
                <WorkbenchAssetPicker
                    open
                    accept={library.video ? ["video"] : library.slot?.media === "audio" ? ["audio"] : ["image"]}
                    maxCount={library.video ? 1 : Math.max(1, (library.slot?.max || 1) - state.slotImages(library.slot?.key || "").length)}
                    slotName={library.video ? "参考视频" : library.slot?.label}
                    onClose={() => setLibrary(null)}
                    onPick={(picked) => {
                        if (library.video) {
                            if (picked[0]) state.pickVideo(picked[0]);
                        } else if (library.slot) state.addPickedImages(library.slot, picked);
                    }}
                />
            ) : null}
        </>
    );
}
