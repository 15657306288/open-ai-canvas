import assert from "node:assert/strict";
import http, { type Server } from "node:http";
import { test } from "node:test";

import { createCanvasBridgeBroker } from "../src/bridge/broker.js";
import { createCanvasBridgeClient } from "../src/bridge/client.js";

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

interface TestHarness {
    brokerServer: Server;
    runtimeServer: Server;
    brokerUrl: string;
    runtimeUrl: string;
    close: () => Promise<void>;
}

async function startHarness(): Promise<TestHarness> {
    const broker = createCanvasBridgeBroker();
    const brokerServer = http.createServer((req, res) => broker.handle(req, res));
    await new Promise<void>((resolve) => brokerServer.listen(0, "127.0.0.1", () => resolve()));

    // 假 Runtime：/api/tools 收到调用后回显
    const runtimeServer = http.createServer((req, res) => {
        let raw = "";
        req.on("data", (chunk) => (raw += chunk));
        req.on("end", () => {
            const body = JSON.parse(raw || "{}") as { name?: string; input?: Record<string, unknown> };
            res.writeHead(200, { "content-type": "application/json" });
            res.end(JSON.stringify({
                ok: true,
                result: { echoedName: body.name, echoedInput: body.input },
            }));
        });
    });
    await new Promise<void>((resolve) => runtimeServer.listen(0, "127.0.0.1", () => resolve()));

    const addr = (server: Server) => {
        const a = server.address();
        return a && typeof a === "object" ? a.port : 0;
    };
    const brokerUrl = `http://127.0.0.1:${addr(brokerServer)}`;
    const runtimeUrl = `http://127.0.0.1:${addr(runtimeServer)}`;
    return {
        brokerServer,
        runtimeServer,
        brokerUrl,
        runtimeUrl,
        close: async () => {
            broker.close();
            for (const server of [brokerServer, runtimeServer]) {
                await new Promise<void>((resolve) => {
                    server.closeAllConnections?.();
                    server.close(() => resolve());
                });
            }
        },
    };
}

test("[connector] P0-B-3 bridge 全链路：远程 Agent 经 Broker 调用本地画布工具", async () => {
    const h = await startHarness();
    const bridgeId = "test-mac-runtime";
    const bridgeToken = "bridge-secret-token";
    const client = createCanvasBridgeClient({
        server: h.brokerUrl,
        bridgeId,
        token: bridgeToken,
        endpoint: h.runtimeUrl,
        runtimeToken: "master-token",
        pollSeconds: 1,
        heartbeatSeconds: 2,
    });
    await client.start();
    try {
        // 等 client 完成 register + 首次 poll 就位
        await sleep(400);

        // 远程 Agent 提交一个工具调用
        const submitRes = await fetch(`${h.brokerUrl}/api/canvas-bridge/request`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ bridgeId, name: "canvas_get_context", input: { canvasId: "c1" } }),
        });
        assert.equal(submitRes.status, 200);
        const submit = (await submitRes.json()) as { data: { requestId: string; status: string } };
        assert.ok(submit.data.requestId);

        // Agent 轮询结果直到 succeeded
        let finalStatus = "";
        let finalResult: unknown;
        for (let i = 0; i < 20; i++) {
            const pollRes = await fetch(`${h.brokerUrl}/api/canvas-bridge/request/${submit.data.requestId}`);
            const poll = (await pollRes.json()) as { data: { status: string; result?: unknown; error?: string } };
            finalStatus = poll.data.status;
            finalResult = poll.data.result;
            if (finalStatus === "succeeded" || finalStatus === "failed") break;
            await sleep(150);
        }
        assert.equal(finalStatus, "succeeded", "远程请求应被本地 client 处理成功");
        const result = finalResult as { echoedName?: string; echoedInput?: Record<string, unknown> };
        assert.equal(result.echoedName, "canvas_get_context");
        assert.equal(result.echoedInput?.canvasId, "c1");

        // client 累计计数
        assert.deepEqual(client.stats(), { processed: 1, failed: 0 });
    } finally {
        client.stop();
        await h.close();
    }
});

test("[connector] P0-B-3 鉴权：错误 token 的 client poll 被拒", async () => {
    const h = await startHarness();
    const bridgeId = "auth-test-bridge";
    // 先正常注册
    const reg = await fetch(`${h.brokerUrl}/api/canvas-bridge/register`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ bridgeId, token: "correct-token", endpoint: h.runtimeUrl }),
    });
    assert.equal(reg.status, 200);

    // 错误 token poll → 401
    const badPoll = await fetch(`${h.brokerUrl}/api/canvas-bridge/poll?bridgeId=${bridgeId}&wait=0`, {
        headers: { authorization: "Bearer wrong-token" },
    });
    assert.equal(badPoll.status, 401);

    // 正确 token poll → 200 且 request: null（队列空）
    const goodPoll = await fetch(`${h.brokerUrl}/api/canvas-bridge/poll?bridgeId=${bridgeId}&wait=0`, {
        headers: { authorization: "Bearer correct-token" },
    });
    assert.equal(goodPoll.status, 200);
    const body = (await goodPoll.json()) as { data: { request: unknown } };
    assert.equal(body.data.request, null);
    await h.close();
});

test("[connector] P0-B-3 未注册 bridge 的 request 被拒 + bridges 列表在线状态", async () => {
    const h = await startHarness();
    const res = await fetch(`${h.brokerUrl}/api/canvas-bridge/request`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ bridgeId: "nope", name: "canvas_get_context", input: {} }),
    });
    assert.equal(res.status, 404);

    const client = createCanvasBridgeClient({
        server: h.brokerUrl,
        bridgeId: "list-test",
        token: "t",
        endpoint: h.runtimeUrl,
        runtimeToken: "m",
        pollSeconds: 1,
    });
    await client.start();
    await sleep(300);
    const listRes = await fetch(`${h.brokerUrl}/api/canvas-bridge/bridges`);
    const list = (await listRes.json()) as { data: { bridges: Array<{ bridgeId: string; online: boolean }> } };
    const found = list.data.bridges.find((b) => b.bridgeId === "list-test");
    assert.ok(found, "list-test 应在在线列表");
    assert.equal(found.online, true);
    client.stop();
    await h.close();
});

test("[connector] P2 §9.5 bridge 限流：超频 request 返回 429", async () => {
    const broker = createCanvasBridgeBroker({ rateLimit: { maxRequests: 3, windowMs: 60_000 } });
    const brokerServer = http.createServer((req, res) => broker.handle(req, res));
    await new Promise<void>((resolve) => brokerServer.listen(0, "127.0.0.1", () => resolve()));
    const port = (brokerServer.address() as { port: number }).port;
    const base = `http://127.0.0.1:${port}`;
    try {
        await fetch(`${base}/api/canvas-bridge/register`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ bridgeId: "rate-b", token: "rate-t", endpoint: "http://local" }),
        });
        for (let i = 0; i < 3; i++) {
            const r = await fetch(`${base}/api/canvas-bridge/request`, {
                method: "POST",
                headers: { "content-type": "application/json" },
                body: JSON.stringify({ bridgeId: "rate-b", name: "canvas_get_state", input: {} }),
            });
            assert.equal(r.status, 200, `第 ${i + 1} 次应在限流内`);
        }
        const limited = await fetch(`${base}/api/canvas-bridge/request`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ bridgeId: "rate-b", name: "canvas_get_state", input: {} }),
        });
        assert.equal(limited.status, 429);
        const body = (await limited.json()) as { code: number; msg: string };
        assert.equal(body.code, 42901);
        assert.match(body.msg, /过于频繁/);
    } finally {
        broker.close();
        brokerServer.closeAllConnections?.();
        await new Promise<void>((resolve) => brokerServer.close(() => resolve()));
    }
});

test("[connector] L3 注册鉴权：首次注册必须使用 registration token", async () => {
    const broker = createCanvasBridgeBroker({ registrationToken: "registration-secret" });
    const server = http.createServer((req, res) => broker.handle(req, res));
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
    const port = (server.address() as { port: number }).port;
    const base = `http://127.0.0.1:${port}`;
    const body = JSON.stringify({ bridgeId: "registration-bridge", token: "bridge-secret", endpoint: "http://local" });
    const post = async (authorization?: string) => fetch(`${base}/api/canvas-bridge/register`, {
        method: "POST",
        headers: {
            "content-type": "application/json",
            ...(authorization ? { authorization } : {}),
        },
        body,
    });
    try {
        assert.equal((await post()).status, 401);
        assert.equal((await post("Bearer wrong-registration-secret")).status, 401);
        assert.equal((await post("Bearer registration-secret")).status, 200);
    } finally {
        broker.close();
        server.closeAllConnections?.();
        await new Promise<void>((resolve) => server.close(() => resolve()));
    }
});

test("[connector] L3 bridge token 轮换：只有注册凭据可以替换 token", async () => {
    const broker = createCanvasBridgeBroker({ registrationToken: "registration-secret" });
    const server = http.createServer((req, res) => broker.handle(req, res));
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
    const port = (server.address() as { port: number }).port;
    const base = `http://127.0.0.1:${port}`;
    const register = (token: string, authorization?: string) => fetch(`${base}/api/canvas-bridge/register`, {
        method: "POST",
        headers: {
            "content-type": "application/json",
            ...(authorization ? { authorization } : {}),
        },
        body: JSON.stringify({ bridgeId: "rotation-bridge", token, endpoint: "http://local" }),
    });
    const heartbeat = (token: string) => fetch(`${base}/api/canvas-bridge/heartbeat`, {
        method: "POST",
        headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
        body: JSON.stringify({ bridgeId: "rotation-bridge" }),
    });
    try {
        assert.equal((await register("old-token", "Bearer registration-secret")).status, 200);
        assert.equal((await register("new-token", "Bearer old-token")).status, 401);
        assert.equal((await register("new-token", "Bearer registration-secret")).status, 200);
        assert.equal((await heartbeat("old-token")).status, 401);
        assert.equal((await heartbeat("new-token")).status, 200);
    } finally {
        broker.close();
        server.closeAllConnections?.();
        await new Promise<void>((resolve) => server.close(() => resolve()));
    }
});

test("[connector] L3 Agent API 鉴权：request/query/bridges 使用独立 agent token", async () => {
    const broker = createCanvasBridgeBroker({
        agentToken: "agent-secret",
        registrationToken: "registration-secret",
    });
    const server = http.createServer((req, res) => broker.handle(req, res));
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
    const port = (server.address() as { port: number }).port;
    const base = `http://127.0.0.1:${port}`;
    const agentHeaders = { authorization: "Bearer agent-secret" };
    const register = await fetch(`${base}/api/canvas-bridge/register`, {
        method: "POST",
        headers: { authorization: "Bearer registration-secret", "content-type": "application/json" },
        body: JSON.stringify({ bridgeId: "agent-api-bridge", token: "bridge-secret", endpoint: "http://local" }),
    });
    assert.equal(register.status, 200);
    try {
        const requestBody = JSON.stringify({ bridgeId: "agent-api-bridge", name: "canvas_get_state", input: {} });
        const unauthenticatedRequest = await fetch(`${base}/api/canvas-bridge/request`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: requestBody,
        });
        assert.equal(unauthenticatedRequest.status, 401);
        assert.equal((await fetch(`${base}/api/canvas-bridge/bridges`)).status, 401);
        const submit = await fetch(`${base}/api/canvas-bridge/request`, {
            method: "POST",
            headers: { "content-type": "application/json", ...agentHeaders },
            body: requestBody,
        });
        assert.equal(submit.status, 200);
        const submitBody = (await submit.json()) as { data: { requestId: string } };
        const query = await fetch(`${base}/api/canvas-bridge/request/${encodeURIComponent(submitBody.data.requestId)}`, { headers: agentHeaders });
        assert.equal(query.status, 200);
        assert.equal((await fetch(`${base}/api/canvas-bridge/bridges`, { headers: agentHeaders })).status, 200);
    } finally {
        broker.close();
        server.closeAllConnections?.();
        await new Promise<void>((resolve) => server.close(() => resolve()));
    }
});

test("[connector] L3 队列满时保留 pending/running，只清理 done", async () => {
    const broker = createCanvasBridgeBroker({ maxPendingPerBridge: 2 });
    const server = http.createServer((req, res) => broker.handle(req, res));
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
    const port = (server.address() as { port: number }).port;
    const base = `http://127.0.0.1:${port}`;
    const bridgeId = "queue-bridge";
    const bridgeToken = "queue-token";
    const register = await fetch(`${base}/api/canvas-bridge/register`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ bridgeId, token: bridgeToken, endpoint: "http://local" }),
    });
    assert.equal(register.status, 200);
    try {
        const first = broker.enqueueForTest(bridgeId, { name: "first", input: {} });
        const second = broker.enqueueForTest(bridgeId, { name: "second", input: {} });
        assert.ok(first);
        assert.ok(second);
        const poll = await fetch(`${base}/api/canvas-bridge/poll?bridgeId=${bridgeId}&wait=0`, {
            headers: { authorization: `Bearer ${bridgeToken}` },
        });
        assert.equal(poll.status, 200);
        const running = (await poll.json()) as { data: { request: { requestId: string } } };
        assert.equal(running.data.request.requestId, first);

        assert.equal(broker.enqueueForTest(bridgeId, { name: "third", input: {} }), undefined);
        assert.deepEqual(
            broker.listBridges()[0]?.queue.map((item) => [item.requestId, item.status]),
            [[first, "running"], [second, "pending"]],
        );

        const result = await fetch(`${base}/api/canvas-bridge/result`, {
            method: "POST",
            headers: { authorization: `Bearer ${bridgeToken}`, "content-type": "application/json" },
            body: JSON.stringify({ bridgeId, requestId: first, status: "succeeded", result: { ok: true } }),
        });
        assert.equal(result.status, 200);
        const third = broker.enqueueForTest(bridgeId, { name: "third", input: {} });
        assert.ok(third);
        assert.deepEqual(
            broker.listBridges()[0]?.queue.map((item) => [item.requestId, item.status]),
            [[second, "pending"], [third, "pending"]],
        );
    } finally {
        broker.close();
        server.closeAllConnections?.();
        await new Promise<void>((resolve) => server.close(() => resolve()));
    }
});
