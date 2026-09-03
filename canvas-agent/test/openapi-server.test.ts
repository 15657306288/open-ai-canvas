import assert from "node:assert/strict";
import http, { type Server } from "node:http";
import { test } from "node:test";

import { buildOpenApiSpec, createOpenApiHandler } from "../src/openapi-server.js";
import { toolNames } from "../src/schemas.js";

const origin = "http://127.0.0.1:3000";

test("[connector] P0-B-2 buildOpenApiSpec exposes one POST path per canvas tool", () => {
    const spec = buildOpenApiSpec({
        url: "http://127.0.0.1:17371",
        token: "t",
        trustedWebOrigins: [origin],
        browserRegistrations: [],
    }) as {
        openapi: string;
        paths: Record<string, unknown>;
        servers: Array<{ url: string }>;
    };
    assert.equal(spec.openapi, "3.0.3");
    assert.equal(spec.servers[0].url, "http://127.0.0.1:17371");
    for (const name of ["canvas_get_context", "canvas_apply_ops", "canvas_generate_image"]) {
        assert.ok(spec.paths[`/tools/${name}`], `${name} 应有 POST 路径`);
    }
    assert.ok(spec.paths["/health"]);
    assert.ok(Object.keys(spec.paths).length >= toolNames.length);
});

test("[connector] P0-B-2 /openapi.json is served and /tools/:name forwards to the runtime", async () => {
    // 假 runtime 后端：返回结构化错误，验证 OpenAPI 门面错误透传
    const backend = http.createServer((_req, res) => {
        res.writeHead(200, { "content-type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: "当前没有已连接画布" }));
    });
    await new Promise<void>((resolve) => backend.listen(0, "127.0.0.1", () => resolve()));
    const address = backend.address();
    const backendPort = address && typeof address === "object" ? address.port : 0;

    const config = { url: `http://127.0.0.1:${backendPort}`, token: "t", trustedWebOrigins: [origin], browserRegistrations: [] as never[] };
    const handler = createOpenApiHandler(config);
    const server = http.createServer((req, res) => handler(req as never, res as never));
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
    const frontAddress = server.address();
    const frontPort = frontAddress && typeof frontAddress === "object" ? frontAddress.port : 0;
    try {
        const specRes = await fetch(`http://127.0.0.1:${frontPort}/openapi.json`);
        assert.equal(specRes.status, 200);
        const spec = await specRes.json() as { paths: Record<string, unknown> };
        assert.ok(spec.paths["/tools/canvas_get_context"]);

        const toolRes = await fetch(`http://127.0.0.1:${frontPort}/tools/canvas_get_context`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({}),
        });
        assert.equal(toolRes.status, 500);
        const body = await toolRes.json() as { ok?: boolean; error?: string };
        assert.equal(body.error, "当前没有已连接画布", "OpenAPI 门面应透传真实错误");
    } finally {
        await new Promise<void>((resolve) => {
            server.closeAllConnections?.();
            server.close(() => resolve());
        });
        await new Promise<void>((resolve) => {
            backend.closeAllConnections?.();
            backend.close(() => resolve());
        });
    }
});
