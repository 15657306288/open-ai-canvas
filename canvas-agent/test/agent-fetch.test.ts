import assert from "node:assert/strict";
import http, { type Server } from "node:http";
import { test } from "node:test";

import { agentFetch } from "../src/agent-fetch.js";

function startServer(handler: (req: http.IncomingMessage, res: http.ServerResponse) => void) {
    const server = http.createServer(handler);
    return new Promise<{ server: Server; url: string }>((resolve) => {
        server.listen(0, "127.0.0.1", () => {
            const address = server.address();
            assert.ok(address && typeof address === "object");
            resolve({ server, url: `http://127.0.0.1:${address.port}` });
        });
    });
}

function closeServer(server: Server) {
    return new Promise<void>((resolve) => server.close(() => resolve()));
}

test("[connector] P0-A-4 idempotent GET retries transient 5xx and succeeds", async () => {
    let hits = 0;
    const { server, url } = await startServer((_req, res) => {
        hits += 1;
        if (hits < 3) {
            res.writeHead(503, { "content-type": "application/json" });
            res.end("{}");
            return;
        }
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ ok: true }));
    });
    try {
        const res = await agentFetch(`${url}/health`);
        assert.equal(res.status, 200);
        assert.equal(hits, 3, "5xx 应被重试（默认 2 次重试 = 共 3 次尝试）");
    } finally {
        await closeServer(server);
    }
});

test("[connector] P0-A-4 idempotent GET gives up after retries on persistent 5xx", async () => {
    let hits = 0;
    const { server, url } = await startServer((_req, res) => {
        hits += 1;
        res.writeHead(500, { "content-type": "application/json" });
        res.end("{}");
    });
    try {
        const res = await agentFetch(`${url}/health`, { retries: 1, timeoutMs: 5_000 });
        assert.equal(res.status, 500, "重试耗尽后最后一次 5xx 原样返回给调用方，不吞错误");
        assert.equal(hits, 2, "retries=1 时共 2 次尝试");
    } finally {
        await closeServer(server);
    }
});

test("[connector] P0-A-4 non-idempotent POST is not retried", async () => {
    let hits = 0;
    const { server, url } = await startServer((_req, res) => {
        hits += 1;
        res.writeHead(502, { "content-type": "application/json" });
        res.end("{}");
    });
    try {
        const res = await agentFetch(`${url}/api/tools`, { method: "POST", body: "{}", timeoutMs: 5_000 });
        assert.equal(res.status, 502);
        assert.equal(hits, 1, "POST 非只读不应重试");
    } finally {
        await closeServer(server);
    }
});

test("[connector] P0-A-4 external abort is not retried", async () => {
    let hits = 0;
    const { server, url } = await startServer((_req, res) => {
        hits += 1;
        // 不响应，等待客户端 abort
        res.writeHead(200);
    });
    const controller = new AbortController();
    controller.abort();
    try {
        await assert.rejects(
            agentFetch(`${url}/health`, { signal: controller.signal }),
            (error: unknown) => error instanceof DOMException && error.name === "AbortError",
        );
        assert.equal(hits, 0, "外部已取消时不应发起请求");
    } finally {
        await closeServer(server);
    }
});

test("[connector] P0-A-4 timeout on a hanging endpoint surfaces AbortError", async () => {
    const { server, url } = await startServer(() => {
        // 挂起不响应，触发 agentFetch 超时
    });
    try {
        await assert.rejects(
            agentFetch(`${url}/health`, { timeoutMs: 100 }),
            (error: unknown) => error instanceof Error && error.name === "AbortError",
        );
    } finally {
        await closeServer(server);
    }
});
