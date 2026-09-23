import type { ReactNode } from "react";
import { ImageOff, LoaderCircle, RefreshCw } from "lucide-react";

import { Button } from "antd";

/**
 * 弹窗底图的加载/失败状态。工具弹窗的预览区都有一块图片容器，
 * 这里统一给出「读取中」和「加载失败 + 重试」，避免出现「空白且无提示」。
 */
export function CanvasNodeImageStatus({ status, error, onRetry, className = "" }: { status: "idle" | "loading" | "ready" | "error"; error?: string; onRetry?: () => void; className?: string }): ReactNode {
    if (status === "ready" || status === "idle") return null;
    return (
        <div className={`pointer-events-none absolute inset-0 z-20 grid place-items-center text-xs ${className}`}>
            {status === "loading" ? (
                <span className="flex items-center gap-2 opacity-65">
                    <LoaderCircle className="size-4 animate-spin" />
                    正在读取底图...
                </span>
            ) : (
                <span className="flex flex-col items-center gap-2 px-6 text-center opacity-75">
                    <ImageOff className="size-5" />
                    <span>{error || "底图加载失败"}</span>
                    {onRetry ? (
                        <Button size="small" className="pointer-events-auto" icon={<RefreshCw className="size-3.5" />} onClick={onRetry}>
                            重新加载
                        </Button>
                    ) : null}
                </span>
            )}
        </div>
    );
}
