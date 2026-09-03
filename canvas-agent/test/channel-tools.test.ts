import assert from "node:assert/strict";
import fs from "node:fs";
import http, { type Server } from "node:http";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { createJsonCatalogProvider } from "../src/channel-catalog.js";
import { createChannelGenerateClient } from "../src/channel-generate.js";
import { channelToolDefs, channelToolNames } from "../src/channel-tools.js";

function makeCatalog(dir: string, baseUrl: string): string {
    const file = path.join(dir, "channel-catalog.json");
    fs.writeFileSync(file, JSON.stringify({
        version: 1,
        channels: [{ id: "mock-chat", name: "Mock 文本", protocol: "openai-compatible", baseUrl, apiKey: "sk-mock", enabled: true }],
        models: [{ key: "mock-model", channelId: "mock-chat", capability: "text", enabled: true, pricing: { type: "per_call", amount: 0.01, currency: "CNY" } }],
        logicalModels: [{ id: "mock-chat", name: "Mock 对话", capability: "text", lines: ["mock-chat:mock-model"] }],
    }, null, 2));
    return file;
}

function startMockOpenAi(): Promise<{ server: Server; port: number }> {
    const server = http.createServer((req, res) => {
        let raw = "";
        req.on("data", (c) => (raw += c));
        req.on("end", () => {
            const body = raw ? JSON.parse(raw) : {};
            if (req.url?.includes("/v1/chat/completions")) {
                // one-api 风格业务码：HTTP 200 + body.code 非 200
                if (body.model === "biz-err") {
                    res.writeHead(200, { "content-type": "application/json" });
                    res.end(JSON.stringify({ code: 404, msg: "Cannot POST /v1/chat/completions", data: null }));
                    return;
                }
                res.writeHead(200, { "content-type": "application/json" });
                res.end(JSON.stringify({ choices: [{ message: { content: `echo:${body.messages?.[0]?.content}` } }] }));
                return;
            }
            res.writeHead(404, { "content-type": "application/json" });
            res.end(JSON.stringify({ error: { message: "not found" } }));
        });
    });
    return new Promise((resolve) => {
        server.listen(0, "127.0.0.1", () => {
            const a = server.address();
            resolve({ server, port: a && typeof a === "object" ? a.port : 0 });
        });
    });
}

async function closeServer(server: Server) {
    await new Promise<void>((resolve) => {
        server.closeAllConnections?.();
        server.close(() => resolve());
    });
}

test("[connector] P0-B-4 工具集：7 个渠道工具注册齐全且 schema 稳定", () => {
    assert.deepEqual(channelToolNames, [
        "channel_list",
        "channel_list_models",
        "model_list_logical",
        "model_get_capability",
        "channel_catalog_version",
        "channel_generate",
        "channel_get_task",
    ]);
});

test("[connector] P0-B-4 channel_generate/get_task：直连 OpenAI 兼容渠道发起文本生成", async () => {
    const { server, port } = await startMockOpenAi();
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "channel-gen-"));
    // baseUrl 为 OpenAI 兼容根（不含 /v1），text 拼接 /v1/chat/completions
    const file = makeCatalog(dir, `http://127.0.0.1:${port}`);
    const catalog = createJsonCatalogProvider(file);
    const ctx = { catalog, generate: createChannelGenerateClient(catalog) };
    try {
        const genTool = channelToolDefs.find((t) => t.name === "channel_generate")!;
        const submitted = await genTool.handler(ctx, {
            channelId: "mock-chat",
            model: "mock-model",
            capability: "text",
            prompt: "你好",
        }) as { taskId: string; status: string };
        assert.equal(submitted.status, "succeeded");

        const getTool = channelToolDefs.find((t) => t.name === "channel_get_task")!;
        const task = await getTool.handler(ctx, { taskId: submitted.taskId }) as { status: string; result: { content: string } };
        assert.equal(task.status, "succeeded");
        assert.equal(task.result.content, "echo:你好");

        // 只读工具
        const listTool = channelToolDefs.find((t) => t.name === "channel_list")!;
        const channels = await listTool.handler(ctx, {}) as Array<{ id: string; modelCount: number }>;
        assert.equal(channels[0].id, "mock-chat");
        assert.ok(!("apiKey" in channels[0]));

        const versionTool = channelToolDefs.find((t) => t.name === "channel_catalog_version")!;
        const version = await versionTool.handler(ctx, {}) as { counts: { channels: number; models: number } };
        assert.equal(version.counts.models, 1);

        const listModelsTool = channelToolDefs.find((t) => t.name === "channel_list_models")!;
        const models = await listModelsTool.handler(ctx, { capability: "text" }) as Array<{ key: string }>;
        assert.equal(models[0].key, "mock-model");

        const capTool = channelToolDefs.find((t) => t.name === "model_get_capability")!;
        const cap = await capTool.handler(ctx, { model: "mock-model" }) as { capability: string };
        assert.equal(cap.capability, "text");
    } finally {
        catalog.close();
        fs.rmSync(dir, { recursive: true, force: true });
        await closeServer(server);
    }
});

test("[connector] P0-B-4 channel_generate：one-api 风格业务码（HTTP 200 + code≠200）报业务错误而非误判成功", async () => {
    const { server, port } = await startMockOpenAi();
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "channel-bizerr-"));
    const file = path.join(dir, "channel-catalog.json");
    fs.writeFileSync(file, JSON.stringify({
        version: 1,
        channels: [{ id: "one-api", name: "中转", protocol: "openai-compatible", baseUrl: `http://127.0.0.1:${port}`, apiKey: "sk-mock", enabled: true }],
        models: [{ key: "biz-err", channelId: "one-api", capability: "text", enabled: true }],
    }));
    const catalog = createJsonCatalogProvider(file);
    const ctx = { catalog, generate: createChannelGenerateClient(catalog) };
    try {
        const genTool = channelToolDefs.find((t) => t.name === "channel_generate")!;
        await assert.rejects(
            () => genTool.handler(ctx, { channelId: "one-api", model: "biz-err", capability: "text", prompt: "p" }),
            /Cannot POST/,
            "HTTP 200 + body.code=404 应被识别为业务错误并抛出，而非返回 running",
        );
    } finally {
        catalog.close();
        fs.rmSync(dir, { recursive: true, force: true });
        await closeServer(server);
    }
});

test("[connector] P0-B-4 channel_generate：错误渠道/未配置密钥返回清晰错误", async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "channel-gen-err-"));
    const file = makeCatalog(dir, "http://127.0.0.1:1/v1");
    const catalog = createJsonCatalogProvider(file);
    const ctx = { catalog, generate: createChannelGenerateClient(catalog) };
    try {
        const genTool = channelToolDefs.find((t) => t.name === "channel_generate")!;
        await assert.rejects(
            () => genTool.handler(ctx, { channelId: "nope", model: "x", capability: "text", prompt: "p" }),
            /渠道不存在/,
        );
        // 渠道存在但无 apiKey
        const dir2 = fs.mkdtempSync(path.join(os.tmpdir(), "channel-gen-nokey-"));
        const file2 = path.join(dir2, "channel-catalog.json");
        fs.writeFileSync(file2, JSON.stringify({
            version: 1,
            channels: [{ id: "no-key", name: "无密钥", protocol: "openai-compatible", baseUrl: "http://127.0.0.1:1/v1", enabled: true }],
            models: [],
        }));
        const catalog2 = createJsonCatalogProvider(file2);
        try {
            await assert.rejects(
                () => genTool.handler({ catalog: catalog2, generate: createChannelGenerateClient(catalog2) },
                    { channelId: "no-key", model: "x", capability: "text", prompt: "p" }),
                /未配置密钥/,
            );
        } finally {
            catalog2.close();
            fs.rmSync(dir2, { recursive: true, force: true });
        }
    } finally {
        catalog.close();
        fs.rmSync(dir, { recursive: true, force: true });
    }
});
