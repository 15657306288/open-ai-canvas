import { useEffect, useState } from "react";

import { getResourceAccess, resolveResourceAccessURL, resourceIdFromStorageKey } from "@/services/api/resources";
import { getActiveUserScope } from "@/lib/user-scope";
import { resolveImageUrl } from "@/services/image-storage";
import { prepareCanvasImage } from "@/services/canvas-image-loader";
import { taskImagePreview } from "@/services/task-image-preview-store";
import type { CanvasNodeData } from "@/types/canvas";

/**
 * Resolve a displayable image URL for one canvas node.
 *
 * The contract mirrors what a preview card needs: thumbnail-first, cancellable,
 * stable across re-renders (so zoom never re-downloads), and scoped to the active
 * user. Resource authorization, OSS/CDN signing, the shared decode queue and
 * local blob storage are all reused from the canvas host rather than reimplemented.
 */
export type CanvasImageSourceState = {
    url: string;
    /** True when the URL is a server-generated thumbnail rather than the original. */
    thumbnail: boolean;
    failed: boolean;
    loading: boolean;
    /** Internal: guards against rendering a stale resolution after the node changed. */
    identity?: string;
};

const EMPTY: CanvasImageSourceState = { url: "", thumbnail: false, failed: false, loading: false };

export function useCanvasImageSource(node: CanvasNodeData, enabled: boolean): CanvasImageSourceState {
    const scope = getActiveUserScope();
    const storageKey = node.metadata?.storageKey || "";
    const content = node.metadata?.content || "";
    const previewContent = node.metadata?.previewContent || "";
    const taskId = node.metadata?.taskId;
    const resourceId = resourceIdFromStorageKey(storageKey);
    const localImageResource = Boolean(storageKey.startsWith("image:"));

    // A still-running task has no persisted content yet; its intermediate image
    // only exists in the ephemeral preview store.
    const taskPreview = taskImagePreview(scope, taskId);
    const src = taskPreview || content;
    const identity = `${scope}:${node.id}:${resourceId || storageKey || taskId || src}`;

    const [state, setState] = useState<CanvasImageSourceState>(() => ({ ...EMPTY, loading: Boolean(enabled && (src || storageKey)) }));

    useEffect(() => {
        if (!enabled) {
            setState((current) => (current.loading ? { ...current, loading: false } : current));
            return;
        }
        if (!src && !storageKey) {
            setState(EMPTY);
            return;
        }
        const controller = new AbortController();
        let cancelled = false;
        const isCurrent = () => !cancelled && !controller.signal.aborted && getActiveUserScope() === scope;
        setState((current) => (current.identity === identity ? { ...current, loading: true } : { ...EMPTY, loading: true }));

        const load = async () => {
            let candidate = src;
            let thumbnail = false;
            if (taskPreview) {
                // Task previews are already display-ready URLs from the task center.
                candidate = taskPreview;
            } else if (resourceId) {
                try {
                    const access = await getResourceAccess(storageKey, "display", "thumbnail");
                    candidate = resolveResourceAccessURL(access.url);
                    thumbnail = true;
                } catch {
                    const access = await getResourceAccess(storageKey, "display", "original");
                    candidate = resolveResourceAccessURL(access.url);
                    thumbnail = false;
                }
            } else if (localImageResource && storageKey) {
                candidate = (await resolveImageUrl(storageKey, previewContent || src)) || src;
            } else if (!candidate) {
                candidate = previewContent;
            }
            if (!candidate) throw new Error("图片地址为空");
            try {
                await prepareCanvasImage(candidate, controller.signal);
            } catch (error) {
                // A missing thumbnail must not fail the card when an original exists.
                if (!thumbnail || !src || src === candidate) throw error;
                const access = await getResourceAccess(storageKey, "display", "original");
                const originalURL = resolveResourceAccessURL(access.url);
                if (!originalURL || originalURL === candidate) throw error;
                await prepareCanvasImage(originalURL, controller.signal);
                candidate = originalURL;
                thumbnail = false;
            }
            if (isCurrent()) setState({ url: candidate, thumbnail, failed: false, loading: false, identity });
        };

        void load().catch(() => {
            if (isCurrent()) setState({ url: "", thumbnail: false, failed: true, loading: false, identity });
        });
        return () => {
            cancelled = true;
            controller.abort();
        };
    }, [enabled, identity, localImageResource, previewContent, resourceId, scope, src, storageKey, taskPreview]);

    return state.identity === identity ? state : { ...EMPTY, loading: Boolean(enabled && (src || storageKey)) };
}
