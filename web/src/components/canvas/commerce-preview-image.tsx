import { memo, useRef } from "react";
import { ImageIcon } from "lucide-react";
import type { CanvasNodeData } from "@/types/canvas";
import { useNearViewport } from "./use-near-viewport";
import { useCanvasImageSource } from "./use-canvas-image-source";

/** Display-only thumbnail: shares the canvas decode queue and user-scoped recent-source cache. */
export const CommercePreviewImage = memo(function CommercePreviewImage({ node, alt, className = "size-full", fit = "contain" }: {
    node: CanvasNodeData;
    alt: string;
    className?: string;
    fit?: "contain" | "cover";
}) {
    const ref = useRef<HTMLSpanElement>(null);
    const nearViewport = useNearViewport(ref);
    const { url, thumbnail, failed } = useCanvasImageSource(node, nearViewport);
    return <span ref={ref} className={`inline-flex items-center justify-center overflow-hidden ${className}`} title={failed ? "预览加载失败，可打开原图查看" : undefined}>
        {url ? <img src={url} alt={alt} loading="lazy" decoding="async" draggable={false} referrerPolicy="no-referrer" data-preview-variant={thumbnail ? "thumbnail" : "original"}
            className={`pointer-events-none block size-full select-none ${fit === "cover" ? "object-cover" : "object-contain"}`} />
            : <ImageIcon aria-hidden className="size-5 opacity-35" />}
    </span>;
});
