import assert from "node:assert/strict";
import http, { type Server } from "node:http";
import { test } from "node:test";

import { CanvasSession } from "../src/canvas-session.js";
import { createLocalRuntimeApp } from "../src/local-runtime.js";
import { LocalRuntimeSessionManager } from "../src/local-runtime-session.js";
import { createCanvasAgentHttpModule } from "../src/modules/canvas-agent-http.js";
import { buildAgentCard } from "../src/openapi-server.js";
import { createMetricsRegistry, getMetricsRegistry, resetMetricsRegistryForTest } from "../src/metrics.js";
import type { LocalRuntimeConfig } from "../src/config.js";

const authority = "127.0.0.1:41760";
const endpoint = `http://${authority}`;
const origin = "http://127.0.0.1:3001";
const token = "metrics-token";

function fixtureConfig(): LocalRuntimeConfig {
    return { url: endpoint, token, ownerId: "owner-metrics-001", origins: [origin], trustedWebOrigins: [origin], browserRegistrations: [], canvases: {} };
}

function request(server: Server, path: string): Promise<{ status: number; text: string; headers: http.IncomingHttpHeaders }> {
    return new Promise((resolve, reject) => {
        const req = http.request({
            host: "127.0.0.1",
            port: (server.address() as { port: number }).port,
            path,
            headers: { Host: authority },
        }, (res) => {
            const chunks: Buffer[] = [];
            res.on("data", (c) => chunks.push(c as Buffer));
            res.on("end", () => resolve({ status: res.statusCode ?? 0, text: Buffer.concat(chunks).toString("utf8"), headers: res.headers }));
        });
        req.on("error", reject);
        req.end();
    });
}

test("[connector] P2 §9.4 metrics registry：计数/时延/错误/快照/Prometheus", () => {
    const m = createMetricsRegistry();
    m.incCounter("tools.called.canvas_get_state");
    m.incCounter("tools.called.canvas_get_state");
    m.incCounter("channel.generate.video");
    m.observeLatency("tools", 10);
    m.observeLatency("tools", 30);
    m.setGauge("bridge.online", 2);

    const snap = m.snapshot();
    assert.equal(snap["counter.tools.called.canvas_get_state"], 2);
    assert.equal(snap["counter.channel.generate.video"], 1);
    assert.equal(snap["latency.tools.count"], 2);
    assert.equal(snap["latency.tools.avg_ms"], 20);
    assert.equal(snap["gauge.bridge.online"], 2);

    const prom = m.toPrometheus();
    assert.match(prom, /yingce_counter_tools_called_canvas_get_state 2/);
    assert.match(prom, /yingce_gauge_bridge_online 2/);
});

test("[connector] P2 §9.4 /metrics 端点：JSON 快照与 Prometheus 格式，含工具调用计数", async () => {
    resetMetricsRegistryForTest();
    const session = new CanvasSession({ metrics: getMetricsRegistry() });
    const module = createCanvasAgentHttpModule(fixtureConfig(), session);
    const manager = new LocalRuntimeSessionManager({ endpoint, trustedOrigins: [origin], registrations: [] });
    const app = createLocalRuntimeApp({
        authority, endpoint, version: "0.1.0", sessionManager: manager, modules: [module],
        legacyMasterToken: token, legacyOrigins: [origin],
    });
    const server = app.listen(0, "127.0.0.1");
    await new Promise<void>((resolve) => server.once("listening", () => resolve()));
    try {
        // 无画布时调用 canvas_get_state → 抛"没有已连接画布"，应计入 tools.called + tools.errors
        const res = await new Promise<{ status: number; text: string }>((resolve, reject) => {
            const req = http.request({
                host: "127.0.0.1", port: (server.address() as { port: number }).port,
                method: "POST", path: `/api/tools?token=${token}`,
                headers: { Host: authority, "content-type": "application/json" },
            }, (r) => {
                const chunks: Buffer[] = [];
                r.on("data", (c) => chunks.push(c as Buffer));
                r.on("end", () => resolve({ status: r.statusCode ?? 0, text: Buffer.concat(chunks).toString("utf8") }));
            });
            req.on("error", reject);
            req.end(JSON.stringify({ name: "canvas_get_state", input: {} }));
        });
        assert.equal(res.status, 200);
        assert.match(res.text, /没有已连接画布/);

        const json = await request(server, "/metrics");
        assert.equal(json.status, 200);
        assert.match(json.text, /"counter.tools.called.canvas_get_state":1/);
        assert.match(json.text, /"counter.tools.errors":1/);

        const prom = await request(server, "/metrics?format=prometheus");
        assert.equal(prom.status, 200);
        assert.match(prom.headers["content-type"] ?? "", /text\/plain/);
        assert.match(prom.text, /yingce_counter_tools_called_canvas_get_state 1/);
    } finally {
        await new Promise<void>((resolve) => server.close(() => resolve()));
        resetMetricsRegistryForTest();
    }
});

test("[connector] P2 §9.4 Agent Card：/.well-known/agent.json 结构完整", async () => {
    const card = buildAgentCard(fixtureConfig());
    assert.equal(card.name, "yingce-canvas（影策画布连接器）");
    assert.deepEqual(card.capabilities, ["canvas-read", "canvas-write", "media-read", "channel-read", "channel-generate"]);
    assert.equal(card.endpoints.mcp, `${endpoint}/mcp`);
    assert.equal(card.endpoints.openapi, `${endpoint}/openapi.json`);
    assert.equal(card.endpoints.metrics, `${endpoint}/metrics`);
    assert.ok(Array.isArray(card.protocol) && card.protocol.includes("MCP"));
});
