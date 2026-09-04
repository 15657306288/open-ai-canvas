import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { createJsonCatalogProvider } from "../src/channel-catalog.js";

function makeCatalogFile(dir: string, content: unknown): string {
    const file = path.join(dir, "channel-catalog.json");
    fs.writeFileSync(file, JSON.stringify(content, null, 2));
    return file;
}

const catalog = {
    version: 3,
    updatedAt: "2026-09-04T00:00:00Z",
    channels: [
        { id: "ch_a6api", name: "a6api 文本", protocol: "openai-compatible", baseUrl: "https://api.a6api.com/v1", apiKey: "sk-a6api-secret", enabled: true },
        { id: "ch_artbox", name: "artbox 视频", protocol: "openai-compatible", baseUrl: "https://artbox.top", apiKey: "sk-artbox-secret", enabled: true },
        { id: "ch_hn", name: "红鸟", protocol: "openai-compatible", baseUrl: "https://open.hongniaoai.com/api/v1", apiKey: "sk-hn-secret", enabled: true, videoUrl: "/video/generations", taskUrl: "/tasks/{requestId}" },
    ],
    models: [
        { key: "gpt-4o-mini", channelId: "ch_a6api", capability: "text", enabled: true, pricing: { type: "per_call", amount: 0.01, currency: "CNY" } },
        { key: "hailuo-h3", channelId: "ch_hn", capability: "video", enabled: true, pricing: { type: "per_call", amount: 1.8, currency: "CNY" } },
        { key: "flux", channelId: "ch_a6api", capability: "image", enabled: false },
    ],
    logicalModels: [
        { id: "lm_a6api-chat", name: "a6api 对话", capability: "text", lines: ["ch_a6api:gpt-4o-mini"] },
    ],
};

test("[connector] P0-B-4 catalog：目录读取、密钥隔离、过滤与版本探测", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "catalog-test-"));
    const file = makeCatalogFile(dir, catalog);
    const provider = createJsonCatalogProvider(file);
    try {
        const channels = provider.listChannels();
        assert.equal(channels.length, 3);
        // 密钥隔离：只读视图绝不含 apiKey
        for (const channel of channels) {
            assert.ok(!("apiKey" in channel), "只读视图不得包含 apiKey");
        }
        assert.equal(channels[0].modelCount, 2);

        const models = provider.listModels({ channelId: "ch_a6api" });
        assert.equal(models.length, 2);
        const enabledOnly = provider.listModels({ enabled: true, capability: "text" });
        assert.equal(enabledOnly.length, 1);
        assert.equal(enabledOnly[0].key, "gpt-4o-mini");

        // 内部解析可拿到密钥（仅 generate 用）
        assert.equal(provider.resolveChannel("ch_hn")?.apiKey, "sk-hn-secret");

        const logical = provider.listLogicalModels();
        assert.equal(logical[0].id, "lm_a6api-chat");

        const version = provider.catalogVersion();
        assert.equal(version.version, "3");
        assert.equal(version.counts.channels, 3);
        assert.equal(version.counts.models, 3);
        assert.ok(version.hash.length >= 12);
    } finally {
        provider.close();
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test("[connector] P0-B-4 catalog：文件变更触发 onChange（层3）且版本/数据实时刷新（层1）", async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "catalog-watch-"));
    const file = makeCatalogFile(dir, catalog);
    const provider = createJsonCatalogProvider(file);
    const changes: number[] = [];
    provider.onChange(() => changes.push(1));
    try {
        const before = provider.catalogVersion();
        assert.equal(provider.listModels().length, 3);

        // 修改文件：加一个模型
        const updated = { ...catalog, models: [...catalog.models, { key: "new-model", channelId: "ch_artbox", capability: "video", enabled: true }] };
        makeCatalogFile(dir, updated);

        // 层3：onChange 应在文件变更后触发（轮询等待 fs.watch 事件）
        const deadline = Date.now() + 3000;
        while (changes.length === 0 && Date.now() < deadline) {
            await new Promise((resolve) => setTimeout(resolve, 50));
        }
        assert.ok(changes.length >= 1, "目录文件变更应触发 onChange");

        // 层1：下一次 list/version 立即反映新模型
        assert.equal(provider.listModels().length, 4);
        const after = provider.catalogVersion();
        assert.notEqual(after.hash, before.hash, "内容变化后 hash 应更新");
        assert.equal(after.counts.models, 4);
    } finally {
        provider.close();
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test("[connector] P0-B-4 catalog：文件不存在/非法时降级为空目录不崩", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "catalog-empty-"));
    const missing = path.join(dir, "nope.json");
    const provider = createJsonCatalogProvider(missing);
    try {
        assert.deepEqual(provider.listChannels(), []);
        assert.equal(provider.catalogVersion().counts.models, 0);
        assert.equal(provider.resolveChannel("x"), undefined);
    } finally {
        provider.close();
        fs.rmSync(dir, { recursive: true, force: true });
    }
});
