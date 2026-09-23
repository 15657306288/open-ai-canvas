import { generateDoubaoVideo } from "@/services/api/doubao-accounts";
import type { ResolvedAiConfig, VideoGenerationResult, VideoGenerationTask, VideoGenerationTaskState } from "./video-contracts";

/**
 * 豆包账号池视频任务：后端同步等待出片（最长约 12 分钟），
 * 这里在「创建」阶段发起请求并挂起 Promise，「轮询」阶段读取结果，
 * 与既有 task/poll 调用方（requestVideoGeneration）保持同一形状。
 */

type DoubaoVideoEntry = {
    settled: boolean;
    result?: VideoGenerationResult;
    error?: Error;
    promise: Promise<void>;
};

const pending = new Map<string, DoubaoVideoEntry>();
let seq = 0;

export async function createDoubaoVideoTask(requestConfig: ResolvedAiConfig, prompt: string, videoSeconds: string): Promise<VideoGenerationTask> {
    const id = `doubao-pool-${Date.now()}-${seq++}`;
    // 时长上限对齐 Dola/豆包网页端 30s 档位（上游 samantha 服务端接受 30）。
    const duration = Math.min(30, Math.max(4, Math.round(Number(videoSeconds) || 10)));
    const entry: DoubaoVideoEntry = { settled: false, promise: Promise.resolve() };
    // dola-* 模型键锁定 Dola 站点取号；其余豆包模型跨站（豆包优先，自动落 Dola）。
    const site = requestConfig.model.startsWith("dola-") ? "dola" : undefined;
    entry.promise = generateDoubaoVideo({ prompt, duration, model: requestConfig.model, site })
        .then((result) => {
            const url = result.urls[0];
            if (!url) throw new Error("豆包没有返回视频");
            entry.result = { url, mimeType: "video/mp4" };
        })
        .catch((error) => {
            entry.error = error instanceof Error ? error : new Error("豆包视频生成失败");
        })
        .finally(() => {
            entry.settled = true;
        });
    pending.set(id, entry);
    return { id, provider: "doubao-pool", model: requestConfig.model };
}

export async function pollDoubaoVideoTask(task: VideoGenerationTask): Promise<VideoGenerationTaskState> {
    const entry = pending.get(task.id);
    if (!entry) return { status: "failed", error: "豆包视频任务不存在或已过期" };
    if (!entry.settled) return { status: "pending" };
    pending.delete(task.id);
    if (entry.error) return { status: "failed", error: entry.error.message };
    if (!entry.result) return { status: "failed", error: "豆包视频生成失败：空结果" };
    return { status: "completed", result: entry.result };
}


