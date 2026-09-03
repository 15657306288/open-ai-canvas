import assert from "node:assert/strict";
import http, { type Server } from "node:http";
import { test } from "node:test";

import { CanvasSession } from "../src/canvas-session.js";
import { createLocalRuntimeApp } from "../src/local-runtime.js";
import { LocalRuntimeSessionManager } from "../src/local-runtime-session.js";
import { createCanvasAgentHttpModule } from "../src/modules/canvas-agent-http.js";
import type { LocalRuntimeConfig } from "../src/config.js";

const authority = "127.0.0.1:41750";
const endpoint = `http://${authority}`;
const origin = "http://127.0.0.1:3001";
const token = "media-route-token";
const PNG_1PX = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==", "base64");

function fixtureConfig(): LocalRuntimeConfig {
    return { url: endpoint, token, ownerId: "owner-media-001", origins: [origin], trustedWebOrigins: [origin], browserRegistrations: [], canvases: {} };
}

function jsonHeaders() {
    return { "content-type": "application/json" };
}

function request(server: Server, options: { method?: string; path: string; headers?: Record<string, string>; body?: string }) {
    return new Promise<{ status: number; headers: http.IncomingHttpHeaders; buffer: Buffer; text: string }>((resolve, reject) => {
        const req = http.request({
            host: "127.0.0.1",
            port: (server.address() as { port: number }).port,
            method: options.method ?? "GET",
            path: options.path,
            headers: { Host: authority, ...(options.headers ?? {}) },
        }, (res) => {
            const chunks: Buffer[] = [];
            res.on("data", (c) => chunks.push(c as Buffer));
            res.on("end", () => {
                const buffer = Buffer.concat(chunks);
                resolve({ status: res.statusCode ?? 0, headers: res.headers, buffer, text: buffer.toString("utf8") });
            });
        });
        req.on("error", reject);
        if (options.body) req.write(options.body);
        req.end();
    });
}

function listening(server: Server) {
    return new Promise<void>((resolve) => server.once("listening", () => resolve()));
}

async function startApp() {
    const session = new CanvasSession();
    const module = createCanvasAgentHttpModule(fixtureConfig(), session);
    const manager = new LocalRuntimeSessionManager({ endpoint, trustedOrigins: [origin], registrations: [] });
    const app = createLocalRuntimeApp({
        authority,
        endpoint,
        version: "0.1.0",
        sessionManager: manager,
        modules: [module],
        legacyMasterToken: token,
        legacyOrigins: [origin],
    });
    const server = app.listen(0, "127.0.0.1");
    await listening(server);
    return { server, session };
}

test("[connector] P1-Q5 媒体路由：无画布报错、有画布返回 block、签名 URL 单次消费", async () => {
    const { server, session } = await startApp();
    try {
        // 1. 无画布 → /api/media/get 返回明确错误
        const noCanvas = await request(server, {
            method: "POST",
            path: `/api/media/get?token=${token}`,
            headers: jsonHeaders(),
            body: JSON.stringify({ nodeId: "n1", mode: "block" }),
        });
        assert.equal(noCanvas.status, 200);
        assert.match(noCanvas.text, /没有已连接画布/);

        // 2. 写入带 media 的画布状态
        const stateAccepted = await request(server, {
            method: "POST",
            path: `/canvas/state?clientId=fixture&token=${token}`,
            headers: jsonHeaders(),
            body: JSON.stringify({
                nodes: [{
                    id: "n1", type: "image",
                    position: { x: 0, y: 0 }, width: 100, height: 100,
                    metadata: { dataUrl: `data:image/png;base64,${PNG_1PX.toString("base64")}` },
                }],
            }),
        });
        assert.equal(stateAccepted.status, 200);

        // 3. /api/media/get → block base64
        const block = await request(server, {
            method: "POST",
            path: `/api/media/get?token=${token}`,
            headers: jsonHeaders(),
            body: JSON.stringify({ nodeId: "n1", mode: "block" }),
        });
        assert.equal(block.status, 200);
        const blockBody = JSON.parse(block.text) as { ok: boolean; result?: { mode: string; mimeType: string; dataBase64: string } };
        assert.equal(blockBody.ok, true);
        assert.equal(blockBody.result?.mode, "block");
        assert.equal(blockBody.result?.mimeType, "image/png");
        assert.equal(blockBody.result?.dataBase64, PNG_1PX.toString("base64"));

        // 4. canvas_get_media 工具（经 /api/tools）同样可用
        const viaTool = await request(server, {
            method: "POST",
            path: `/api/tools?token=${token}`,
            headers: jsonHeaders(),
            body: JSON.stringify({ name: "canvas_get_media", input: { nodeId: "n1", mode: "url" } }),
        });
        const toolBody = JSON.parse(viaTool.text) as { ok: boolean; result?: { mode: string; url: string; token: string } };
        assert.equal(toolBody.ok, true);
        assert.equal(toolBody.result?.mode, "url");

        // 5. 签名 URL 消费：GET /api/media/:token → bytes（公开，无需 token 头）
        const consume = await request(server, { path: toolBody.result!.url });
        assert.equal(consume.status, 200);
        assert.equal(consume.headers["content-type"], "image/png");
        assert.equal(consume.buffer.toString("base64"), PNG_1PX.toString("base64"), "签名 URL 应原样返回媒体字节");

        // 6. 二次消费 → 404（单次）
        const second = await request(server, { path: toolBody.result!.url });
        assert.equal(second.status, 404);
    } finally {
        await new Promise<void>((resolve) => server.close(() => resolve()));
    }
});

test("[connector] P1-Q5 媒体读取：错误 token 的 /api/media/get 被拒", async () => {
    const { server } = await startApp();
    try {
        const res = await request(server, {
            method: "POST",
            path: "/api/media/get?token=wrong",
            headers: jsonHeaders(),
            body: JSON.stringify({ nodeId: "n1" }),
        });
        assert.notEqual(res.status, 200);
    } finally {
        await new Promise<void>((resolve) => server.close(() => resolve()));
    }
});
