import assert from "node:assert/strict";
import http, { type Server } from "node:http";
import { test } from "node:test";

import { createLocalRuntimeApp, type LocalRuntimeModule } from "../src/local-runtime.js";
import { LocalRuntimeSessionManager } from "../src/local-runtime-session.js";

// 用可变 health 状态 + 单个共享 server，避免并行全量测试下的端口竞态
const state: Record<string, string | number | boolean> = { hasCanvas: false, clients: 0, reconnecting: 0 };

function buildApp(port: number) {
    const authority = `127.0.0.1:${port}`;
    const endpoint = `http://${authority}`;
    const module: LocalRuntimeModule = {
        descriptor: { id: "canvas-agent", displayName: "Canvas Agent", apiVersion: 1, scopes: ["canvas:connect"] },
        routes: [],
        publicHealth: () => ({ ...state }),
    };
    const manager = new LocalRuntimeSessionManager({
        endpoint,
        trustedOrigins: ["http://127.0.0.1:3000"],
        registrations: [],
    });
    return createLocalRuntimeApp({ authority, endpoint, version: "0.1.0", sessionManager: manager, modules: [module] });
}

async function startSharedServer() {
    const probe = http.createServer();
    await new Promise<void>((resolve) => probe.listen(0, "127.0.0.1", () => resolve()));
    const address = probe.address();
    const port = address && typeof address === "object" ? address.port : 0;
    await new Promise<void>((resolve) => probe.close(() => resolve()));
    const server = buildApp(port).listen(port, "127.0.0.1");
    await new Promise<void>((resolve) => server.once("listening", () => resolve()));
    const url = `http://127.0.0.1:${port}`;
    return { server: server as Server, url };
}

async function getHealth(url: string) {
    const res = await fetch(`${url}/health`);
    return { status: res.status, body: await res.json() as Record<string, unknown> };
}

let shared: { server: Server; url: string } | null = null;
test("[connector] P0-A-5 shared health server starts", async () => {
    shared = await startSharedServer();
    assert.ok(shared);
});

test("[connector] P0-A-5 /health reports offline before any canvas connects", async () => {
    state.hasCanvas = false; state.clients = 0; state.reconnecting = 0;
    const { status, body } = await getHealth(shared!.url);
    assert.equal(status, 200);
    assert.equal(body.ok, true);
    assert.equal(body.status, "offline");
});

test("[connector] P0-A-5 /health reports healthy with an active canvas client", async () => {
    state.hasCanvas = true; state.clients = 1; state.reconnecting = 0;
    const { body } = await getHealth(shared!.url);
    assert.equal(body.status, "healthy");
});

test("[connector] P0-A-5 /health reports reconnecting while a stream is in grace", async () => {
    state.hasCanvas = true; state.clients = 0; state.reconnecting = 1;
    const { body } = await getHealth(shared!.url);
    assert.equal(body.status, "reconnecting");
});

test("[connector] P0-A-5 /health reports degraded when canvas exists but no live client", async () => {
    state.hasCanvas = true; state.clients = 0; state.reconnecting = 0;
    const { body } = await getHealth(shared!.url);
    assert.equal(body.status, "degraded");
});

test("[connector] P0-A-5 shared health server closes", async () => {
    await new Promise<void>((resolve) => shared!.server.close(() => resolve()));
    shared = null;
});
