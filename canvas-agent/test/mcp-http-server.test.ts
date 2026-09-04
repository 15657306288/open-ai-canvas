import assert from "node:assert/strict";
import http, { type Server } from "node:http";
import { test } from "node:test";

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

import { createLocalRuntimeApp } from "../src/local-runtime.js";
import { LocalRuntimeSessionManager } from "../src/local-runtime-session.js";
import { createMcpHttpHandler } from "../src/mcp-http-server.js";
import { createCanvasAgentHttpModule } from "../src/modules/canvas-agent-http.js";

const origin = "http://127.0.0.1:3000";

function buildApp(port: number) {
    const authority = `127.0.0.1:${port}`;
    const endpoint = `http://${authority}`;
    const config = {
        url: endpoint,
        token: "mcp-test-token",
        trustedWebOrigins: [origin],
        browserRegistrations: [] as never[],
    };
    const manager = new LocalRuntimeSessionManager({
        endpoint,
        trustedOrigins: [origin],
        registrations: [],
    });
    return createLocalRuntimeApp({
        authority,
        endpoint,
        version: "0.1.0",
        sessionManager: manager,
        modules: [createCanvasAgentHttpModule(config)],
        legacyMasterToken: config.token,
        mcpHandler: createMcpHttpHandler(config),
    });
}

async function start() {
    const probe = http.createServer();
    await new Promise<void>((resolve) => probe.listen(0, "127.0.0.1", () => resolve()));
    const address = probe.address();
    const port = address && typeof address === "object" ? address.port : 0;
    await new Promise<void>((resolve) => probe.close(() => resolve()));
    const server = buildApp(port).listen(port, "127.0.0.1");
    await new Promise<void>((resolve) => server.once("listening", () => resolve()));
    return { server: server as Server, url: `http://127.0.0.1:${port}` };
}

test("[connector] P0-B-1 MCP client connects to /mcp, initializes, and lists canvas tools", async () => {
    const { server, url } = await start();
    const client = new Client({ name: "connector-test-client", version: "0.1.0" });
    const transport = new StreamableHTTPClientTransport(new URL(`${url}/mcp`), {
        requestInit: { headers: { Authorization: "Bearer mcp-test-token" } },
    });
    try {
        await client.connect(transport);
        const result = await client.listTools();
        const names = result.tools.map((tool) => tool.name);
        assert.ok(names.includes("canvas_get_context"), "画布语义工具应通过 MCP HTTP 暴露");
        assert.ok(names.includes("canvas_apply_ops"), "画布写工具应通过 MCP HTTP 暴露");
        // [connector] P0-B-4 渠道/模型工具也通过 MCP HTTP 暴露（目录自更新）
        assert.ok(names.includes("channel_list"), "渠道工具应通过 MCP HTTP 暴露");
        assert.ok(names.includes("channel_generate"), "渠道生成工具应通过 MCP HTTP 暴露");
    } finally {
        await client.close();
        await new Promise<void>((resolve) => server.close(() => resolve()));
    }
});

test("[connector] P0-B-1 MCP HTTP call tool surfaces runtime errors without a connected canvas", async () => {
    const { server, url } = await start();
    const client = new Client({ name: "connector-test-client", version: "0.1.0" });
    const transport = new StreamableHTTPClientTransport(new URL(`${url}/mcp`), {
        requestInit: { headers: { Authorization: "Bearer mcp-test-token" } },
    });
    try {
        await client.connect(transport);
        const result = await client.callTool({ name: "canvas_get_context", arguments: {} });
        const text = result.content
            .filter((item): item is { type: "text"; text: string } => item.type === "text")
            .map((item) => item.text)
            .join("\n");
        assert.match(text, /没有已连接画布/, "无画布时应返回明确错误而非静默");
    } finally {
        await client.close();
        await new Promise<void>((resolve) => server.close(() => resolve()));
    }
});

test("[connector] L2 /mcp requires bearer token when configured", async () => {
    const { server, url } = await start();
    try {
        const res = await fetch(`${url}/mcp`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2025-03-26", capabilities: {}, clientInfo: { name: "unauth", version: "0.0.1" } } }),
        });
        assert.equal(res.status, 401, "未携带 token 的 /mcp 请求应返回 401");
        const body = (await res.json()) as { error?: { code?: number; message?: string } };
        assert.match(body.error?.message ?? "", /Unauthorized/i, "应返回明确的鉴权错误");
    } finally {
        await new Promise<void>((resolve) => server.close(() => resolve()));
    }
});
