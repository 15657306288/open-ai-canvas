import assert from "node:assert/strict";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { createJsonCatalogProvider } from "../src/channel-catalog.js";
import { createChannelGenerateClient } from "../src/channel-generate.js";
import { createOpenApiHandler } from "../src/openapi-server.js";
import type { ChannelToolContext } from "../src/channel-tools.js";

const origin = "http://127.0.0.1:3000";

function makeCatalog(dir: string): string {
    const file = path.join(dir, "catalog.json");
    fs.writeFileSync(file, JSON.stringify({
        version: 7,
        updatedAt: "2026-09-04T00:00:00Z",
        channels: [
            { id: "c1", name: "mock-text", baseUrl: "https://mock.example.com", apiKey: "secret-key-c1", capabilities: ["text"] },
            { id: "c2", name: "mock-video", baseUrl: "https://mock.example.com", apiKey: "secret-key-c2", capabilities: ["video"] },
        ],
        models: [
            { channelId: "c1", key: "gpt-4o-mini", name: "gpt-4o-mini", capability: "text", pricing: { input: 0.5, output: 1.5 } },
            { channelId: "c2", key: "artdance-720p", name: "Artdance 720p", capability: "video" },
        ],
    }, null, 2));
    return file;
}

function startServer(ctx: ChannelToolContext): Promise<{ url: string; close: () => Promise<void> }> {
    const config = { url: "http://127.0.0.1:17999", token: "t", trustedWebOrigins: [origin], browserRegistrations: [] as never[] };
    const handler = createOpenApiHandler(config, ctx);
    const server = http.createServer((req, res) => {
        // 原生 http server 无 body parser：手动收集 body 注入 req.body（等价于 express.raw）
        const chunks: Buffer[] = [];
        req.on("data", (c) => chunks.push(c as Buffer));
        req.on("end", () => {
            const raw = Buffer.concat(chunks).toString("utf8");
            (req as { body?: unknown }).body = raw.trim() ? Buffer.from(raw) : Buffer.alloc(0);
            handler(req as never, res as never);
        });
    });
    return new Promise((resolve) => {
        server.listen(0, "127.0.0.1", () => {
            const a = server.address() as { port: number };
            resolve({
                url: `http://127.0.0.1:${a.port}`,
                close: () => new Promise<void>((r) => { server.closeAllConnections?.(); server.close(() => r()); }),
            });
        });
    });
}

test("[connector] P2 §9.1 OpenAPI 深度联调：渠道读工具经门面真实调用（model_list / channel_list / catalog_version）", async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "oapi-deep-"));
    const catalog = createJsonCatalogProvider(makeCatalog(dir));
    const ctx: ChannelToolContext = { catalog, generate: createChannelGenerateClient(catalog) };
    const server = await startServer(ctx);
    try {
        const list = await fetch(`${server.url}/tools/channel_list`, {
            method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({}),
        });
        assert.equal(list.status, 200);
        const listBody = (await list.json()) as { ok: boolean; result: Array<{ id: string; apiKey?: string }> };
        assert.equal(listBody.ok, true);
        assert.equal(listBody.result.length, 2);
        // 密钥隔离：经 OpenAPI 门面返回的渠道视图绝不含 apiKey
        assert.equal(listBody.result[0].apiKey, undefined, "channel_list 不得泄漏 apiKey");

        const models = await fetch(`${server.url}/tools/channel_list_models`, {
            method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ channelId: "c2" }),
        });
        assert.equal(models.status, 200);
        const modelsBody = (await models.json()) as { ok: boolean; result: Array<{ key: string }> };
        assert.equal(modelsBody.result.length, 1);
        assert.equal(modelsBody.result[0].key, "artdance-720p");

        const ver = await fetch(`${server.url}/tools/channel_catalog_version`, {
            method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({}),
        });
        assert.equal(ver.status, 200);
        const verBody = (await ver.json()) as { ok: boolean; result: { version: string } };
        assert.equal(verBody.result.version, "7");
    } finally {
        await server.close();
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test("[connector] P2 §9.1 OpenAPI 深度联调：spec 为渠道工具生成完整 JSON Schema（含必填字段）", async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "oapi-deep-"));
    const catalog = createJsonCatalogProvider(makeCatalog(dir));
    const ctx: ChannelToolContext = { catalog, generate: createChannelGenerateClient(catalog) };
    const server = await startServer(ctx);
    try {
        const specRes = await fetch(`${server.url}/openapi.json`);
        assert.equal(specRes.status, 200);
        const spec = (await specRes.json()) as {
            paths: Record<string, { post: { operationId: string; requestBody: { content: { "application/json": { schema: { properties: Record<string, unknown>; required?: string[] } } } } } }>;
        };
        const channelModels = spec.paths["/tools/channel_list_models"].post;
        assert.equal(channelModels.operationId, "channel_list_models");
        // channelId 是 optional 过滤字段：出现在 properties，不在 required
        assert.ok(channelModels.requestBody.content["application/json"].schema.properties.channelId);
        assert.ok(spec.paths["/tools/channel_generate"]);
        assert.ok(spec.paths["/.well-known/agent.json"] === undefined, "agent.json 是 GET 元数据端点，不出现在工具 paths");
    } finally {
        await server.close();
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test("[connector] P2 §9.4 Agent Card 经门面服务：GET /.well-known/agent.json", async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "oapi-deep-"));
    const catalog = createJsonCatalogProvider(makeCatalog(dir));
    const ctx: ChannelToolContext = { catalog, generate: createChannelGenerateClient(catalog) };
    const server = await startServer(ctx);
    try {
        const res = await fetch(`${server.url}/.well-known/agent.json`);
        assert.equal(res.status, 200);
        const card = (await res.json()) as { name: string; capabilities: string[]; endpoints: { mcp: string; openapi: string } };
        assert.equal(card.name, "yingce-canvas（影策画布连接器）");
        assert.ok(card.capabilities.includes("channel-generate"));
        assert.equal(card.endpoints.openapi, "http://127.0.0.1:17999/openapi.json");
    } finally {
        await server.close();
        fs.rmSync(dir, { recursive: true, force: true });
    }
});
