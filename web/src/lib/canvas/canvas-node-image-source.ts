/**
 * 画布节点底图来源解析（弹窗与画布节点共用一套）。
 *
 * 画布节点显示图片走的是 `useNodeResourceUrl`：storageKey 解析出的稳定资源地址优先，
 * `metadata.content` 只作兜底。图片工具弹窗过去直接把 content 当 `<img src>` 用，
 * 于是遇到「content 是失效的 blob:/临时地址」或「只有 storageKey」的节点时，
 * 弹窗里就只剩一块空背景，坐标换算也拿不到真实像素。
 *
 * 这里把同一套优先级抽成纯函数 + 一个可复用的加载器：
 * - 选择逻辑（纯函数）可单测；
 * - 加载器负责候选地址解码、字节兜底（data URL）与错误信息。
 */

export type CanvasNodeImageLike = {
    width?: number;
    height?: number;
    metadata?: {
        content?: string;
        previewContent?: string;
        storageKey?: string;
        naturalWidth?: number;
        naturalHeight?: number;
    } | null;
};

export type CanvasNodeImageSource = {
    /** content 或 storageKey 任一存在即可打开编辑器，与画布节点「能显示图片」的判据一致。 */
    hasSource: boolean;
    /** storageKey 解析失败时的兜底地址（content，其次 previewContent）。 */
    url: string;
    storageKey: string;
    /** 原图像素尺寸：metadata 记录的真实尺寸优先，其次节点尺寸；解码成功后以解码结果为准。 */
    width: number;
    height: number;
};

/** 弹窗可接收的图片入参：新调用点用 `{ url, storageKey }`，老调用点可继续只传 dataUrl。 */
export type CanvasDialogImageInput = {
    url?: string;
    storageKey?: string;
    /** 解码出真实尺寸之前的兜底像素尺寸。 */
    width?: number;
    height?: number;
};

export type CanvasNodeImageLoaded = {
    /** 可直接渲染的地址；`dataUrl` 为 true 时是字节读出的 data URL（也可安全绘进 canvas）。 */
    url: string;
    width: number;
    height: number;
    dataUrl: boolean;
};

export function canvasNodeImageSource(node: CanvasNodeImageLike | null | undefined): CanvasNodeImageSource {
    const metadata = node?.metadata;
    const storageKey = trimmed(metadata?.storageKey);
    const content = trimmed(metadata?.content);
    const preview = trimmed(metadata?.previewContent);
    return {
        hasSource: Boolean(storageKey || content),
        url: content || preview,
        storageKey,
        width: positive(metadata?.naturalWidth) ?? positive(node?.width) ?? 0,
        height: positive(metadata?.naturalHeight) ?? positive(node?.height) ?? 0,
    };
}

/**
 * 依次尝试的底图地址：storageKey 解析结果 > content/preview 兜底；空值与重复地址只保留一个。
 * 两条都不可用时由加载器退回项目统一的字节读取（data URL）。
 */
export function canvasNodeImageCandidates(source: Pick<CanvasNodeImageSource, "url" | "storageKey">, storedUrl = ""): string[] {
    const candidates = [trimmed(storedUrl), trimmed(source.url)];
    return candidates.filter((candidate, index) => candidate.length > 0 && candidates.indexOf(candidate) === index);
}

/** 弹窗的兜底入参：新调用点传解析结果（含 storageKey），老调用点只传 dataUrl。 */
export function canvasDialogImageInput(dataUrl?: string, image?: CanvasDialogImageInput | null): CanvasDialogImageInput | null {
    const url = image ? trimmed(image.url) : trimmed(dataUrl);
    const storageKey = trimmed(image?.storageKey);
    if (!url && !storageKey) return null;
    return { url, storageKey, width: positive(image?.width) ?? 0, height: positive(image?.height) ?? 0 };
}

/**
 * 加载弹窗底图：storageKey 解析结果优先，content 兜底，最后退回字节读取（data URL）。
 * 会画进 canvas 的场景（裁剪、放大）由 readCanvasNodeImageBytes 直接拿字节，避免临时地址失效或跨域污染。
 */
export async function loadCanvasNodeImage(source: Pick<CanvasNodeImageSource, "url" | "storageKey">): Promise<CanvasNodeImageLoaded> {
    const url = trimmed(source.url);
    const storageKey = trimmed(source.storageKey);
    if (!url && !storageKey) throw new Error("底图加载失败：图片资源不可用或已失效");
    const fromBytes = async () => {
        const dataUrl = await readCanvasNodeImageBytes({ url, storageKey });
        return { url: dataUrl, ...(await decodeCanvasNodeImage(dataUrl)), dataUrl: true };
    };
    for (const candidate of await canvasNodeImageCandidateUrls({ url, storageKey })) {
        try {
            return { url: candidate, ...(await decodeCanvasNodeImage(candidate)), dataUrl: candidate.startsWith("data:") };
        } catch {
            // 试下一个候选：过期 blob: / 临时地址失效时继续往下走。
        }
    }
    try {
        return await fromBytes();
    } catch (error) {
        throw new Error(`底图加载失败：${error instanceof Error ? error.message : "图片资源不可用或已失效"}`);
    }
}

/** 读取底图字节并转成 data URL；会画进 canvas 的操作（裁剪、放大）用它拿不会被污染的源。 */
export async function readCanvasNodeImageBytes(source: Pick<CanvasNodeImageSource, "url" | "storageKey">): Promise<string> {
    const { imageToDataUrl } = await import("@/services/image-storage");
    const dataUrl = await imageToDataUrl({ url: trimmed(source.url), storageKey: trimmed(source.storageKey) });
    if (!dataUrl || dataUrl.startsWith("[image omitted]")) throw new Error("图片资源不可用或已失效");
    return dataUrl;
}

/** 解码图片拿真实像素尺寸；弹窗的坐标与尺寸换算依赖这个结果，不能只信节点宽高。 */
export function decodeCanvasNodeImage(url: string, timeoutMs = 20000) {
    return new Promise<{ width: number; height: number }>((resolve, reject) => {
        const element = new Image();
        let timer: ReturnType<typeof setTimeout> | undefined;
        const cleanup = () => {
            if (timer !== undefined) clearTimeout(timer);
            element.onload = null;
            element.onerror = null;
        };
        timer = setTimeout(() => {
            cleanup();
            reject(new Error("图片解码超时"));
        }, timeoutMs);
        element.onload = () => {
            const size = { width: element.naturalWidth, height: element.naturalHeight };
            cleanup();
            if (size.width > 0 && size.height > 0) resolve(size);
            else reject(new Error("图片尺寸无效"));
        };
        element.onerror = () => {
            cleanup();
            reject(new Error("图片加载失败"));
        };
        element.decoding = "async";
        element.src = url;
    });
}

/** storageKey 解析结果 + content 兜底，按优先级去重。 */
export async function canvasNodeImageCandidateUrls(source: Pick<CanvasNodeImageSource, "url" | "storageKey">): Promise<string[]> {
    let storedUrl = "";
    if (trimmed(source.storageKey)) {
        try {
            const { resolveImageUrl } = await import("@/services/image-storage");
            storedUrl = await resolveImageUrl(trimmed(source.storageKey), "");
        } catch (error) {
            // storageKey 失效（换设备、缓存被清）时继续用节点上的地址兜底，不直接报错。
            console.warn("弹窗底图：storageKey 解析失败，改用节点图片地址", error);
        }
    }
    return canvasNodeImageCandidates(source, storedUrl);
}

function trimmed(value: unknown) {
    return typeof value === "string" ? value.trim() : "";
}

function positive(value: unknown) {
    const parsed = typeof value === "number" ? value : Number(value);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}
