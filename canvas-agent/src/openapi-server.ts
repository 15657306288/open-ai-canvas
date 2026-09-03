// [connector] P0-B-2 OpenAPI 兜底门面
//
// 目标：为不支持 MCP 的 agent/工具链提供 OpenAPI 3.0 描述，让它们也能按 REST 调用画布。
//  - GET  /openapi.json —— 生成 OpenAPI spec（info + servers + 每个画布工具一个 POST /tools/{name} 路径）
//  - POST /tools/:name —— 单工具执行端点（等价于 /api/tools 的 name+input）
//
// 与 MCP 门面互补：MCP 是主协议，OpenAPI 是兜底（"一份工具内核 + 多协议薄门面"）。

import type { Request, RequestHandler, Response } from "express";
import { ZodType } from "zod";

import { VERSION, type CanvasAgentConfig } from "./config.js";
import { toolInputSchemas, toolNames, type ToolName } from "./schemas.js";
import { agentFetch } from "./agent-fetch.js";
import { channelToolDefs, channelToolNames } from "./channel-tools.js";
import { getChannelToolContext } from "./mcp-server.js";

export function createOpenApiHandler(config: CanvasAgentConfig): RequestHandler {
    return (req: Request, res: Response) => {
        const pathname = new URL(req.url ?? "/", config.url).pathname;
        if (req.method === "GET" && pathname === "/openapi.json") {
            json(res, 200, buildOpenApiSpec(config));
            return;
        }
        if (req.method === "POST" && pathname.startsWith("/tools/")) {
            const name = decodeURIComponent(pathname.slice("/tools/".length)) as ToolName | string;
            // [connector] P0-B-4 渠道工具走本地 ctx（目录/生成），画布工具转发 Runtime
            if (channelToolNames.includes(name)) {
                const tool = channelToolDefs.find((t) => t.name === name)!;
                void (async () => {
                    try {
                        const input = Buffer.isBuffer(req.body) && req.body.length
                            ? JSON.parse(req.body.toString("utf8"))
                            : {};
                        const result = await tool.handler(getChannelToolContext(), tool.inputSchema.parse(input));
                        json(res, 200, { ok: true, result });
                    } catch (error) {
                        json(res, 500, { ok: false, error: error instanceof Error ? error.message : "channel tool call failed" });
                    }
                })();
                return;
            }
            if (!(toolNames as readonly string[]).includes(name)) {
                json(res, 404, { ok: false, error: `未知工具：${String(name)}` });
                return;
            }
            void (async () => {
                try {
                    const input = Buffer.isBuffer(req.body) && req.body.length
                        ? JSON.parse(req.body.toString("utf8"))
                        : {};
                    const result = await postTool(config, name as ToolName, input);
                    json(res, 200, { ok: true, result });
                } catch (error) {
                    json(res, 500, { ok: false, error: error instanceof Error ? error.message : "OpenAPI tool call failed" });
                }
            })();
            return;
        }
        json(res, 404, { ok: false, error: "Not found" });
    };
}

// 用原生 http API（statusCode/setHeader/end）而非 express res.json，保证在
// express 挂载与原生 http server（测试）两种环境下都可用。
function json(res: Response, status: number, body: unknown) {
    if (res.headersSent) return;
    res.statusCode = status;
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify(body));
}

async function postTool(config: CanvasAgentConfig, name: ToolName, input: unknown) {
    const res = await agentFetch(`${config.url}/api/tools`, {
        method: "POST",
        headers: { "content-type": "application/json", "x-canvas-agent-token": config.token },
        body: JSON.stringify({ name, input }),
        timeoutMs: 30_000,
    });
    const body = (await res.json()) as { ok?: boolean; result?: unknown; error?: string };
    if (!body.ok) throw new Error(body.error || "tool call failed");
    return body.result;
}

export function buildOpenApiSpec(config: CanvasAgentConfig) {
    const paths: Record<string, unknown> = {};
    for (const name of toolNames) {
        const schema = toolInputSchemas[name];
        paths[`/tools/${name}`] = {
            post: {
                summary: `调用画布工具 ${name}`,
                operationId: name,
                requestBody: {
                    required: false,
                    content: {
                        "application/json": {
                            schema: zodObjectToJsonSchema(schema.shape),
                        },
                    },
                },
                responses: {
                    "200": { description: "工具执行结果", content: { "application/json": { schema: { type: "object" } } } },
                    "500": { description: "工具执行失败，error 字段含原因", content: { "application/json": { schema: { type: "object" } } } },
                },
            },
        };
    }
    // [connector] P0-B-4 渠道/模型工具同样暴露到 OpenAPI 门面
    for (const tool of channelToolDefs) {
        paths[`/tools/${tool.name}`] = {
            post: {
                summary: tool.description,
                operationId: tool.name,
                requestBody: {
                    required: false,
                    content: {
                        "application/json": {
                            schema: zodObjectToJsonSchema(tool.inputSchema.shape),
                        },
                    },
                },
                responses: {
                    "200": { description: "工具执行结果", content: { "application/json": { schema: { type: "object" } } } },
                    "500": { description: "工具执行失败，error 字段含原因", content: { "application/json": { schema: { type: "object" } } } },
                },
            },
        };
    }
    return {
        openapi: "3.0.3",
        info: {
            title: "canvas-agent (影策画布连接器)",
            version: VERSION,
            description: "影策本地 Runtime 的 OpenAPI 兜底门面，允许不支持 MCP 的 agent 通过 REST 调用画布与渠道工具。协议说明见 MCP 门面 /mcp。",
        },
        servers: [{ url: config.url }],
        paths: {
            ...paths,
            "/health": {
                get: {
                    summary: "Runtime 健康状态（四态：healthy/reconnecting/degraded/offline）",
                    responses: { "200": { description: "健康状态" } },
                },
            },
        },
    };
}

function zodObjectToJsonSchema(shape: Record<string, ZodType>): Record<string, unknown> {
    const properties: Record<string, unknown> = {};
    const required: string[] = [];
    for (const [key, field] of Object.entries(shape)) {
        const optional = field.isOptional?.() ?? false;
        properties[key] = zodToJsonSchema(field);
        if (!optional) required.push(key);
    }
    return { type: "object", properties, required };
}

function zodToJsonSchema(schema: ZodType): Record<string, unknown> {
    const def = schema._def as { typeName?: string; values?: unknown[]; innerType?: ZodType; value?: unknown };
    const typeName = def.typeName;
    if (typeName === "ZodString") return { type: "string" };
    if (typeName === "ZodNumber") return { type: "number" };
    if (typeName === "ZodBoolean") return { type: "boolean" };
    if (typeName === "ZodEnum" && Array.isArray(def.values)) return { type: "string", enum: def.values.map(String) };
    if (typeName === "ZodLiteral") return { type: typeof def.value === "number" ? "number" : "string", enum: [def.value as string | number] };
    if (typeName === "ZodArray" && def.innerType) return { type: "array", items: zodToJsonSchema(def.innerType) };
    if (typeName === "ZodObject") {
        const inner = schema as unknown as { shape: Record<string, ZodType> };
        return zodObjectToJsonSchema(inner.shape);
    }
    if (typeName === "ZodOptional" || typeName === "ZodNullable" || typeName === "ZodDefault") {
        const inner = def.innerType;
        if (inner) return zodToJsonSchema(inner);
    }
    if (typeName === "ZodRecord" || typeName === "ZodAny" || typeName === "ZodUnknown") return { type: "object" };
    if (typeName === "ZodUnion") return { type: "object" };
    return { type: "object" };
}
