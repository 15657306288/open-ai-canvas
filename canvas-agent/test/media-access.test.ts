import assert from "node:assert/strict";
import http, { type Server } from "node:http";
import { test } from "node:test";

import { CanvasMediaAccess } from "../src/media-access.js";
import type { CanvasNode } from "../src/types.js";

function node(overrides: Partial<CanvasNode> = {}): CanvasNode {
    return {
        id: "n1",
        type: "image",
        position: { x: 0, y: 0 },
        width: 100,
        height: 100,
        metadata: { url: "https://example.com/pic.png", prompt: "测试" },
        ...overrides,
    };
}

const PNG_1PX = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==", "base64");

test("[connector] P1-Q5 media-access：dataUrl 解码返回 block base64", async () => {
    const access = new CanvasMediaAccess();
    const n = node({ metadata: { dataUrl: `data:image/png;base64,${PNG_1PX.toString("base64")}` } });
    const result = await access.getNodeMedia(n, { mode: "block" });
    assert.equal(result.mode, "block");
    const block = result as { mode: "block"; mimeType: string; dataBase64: string; bytes: number };
    assert.equal(block.mimeType, "image/png");
    assert.equal(block.bytes, PNG_1PX.length);
    assert.equal(block.dataBase64, PNG_1PX.toString("base64"));
});

test("[connector] P1-Q5 media-access：http 引用加载 + 超限提示改用 url", async () => {
    const server = http.createServer((_req, res) => {
        res.writeHead(200, { "content-type": "image/png" });
        res.end(PNG_1PX);
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
    const a = server.address();
    const port = a && typeof a === "object" ? a.port : 0;
    try {
        const access = new CanvasMediaAccess();
        const n = node({ metadata: { url: `http://127.0.0.1:${port}/pic.png` } });
        const result = await access.getNodeMedia(n, { mode: "block" });
        assert.equal(result.mode, "block");
        assert.equal((result as { bytes: number }).bytes, PNG_1PX.length);

        // 超限：block 上限很小 → 应报错提示 url
        await assert.rejects(
            () => access.getNodeMedia(n, { mode: "block", maxBytes: 4 }),
            /改用 mode=url/,
        );
    } finally {
        await new Promise<void>((resolve) => {
            server.closeAllConnections?.();
            server.close(() => resolve());
        });
    }
});

test("[connector] P1-Q5 media-access：短 TTL 单次签名 URL 生成/消费/二次拒绝", async () => {
    const access = new CanvasMediaAccess();
    const n = node();
    const result = await access.getNodeMedia(n, { mode: "url" });
    assert.equal(result.mode, "url");
    const urlResult = result as { mode: "url"; url: string; token: string; expiresAtMs: number };
    assert.match(urlResult.url, /^\/api\/media\/[a-f0-9]{32}$/);
    assert.ok(urlResult.expiresAtMs > Date.now());

    // 消费一次成功
    const claimed = access.consumeToken(urlResult.token);
    assert.ok(claimed, "签名 token 应可消费一次");
    assert.equal(claimed.nodeId, "n1");

    // 二次消费被拒（单次）
    assert.equal(access.consumeToken(urlResult.token), undefined, "签名 token 单次消费后应失效");
});

test("[connector] P1-Q5 media-access：过期 token 被拒 + 审计记录", async () => {
    const audits: Array<{ nodeId: string; mode: string; bytes: number }> = [];
    const access = new CanvasMediaAccess({
        urlTtlMs: -1000, // 立即过期
        onAudit: (entry) => audits.push(entry),
    });
    const n = node({ metadata: { dataUrl: `data:image/png;base64,${PNG_1PX.toString("base64")}` } });
    const result = await access.getNodeMedia(n, { mode: "url" });
    const urlResult = result as { token: string };
    assert.equal(access.consumeToken(urlResult.token), undefined, "过期 token 应被拒");
    assert.ok(audits.some((entry) => entry.mode === "url"), "应记录 url 读取审计");
});

test("[connector] P1-Q5 media-access：无媒体节点报明确错误", async () => {
    const access = new CanvasMediaAccess();
    const n = node({ metadata: { prompt: "只有文字" } });
    await assert.rejects(() => access.getNodeMedia(n, { mode: "block" }), /没有可读媒体/);
});
