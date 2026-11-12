import { useTranslation } from "react-i18next";
import { CheckCircle2, XCircle } from "lucide-react";
import type { ReactNode } from "react";

import type { CompareKind, TipKind } from "./video-kinds";

/*
 * 上传指引的示意插画：全部是内联 SVG，跟随明暗主题，不依赖外部图片。
 * 每张图 120×80，画的是「推荐 / 不推荐」的拍法示意，而不是真人照片。
 */

const SKIN = "#f2c6a8";
const HAIR = "#3b2a25";

type PersonProps = { x: number; y: number; s?: number; shirt?: string; pants?: string; hair?: string; side?: boolean; full?: boolean; glasses?: boolean };

function Person({ x, y, s = 1, shirt = "#f5f5f4", pants = "#7aa2d6", hair = HAIR, side = false, full = false, glasses = false }: PersonProps) {
    useTranslation();
    return (
        <g transform={`translate(${x} ${y}) scale(${s})`}>
            {full ? (
                <>
                    <rect x={-7} y={30} width={6} height={26} rx={3} fill={pants} />
                    <rect x={1} y={30} width={6} height={26} rx={3} fill={pants} />
                </>
            ) : null}
            <path d="M-15 34 Q-15 14 0 13 Q15 14 15 34 Z" fill={shirt} stroke="rgba(0,0,0,.08)" />
            <rect x={-3} y={6} width={6} height={8} fill={SKIN} />
            {side ? (
                <>
                    <ellipse cx={1} cy={0} rx={8} ry={9.5} fill={SKIN} />
                    <path d="M-8 1 Q-9 -12 2 -11 Q9 -10 8 -4 Q2 -7 -2 -3 L-3 9 Q-9 8 -8 1 Z" fill={hair} />
                    <path d="M8.5 1 l2 2 l-2 1" fill={SKIN} />
                </>
            ) : (
                <>
                    <ellipse cx={0} cy={0} rx={8.5} ry={9.5} fill={SKIN} />
                    <path d="M-9 4 Q-11 -12 0 -11 Q11 -12 9 4 Q8 -4 0 -5 Q-8 -4 -9 4 Z" fill={hair} />
                    <circle cx={-3} cy={1} r={0.9} fill="#3b2a25" />
                    <circle cx={3} cy={1} r={0.9} fill="#3b2a25" />
                    <path d="M-2 5 Q0 6.4 2 5" stroke="#c2766a" strokeWidth={0.9} fill="none" strokeLinecap="round" />
                    {glasses ? <path d="M-7 -0.5 h5 v3 h-5 z M2 -0.5 h5 v3 h-5 z M-2 0.6 h4" fill="#1f2937" stroke="#1f2937" strokeWidth={0.6} /> : null}
                </>
            )}
        </g>
    );
}

function Frame({ children, bg = "var(--ecv-illus-bg)", blur = false }: { children: ReactNode; bg?: string; blur?: boolean }) {
    const id = "ecv-blur";
    return (
        <svg viewBox="0 0 120 80" className="ecv-illus" aria-hidden="true">
            <defs>
                <filter id={id}>
                    <feGaussianBlur stdDeviation="2.4" />
                </filter>
            </defs>
            <rect width={120} height={80} rx={6} fill={bg} />
            <g filter={blur ? `url(#${id})` : undefined}>{children}</g>
        </svg>
    );
}

function Clutter() {
    return (
        <>
            <rect x={4} y={10} width={26} height={44} rx={2} fill="#c9b79c" />
            <rect x={6} y={14} width={22} height={6} fill="#8d7b64" />
            <rect x={6} y={24} width={22} height={6} fill="#a3876a" />
            <rect x={88} y={8} width={28} height={40} rx={2} fill="#9fb4c7" />
            <circle cx={100} cy={60} r={8} fill="#7c9a6d" />
            <rect x={34} y={54} width={18} height={14} fill="#d6a76a" />
            <Person x={102} y={40} s={0.7} shirt="#334155" />
        </>
    );
}

function Box({ x, y, color, s = 1 }: { x: number; y: number; color: string; s?: number }) {
    useTranslation();
    return (
        <g transform={`translate(${x} ${y}) scale(${s})`}>
            <path d="M-14 -6 L0 -13 L14 -6 L14 12 L0 19 L-14 12 Z" fill={color} stroke="rgba(0,0,0,.15)" />
            <path d="M-14 -6 L0 1 L14 -6 M0 1 L0 19" stroke="rgba(0,0,0,.18)" fill="none" />
            <path d="M-7 -9.5 L7 -2.5 L7 3" stroke="rgba(255,255,255,.55)" strokeWidth={2.4} fill="none" />
        </g>
    );
}

function Landscape({ people = false }: { people?: boolean }) {
    return (
        <>
            <rect width={120} height={50} fill="#bfdbfe" />
            <circle cx={94} cy={16} r={7} fill="#fde68a" />
            <path d="M0 50 L24 26 L44 44 L64 22 L92 50 Z" fill="#86a789" />
            <rect y={50} width={120} height={30} fill="#e7d7b8" />
            <rect x={14} y={54} width={36} height={16} rx={2} fill="#f8fafc" stroke="rgba(0,0,0,.1)" />
            {people ? (
                <>
                    <Person x={70} y={44} s={0.8} shirt="#f97316" full />
                    <Person x={92} y={46} s={0.7} shirt="#0ea5e9" full />
                </>
            ) : null}
        </>
    );
}

function PlayBadge() {
    return (
        <g>
            <circle cx={60} cy={40} r={10} fill="rgba(0,0,0,.45)" />
            <path d="M57 34 L66 40 L57 46 Z" fill="#fff" />
        </g>
    );
}

function Wave({
    x = 6,
    y = 40,
    width = 108,
    amp = 14,
    color = "#3b82f6",
    seed = 1,
    steady = false,
    noisy = false,
    clip = false,
}: {
    x?: number;
    y?: number;
    width?: number;
    amp?: number;
    color?: string;
    seed?: number;
    steady?: boolean;
    noisy?: boolean;
    clip?: boolean;
}) {
    const bars = Math.floor(width / 3);
    return (
        <g>
            {Array.from({ length: bars }, (_, index) => {
                const wobble = Math.abs(Math.sin(index * 0.7 * seed) * Math.cos(index * 0.23 + seed));
                let height = steady ? amp * (0.55 + 0.35 * wobble) : amp * (0.15 + 0.85 * wobble);
                if (noisy) height = amp * (0.35 + 0.65 * Math.abs(Math.sin(index * 1.9 + seed)));
                if (clip) height = Math.min(amp * 1.6, amp * (0.6 + 1.2 * wobble));
                return <rect key={index} x={x + index * 3} y={y - height} width={1.8} height={height * 2} rx={0.9} fill={color} opacity={0.85} />;
            })}
        </g>
    );
}

function Mic({ x, y, s = 1 }: { x: number; y: number; s?: number }) {
    useTranslation();
    return (
        <g transform={`translate(${x} ${y}) scale(${s})`}>
            <rect x={-7} y={-22} width={14} height={24} rx={7} fill="#1f2937" />
            {[-16, -11, -6].map((line) => (
                <line key={line} x1={-6} x2={6} y1={line} y2={line} stroke="#4b5563" strokeWidth={1} />
            ))}
            <path d="M-11 -4 Q-11 9 0 9 Q11 9 11 -4" fill="none" stroke="#1f2937" strokeWidth={2} />
            <rect x={-1.2} y={9} width={2.4} height={12} fill="#1f2937" />
            <rect x={-8} y={20} width={16} height={3} rx={1.5} fill="#1f2937" />
        </g>
    );
}

function Bubble({ x, y }: { x: number; y: number }) {
    useTranslation();
    return (
        <g transform={`translate(${x} ${y})`}>
            <rect x={-9} y={-6} width={18} height={12} rx={5} fill="#e5e7eb" />
            <path d="M-4 0 h8 M-5 -2.5 h10 M-3 2.5 h6" stroke="#9ca3af" strokeWidth={1} />
        </g>
    );
}

function Cross({ x, y }: { x: number; y: number }) {
    useTranslation();
    return (
        <g transform={`translate(${x} ${y})`}>
            <circle r={5} fill="#ef4444" />
            <path d="M-2 -2 L2 2 M2 -2 L-2 2" stroke="#fff" strokeWidth={1.4} strokeLinecap="round" />
        </g>
    );
}

const SCENES: Record<string, () => ReactNode> = {
    "voice-clean": () => (
        <Frame>
            <Wave x={4} width={36} amp={10} seed={1.3} />
            <Wave x={82} width={36} amp={10} seed={2.1} />
            <Mic x={60} y={44} s={1.15} />
        </Frame>
    ),
    "voice-single": () => (
        <Frame bg="#f8fafc">
            <Person x={42} y={40} s={2.1} />
            <path d="M66 34 q4 6 0 12 M71 31 q6 9 0 18" stroke="#3b82f6" strokeWidth={1.4} fill="none" strokeLinecap="round" />
            <circle cx={90} cy={40} r={11} fill="#fff" stroke="#e5e7eb" />
            <Mic x={90} y={42} s={0.42} />
        </Frame>
    ),
    "voice-steady": () => (
        <Frame>
            <Wave x={6} y={40} width={108} amp={11} seed={0.9} steady />
        </Frame>
    ),
    "voice-complete": () => (
        <Frame>
            <rect x={8} y={14} width={104} height={52} rx={4} fill="#fff" stroke="#e5e7eb" />
            <circle cx={16} cy={22} r={3} fill="#9ca3af" />
            <rect x={22} y={20} width={40} height={4} rx={2} fill="#e5e7eb" />
            <Wave x={14} y={44} width={92} amp={8} seed={1.7} />
            <rect x={14} y={58} width={92} height={2} rx={1} fill="#bfdbfe" />
        </Frame>
    ),
    "voice-noise": () => (
        <Frame bg="#f3f4f6">
            {[12, 32, 52, 72, 92, 108].map((x, index) => (
                <circle key={x} cx={x} cy={14} r={4} fill="#d1d5db" opacity={0.6 + (index % 2) * 0.3} />
            ))}
            <Wave x={6} y={46} width={108} amp={12} color="#6b7280" seed={3.1} noisy />
            <Cross x={110} y={70} />
        </Frame>
    ),
    "voice-crowd": () => (
        <Frame bg="#f3f4f6">
            <Bubble x={22} y={12} />
            <Bubble x={50} y={10} />
            <Bubble x={80} y={12} />
            <Person x={32} y={44} s={1.4} shirt="#374151" hair="#111827" />
            <Person x={60} y={42} s={1.5} shirt="#4b5563" hair="#111827" />
            <Person x={88} y={44} s={1.4} shirt="#374151" hair="#111827" />
            <Cross x={110} y={70} />
        </Frame>
    ),
    "voice-clip": () => (
        <Frame>
            <line x1={4} x2={116} y1={22} y2={22} stroke="#fecaca" strokeDasharray="3 2" />
            <line x1={4} x2={116} y1={58} y2={58} stroke="#fecaca" strokeDasharray="3 2" />
            <Wave x={6} y={40} width={108} amp={12} color="#ef4444" seed={2.6} clip />
            <Cross x={110} y={70} />
        </Frame>
    ),
    "model-front": () => (
        <Frame>
            <Person x={60} y={36} s={1.7} />
        </Frame>
    ),
    "model-full": () => (
        <Frame>
            <Person x={60} y={20} s={0.95} full />
        </Frame>
    ),
    "model-side": () => (
        <Frame>
            <Person x={60} y={36} s={1.7} side />
        </Frame>
    ),
    "model-multi": () => (
        <Frame>
            <Person x={22} y={34} s={0.9} />
            <Person x={48} y={34} s={0.9} side />
            <Person x={74} y={20} s={0.55} full />
            <Person x={98} y={34} s={0.9} hair="#3b2a25" shirt="#e7e5e4" />
        </Frame>
    ),
    "model-blur": () => (
        <Frame blur>
            <Person x={60} y={36} s={1.7} />
        </Frame>
    ),
    "model-group": () => (
        <Frame>
            <Person x={32} y={38} s={1.15} />
            <Person x={60} y={36} s={1.2} shirt="#fde2e4" />
            <Person x={88} y={38} s={1.15} shirt="#e0f2fe" />
        </Frame>
    ),
    "model-clutter": () => (
        <Frame bg="#ece7df">
            <Clutter />
            <Person x={58} y={38} s={1.4} />
        </Frame>
    ),
    "face-front": () => (
        <Frame>
            <Person x={60} y={40} s={2.6} />
        </Frame>
    ),
    "face-light": () => (
        <Frame bg="#fff7ed">
            <circle cx={104} cy={12} r={8} fill="#fde68a" opacity={0.8} />
            <Person x={60} y={40} s={2.6} />
        </Frame>
    ),
    "face-blur": () => (
        <Frame blur>
            <Person x={60} y={40} s={2.6} />
        </Frame>
    ),
    "face-cover": () => (
        <Frame>
            <Person x={60} y={40} s={2.6} glasses />
            <rect x={48} y={44} width={24} height={12} rx={5} fill="#93c5fd" />
        </Frame>
    ),
    "bg-clean": () => (
        <Frame>
            <Landscape />
        </Frame>
    ),
    "bg-room": () => (
        <Frame bg="#f5efe6">
            <rect x={0} y={52} width={120} height={28} fill="#d9c4a5" />
            <rect x={14} y={14} width={30} height={22} rx={2} fill="#bfdbfe" stroke="#fff" strokeWidth={2} />
            <rect x={62} y={34} width={46} height={20} rx={4} fill="#a8a29e" />
            <rect x={66} y={28} width={38} height={10} rx={4} fill="#d6d3d1" />
            <circle cx={22} cy={58} r={6} fill="#86a789" />
        </Frame>
    ),
    "bg-people": () => (
        <Frame>
            <Landscape people />
        </Frame>
    ),
    "bg-blur": () => (
        <Frame blur>
            <Landscape />
        </Frame>
    ),
    "bg-dark": () => (
        <Frame bg="#1f2937">
            <g opacity={0.25}>
                <Landscape />
            </g>
        </Frame>
    ),
    "product-clear": () => (
        <Frame bg="#ffffff">
            <Box x={60} y={38} color="#f5d48a" s={1.6} />
        </Frame>
    ),
    "product-multi": () => (
        <Frame bg="#ffffff">
            <Box x={26} y={40} color="#f5d48a" s={0.9} />
            <Box x={60} y={40} color="#f5d48a" s={0.9} />
            <g transform="translate(94 40) scale(0.9)">
                <rect x={-12} y={-14} width={24} height={30} rx={3} fill="#f5d48a" stroke="rgba(0,0,0,.15)" />
                <rect x={-6} y={-6} width={12} height={8} rx={1} fill="#fff" opacity={0.7} />
            </g>
        </Frame>
    ),
    "product-blur": () => (
        <Frame bg="#ffffff" blur>
            <Box x={60} y={38} color="#f5d48a" s={1.6} />
        </Frame>
    ),
    "product-many": () => (
        <Frame bg="#ffffff">
            <Box x={30} y={36} color="#f5d48a" s={0.9} />
            <Box x={60} y={46} color="#93c5fd" s={0.9} />
            <Box x={90} y={34} color="#fca5a5" s={0.9} />
            <circle cx={50} cy={20} r={7} fill="#86efac" />
        </Frame>
    ),
    "product-crop": () => (
        <Frame bg="#ffffff">
            <Box x={18} y={56} color="#f5d48a" s={2.2} />
            <path d="M70 10 Q100 30 80 70" stroke={SKIN} strokeWidth={14} strokeLinecap="round" fill="none" />
        </Frame>
    ),
    "video-subject": () => (
        <Frame bg="#f5efe6">
            <Person x={60} y={36} s={1.5} shirt="#f5f5f4" />
            <PlayBadge />
        </Frame>
    ),
    "video-stable": () => (
        <Frame bg="#eef2f7">
            <Person x={60} y={22} s={0.9} full shirt="#f5f5f4" />
            <rect x={6} y={6} width={108} height={68} rx={4} fill="none" stroke="rgba(0,0,0,.18)" strokeDasharray="3 3" />
            <PlayBadge />
        </Frame>
    ),
    "video-rhythm": () => (
        <Frame bg="#ede9fe">
            <Person x={60} y={20} s={0.85} full shirt="#1f2937" pants="#1f2937" />
            {[8, 16, 24, 32, 88, 96, 104, 112].map((x, i) => (
                <rect key={x} x={x} y={56 - (i % 4) * 5} width={3} height={14 + (i % 4) * 5} rx={1.5} fill="#a78bfa" />
            ))}
            <PlayBadge />
        </Frame>
    ),
    "video-blur": () => (
        <Frame blur>
            <Person x={60} y={36} s={1.5} />
            <PlayBadge />
        </Frame>
    ),
    "video-cuts": () => (
        <Frame>
            {[0, 1, 2].flatMap((row) =>
                [0, 1, 2].map((col) => (
                    <g key={`${row}-${col}`}>
                        <rect x={4 + col * 38} y={3 + row * 25.5} width={36} height={24} rx={2} fill={["#fde2e4", "#e0f2fe", "#ecfccb"][(row + col) % 3]} />
                        <Person x={22 + col * 38} y={14 + row * 25.5} s={0.45} />
                    </g>
                )),
            )}
        </Frame>
    ),
    "video-crowd": () => (
        <Frame bg="#f5efe6">
            <Person x={30} y={38} s={1.1} shirt="#e0f2fe" />
            <Person x={60} y={36} s={1.15} />
            <Person x={90} y={38} s={1.1} shirt="#fde2e4" />
            <PlayBadge />
        </Frame>
    ),
};

type TipItem = { scene: string; title: string; note?: string };

export const TIP_CONTENT: Record<TipKind, { title: string; good: TipItem[]; bad: TipItem[] }> = {
    model: {
        title: "模特图注意事项",
        good: [
            { scene: "model-front", title: "正脸清晰", note: "五官清楚，方便识别人物特征" },
            { scene: "model-full", title: "全身完整", note: "头部到脚部尽量完整，判断身材比例" },
            { scene: "model-side", title: "侧脸角度", note: "补充侧面角度，保持脸型和轮廓稳定" },
            { scene: "model-multi", title: "多角度参考", note: "正面、侧面、背面或半身图可组合上传" },
        ],
        bad: [
            { scene: "model-blur", title: "模糊低清" },
            { scene: "model-group", title: "多人合照" },
            { scene: "model-clutter", title: "背景杂乱" },
        ],
    },
    face: {
        title: "人脸图注意事项",
        good: [
            { scene: "face-front", title: "正脸大头贴", note: "脸部占画面大半，五官清楚" },
            { scene: "face-light", title: "光线均匀", note: "无明显阴影，肤色自然" },
            { scene: "model-side", title: "补充侧脸", note: "多角度让转头镜头更稳定" },
        ],
        bad: [
            { scene: "face-blur", title: "模糊低清" },
            { scene: "face-cover", title: "墨镜口罩遮挡" },
            { scene: "model-group", title: "多人合照" },
        ],
    },
    background: {
        title: "背景图注意事项",
        good: [
            { scene: "bg-clean", title: "场景完整", note: "画面里没有人物，空间关系清楚" },
            { scene: "bg-room", title: "透视清晰", note: "地面与墙面清楚，方便放入人物" },
        ],
        bad: [
            { scene: "bg-people", title: "有人物入镜" },
            { scene: "bg-blur", title: "模糊低清" },
            { scene: "bg-dark", title: "画面过暗" },
        ],
    },
    product: {
        title: "商品图注意事项",
        good: [
            { scene: "product-clear", title: "主体清晰", note: "白底或干净背景，商品完整不裁切" },
            { scene: "product-multi", title: "多角度补充", note: "正面、侧面、细节图让还原更准" },
        ],
        bad: [
            { scene: "product-blur", title: "模糊低清" },
            { scene: "product-many", title: "多个商品" },
            { scene: "product-crop", title: "遮挡裁切" },
        ],
    },
    voice: {
        title: "声音注意事项",
        good: [
            { scene: "voice-clean", title: "人声清晰干净", note: "说话声音清楚，没有明显噪音和混响" },
            { scene: "voice-single", title: "单人说话", note: "尽量保持一个人连续说话，方便识别声音特征" },
            { scene: "voice-steady", title: "音量稳定", note: "音量大小均匀，不要忽大忽小" },
            { scene: "voice-complete", title: "完整连续录音", note: "录音内容完整，支持 MP3、WAV 格式" },
        ],
        bad: [
            { scene: "voice-noise", title: "背景噪音大" },
            { scene: "voice-crowd", title: "多人同时说话" },
            { scene: "voice-clip", title: "爆音或破损" },
        ],
    },
    video: {
        title: "参考视频注意事项",
        good: [
            { scene: "video-subject", title: "主体清晰", note: "主体完整可见，动作和商品重点清楚" },
            { scene: "video-stable", title: "镜头稳定", note: "运镜平稳，方便识别动作和构图" },
            { scene: "video-rhythm", title: "节奏完整", note: "适合舞蹈、卡点、BGM 等节奏视频" },
        ],
        bad: [
            { scene: "video-blur", title: "画面模糊低清" },
            { scene: "video-cuts", title: "镜头过多杂乱" },
            { scene: "video-crowd", title: "多主体干扰" },
        ],
    },
};

export function TipScene({ scene }: { scene: string }) {
    const render = SCENES[scene];
    return render ? <>{render()}</> : null;
}

/** 悬停上传位时出现的「注意事项」卡片。 */
export function UploadTipCard({ kind, onDismiss, onClose }: { kind: TipKind; onDismiss: () => void; onClose: () => void }) {
    const content = TIP_CONTENT[kind];
    const photo = { product: "product", model: "model", face: "face", background: "background" }[kind as "product" | "model" | "face" | "background"];
    if (photo)
        return (
            <div className="ecv-tip ecv-photo-tip" role="dialog" aria-label={content.title}>
                <div className="ecv-tip-actions">
                    <button type="button" onClick={onDismiss}>
                        不再提醒
                    </button>
                    <button type="button" aria-label="关闭" onClick={onClose}>
                        ×
                    </button>
                </div>
                <h4 className="ecv-tip-title">{content.title}</h4>
                <img className="ecv-tip-photo" src={`/images/video-guides/${photo}.jpg`} width={720} height={480} loading="lazy" decoding="async" alt={`${content.title}参考示例`} />
                <div className="ecv-photo-tip-copy">
                    <div>
                        <p className="ecv-tip-group is-good">
                            <CheckCircle2 size={15} />
                            推荐
                        </p>
                        {content.good.map((item) => (
                            <p key={item.title}>
                                <strong>{item.title}</strong>
                                {item.note ? <small>{item.note}</small> : null}
                            </p>
                        ))}
                    </div>
                    <div>
                        <p className="ecv-tip-group is-bad">
                            <XCircle size={15} />
                            不推荐
                        </p>
                        {content.bad.map((item) => (
                            <p key={item.title}>{item.title}</p>
                        ))}
                    </div>
                </div>
            </div>
        );
    return (
        <div className="ecv-tip" role="dialog" aria-label={content.title}>
            <div className="ecv-tip-actions">
                <button type="button" onClick={onDismiss}>
                    不再提醒
                </button>
                <button type="button" aria-label="关闭" onClick={onClose}>
                    ×
                </button>
            </div>
            <h4 className="ecv-tip-title">{content.title}</h4>
            <p className="ecv-tip-group is-good">
                <CheckCircle2 size={15} /> 推荐
            </p>
            <div className="ecv-tip-grid" data-count={content.good.length}>
                {content.good.map((item, index) => (
                    <figure key={item.title}>
                        <TipScene scene={item.scene} />
                        <figcaption>
                            <strong>
                                <i className="is-good">{index + 1}</i>
                                {item.title}
                            </strong>
                            {item.note ? <small>{item.note}</small> : null}
                        </figcaption>
                    </figure>
                ))}
            </div>
            <p className="ecv-tip-group is-bad">
                <XCircle size={15} /> 不推荐
            </p>
            <div className="ecv-tip-grid" data-count={content.bad.length}>
                {content.bad.map((item, index) => (
                    <figure key={item.title} className="is-bad">
                        <TipScene scene={item.scene} />
                        <figcaption>
                            <strong>
                                <i className="is-bad">{index + 1}</i>
                                {item.title}
                            </strong>
                        </figcaption>
                    </figure>
                ))}
            </div>
        </div>
    );
}

function CompareTile({ label, children }: { label: string; children: ReactNode }) {
    return (
        <span className="ecv-compare-tile">
            <small>{label}</small>
            <svg viewBox="0 0 60 44" aria-hidden="true">
                {children}
            </svg>
        </span>
    );
}

function Tee({ color }: { color: string }) {
    return <path d="M19 12 L26 9 Q30 13 34 9 L41 12 L46 19 L41 22 L40 36 L20 36 L19 22 L14 19 Z" fill={color} stroke="rgba(0,0,0,.14)" />;
}

function Wearer({ color }: { color: string }) {
    return (
        <g>
            <ellipse cx={30} cy={12} rx={5.5} ry={6} fill={SKIN} />
            <path d="M24.5 11 Q24 4 30 4 Q36 4 35.5 11 Q34 7 30 7 Q26 7 24.5 11 Z" fill={HAIR} />
            <path d="M18 42 Q18 22 30 20 Q42 22 42 42 Z" fill={color} stroke="rgba(0,0,0,.12)" />
        </g>
    );
}

function SmallBox({ color }: { color: string }) {
    useTranslation();
    return (
        <g transform="translate(30 23)">
            <path d="M-12 -5 L0 -11 L12 -5 L12 10 L0 16 L-12 10 Z" fill={color} stroke="rgba(0,0,0,.16)" />
            <path d="M-12 -5 L0 1 L12 -5 M0 1 L0 16" stroke="rgba(0,0,0,.2)" fill="none" />
            <path d="M-6 -8 L6 -2 L6 3" stroke="rgba(255,255,255,.6)" strokeWidth={2} fill="none" />
        </g>
    );
}

/** 「颜色不同识别准、颜色相近效果差」对照卡（服饰 / 商品）。 */
export function CompareCard({ kind, title, open, onToggle }: { kind: CompareKind; title: string; open: boolean; onToggle: () => void }) {
    const mine = kind === "clothing" ? "#ffffff" : "#f5d48a";
    const same = kind === "clothing" ? "#f28b9a" : "#a0582c";
    const original = kind === "clothing" ? "#dc2626" : "#a0582c";
    const subject = (color: string) => (kind === "clothing" ? <Wearer color={color} /> : <SmallBox color={color} />);
    const source = (color: string) => (kind === "clothing" ? <Tee color={color} /> : <SmallBox color={color} />);
    return (
        <div className={`ecv-compare is-${kind}`} data-open={open}>
            <button type="button" className="ecv-compare-head" aria-expanded={open} onClick={onToggle}>
                <span className="ecv-compare-icon">{kind === "clothing" ? "👕" : "★"}</span>
                <strong>{title}</strong>
                <span className="ecv-compare-chevron" aria-hidden="true">
                    ⌄
                </span>
            </button>
            {open ? (
                <div className="ecv-compare-body">
                    <div className="ecv-compare-row is-good">
                        <p>
                            <span className="ecv-chip is-good">✓ 颜色不同</span> 识别精准，成功换上
                        </p>
                        <div className="ecv-compare-tiles">
                            <CompareTile label="你的图">{source(mine)}</CompareTile>
                            <i>+</i>
                            <CompareTile label="原视频">{subject(original)}</CompareTile>
                            <i>=</i>
                            <CompareTile label="结果">{subject(mine)}</CompareTile>
                        </div>
                    </div>
                    <div className="ecv-compare-row is-bad">
                        <p>
                            <span className="ecv-chip is-bad">✕ 颜色相近</span> 识别不准，效果不佳
                        </p>
                        <div className="ecv-compare-tiles">
                            <CompareTile label="你的图">{source(same)}</CompareTile>
                            <i>+</i>
                            <CompareTile label="原视频">{subject(original)}</CompareTile>
                            <i>=</i>
                            <CompareTile label="结果">{subject(original)}</CompareTile>
                        </div>
                    </div>
                </div>
            ) : null}
        </div>
    );
}
