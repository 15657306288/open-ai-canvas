// [connector] P0-B-4 渠道/模型连接器工具（Q4：渠道开成连接器工具且目录自更新）
//
// 原则（roadmap §7.1）：工具集稳定（list/get/generate 通用工具），模型目录是工具返回的动态数据，
// 加模型=数据变化，协议零改动。渠道密钥绝不通过工具返回。
// 注册到 MCP（stdio + Streamable HTTP）与 OpenAPI 两个门面。

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";

import type { ChannelCatalogProvider } from "./channel-catalog.js";
import type { ChannelGenerateClient } from "./channel-generate.js";

export interface ChannelToolContext {
    catalog: ChannelCatalogProvider;
    generate: ChannelGenerateClient;
}

export interface ChannelToolDef {
    name: string;
    description: string;
    inputSchema: z.ZodObject<z.ZodRawShape>;
    handler: (ctx: ChannelToolContext, input: unknown) => Promise<unknown>;
}

const capabilitySchema = z.enum(["text", "image", "video", "audio"]);
const jsonSchema = z.record(z.string(), z.unknown());

export const channelToolDefs: ChannelToolDef[] = [
    {
        name: "channel_list",
        description: "列出影策已接入的模型渠道（id/名称/协议/启用状态/模型计数）。只读，实时反映目录。",
        inputSchema: z.object({}),
        async handler(ctx) {
            return ctx.catalog.listChannels();
        },
    },
    {
        name: "channel_list_models",
        description: "列出渠道模型：key、所属渠道、能力（text/image/video/audio）、计费、启用状态、任务参数。可按渠道/能力/启用过滤。",
        inputSchema: z.object({
            channelId: z.string().optional().describe("按渠道 id 过滤"),
            capability: capabilitySchema.optional().describe("按能力过滤"),
            enabled: z.boolean().optional().describe("只列启用/停用模型"),
        }),
        async handler(ctx, input) {
            const { channelId, capability, enabled } = input as { channelId?: string; capability?: "text" | "image" | "video" | "audio"; enabled?: boolean };
            return ctx.catalog.listModels({ channelId, capability, enabled });
        },
    },
    {
        name: "model_list_logical",
        description: "列出逻辑模型家族（外部 Agent 选择生成模型的主入口）及可用线路数。",
        inputSchema: z.object({}),
        async handler(ctx) {
            return ctx.catalog.listLogicalModels();
        },
    },
    {
        name: "model_get_capability",
        description: "取指定模型的输入约束/规格（能力类型、计费、任务参数定义）。model 取 channel_list_models 的 key。",
        inputSchema: z.object({ model: z.string().describe("模型 key") }),
        async handler(ctx, input) {
            const { model } = input as { model: string };
            const capability = ctx.catalog.getCapability(model);
            if (!capability) throw new Error(`模型不存在：${model}`);
            return capability;
        },
    },
    {
        name: "channel_catalog_version",
        description: "返回渠道/模型目录版本（version/updatedAt/hash/counts）。外部 Agent 可在长任务前后比对，判断是否需要重拉目录。",
        inputSchema: z.object({}),
        async handler(ctx) {
            return ctx.catalog.catalogVersion();
        },
    },
    {
        name: "channel_generate",
        description: "经影策已接入渠道直接发起生成（text/image/video）。返回 taskId，用 channel_get_task 查询结果。渠道密钥由本机 Runtime 持有，不会返回。",
        inputSchema: z.object({
            channelId: z.string().describe("渠道 id（channel_list 获取）"),
            model: z.string().describe("渠道模型 key（channel_list_models 获取）"),
            capability: capabilitySchema.describe("生成能力类型"),
            prompt: z.string().describe("生成提示词"),
            params: jsonSchema.optional().describe("额外参数（如尺寸/时长，随渠道模型规格）"),
        }),
        async handler(ctx, input) {
            const { channelId, model, capability, prompt, params } = input as {
                channelId: string; model: string; capability: "text" | "image" | "video" | "audio"; prompt: string; params?: Record<string, unknown>;
            };
            return ctx.generate.generate({ channelId, model, capability, prompt, params });
        },
    },
    {
        name: "channel_get_task",
        description: "查询渠道生成任务状态与结果（channel_generate 返回的 taskId）。",
        inputSchema: z.object({ taskId: z.string().describe("channel_generate 返回的 taskId") }),
        async handler(ctx, input) {
            const { taskId } = input as { taskId: string };
            const task = ctx.generate.getTask(taskId);
            if (!task) throw new Error(`任务不存在：${taskId}`);
            return task;
        },
    },
];

export const channelToolNames = channelToolDefs.map((tool) => tool.name);

/** 注册全部渠道工具到 MCP server，并挂接目录变更 → notifications/tools/list_changed（三层更新第3层）。
 *  广播采用 module 级活跃 server 集合：stdio server 进程常驻；HTTP session 关闭时
 *  须调用 unregisterChannelMcpServer(server) 避免回调累积。 */
const channelServers = new Set<McpServer>();
let watcherInstalled = false;

export function registerChannelTools(server: McpServer, ctx: ChannelToolContext) {
    for (const tool of channelToolDefs) {
        server.registerTool(
            tool.name,
            { description: tool.description, inputSchema: tool.inputSchema.shape },
            async (input: unknown) => {
                const result = await tool.handler(ctx, tool.inputSchema.parse(input));
                return { content: [{ type: "text" as const, text: JSON.stringify(result, null, 2) }] };
            },
        );
    }
    channelServers.add(server);
    if (!watcherInstalled) {
        watcherInstalled = true;
        // 目录文件变更 → 向所有活跃 MCP server 广播 list_changed，Agent 自动重拉工具/目录
        ctx.catalog.onChange(() => {
            for (const active of channelServers) {
                try {
                    active.sendToolListChanged();
                } catch {
                    // 客户端可能已断开，忽略
                }
            }
        });
    }
}

/** HTTP MCP session 关闭时移除其 server，避免向已断开客户端发通知 */
export function unregisterChannelMcpServer(server: McpServer) {
    channelServers.delete(server);
}
