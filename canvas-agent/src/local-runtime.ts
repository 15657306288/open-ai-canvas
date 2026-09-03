import type { Express, Request, RequestHandler, Response } from "express";
import express from "express";

import {
    LOCAL_RUNTIME_API_VERSION,
    LOCAL_RUNTIME_ID,
    type LocalRuntimeModuleDescriptor,
    type LocalRuntimeModuleId,
    type LocalRuntimeScope,
} from "./local-runtime-contract.js";
import {
    assertExactKeys,
    exactAuthorityGuard,
    noStore,
    protectedCorsHeaders,
    legacyOrSignedRuntimeGuard,
    runtimeCors,
    runtimeErrorHandler,
    signedRuntimeGuard,
    strictJsonObject,
    trustedOriginGuard,
    type RuntimeCorsPolicy,
} from "./local-runtime-security.js";
import {
    LocalRuntimeSessionError,
    type LocalRuntimeSessionManager,
} from "./local-runtime-session.js";

export type LocalRuntimeProtectedRoute = {
    method: "GET" | "POST";
    path: string;
    scope: LocalRuntimeScope;
    handler: RequestHandler;
    lastEventId?: boolean;
    queryKeys?: readonly string[];
    legacy?: boolean;
};

export type LocalRuntimeModule = {
    descriptor: LocalRuntimeModuleDescriptor;
    routes: readonly LocalRuntimeProtectedRoute[];
    start?: () => void | Promise<void>;
    onRuntimeSessionRevoked?: (sessionId: string) => void;
    dispose?: () => void | Promise<void>;
    publicHealth?: () => Record<string, string | number | boolean>;
};

export type CreateLocalRuntimeAppOptions = {
    authority: string;
    endpoint: string;
    version: string;
    sessionManager: LocalRuntimeSessionManager;
    modules: readonly LocalRuntimeModule[];
    legacyMasterToken?: string;
    legacyOrigins?: readonly string[];
    /** [connector] P0-B-1 MCP Streamable HTTP 门面处理器（/mcp），由宿主层创建注入 */
    mcpHandler?: RequestHandler;
};

export function createLocalRuntimeApp(options: CreateLocalRuntimeAppOptions): Express {
    const modules = validateModules(options.modules);
    const policies = corsPolicies(modules, options.legacyOrigins ?? []);
    const app = express();
    app.disable("x-powered-by");
    app.use(express.raw({
        type: "application/json",
        limit: "64mb",
    }));
    app.use(noStore);
    app.use(exactAuthorityGuard(options.authority));
    app.use(runtimeCors(policies, options.sessionManager));

    app.get("/runtime/info", (req, res) => {
        const origin = typeof req.headers.origin === "string" ? req.headers.origin : "";
        res.json({
            runtime: LOCAL_RUNTIME_ID,
            apiVersion: LOCAL_RUNTIME_API_VERSION,
            protocolVersion: "framefield-runtime-session-v1",
            runtimeInstanceId: options.sessionManager.runtimeInstanceId,
            originTrusted: options.sessionManager.isTrustedOrigin(origin),
        });
    });
    app.get("/health", (_req, res) => {
        const health = modules.reduce<Record<string, string | number | boolean>>((result, module) => {
            if (module.publicHealth) Object.assign(result, module.publicHealth());
            return result;
        }, {});
        // [connector] P0-A-5 四态健康字段（供外部 agent/前端探测连接状态）：
        //   healthy   —— 有活跃画布连接且无断线宽限
        //   reconnecting —— 存在断线宽限中的连接（SSE 瞬断，正在等重连）
        //   degraded  —— 有画布状态但无活跃连接（宽限已过，画布待重新连接）
        //   offline   —— 从未连接过画布（Runtime 已起但画布未就绪）
        const clients = Number(health.clients ?? 0);
        const reconnecting = Number(health.reconnecting ?? 0);
        const hasCanvas = Boolean(health.hasCanvas);
        const status = reconnecting > 0 ? "reconnecting" : clients > 0 ? "healthy" : hasCanvas ? "degraded" : "offline";
        res.json({ ok: true, status, ...health });
    });
    app.get("/config", (_req, res) => res.json({
        ok: true,
        url: options.endpoint,
        hasToken: Boolean(options.legacyMasterToken),
    }));

    // [connector] P0-B-1 MCP Streamable HTTP 门面（Q1 默认开）：GET/POST/DELETE 统一交给
    // mcpHandler 按 MCP 协议处理（会话由 Mcp-Session-Id 头维护，与 Runtime 签名体系正交）。
    if (options.mcpHandler) {
        app.all("/mcp", options.mcpHandler);
    }

    app.post(
        "/runtime/session/challenge",
        trustedOriginGuard(options.sessionManager),
        route((req, res) => {
            const body = strictJsonObject(req);
            if ("publicKeyJwk" in body) {
                assertExactKeys(body, ["publicKeyJwk"]);
                res.json(options.sessionManager.createChallenge(requiredOrigin(req), {
                    publicKeyJwk: body.publicKeyJwk as JsonWebKey,
                }));
                return;
            }
            assertExactKeys(body, ["keyId"]);
            if (typeof body.keyId !== "string") throw requestInvalid();
            res.json(options.sessionManager.createChallenge(requiredOrigin(req), { keyId: body.keyId }));
        }),
    );
    app.post(
        "/runtime/session/exchange",
        trustedOriginGuard(options.sessionManager),
        route((req, res) => {
            const body = strictJsonObject(req);
            assertExactKeys(body, ["challengeId", "signature"]);
            if (typeof body.challengeId !== "string" || typeof body.signature !== "string") {
                throw requestInvalid();
            }
            res.json(options.sessionManager.exchange(requiredOrigin(req), {
                challengeId: body.challengeId,
                signature: body.signature,
            }));
        }),
    );

    app.get(
        "/runtime/status",
        signedRuntimeGuard(options.sessionManager, "runtime:status"),
        (_req, res) => res.json({
            ok: true,
            runtime: {
                id: LOCAL_RUNTIME_ID,
                version: options.version,
                apiVersion: LOCAL_RUNTIME_API_VERSION,
            },
            modules: modules.map((module) => ({ ...module.descriptor })),
        }),
    );
    app.post(
        "/runtime/session/revoke",
        signedRuntimeGuard(options.sessionManager, "runtime:revoke"),
        route((req, res) => {
            assertExactKeys(strictJsonObject(req), []);
            options.sessionManager.revokeSession(requiredRuntimeSessionId(res));
            res.json({ ok: true });
        }),
    );
    app.post(
        "/runtime/session/registration/revoke",
        signedRuntimeGuard(options.sessionManager, "runtime:revoke"),
        route((req, res) => {
            assertExactKeys(strictJsonObject(req), []);
            options.sessionManager.revokeRegistration(requiredRuntimeSessionId(res));
            res.json({ ok: true });
        }),
    );

    for (const module of modules) {
        for (const item of module.routes) {
            const guard = item.legacy
                ? legacyOrSignedRuntimeGuard(options.sessionManager, item.scope, {
                    queryKeys: item.queryKeys,
                    masterToken: options.legacyMasterToken,
                    origins: options.legacyOrigins,
                })
                : signedRuntimeGuard(options.sessionManager, item.scope, {
                    queryKeys: item.queryKeys,
                });
            const handlers = [
                guard,
                item.handler,
            ];
            if (item.method === "GET") app.get(item.path, ...handlers);
            else app.post(item.path, ...handlers);
        }
    }

    app.use((_req, res) => res.status(404).json({ ok: false, code: "not_found", message: "未找到本机运行时路由" }));
    app.use(runtimeErrorHandler);
    return app;
}

function validateModules(modules: readonly LocalRuntimeModule[]) {
    const ids = new Set<LocalRuntimeModuleId>();
    const routes = new Set<string>();
    for (const module of modules) {
        if (module.descriptor.id !== "canvas-agent" && module.descriptor.id !== "dreamina" && module.descriptor.id !== "portrait-clearance") {
            throw new Error(`Unsupported Local Runtime module id: ${module.descriptor.id}`);
        }
        if (ids.has(module.descriptor.id)) throw new Error(`Duplicate Local Runtime module id: ${module.descriptor.id}`);
        ids.add(module.descriptor.id);
        for (const item of module.routes) {
            const key = `${item.method} ${item.path}`;
            if (routes.has(key)) throw new Error(`Duplicate Local Runtime route: ${key}`);
            if (!item.path.startsWith("/")
                || item.path.includes("?")
                || item.path.split("/").some((segment) => segment.includes(":") && !/^:[A-Za-z][A-Za-z0-9]*$/.test(segment))) {
                throw new Error(`Invalid Local Runtime route: ${key}`);
            }
            if (!module.descriptor.scopes.includes(item.scope)) {
                throw new Error(`Undeclared Local Runtime route scope: ${key}`);
            }
            routes.add(key);
        }
    }
    return [...modules];
}

function corsPolicies(modules: readonly LocalRuntimeModule[], legacyOrigins: readonly string[]) {
    const policies = new Map<string, RuntimeCorsPolicy>([
        ["/runtime/info", { methods: ["GET"], headers: [], publicInfo: true }],
        ["/health", { methods: ["GET"], headers: [], legacyOrigins }],
        ["/config", { methods: ["GET"], headers: [], legacyOrigins }],
        ["/runtime/session/challenge", { methods: ["POST"], headers: ["content-type"], trustedOrigin: true }],
        ["/runtime/session/exchange", { methods: ["POST"], headers: ["content-type"], trustedOrigin: true }],
        ["/runtime/status", { methods: ["GET"], headers: protectedCorsHeaders("GET"), trustedOrigin: true }],
        ["/runtime/session/revoke", { methods: ["POST"], headers: protectedCorsHeaders("POST"), trustedOrigin: true }],
        ["/runtime/session/registration/revoke", { methods: ["POST"], headers: protectedCorsHeaders("POST"), trustedOrigin: true }],
        // [connector] P0-B-1 MCP 门面：允许浏览器端 MCP 客户端（trusted origin）跨域调用
        ["/mcp", { methods: ["GET", "POST", "DELETE"], headers: ["content-type", "mcp-session-id"], trustedOrigin: true }],
    ]);
    for (const module of modules) {
        for (const item of module.routes) {
            const previous = policies.get(item.path);
            const legacyHeaders = item.legacy
                ? item.method === "POST" ? ["content-type", "x-canvas-agent-token"] : ["x-canvas-agent-token"]
                : [];
            policies.set(item.path, {
                methods: uniqueStrings([...(previous?.methods || []), item.method]),
                headers: uniqueStrings([...(previous?.headers || []), ...protectedCorsHeaders(item.method, item.lastEventId)]),
                ...(previous?.publicInfo ? { publicInfo: true } : {}),
                ...(previous?.trustedOrigin || item.legacy || !previous ? { trustedOrigin: true } : {}),
                ...(previous?.legacyOrigins || item.legacy ? {
                    legacyOrigins: previous?.legacyOrigins || legacyOrigins,
                    legacyHeaders: uniqueStrings([...(previous?.legacyHeaders || []), ...legacyHeaders]),
                } : {}),
            });
        }
    }
    return policies;
}

function uniqueStrings(values: readonly string[]) {
    return [...new Set(values)];
}

function route(handler: (req: Request, res: Response) => void | Promise<void>): RequestHandler {
    return (req, res, next) => void Promise.resolve(handler(req, res)).catch(next);
}

function requiredOrigin(req: Request) {
    const value = req.headers.origin;
    if (typeof value !== "string") throw requestInvalid();
    return value;
}

function requiredRuntimeSessionId(res: Response) {
    const value = (res.locals.runtimeSession as { sessionId?: unknown } | undefined)?.sessionId;
    if (typeof value !== "string" || !value) throw requestInvalid();
    return value;
}

function requestInvalid() {
    return new LocalRuntimeSessionError("request_invalid", "请求字段无效", 400);
}
