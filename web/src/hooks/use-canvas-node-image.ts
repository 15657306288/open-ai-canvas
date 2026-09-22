import { useCallback, useEffect, useState } from "react";

import { loadCanvasNodeImage, type CanvasDialogImageInput } from "@/lib/canvas/canvas-node-image-source";

export type CanvasNodeImageState = {
    status: "idle" | "loading" | "ready" | "error";
    /** 可渲染的底图地址；未就绪时为空串。 */
    url: string;
    /** 解码出的真实像素尺寸（坐标换算用），未就绪时为 0。 */
    width: number;
    height: number;
    /** 读取结果是否为 data URL（可直接画进 canvas，不会被跨域污染）。 */
    dataUrl: boolean;
    error: string;
    reload: () => void;
};

const idleState: Omit<CanvasNodeImageState, "reload"> = { status: "idle", url: "", width: 0, height: 0, dataUrl: false, error: "" };

/**
 * 弹窗底图统一加载：storageKey 解析结果优先 → content 兜底 → 字节读取，解码后给出真实像素尺寸。
 * 组件只关心 status / url / 尺寸，不再各自写一份 readImageMeta。
 */
export function useCanvasNodeImage(source: CanvasDialogImageInput | null | undefined, enabled = true): CanvasNodeImageState {
    const [state, setState] = useState(idleState);
    const [reloadToken, setReloadToken] = useState(0);
    const url = source?.url?.trim() ?? "";
    const storageKey = source?.storageKey?.trim() ?? "";

    useEffect(() => {
        if (!enabled || (!url && !storageKey)) {
            setState(idleState);
            return;
        }
        let cancelled = false;
        setState((current) => ({ ...current, status: "loading", error: "" }));
        void loadCanvasNodeImage({ url, storageKey })
            .then((loaded) => {
                if (cancelled) return;
                setState({ status: "ready", url: loaded.url, width: loaded.width, height: loaded.height, dataUrl: loaded.dataUrl, error: "" });
            })
            .catch((reason: unknown) => {
                if (cancelled) return;
                setState({ status: "error", url: "", width: 0, height: 0, dataUrl: false, error: reason instanceof Error ? reason.message : "底图加载失败" });
            });
        return () => {
            cancelled = true;
        };
    }, [enabled, reloadToken, storageKey, url]);

    const reload = useCallback(() => setReloadToken((current) => current + 1), []);
    return { ...state, reload };
}
