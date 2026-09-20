// 媒体时长探测工具：上传前通过 <video>/<audio> 元数据读取真实时长（毫秒）。
// 结果随上传 meta 传给后端，供资源摘要和时间线片段使用；这里不负责验证完整播放能力。

const PROBE_TIMEOUT_MS = 8000;

function isProbeable(file: File): boolean {
    return /^video\//.test(file.type) || /^audio\//.test(file.type);
}

/**
 * 返回可确认的媒体时长；非音视频、浏览器不支持解码、元数据错误或超时都返回 undefined。
 * 这是上传附加信息而不是写入前置条件，调用方必须允许缺少时长，但不能编造默认时长。
 */
export async function probeMediaDurationMs(file: File): Promise<number | undefined> {
    if (!isProbeable(file)) return undefined;
    const url = URL.createObjectURL(file);
    const el: HTMLVideoElement | HTMLAudioElement = /^video\//.test(file.type)
        ? document.createElement("video")
        : document.createElement("audio");
    let timeoutId: number | undefined;
    el.preload = "metadata";
    // 某些浏览器只有在静音时才允许视频元素主动加载元数据；这里只禁止声音，不触发自动播放。
    el.muted = true;
    try {
        const durationMs = await new Promise<number>((resolve, reject) => {
            timeoutId = window.setTimeout(() => reject(new Error("媒体元数据读取超时")), PROBE_TIMEOUT_MS);
            el.onloadedmetadata = () => {
                const durationSeconds = el.duration;
                resolve(Number.isFinite(durationSeconds) && durationSeconds > 0 ? Math.round(durationSeconds * 1000) : 0);
            };
            el.onerror = () => resolve(0);
            el.src = url;
        });
        return durationMs > 0 ? durationMs : undefined;
    } catch {
        return undefined;
    } finally {
        if (timeoutId !== undefined) window.clearTimeout(timeoutId);
        el.onloadedmetadata = null;
        el.onerror = null;
        el.removeAttribute("src");
        el.load();
        URL.revokeObjectURL(url);
    }
}
