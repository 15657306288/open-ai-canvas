// [connector] P0-B-4 channel_generate / channel_get_task —— 渠道直连生成客户端
//
// 复用已接入的渠道（a6api/artbox/红鸟，均为 OpenAI 兼容或任务型中转）：
//   - text  : POST {baseUrl}/v1/chat/completions（同步）
//   - image : POST {baseUrl}/v1/images/generations（同步）
//   - video : 渠道声明 videoUrl 则 POST 提交任务，返回 taskId 后用 taskUrl 模板轮询
// 约定：目录 baseUrl 为 OpenAI 兼容根（不含 /v1），text/image 在此拼接 /v1/...；video 端点走渠道 videoUrl（含 /v1 的相对路径）。
// one-api 风格中转（如红鸟）常返回 HTTP 200 + body.code 非 0/200 表示业务错误，本客户端会检测并报错，避免误判任务已提交。
// 渠道密钥仅在本进程内用于 Authorization 头，绝不返回给外部 Agent。
// 任务表为进程内内存态（channel_get_task 查询），断连即失效（P1 语义）。

import { agentFetch } from "./agent-fetch.js";
import type { ChannelCatalogProvider, ModelCapability } from "./channel-catalog.js";
import { getMetricsRegistry } from "./metrics.js";

export interface ChannelGenerateInput {
    channelId: string;
    model: string;
    capability: ModelCapability;
    prompt: string;
    params?: Record<string, unknown>;
}

export interface ChannelTask {
    taskId: string;
    status: "pending" | "running" | "succeeded" | "failed";
    result?: unknown;
    error?: string;
    createdAtMs: number;
}

export interface ChannelGenerateClient {
    generate(input: ChannelGenerateInput): Promise<{ taskId: string; status: ChannelTask["status"] }>;
    getTask(taskId: string): ChannelTask | undefined;
    listTasks(): ChannelTask[];
}

export function createChannelGenerateClient(catalog: ChannelCatalogProvider): ChannelGenerateClient {
    const tasks = new Map<string, ChannelTask>();

    const newTask = (): ChannelTask => ({
        taskId: `gen-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
        status: "pending",
        createdAtMs: Date.now(),
    });

    // one-api 风格业务码检查：HTTP 200 + body.code 非 0/200 视为业务错误
    function businessError(body: Record<string, unknown> | undefined | null): string | undefined {
        if (body && typeof body.code !== "undefined") {
            const code = String(body.code);
            if (code !== "0" && code !== "200") {
                return typeof body.msg === "string" && body.msg
                    ? body.msg
                    : typeof body.message === "string" && body.message
                        ? body.message
                        : `业务错误 code=${code}`;
            }
        }
        return undefined;
    }

    function extractRemoteTaskId(body: Record<string, unknown>): string | undefined {
        const direct = body.task_id ?? body.taskId ?? body.id ?? body.request_id ?? body.requestId;
        if (typeof direct === "string" && direct) return direct;
        const data = body.data;
        if (data && typeof data === "object") {
            const nested = (data as Record<string, unknown>).request_id ?? (data as Record<string, unknown>).task_id ?? (data as Record<string, unknown>).taskId ?? (data as Record<string, unknown>).id;
            if (typeof nested === "string" && nested) return nested;
        }
        return undefined;
    }

    async function generate(input: ChannelGenerateInput): Promise<{ taskId: string; status: ChannelTask["status"] }> {
        // [connector] P2 §9.4 渠道生成可观测
        const started = Date.now();
        const metrics = getMetricsRegistry();
        metrics.incCounter(`channel.generate.${input.capability}`);
        try {
            const outcome = await generateInner(input);
            metrics.observeLatency("channel.generate", Date.now() - started);
            if (outcome.status === "failed") metrics.incCounter("channel.generate.errors");
            return outcome;
        } catch (error) {
            metrics.observeLatency("channel.generate", Date.now() - started);
            metrics.incCounter("channel.generate.errors");
            throw error;
        }
    }

    async function generateInner(input: ChannelGenerateInput): Promise<{ taskId: string; status: ChannelTask["status"] }> {
        const channel = catalog.resolveChannel(input.channelId);
        if (!channel) throw new Error(`渠道不存在：${input.channelId}`);
        if (!channel.enabled) throw new Error(`渠道已停用：${input.channelId}`);
        if (!channel.apiKey) throw new Error(`渠道 ${input.channelId} 未配置密钥（目录文件 apiKey 缺失）`);

        const task = newTask();
        tasks.set(task.taskId, task);
        const baseUrl = channel.baseUrl.replace(/\/+$/, "");
        const headers = {
            "content-type": "application/json",
            authorization: `Bearer ${channel.apiKey}`,
        };

        if (input.capability === "text") {
            const res = await agentFetch(`${baseUrl}/v1/chat/completions`, {
                method: "POST",
                headers,
                body: JSON.stringify({
                    model: input.model,
                    messages: [{ role: "user", content: input.prompt }],
                    ...(input.params ?? {}),
                }),
                timeoutMs: 120_000,
            });
            const body = (await res.json()) as { choices?: Array<{ message?: { content?: string } }>; error?: { message?: string } } & Record<string, unknown>;
            if (!res.ok) throw new Error(body.error?.message || `渠道 HTTP ${res.status}`);
            const bizErr = businessError(body);
            if (bizErr) throw new Error(bizErr);
            const content = body.choices?.[0]?.message?.content ?? "";
            task.status = "succeeded";
            task.result = { kind: "text", content };
            return { taskId: task.taskId, status: task.status };
        }

        if (input.capability === "image") {
            const res = await agentFetch(`${baseUrl}/v1/images/generations`, {
                method: "POST",
                headers,
                body: JSON.stringify({ model: input.model, prompt: input.prompt, ...(input.params ?? {}) }),
                timeoutMs: 180_000,
            });
            const body = (await res.json()) as { data?: Array<{ url?: string; b64_json?: string }>; error?: { message?: string } } & Record<string, unknown>;
            if (!res.ok) throw new Error(body.error?.message || `渠道 HTTP ${res.status}`);
            const bizErr = businessError(body);
            if (bizErr) throw new Error(bizErr);
            task.status = "succeeded";
            task.result = { kind: "image", images: (body.data ?? []).map((item) => ({ url: item.url, b64: item.b64_json ? "present" : undefined })) };
            return { taskId: task.taskId, status: task.status };
        }

        if (input.capability === "video") {
            if (!channel.videoUrl) {
                task.status = "failed";
                task.error = `渠道 ${input.channelId} 未配置 videoUrl，无法提交视频任务`;
                return { taskId: task.taskId, status: task.status };
            }
            const submitUrl = /^https?:\/\//.test(channel.videoUrl) ? channel.videoUrl : `${baseUrl}${channel.videoUrl}`;
            const res = await agentFetch(submitUrl, {
                method: "POST",
                headers,
                body: JSON.stringify({ model: input.model, prompt: input.prompt, ...(input.params ?? {}) }),
                timeoutMs: 60_000,
            });
            const body = (await res.json()) as { task_id?: string; taskId?: string; id?: string; data?: Record<string, unknown>; error?: { message?: string } } & Record<string, unknown>;
            if (!res.ok) throw new Error(body.error?.message || `渠道 HTTP ${res.status}`);
            const bizErr = businessError(body);
            if (bizErr) throw new Error(bizErr);
            task.status = "running";
            task.result = { kind: "video", submitted: body.data ?? { task_id: extractRemoteTaskId(body) } };
            if (channel.taskUrl) {
                const remoteTaskId = extractRemoteTaskId(body);
                if (remoteTaskId) void pollRemoteTask(channel.taskUrl, channel.apiKey, channel.baseUrl, remoteTaskId, task);
            }
            return { taskId: task.taskId, status: task.status };
        }

        task.status = "failed";
        task.error = `暂不支持的能力类型：${input.capability}`;
        return { taskId: task.taskId, status: task.status };
    }

    async function pollRemoteTask(taskUrlTemplate: string, apiKey: string, baseUrl: string, remoteId: string, task: ChannelTask) {
        const url = /^https?:\/\//.test(taskUrlTemplate)
            ? taskUrlTemplate.replace("{requestId}", encodeURIComponent(remoteId))
            : `${baseUrl}${taskUrlTemplate.replace("{requestId}", encodeURIComponent(remoteId))}`;
        try {
            for (let i = 0; i < 120; i++) {
                const res = await agentFetch(url, {
                    method: "GET",
                    headers: { authorization: `Bearer ${apiKey}` },
                    timeoutMs: 30_000,
                });
                const body = (await res.json()) as { status?: string; data?: Record<string, unknown>; error?: { message?: string } } & Record<string, unknown>;
                const bizErr = businessError(body);
                if (bizErr) {
                    task.status = "failed";
                    task.error = bizErr;
                    return;
                }
                const status = String(body.status ?? body.data?.status ?? "").toLowerCase();
                if (status.includes("success") || status.includes("succeed") || status.includes("done")) {
                    task.status = "succeeded";
                    task.result = { kind: "video", data: body.data ?? body };
                    return;
                }
                if (status.includes("fail") || status.includes("error") || body.error) {
                    task.status = "failed";
                    task.error = body.error?.message ?? "视频任务失败";
                    return;
                }
                await new Promise((resolve) => setTimeout(resolve, 5000));
            }
            task.status = "failed";
            task.error = "视频任务轮询超时";
        } catch (error) {
            task.status = "failed";
            task.error = error instanceof Error ? error.message : "视频任务轮询失败";
        }
    }

    return {
        generate,
        getTask: (taskId) => tasks.get(taskId),
        listTasks: () => Array.from(tasks.values()),
    };
}
