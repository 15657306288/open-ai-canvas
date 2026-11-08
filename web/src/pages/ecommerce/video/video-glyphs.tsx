import type { ReactNode } from "react";

import type { EcommerceVideoFeature, ReplaceKind } from "./video-kinds";

/*
 * 商品视频的功能 / 替换类型标识：24×24 单色线稿，统一笔宽与光学留白，
 * 线条和浅填充跟随前景色，暗色白线、亮色墨线。
 */

function Glyph({ children }: { children: ReactNode }) {
    return (
        <svg viewBox="0 0 24 24" width="100%" height="100%" fill="none" stroke="currentColor" strokeWidth={1.75} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            {children}
        </svg>
    );
}

const ACCENT = "currentColor";

export function FeatureGlyph(_props: { id: EcommerceVideoFeature }) {
    return (
        <Glyph>
            <rect x={3} y={3.5} width={9} height={12} rx={2.25} />
            <rect x={12} y={8.5} width={9} height={12} rx={2.25} fill={ACCENT} fillOpacity={0.08} />
            <path d="M3.5 18.5a2 2 0 0 0 2 2H9m-2-2 2 2-2 2M20.5 5.5a2 2 0 0 0-2-2H15m2-2-2 2 2 2" />
        </Glyph>
    );
}

export function KindGlyph({ id }: { id: ReplaceKind }) {
    switch (id) {
        case "model":
            return (
                <Glyph>
                    <circle cx={12} cy={6.5} r={3} />
                    <path d="M6 20 Q6 12.5 12 12.5 Q18 12.5 18 20" fill={ACCENT} fillOpacity={0.22} stroke={ACCENT} />
                </Glyph>
            );
        case "clothing":
            return (
                <Glyph>
                    <path d="M8.5 4 L5 6 L3 10 L6 11.5 L6.5 20 H17.5 L18 11.5 L21 10 L19 6 L15.5 4 Q12 7 8.5 4 Z" fill={ACCENT} fillOpacity={0.22} stroke={ACCENT} />
                    <path d="M12 7.2 V12" />
                </Glyph>
            );
        case "face":
            return (
                <Glyph>
                    <path d="M4 8 V5.5 Q4 4 5.5 4 H8 M16 4 H18.5 Q20 4 20 5.5 V8 M20 16 V18.5 Q20 20 18.5 20 H16 M8 20 H5.5 Q4 20 4 18.5 V16" />
                    <ellipse cx={12} cy={12} rx={4.2} ry={5} fill={ACCENT} fillOpacity={0.22} stroke={ACCENT} />
                    <path d="M10.4 11.2 h0.01 M13.6 11.2 h0.01" strokeWidth={2.2} />
                </Glyph>
            );
        case "background":
            return (
                <Glyph>
                    <rect x={3} y={4.5} width={18} height={15} rx={2.4} />
                    <path d="M3.5 17 L9 11.5 L13 15 L15.5 12.5 L20.5 17" fill={ACCENT} fillOpacity={0.22} stroke={ACCENT} />
                    <circle cx={16.5} cy={8.5} r={1.5} fill={ACCENT} stroke="none" />
                </Glyph>
            );
        case "product":
            return (
                <Glyph>
                    <path d="M12 3 L20 7.2 V16.8 L12 21 L4 16.8 V7.2 Z" />
                    <path d="M4 7.2 L12 11.4 L20 7.2 M12 11.4 V21" />
                    <path d="M12 11.4 L20 7.2 V16.8 L12 21 Z" fill={ACCENT} fillOpacity={0.22} stroke={ACCENT} />
                </Glyph>
            );
        case "precise-product":
            return (
                <Glyph>
                    <path d="M3.5 8 V4.5 H7 M17 4.5 H20.5 V8 M20.5 16 V19.5 H17 M7 19.5 H3.5 V16" stroke={ACCENT} />
                    <rect x={9} y={6.5} width={6} height={11} rx={2} fill={ACCENT} fillOpacity={0.22} />
                    <path d="M10.5 6.5 V5 H13.5 V6.5" />
                </Glyph>
            );
        case "precise-person":
            return (
                <Glyph>
                    <path d="M3.5 8 V4.5 H7 M17 4.5 H20.5 V8 M20.5 16 V19.5 H17 M7 19.5 H3.5 V16" stroke={ACCENT} />
                    <circle cx={12} cy={9} r={2.4} />
                    <path d="M7.8 17.5 Q7.8 13 12 13 Q16.2 13 16.2 17.5" fill={ACCENT} fillOpacity={0.22} />
                </Glyph>
            );
        default:
            return (
                <Glyph>
                    <rect x={3.5} y={3.5} width={7.5} height={7.5} rx={2} />
                    <rect x={13} y={3.5} width={7.5} height={7.5} rx={2} fill={ACCENT} fillOpacity={0.22} stroke={ACCENT} />
                    <rect x={3.5} y={13} width={7.5} height={7.5} rx={2} fill={ACCENT} fillOpacity={0.22} stroke={ACCENT} />
                    <path d="M16.75 14 V20 M13.75 17 H19.75" />
                </Glyph>
            );
    }
}

/** 替换类型的悬浮说明。 */
export const KIND_DETAILS: Record<ReplaceKind, { title: string; description: string }> = {
    model: { title: "换模特", description: "把视频里的人换成你的模特，动作、镜头和节奏跟原视频一致" },
    clothing: { title: "换服饰", description: "把你的服装穿到视频人物身上，人物和动作不变" },
    face: { title: "换人脸", description: "只替换面部，保留发型、身材、动作与场景" },
    background: { title: "换背景", description: "人物和商品不动，把场景换成你的背景图" },
    product: { title: "换商品", description: "原视频里拿着 / 展示的商品换成你的商品" },
    "precise-product": { title: "精准商品", description: "在画面里框住要换的商品，框几件换几件，多角度还原细节" },
    "precise-person": { title: "精准人物", description: "框住要换的那个人，旁边的人保持不动，可选口播对口型" },
    combination: { title: "组合替换", description: "商品、模特、背景一起换，传哪类就换哪类" },
};
