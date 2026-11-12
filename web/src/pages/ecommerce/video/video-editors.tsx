import { useTranslation } from "react-i18next";
import { Button } from "antd";
import { Check, ChevronDown, Crosshair, Pause, Play, RotateCcw, ScanSearch, Trash2, UserRoundSearch } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";

import { AppModal } from "@/components/ui/product/app-modal";
import type { VideoReplicationRegion } from "@/services/api/video-replication";

import type { VideoRegions, VideoTrim } from "./use-ecommerce-video";
import type { LockKind } from "./video-kinds";
import "./video-editors.css";

/*
 * 商品视频的两个编辑弹窗与框选说明卡：
 * - 裁剪视频：双滑块选起止时间，选区循环预览；后端分析时按这个区间截取参考视频。
 * - 框选目标：在参考视频某一帧上拖出方框（框几件换几件 / 只换框住的人），后端识别框里的目标后写进提示词。
 */

const MIN_CLIP_MS = 4000;

export function formatClock(ms: number) {
    const total = Math.max(0, ms) / 1000;
    const minutes = Math.floor(total / 60);
    const seconds = total - minutes * 60;
    return `${String(minutes).padStart(2, "0")}:${seconds.toFixed(1).padStart(4, "0")}`;
}

type Meta = { durationMs: number; width: number; height: number };

function useVideoMeta(onMeta?: (meta: Meta) => void) {
    const [meta, setMeta] = useState<Meta>({ durationMs: 0, width: 0, height: 0 });
    const onLoaded = useCallback(
        (video: HTMLVideoElement) => {
            const next = { durationMs: Number.isFinite(video.duration) ? Math.round(video.duration * 1000) : 0, width: video.videoWidth, height: video.videoHeight };
            setMeta(next);
            onMeta?.(next);
        },
        [onMeta],
    );
    return [meta, onLoaded] as const;
}

/* ---------------- 裁剪视频 ---------------- */

export function TrimVideoModal({
    open,
    src,
    value,
    maxSeconds,
    onMeta,
    onCancel,
    onApply,
}: {
    open: boolean;
    src: string;
    value: VideoTrim | null;
    maxSeconds: number;
    onMeta?: (meta: Meta) => void;
    onCancel: () => void;
    onApply: (trim: VideoTrim | null) => void;
}) {
    const videoRef = useRef<HTMLVideoElement>(null);
    const trackRef = useRef<HTMLDivElement>(null);
    const [meta, onLoaded] = useVideoMeta(onMeta);
    const [range, setRange] = useState<VideoTrim>({ startMs: 0, endMs: 0 });
    const [current, setCurrent] = useState(0);
    const [playing, setPlaying] = useState(false);
    const dragging = useRef<"start" | "end" | null>(null);
    const duration = meta.durationMs;
    const maxMs = maxSeconds * 1000;

    // 读到时长后初始化选区：沿用已有裁剪；没有时从头选到上限。
    useEffect(() => {
        if (!duration) return;
        if (value && value.endMs > value.startMs) setRange({ startMs: Math.min(value.startMs, duration), endMs: Math.min(value.endMs, duration) });
        else setRange({ startMs: 0, endMs: Math.min(duration, maxMs) });
    }, [duration, maxMs, value]);

    useEffect(() => {
        if (!open) {
            setPlaying(false);
            videoRef.current?.pause();
        }
    }, [open]);

    const seek = (ms: number) => {
        const video = videoRef.current;
        if (video) video.currentTime = ms / 1000;
        setCurrent(ms);
    };

    const fromPointer = (clientX: number) => {
        const rect = trackRef.current?.getBoundingClientRect();
        if (!rect || !duration) return 0;
        return Math.max(0, Math.min(duration, ((clientX - rect.left) / rect.width) * duration));
    };

    const moveHandle = (handle: "start" | "end", ms: number) => {
        setRange((prev) => {
            if (handle === "start") {
                let start = Math.min(ms, prev.endMs - MIN_CLIP_MS);
                start = Math.max(0, Math.max(start, prev.endMs - maxMs));
                seek(start);
                return { ...prev, startMs: start };
            }
            let end = Math.max(ms, prev.startMs + MIN_CLIP_MS);
            end = Math.min(duration, Math.min(end, prev.startMs + maxMs));
            seek(end);
            return { ...prev, endMs: end };
        });
    };

    const onPointerDown = (handle: "start" | "end") => (event: ReactPointerEvent) => {
        event.preventDefault();
        dragging.current = handle;
        (event.target as HTMLElement).setPointerCapture(event.pointerId);
        videoRef.current?.pause();
        setPlaying(false);
    };
    const onPointerMove = (event: ReactPointerEvent) => {
        if (dragging.current) moveHandle(dragging.current, fromPointer(event.clientX));
    };
    const onPointerUp = () => {
        dragging.current = null;
    };
    const onKey = (handle: "start" | "end") => (event: React.KeyboardEvent) => {
        const step = event.shiftKey ? 1000 : 100;
        if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
            event.preventDefault();
            moveHandle(handle, (handle === "start" ? range.startMs : range.endMs) + (event.key === "ArrowLeft" ? -step : step));
        }
    };

    const togglePlay = () => {
        const video = videoRef.current;
        if (!video) return;
        if (video.paused) {
            if (video.currentTime * 1000 < range.startMs || video.currentTime * 1000 >= range.endMs - 50) video.currentTime = range.startMs / 1000;
            void video.play();
            setPlaying(true);
        } else {
            video.pause();
            setPlaying(false);
        }
    };

    const length = range.endMs - range.startMs;
    const pct = (ms: number) => (duration ? `${(ms / duration) * 100}%` : "0%");
    const whole = duration > 0 && range.startMs <= 50 && range.endMs >= duration - 50;

    return (
        <AppModal open={open} onCancel={onCancel} footer={null} centered width="min(680px, calc(100vw - 32px))" className="ecv-editor-modal" title={null}>
            <div className="ecv-editor">
                <header className="ecv-editor-head">
                    <small>TRIM REFERENCE VIDEO</small>
                    <strong>裁剪视频</strong>
                </header>
                <div className="ecv-trim-stage">
                    <video
                        ref={videoRef}
                        src={src}
                        playsInline
                        preload="auto"
                        onLoadedMetadata={(event) => onLoaded(event.currentTarget)}
                        onTimeUpdate={(event) => {
                            const ms = event.currentTarget.currentTime * 1000;
                            setCurrent(ms);
                            // 选区循环播放。
                            if (!event.currentTarget.paused && ms >= range.endMs) event.currentTarget.currentTime = range.startMs / 1000;
                        }}
                        onClick={togglePlay}
                    />
                    <button type="button" className="ecv-trim-play" aria-label={playing ? "暂停" : "播放选区"} onClick={togglePlay}>
                        {playing ? <Pause size={16} /> : <Play size={16} />}
                    </button>
                </div>
                <div className="ecv-trim-track" ref={trackRef} onPointerMove={onPointerMove} onPointerUp={onPointerUp} onPointerCancel={onPointerUp}>
                    <span className="ecv-trim-rail" />
                    <span className="ecv-trim-range" style={{ left: pct(range.startMs), width: duration ? `${(length / duration) * 100}%` : "0%" }} />
                    <span className="ecv-trim-cursor" style={{ left: pct(current) }} aria-hidden="true" />
                    {(["start", "end"] as const).map((handle) => (
                        <span
                            key={handle}
                            role="slider"
                            tabIndex={0}
                            aria-label={handle === "start" ? "起点" : "终点"}
                            aria-valuemin={0}
                            aria-valuemax={duration}
                            aria-valuenow={handle === "start" ? range.startMs : range.endMs}
                            aria-valuetext={formatClock(handle === "start" ? range.startMs : range.endMs)}
                            className="ecv-trim-handle"
                            style={{ left: pct(handle === "start" ? range.startMs : range.endMs) }}
                            onPointerDown={onPointerDown(handle)}
                            onKeyDown={onKey(handle)}
                        />
                    ))}
                </div>
                <div className="ecv-trim-times">
                    <span>
                        起点 <b>{formatClock(range.startMs)}</b>
                    </span>
                    <span>
                        终点 <b>{formatClock(range.endMs)}</b>
                    </span>
                    <span>
                        选区 <b>{formatClock(length)}</b>
                    </span>
                </div>
                <p className="ecv-editor-note">
                    选区 4–{maxSeconds} 秒，成片时长跟随选区。{duration > maxMs ? `原视频 ${formatClock(duration)}，超出上限的部分需要裁掉。` : ""}
                </p>
                <footer className="ecv-editor-foot">
                    {value ? (
                        <Button type="text" icon={<RotateCcw size={13} />} onClick={() => onApply(null)} disabled={duration > maxMs + 50}>
                            恢复完整视频
                        </Button>
                    ) : (
                        <span />
                    )}
                    <div>
                        <Button onClick={onCancel}>取消</Button>
                        <Button type="primary" className="ecv-editor-primary" disabled={!duration || length < MIN_CLIP_MS - 50} onClick={() => onApply(whole ? null : { startMs: Math.round(range.startMs), endMs: Math.round(range.endMs) })}>
                            剪辑并使用
                        </Button>
                    </div>
                </footer>
            </div>
        </AppModal>
    );
}

/* ---------------- 框选目标 ---------------- */

const MAX_BOXES: Record<LockKind, number> = { product: 6, person: 2 };

export function RegionSelectModal({
    open,
    kind,
    src,
    trim,
    value,
    onMeta,
    onCancel,
    onApply,
}: {
    open: boolean;
    kind: LockKind;
    src: string;
    trim: VideoTrim | null;
    value: VideoRegions | null;
    onMeta?: (meta: Meta) => void;
    onCancel: () => void;
    onApply: (regions: VideoRegions | null) => void;
}) {
    const videoRef = useRef<HTMLVideoElement>(null);
    const stageRef = useRef<HTMLDivElement>(null);
    const [meta, onLoaded] = useVideoMeta(onMeta);
    const [frameMs, setFrameMs] = useState(0);
    const [boxes, setBoxes] = useState<VideoReplicationRegion[]>([]);
    const [draft, setDraft] = useState<VideoReplicationRegion | null>(null);
    const origin = useRef<{ x: number; y: number } | null>(null);
    const startMs = trim?.startMs || 0;
    const endMs = trim?.endMs || meta.durationMs;
    const limit = MAX_BOXES[kind];

    useEffect(() => {
        if (!open) return;
        setBoxes(value?.boxes || []);
        setDraft(null);
    }, [open, value]);

    useEffect(() => {
        if (!open || !meta.durationMs) return;
        const initial = value?.boxes.length ? value.frameMs : startMs + Math.min(1000, (endMs - startMs) / 3);
        setFrameMs(initial);
        if (videoRef.current) videoRef.current.currentTime = initial / 1000;
    }, [endMs, meta.durationMs, open, startMs, value]);

    const point = (event: ReactPointerEvent) => {
        const rect = stageRef.current!.getBoundingClientRect();
        return { x: Math.max(0, Math.min(1, (event.clientX - rect.left) / rect.width)), y: Math.max(0, Math.min(1, (event.clientY - rect.top) / rect.height)) };
    };
    const onDown = (event: ReactPointerEvent) => {
        if (boxes.length >= limit || (event.target as HTMLElement).closest("button")) return;
        event.preventDefault();
        stageRef.current?.setPointerCapture(event.pointerId);
        origin.current = point(event);
        setDraft({ ...origin.current, w: 0, h: 0 });
    };
    const onMove = (event: ReactPointerEvent) => {
        if (!origin.current) return;
        const p = point(event);
        const o = origin.current;
        setDraft({ x: Math.min(o.x, p.x), y: Math.min(o.y, p.y), w: Math.abs(p.x - o.x), h: Math.abs(p.y - o.y) });
    };
    const onUp = () => {
        if (draft && draft.w >= 0.03 && draft.h >= 0.03) setBoxes((current) => [...current, round(draft)]);
        origin.current = null;
        setDraft(null);
    };

    const aspect = meta.width && meta.height ? meta.width / meta.height : 9 / 16;
    const label = kind === "product" ? "商品" : "人物";

    return (
        <AppModal open={open} onCancel={onCancel} footer={null} centered width="min(720px, calc(100vw - 32px))" className="ecv-editor-modal" title={null}>
            <div className="ecv-editor">
                <header className="ecv-editor-head">
                    <small>{kind === "product" ? "LOCK PRODUCTS" : "LOCK PERSON"}</small>
                    <strong>{kind === "product" ? "框住要换的商品" : "框住要换的人物"}</strong>
                </header>
                <p className="ecv-editor-note">
                    {kind === "product" ? `拖动鼠标框住要替换的商品，框几件换几件（最多 ${limit} 件），没框的保持不变。` : `框住要换的那个人（最多 ${limit} 人），旁边的人保持不动。`}
                    选一帧目标清楚、没被遮挡的画面。
                </p>
                <div className="ecv-region-stage-wrap">
                    <div
                        ref={stageRef}
                        className={`ecv-region-stage${boxes.length >= limit ? " is-full" : ""}`}
                        style={{ aspectRatio: String(aspect), width: `min(100%, calc(min(52vh, 420px) * ${aspect.toFixed(4)}))` }}
                        onPointerDown={onDown}
                        onPointerMove={onMove}
                        onPointerUp={onUp}
                        onPointerCancel={onUp}
                    >
                        <video ref={videoRef} src={src} muted playsInline preload="auto" onLoadedMetadata={(event) => onLoaded(event.currentTarget)} />
                        {boxes.map((box, index) => (
                            <span key={index} className="ecv-region-box" style={boxStyle(box)}>
                                <em>
                                    {label}
                                    {index + 1}
                                </em>
                                <button type="button" aria-label={`删除框 ${index + 1}`} onClick={() => setBoxes((current) => current.filter((_, i) => i !== index))}>
                                    <Trash2 size={11} />
                                </button>
                            </span>
                        ))}
                        {draft ? <span className="ecv-region-box is-draft" style={boxStyle(draft)} /> : null}
                    </div>
                </div>
                <label className="ecv-region-scrub">
                    <span>画面 {formatClock(frameMs - startMs)}</span>
                    <input
                        type="range"
                        min={startMs}
                        max={Math.max(startMs, endMs - 100)}
                        step={100}
                        value={frameMs}
                        disabled={!meta.durationMs}
                        onChange={(event) => {
                            const ms = Number(event.target.value);
                            setFrameMs(ms);
                            if (videoRef.current) videoRef.current.currentTime = ms / 1000;
                            // 换了画面，之前框的位置不再对应，清空重框。
                            if (boxes.length) setBoxes([]);
                        }}
                    />
                </label>
                <footer className="ecv-editor-foot">
                    <span className="ecv-region-count">
                        已框 <b>{boxes.length}</b> / {limit}
                    </span>
                    <div>
                        {value?.boxes.length ? (
                            <Button type="text" onClick={() => onApply(null)}>
                                不框选
                            </Button>
                        ) : null}
                        <Button onClick={onCancel}>取消</Button>
                        <Button type="primary" className="ecv-editor-primary" disabled={!boxes.length} icon={<Check size={14} />} onClick={() => onApply({ frameMs: Math.round(frameMs), boxes })}>
                            确认框选
                        </Button>
                    </div>
                </footer>
            </div>
        </AppModal>
    );
}

function round(box: VideoReplicationRegion): VideoReplicationRegion {
    const r = (value: number) => Math.round(value * 1000) / 1000;
    return { x: r(box.x), y: r(box.y), w: r(box.w), h: r(box.h) };
}

function boxStyle(box: VideoReplicationRegion) {
    return { left: `${box.x * 100}%`, top: `${box.y * 100}%`, width: `${box.w * 100}%`, height: `${box.h * 100}%` };
}

/** 面板里框选结果的缩略预览：定格在框选那一帧，叠加方框。 */
function RegionPreview({ src, regions, aspect }: { src: string; regions: VideoRegions; aspect: number }) {
    return (
        <span className="ecv-region-preview" style={{ aspectRatio: String(aspect || 9 / 16) }}>
            <video src={`${src}#t=${(regions.frameMs / 1000).toFixed(2)}`} muted playsInline preload="metadata" />
            {regions.boxes.map((box, index) => (
                <span key={index} className="ecv-region-box is-mini" style={boxStyle(box)} />
            ))}
        </span>
    );
}

/* ---------------- 框选说明卡（带动画示意） ---------------- */

const SKIN = "#f2c6a8";

function Bottle({ x, y, color, cap = "#1f2937", label = true }: { x: number; y: number; color: string; cap?: string; label?: boolean }) {
    useTranslation();
    return (
        <g transform={`translate(${x} ${y})`}>
            <rect x={-2.2} y={-4} width={4.4} height={4} rx={0.8} fill={cap} />
            <path d="M-1.4 -9 h2.8 v5 h-2.8 z" fill={cap} />
            <path d="M3.2 -8 h2.4 v1.4 h-2.4 z" fill={cap} />
            <rect x={-6} y={0} width={12} height={26} rx={3.2} fill={color} stroke="rgba(0,0,0,.12)" strokeWidth={0.4} />
            {label ? <rect x={-3.6} y={8} width={7.2} height={7} rx={0.8} fill="rgba(255,255,255,.55)" /> : null}
            <rect x={-4.6} y={2} width={1.4} height={20} rx={0.7} fill="rgba(255,255,255,.25)" />
        </g>
    );
}

function Hand() {
    return <path d="M14 74 Q18 58 26 56 L44 56 Q52 58 54 74 Z" fill={SKIN} stroke="rgba(0,0,0,.08)" strokeWidth={0.5} />;
}

function ProductScene({ step }: { step: "mine" | "box" | "result" }) {
    useTranslation();
    if (step === "mine") {
        return (
            <svg viewBox="0 0 60 80" aria-hidden="true">
                <rect width={60} height={80} fill="#f4f4f5" />
                <g transform="translate(30 26) scale(1.55)">
                    <Bottle x={0} y={0} color="#18181b" cap="#27272a" />
                </g>
            </svg>
        );
    }
    const original = step === "box";
    return (
        <svg viewBox="0 0 60 80" aria-hidden="true">
            <rect width={60} height={80} fill={original ? "#dfe7ec" : "#e7e1da"} />
            <rect x={0} y={48} width={60} height={32} fill={original ? "#cdd6dc" : "#d6ccc1"} />
            <g className={original ? undefined : "ecv-anim-swap"}>
                <Bottle x={22} y={26} color={original ? "#f6c9c4" : "#18181b"} cap={original ? "#e9a8a0" : "#27272a"} />
                <Bottle x={38} y={26} color={original ? "#f6c9c4" : "#18181b"} cap={original ? "#e9a8a0" : "#27272a"} />
            </g>
            <Hand />
            {original ? (
                <g className="ecv-anim-boxes">
                    <rect className="ecv-anim-box" x={14.5} y={15} width={15} height={39} rx={1} pathLength={100} />
                    <rect className="ecv-anim-box is-second" x={30.5} y={15} width={15} height={39} rx={1} pathLength={100} />
                    <g className="ecv-anim-badge">
                        <rect x={30} y={6} width={26} height={8} rx={4} fill="#ea580c" />
                        <text x={43} y={11.6} textAnchor="middle" fontSize={4.6} fontWeight={700} fill="#fff">
                            2 件都换
                        </text>
                    </g>
                </g>
            ) : null}
        </svg>
    );
}

function Figure({ x, y, top, bottom, hair = "#2b1d18", long = true }: { x: number; y: number; top: string; bottom: string; hair?: string; long?: boolean }) {
    useTranslation();
    return (
        <g transform={`translate(${x} ${y})`}>
            {long ? <path d="M-5 -4 Q-6 6 -4 10 L4 10 Q6 6 5 -4 Z" fill={hair} /> : null}
            <ellipse cx={0} cy={-4} rx={3.6} ry={4.2} fill={SKIN} />
            <path d="M-3.9 -4.5 Q-4 -9 0 -9 Q4 -9 3.9 -4.5 Q2 -7 0 -7 Q-2 -7 -3.9 -4.5 Z" fill={hair} />
            <path d="M-6.5 14 Q-7 2 0 1 Q7 2 6.5 14 L6 22 L-6 22 Z" fill={top} stroke="rgba(0,0,0,.1)" strokeWidth={0.4} />
            <rect x={-5.5} y={21} width={5} height={22} rx={1.6} fill={bottom} />
            <rect x={0.5} y={21} width={5} height={22} rx={1.6} fill={bottom} />
            <rect x={-6} y={42} width={5.5} height={2.4} rx={1} fill="#111827" />
            <rect x={0.5} y={42} width={5.5} height={2.4} rx={1} fill="#111827" />
        </g>
    );
}

function PersonScene({ step }: { step: "mine" | "box" | "result" }) {
    useTranslation();
    if (step === "mine") {
        const looks: Array<[string, string]> = [
            ["#1f2937", "#334155"],
            ["#e5e7eb", "#1e3a8a"],
            ["#7c2d12", "#111827"],
            ["#166534", "#1f2937"],
        ];
        return (
            <svg viewBox="0 0 60 80" aria-hidden="true">
                <rect width={60} height={80} fill="#f4f4f5" />
                {looks.map(([top, bottom], index) => (
                    <g key={index} transform={`translate(${index % 2 ? 44 : 16} ${index < 2 ? 12 : 50}) scale(0.58)`}>
                        <Figure x={0} y={0} top={top} bottom={bottom} />
                    </g>
                ))}
            </svg>
        );
    }
    const original = step === "box";
    return (
        <svg viewBox="0 0 60 80" aria-hidden="true">
            <rect width={60} height={80} fill="#cbd5e1" />
            <rect x={0} y={0} width={24} height={62} fill="#94a3b8" />
            <rect x={36} y={0} width={24} height={62} fill="#2563eb" opacity={0.75} />
            <rect x={0} y={62} width={60} height={18} fill="#9ca3af" />
            <Figure x={20} y={24} top="#a16207" bottom="#374151" />
            <g className={original ? undefined : "ecv-anim-swap"}>
                <Figure x={40} y={24} top={original ? "#f9a8d4" : "#1e3a8a"} bottom={original ? "#e5e7eb" : "#1f2937"} long={!original} />
            </g>
            {original ? (
                <g className="ecv-anim-boxes">
                    <rect className="ecv-anim-box" x={32} y={13} width={16} height={58} rx={1} pathLength={100} />
                </g>
            ) : null}
        </svg>
    );
}

export function LockCard({
    kind,
    open,
    onToggle,
    hasVideo,
    regions,
    videoSrc,
    videoAspect,
    onSelect,
    onClear,
}: {
    kind: LockKind;
    open: boolean;
    onToggle: () => void;
    hasVideo: boolean;
    regions: VideoRegions | null;
    videoSrc: string;
    videoAspect: number;
    onSelect: () => void;
    onClear: () => void;
}) {
    const product = kind === "product";
    const Scene = product ? ProductScene : PersonScene;
    const picked = Boolean(regions?.boxes.length);
    return (
        <div className={`ecv-lock is-${kind}`} data-open={open}>
            <button type="button" className="ecv-lock-head" aria-expanded={open} onClick={onToggle}>
                <span className="ecv-lock-icon">{product ? <ScanSearch size={14} /> : <UserRoundSearch size={14} />}</span>
                <strong>{product ? "框住商品，精准锁定" : "框住人物，不换错人"}</strong>
                {picked ? <span className="ecv-lock-done">已框 {regions!.boxes.length}</span> : null}
                <ChevronDown size={14} className="ecv-lock-chevron" />
            </button>
            {open ? (
                <div className="ecv-lock-body">
                    <p>
                        <span className="ecv-lock-chip">
                            <Check size={11} /> 框住要换的
                        </span>
                        {product ? "框几件换几件" : "旁边那个不动"}
                    </p>
                    <div className="ecv-lock-tiles">
                        <span className="ecv-lock-tile">
                            <small>你的图</small>
                            <Scene step="mine" />
                        </span>
                        <i>+</i>
                        <span className="ecv-lock-tile">
                            <small>{product ? "框住它" : "框住他"}</small>
                            <Scene step="box" />
                        </span>
                        <i>=</i>
                        <span className="ecv-lock-tile">
                            <small>结果</small>
                            <Scene step="result" />
                        </span>
                    </div>
                </div>
            ) : null}
            <div className="ecv-lock-action">
                {picked && hasVideo ? <RegionPreview src={videoSrc} regions={regions!} aspect={videoAspect} /> : null}
                <span>{!hasVideo ? "先上传参考视频，再框选要替换的目标" : picked ? `已框选 ${regions!.boxes.length} 个${product ? "商品" : "人物"}，只替换框住的` : "可选：不框选时 AI 自动识别要替换的目标"}</span>
                <Button size="small" icon={<Crosshair size={13} />} disabled={!hasVideo} onClick={onSelect}>
                    {picked ? "重新框选" : "去框选"}
                </Button>
                {picked ? (
                    <Button size="small" type="text" onClick={onClear}>
                        清除
                    </Button>
                ) : null}
            </div>
        </div>
    );
}
