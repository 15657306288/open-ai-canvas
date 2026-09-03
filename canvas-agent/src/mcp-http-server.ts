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

export type McpHttpOptions = {
    /** 仅注册画布工具（跳过 dreamina），默认 false */
    canvasOnly?: boolean;
    /** 并发 MCP 会话上限，防止连接泄漏，默认 64 */
    maxSessions?: number;
};

export function createMcpHttpHandler(config: CanvasAgentConfig, options: McpHttpOptions = {}): RequestHandler {
    const maxSessions = options.maxSessions ?? 64;
    const sessions = new Map<string, StreamableHTTPServerTransport>();

    return (req: Request, res: Response) => {
        void handleRequest(req, res);
    };

    async function handleRequest(req: Request, res: Response) {
        const headerSessionId = typeof req.headers["mcp-session-id"] === "string"
            ? req.headers["mcp-session-id"]
            : undefined;
        let transport = headerSessionId ? sessions.get(headerSessionId) : undefined;
        if (!transport) {
            if (req.method !== "POST") {
                jsonRpcError(res, 404, -32001, "Session not found");
                return;
            }
            if (sessions.size >= maxSessions) {
                jsonRpcError(res, 429, -32000, "Too many MCP sessions");
                return;
            }
            transport = createTransport();
            const sessionServer = new McpServer(
                { name: "canvas-agent", version: VERSION },
                { instructions: AGENT_PROMPT },
            );
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
            await transport.handleRequest(req as never, res as never, parsedBody);
        } catch (error) {
            // transport.handleRequest 已尽量自行写响应；兜底处理未发送情况
            if (!res.headersSent) jsonRpcError(res, 500, -32603, "Internal error");
        }
    }

    function createTransport() {
        let created: StreamableHTTPServerTransport;
        created = new StreamableHTTPServerTransport({
            sessionIdGenerator: () => crypto.randomUUID(),
            enableJsonResponse: true,
            onsessioninitialized: (sessionId) => {
                sessions.set(sessionId, created);
            },
            onsessionclosed: (sessionId) => {
                sessions.delete(sessionId);
            },
        });
        return created;
    }
}

function jsonRpcError(res: Response, status: number, code: number, message: string) {
    if (res.headersSent) return;
    res.status(status).json({ jsonrpc: "2.0", error: { code, message }, id: null });
}
