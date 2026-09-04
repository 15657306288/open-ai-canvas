// [connector] P0-B-1 MCP Streamable HTTP 门面（Q1：/mcp 默认开）
//
// 目标：让所有支持"远程 MCP"的 agent（豆包/火山方舟、WorkBuddy Connector 等）无需安装
// 本地 stdio 进程即可直接调用影策画布。本地 Runtime 暴露 /mcp 端点，实现
// Model Context Protocol Streamable HTTP Transport：
//   POST /mcp —— JSON-RPC 请求（initialize 时返回 Mcp-Session-Id）
//   GET  /mcp —— SSE 流（携带 Mcp-Session-Id）
//   DELETE /mcp —— 关闭会话
//
// 复用 mcp-server 的工具注册（canvas + dreamina），每个 MCP 会话一个独立的
// StreamableHTTPServerTransport + McpServer，与会话隔离、避免共享 connect 的语义问题。

import crypto from "node:crypto";
import type { Request, RequestHandler, Response } from "express";

import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";

import { AGENT_PROMPT, VERSION, type CanvasAgentConfig } from "./config.js";
import { registerMcpTools } from "./mcp-server.js";
import { unregisterChannelMcpServer } from "./channel-tools.js";

export type McpHttpOptions = {
    /** 仅注册画布工具（跳过 dreamina），默认 false */
    canvasOnly?: boolean;
    /** 并发 MCP 会话上限，防止连接泄漏，默认 64 */
    maxSessions?: number;
};

export function createMcpHttpHandler(config: CanvasAgentConfig, options: McpHttpOptions = {}): RequestHandler {
    const maxSessions = options.maxSessions ?? 64;
    const sessions = new Map<string, { transport: StreamableHTTPServerTransport; server: McpServer }>();
    // [connector] L2 局域网/公网：/mcp 增加 Bearer token 校验。
    // 配置了 token（canvas-agent.json）即强制校验；支持 Authorization: Bearer 与 x-canvas-agent-token 两种携带方式。
    const expectedToken = config.token || "";

    return (req: Request, res: Response) => {
        if (expectedToken && !authorized(req, expectedToken)) {
            res.status(401).json({ jsonrpc: "2.0", error: { code: -32001, message: "Unauthorized: missing or invalid canvas-agent token" }, id: null });
            return;
        }
        void handleRequest(req, res);
    };

    function authorized(req: Request, token: string): boolean {
        const authHeader = typeof req.headers["authorization"] === "string" ? req.headers["authorization"] : "";
        const bearer = authHeader.startsWith("Bearer ") ? authHeader.slice("Bearer ".length).trim() : "";
        const alt = typeof req.headers["x-canvas-agent-token"] === "string" ? req.headers["x-canvas-agent-token"] : "";
        return bearer === token || alt === token;
    }

    async function handleRequest(req: Request, res: Response) {
        const headerSessionId = typeof req.headers["mcp-session-id"] === "string"
            ? req.headers["mcp-session-id"]
            : undefined;
        let session = headerSessionId ? sessions.get(headerSessionId) : undefined;
        if (!session) {
            if (req.method !== "POST") {
                jsonRpcError(res, 404, -32001, "Session not found");
                return;
            }
            if (sessions.size >= maxSessions) {
                jsonRpcError(res, 429, -32000, "Too many MCP sessions");
                return;
            }
            const sessionServer = new McpServer(
                { name: "canvas-agent", version: VERSION },
                { instructions: AGENT_PROMPT },
            );
            const transport = createTransport(sessionServer);
            session = { transport, server: sessionServer };
            registerMcpTools(sessionServer, config, { canvasOnly: options.canvasOnly });
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
        } catch (error) {
            // transport.handleRequest 已尽量自行写响应；兜底处理未发送情况
            if (!res.headersSent) jsonRpcError(res, 500, -32603, "Internal error");
        }
    }

    function createTransport(server: McpServer) {
        const transport = new StreamableHTTPServerTransport({
            sessionIdGenerator: () => crypto.randomUUID(),
            enableJsonResponse: true,
            onsessioninitialized: (sessionId) => {
                sessions.set(sessionId, { transport, server });
            },
            onsessionclosed: (sessionId) => {
                const closed = sessions.get(sessionId);
                if (closed) {
                    // [connector] P0-B-4 关闭 session 时移除其渠道 list_changed 订阅
                    unregisterChannelMcpServer(closed.server);
                    sessions.delete(sessionId);
                }
            },
        });
        return transport;
    }
}

function jsonRpcError(res: Response, status: number, code: number, message: string) {
    if (res.headersSent) return;
    res.status(status).json({ jsonrpc: "2.0", error: { code, message }, id: null });
}
