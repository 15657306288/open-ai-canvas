// [connector] L3 broker MCP 网关 —— 对外标准 MCP 端点
//
// 独立可运行：node dist/bridge/gateway-server.js
// 让外部 agent（Codex/豆包/WorkBuddy/任意 MCP 客户端）只配一个固定 URL 即可调用
// 通过 broker 注册的画布 Runtime（本机或远端任意一台）。
//
// 工作原理：
//   1) 启动时连接 Schema Runtime（默认本机 127.0.0.1:17371/mcp）拉取工具定义，
//      在 tools/list 时原样下发（影策各 Runtime 工具集一致，schema 通用）。
//   2) tools/call 时把 {name, input} 经 Broker /api/canvas-bridge/request 转发到
//      目标 bridge（默认 CANVAS_BRIDGE_ID，未指定则取第一个在线 bridge），
//      轮询 /request/:id 直至本地 Runtime 回传结果。
//
// 环境变量：
//   CANVAS_GATEWAY_PORT          监听端口（默认 17801）
//   CANVAS_GATEWAY_HOST          监听地址（默认 0.0.0.0）
//   CANVAS_GATEWAY_TOKEN         外部 agent 连接网关的 Bearer 凭据（默认空=不鉴权，建议设置）
//   CANVAS_BROKER_URL            Broker 地址（默认 http://127.0.0.1:17800）
//   CANVAS_BRIDGE_ID             默认目标 bridgeId（空=取第一个在线 bridge）
//   CANVAS_SCHEMA_RUNTIME_URL    Schema Runtime 地址（默认 http://127.0.0.1:17371）
//   CANVAS_SCHEMA_RUNTIME_TOKEN  Schema Runtime 的 /mcp 凭据（默认空）
//   CANVAS_TOOL_TIMEOUT_MS       单次工具调用超时（默认 120000）

import http from "node:http";
import crypto from "node:crypto";

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { ListToolsRequestSchema, CallToolRequestSchema } from "@modelcontextprotocol/sdk/types.js";
import type { Request, Response } from "express";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

import { AGENT_PROMPT, VERSION } from "../config.js";

const env = process.env;
const PORT = Number(env.CANVAS_GATEWAY_PORT ?? 17801);
const HOST = env.CANVAS_GATEWAY_HOST ?? "0.0.0.0";
const GATEWAY_TOKEN = env.CANVAS_GATEWAY_TOKEN ?? "";
const BROKER_URL = (env.CANVAS_BROKER_URL ?? "http://127.0.0.1:17800").replace(/\/+$/, "");
const BRIDGE_ID = env.CANVAS_BRIDGE_ID ?? "";
const SCHEMA_URL = ((env.CANVAS_SCHEMA_RUNTIME_URL ?? "http://127.0.0.1:17371").replace(/\/+$/, "")) + "/mcp";
const SCHEMA_TOKEN = env.CANVAS_SCHEMA_RUNTIME_TOKEN ?? "";
const TOOL_TIMEOUT_MS = Number(env.CANVAS_TOOL_TIMEOUT_MS ?? 120_000);

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

interface ToolMeta {
    name: string;
    description?: string;
    inputSchema?: unknown;
}

/** 从 Schema Runtime 拉取工具定义 */
async function loadTools(): Promise<ToolMeta[]> {
    const client = new Client({ name: "canvas-gateway", version: VERSION });
    const headers: Record<string, string> = SCHEMA_TOKEN ? { authorization: `Bearer ${SCHEMA_TOKEN}` } : {};
    const transport = new StreamableHTTPClientTransport(new URL(SCHEMA_URL), { requestInit: { headers } });
    try {
        await client.connect(transport);
        const result = await client.listTools();
        return result.tools.map((tool) => ({
            name: tool.name,
            description: tool.description,
            inputSchema: tool.inputSchema as unknown,
        }));
    } finally {
        await client.close();
    }
}

/** 带重试的 schema 拉取：launchd 同时拉起三件套时等待 Runtime 就绪（启动解耦） */
async function loadToolsWithRetry(): Promise<ToolMeta[]> {
    let lastError: unknown;
    for (let i = 0; i < 10; i += 1) {
        try {
            return await loadTools();
        } catch (error) {
            lastError = error;
            console.log(`[canvas-gateway] Schema Runtime 未就绪（${i + 1}/10），2s 后重试…`);
            await sleep(2000);
        }
    }
    throw lastError instanceof Error ? lastError : new Error("Schema Runtime 不可达");
}

/** 获取在线 bridge 列表 */
async function listBridges(): Promise<Array<{ bridgeId: string; online: boolean }>> {
    const res = await fetch(`${BROKER_URL}/api/canvas-bridge/bridges`);
    const body = (await res.json()) as { code?: number; data?: { bridges?: Array<{ bridgeId: string; online: boolean }> } };
    if (body.code !== 0 || !body.data?.bridges) return [];
    return body.data.bridges;
}

/** 选择目标 bridge：显式配置优先，否则第一个在线 */
async function pickBridgeId(): Promise<string | undefined> {
    if (BRIDGE_ID) return BRIDGE_ID;
    const bridges = await listBridges();
    const online = bridges.find((b) => b.online) ?? bridges[0];
    return online?.bridgeId;
}

/** 经 Broker 转发工具调用到目标 bridge，轮询直至完成 */
async function callViaBridge(name: string, input: Record<string, unknown>): Promise<unknown> {
    const bridgeId = await pickBridgeId();
    if (!bridgeId) throw new Error("没有在线的画布 bridge（请先启动本地 Runtime 并启用 bridge 外连）");

    const submitRes = await fetch(`${BROKER_URL}/api/canvas-bridge/request`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ bridgeId, name, input }),
    });
    const submitBody = (await submitRes.json()) as { code?: number; data?: { requestId: string }; msg?: string };
    if (submitBody.code !== 0) throw new Error(submitBody.msg || `bridge 提交失败（HTTP ${submitRes.status}）`);
    const requestId = submitBody.data!.requestId;

    const deadline = Date.now() + TOOL_TIMEOUT_MS;
    while (Date.now() < deadline) {
        const resultRes = await fetch(`${BROKER_URL}/api/canvas-bridge/request/${encodeURIComponent(requestId)}`);
        const resultBody = (await resultRes.json()) as { code?: number; msg?: string; data?: { status: string; result?: unknown; error?: string } };
        if (resultBody.code !== 0) throw new Error(resultBody.msg || "查询 bridge 结果失败");
        const data = resultBody.data!;
        if (data.status === "succeeded") return data.result;
        if (data.status === "failed") throw new Error(data.error || "画布工具执行失败");
        await sleep(300);
    }
    throw new Error(`画布工具调用超时（${TOOL_TIMEOUT_MS}ms）`);
}

/** 为单个 MCP 会话创建低层 Server（inputSchema 原样透传 JSON Schema） */
function createSessionServer(tools: ToolMeta[]): Server {
    const server = new Server(
        { name: "canvas-gateway", version: VERSION },
        { capabilities: { tools: { listChanged: false } }, instructions: AGENT_PROMPT },
    );
    server.setRequestHandler(ListToolsRequestSchema, async () => ({
        tools: tools.map((t) => ({
            name: t.name,
            description: t.description ?? "",
            inputSchema: t.inputSchema as never,
        })),
    }));
    server.setRequestHandler(CallToolRequestSchema, async (request) => {
        const name = request.params.name;
        const args = (request.params.arguments ?? {}) as Record<string, unknown>;
        try {
            const result = await callViaBridge(name, args);
            const text = typeof result === "string" ? result : JSON.stringify(result);
            return { content: [{ type: "text" as const, text }] };
        } catch (error) {
            return {
                content: [{ type: "text" as const, text: `[canvas-bridge] ${error instanceof Error ? error.message : String(error)}` }],
                isError: true,
            };
        }
    });
    return server;
}

async function main() {
    // 拉取工具 schema（带重试，等待 Runtime 就绪）
    const tools = await loadToolsWithRetry();
    console.log(`[canvas-gateway] 已从 ${SCHEMA_URL} 拉取 ${tools.length} 个工具定义`);

    // MCP Streamable HTTP 会话管理（含网关自身 Bearer 鉴权）
    const sessions = new Map<string, { transport: StreamableHTTPServerTransport; server: Server }>();
    const httpServer = http.createServer((req, res) => {
        if (GATEWAY_TOKEN) {
            const auth = typeof req.headers.authorization === "string" ? req.headers.authorization : "";
            const bearer = auth.startsWith("Bearer ") ? auth.slice("Bearer ".length).trim() : "";
            const alt = typeof req.headers["x-canvas-agent-token"] === "string" ? req.headers["x-canvas-agent-token"] : "";
            if (bearer !== GATEWAY_TOKEN && alt !== GATEWAY_TOKEN) {
                res.statusCode = 401;
                res.setHeader("content-type", "application/json");
                res.end(JSON.stringify({ jsonrpc: "2.0", error: { code: -32001, message: "Unauthorized: invalid gateway token" }, id: null }));
                return;
            }
        }
        void handleMcpRequest(req as Request, res as Response);
    });

    async function handleMcpRequest(req: Request, res: Response) {
        const headerSessionId = typeof req.headers["mcp-session-id"] === "string" ? req.headers["mcp-session-id"] : undefined;
        let session = headerSessionId ? sessions.get(headerSessionId) : undefined;
        if (!session) {
            if (req.method !== "POST") {
                jsonRpcError(res, 404, -32001, "Session not found");
                return;
            }
            const sessionServer = createSessionServer(tools);
            const transport = new StreamableHTTPServerTransport({
                sessionIdGenerator: () => crypto.randomUUID(),
                enableJsonResponse: true,
                onsessioninitialized: (sessionId) => { sessions.set(sessionId, { transport, server: sessionServer }); },
                onsessionclosed: (sessionId) => { sessions.delete(sessionId); },
            });
            session = { transport, server: sessionServer };
            await sessionServer.connect(transport);
        }
        let parsedBody: unknown;
        if (req.method === "POST" && Buffer.isBuffer(req.body) && req.body.length) {
            try {
                parsedBody = JSON.parse(req.body.toString("utf8"));
            } catch {
                jsonRpcError(res, 400, -32700, "Parse error: Invalid JSON");
                return;
            }
        }
        try {
            await session.transport.handleRequest(req as never, res as never, parsedBody);
        } catch {
            if (!res.headersSent) jsonRpcError(res, 500, -32603, "Internal error");
        }
    }

    httpServer.listen(PORT, HOST, () => {
        console.log(`[canvas-gateway] MCP gateway listening on ${HOST}:${PORT}/mcp`);
        console.log(`[canvas-gateway] broker=${BROKER_URL} defaultBridge=${BRIDGE_ID || "(auto: 第一个在线)"}`);
        console.log(`[canvas-gateway] auth=${GATEWAY_TOKEN ? "enabled (Bearer)" : "disabled"}`);
    });

    function shutdown() {
        for (const s of sessions.values()) {
            try { void s.transport.close(); } catch { /* ignore */ }
        }
        httpServer.close(() => process.exit(0));
        setTimeout(() => process.exit(0), 2000).unref();
    }
    process.on("SIGINT", shutdown);
    process.on("SIGTERM", shutdown);
}

function jsonRpcError(res: Response, status: number, code: number, message: string) {
    if (res.headersSent) return;
    res.status(status).json({ jsonrpc: "2.0", error: { code, message }, id: null });
}

void main();
