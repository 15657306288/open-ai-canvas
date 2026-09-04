import path from "node:path";
import os from "node:os";

import { createServer, type Server } from "node:http";
import type { Express } from "express";

import {
    CONFIG_DIR,
    LOCAL_RUNTIME_DEFAULT_PORT,
    VERSION,
    ensureRuntimeOwnerId,
    loadConfig,
    saveConfig,
    type LocalRuntimeConfig,
} from "./config.js";
import { createLocalRuntimeApp, type LocalRuntimeModule } from "./local-runtime.js";
import { LOCAL_RUNTIME_DEFAULT_SCOPES, LocalRuntimeSessionManager } from "./local-runtime-session.js";
import { acquireRuntimeLock, type RuntimeLockInfo } from "./runtime-lock.js";
import { createMcpHttpHandler } from "./mcp-http-server.js";
import { createOpenApiHandler } from "./openapi-server.js";
import { createCanvasAgentHttpModule } from "./modules/canvas-agent-http.js";
import { createDreaminaHttpModule } from "./modules/dreamina-http.js";
import { createPortraitClearanceHttpModule } from "./modules/portrait-clearance-http.js";
import { createCanvasBridgeClient, type CanvasBridgeClient } from "./bridge/client.js";

export type StartLocalRuntimeOptions = {
    config?: LocalRuntimeConfig;
    modules?: readonly LocalRuntimeModule[];
    port?: number;
    log?: (line: string) => void;
    persistConfig?: (config: LocalRuntimeConfig) => void;
    /** [connector] P0-B-1 MCP HTTP 门面开关；默认开启（Q1 拍板），可传 { enabled: false } 关闭 */
    mcp?: { enabled?: boolean; canvasOnly?: boolean; maxSessions?: number };
    /** [connector] P0-B-3 远程主动外连 bridge（Q3：本地零入站端口）。
     *  配置 broker 地址即可启用；bridgeId 默认 `hostname-<port>`，endpoint 默认本 runtime 实际地址。
     *  也可通过环境变量 CANVAS_BRIDGE_SERVER / CANVAS_BRIDGE_TOKEN / CANVAS_BRIDGE_ID 启用。 */
    bridge?: {
        server: string;
        token: string;
        bridgeId?: string;
        endpoint?: string;
        pollSeconds?: number;
        heartbeatSeconds?: number;
        capabilities?: Record<string, unknown>;
    };
};

export type LocalRuntimeHandle = {
    app: Express;
    server: Server;
    sessions: LocalRuntimeSessionManager;
    ready: Promise<void>;
    close: () => Promise<void>;
};

/** [connector] P0-A-3：同一 config 目录下已有存活实例时抛错（附权威入口），
 *  防止多实例端口漂移导致的 masterToken 链接不稳定。 */
export class RuntimeAlreadyRunningError extends Error {
    constructor(public readonly existing: RuntimeLockInfo) {
        super(`Local Runtime 已在 PID ${existing.pid} 运行（${existing.endpoint}）。请复用该地址，或先停止旧实例再启动。`);
        this.name = "RuntimeAlreadyRunningError";
    }
}

export function createDefaultLocalRuntimeModules(config: LocalRuntimeConfig): LocalRuntimeModule[] {
    return [
        createCanvasAgentHttpModule(config),
        createDreaminaHttpModule({
            ownerId: ensureRuntimeOwnerId(config),
            configDir: CONFIG_DIR,
            referenceRoots: () => [
                path.join(CONFIG_DIR, "codex-workspaces"),
                ...Object.values(config.canvases ?? {}).map((canvas) => canvas.workspacePath),
            ],
        }),
        createPortraitClearanceHttpModule({ ownerId: ensureRuntimeOwnerId(config), configDir: CONFIG_DIR }),
    ];
}

export function startLocalRuntime(options: StartLocalRuntimeOptions = {}): LocalRuntimeHandle {
    const config = options.config ?? loadConfig(true);
    const persistConfig = options.persistConfig ?? saveConfig;
    const requestedPort = options.port ?? (
        Number(process.env.PORT)
        || Number(new URL(config.url).port)
        || LOCAL_RUNTIME_DEFAULT_PORT
    );
    if (!Number.isInteger(requestedPort) || requestedPort < 0 || requestedPort > 65_535) {
        throw new Error("Local Runtime port is invalid");
    }
    const endpoint = requestedPort === 0
        ? config.url
        : `http://127.0.0.1:${requestedPort}`;
    // [connector] L2 局域网/公网：监听地址与对外权威 Host 均可配置。
    //   CANVAS_HOST       监听地址（默认 127.0.0.1；设 0.0.0.0 对外可达）
    //   CANVAS_AUTHORITY  允许的 Host 集合（逗号分隔；默认 127.0.0.1:<port>）
    //                      例：CANVAS_AUTHORITY="127.0.0.1:17371,192.168.1.10:17371"
    const listenHost = process.env.CANVAS_HOST?.trim() || "127.0.0.1";
    const defaultAuthority = requestedPort === 0 ? "127.0.0.1:0" : `127.0.0.1:${requestedPort}`;
    const authorities = process.env.CANVAS_AUTHORITY
        ? process.env.CANVAS_AUTHORITY.split(",").map((s) => s.trim()).filter(Boolean)
        : [defaultAuthority];
    const modules = [...(options.modules ?? createDefaultLocalRuntimeModules(config))];
    const scopes = [...new Set([
        ...LOCAL_RUNTIME_DEFAULT_SCOPES,
        ...modules.flatMap((module) => module.descriptor.scopes),
    ])];
    const sessions = new LocalRuntimeSessionManager({
        endpoint,
        trustedOrigins: config.trustedWebOrigins,
        registrations: config.browserRegistrations,
        scopes,
        persistRegistrations: () => persistConfig(config),
        onSessionRevoked: (sessionId) => {
            for (const module of modules) module.onRuntimeSessionRevoked?.(sessionId);
        },
    });
    const app = createLocalRuntimeApp({
        authority: authorities,
        endpoint,
        version: VERSION,
        sessionManager: sessions,
        modules,
        legacyMasterToken: config.token,
        legacyOrigins: config.origins ?? [],
        // [connector] P0-B-1：默认开 /mcp（Q1），供远程 MCP 客户端直接调用画布
        mcpHandler: options.mcp?.enabled === false ? undefined : createMcpHttpHandler(config, {
            canvasOnly: options.mcp?.canvasOnly,
            maxSessions: options.mcp?.maxSessions,
        }),
        // [connector] P0-B-2 OpenAPI 兜底门面（始终开启，供不支持 MCP 的 agent）
        openApiHandler: createOpenApiHandler(config),
    });
    const server = createServer(app);
    const log = options.log ?? console.log;

    // [connector] P0-B-3 远程主动外连 bridge：runtime 就绪后启用，关闭时一并停止。
    // 非致命：broker 不可达只告警，不影响本机画布主流程。
    const bridgeServer = options.bridge?.server ?? process.env.CANVAS_BRIDGE_SERVER;
    const bridgeToken = options.bridge?.token ?? process.env.CANVAS_BRIDGE_TOKEN;
    let bridgeClient: CanvasBridgeClient | undefined;
    const startBridge = async () => {
        if (!bridgeServer || !bridgeToken) return;
        const bridgeId = options.bridge?.bridgeId
            ?? process.env.CANVAS_BRIDGE_ID
            ?? `${os.hostname()}-${requestedPort}`;
        bridgeClient = createCanvasBridgeClient({
            server: bridgeServer,
            bridgeId,
            token: bridgeToken,
            endpoint: options.bridge?.endpoint ?? endpoint,
            runtimeToken: config.token,
            pollSeconds: options.bridge?.pollSeconds,
            heartbeatSeconds: options.bridge?.heartbeatSeconds,
            capabilities: options.bridge?.capabilities,
        });
        try {
            await bridgeClient.start();
            log(`[connector] Canvas Bridge 已连接 ${bridgeServer} (bridgeId=${bridgeId})`);
        } catch (error) {
            log(`[connector] Canvas Bridge 连接失败（不影响本机使用）：${error instanceof Error ? error.message : String(error)}`);
            bridgeClient = undefined;
        }
    };
    const stopBridge = async () => {
        if (bridgeClient) {
            bridgeClient.stop();
            bridgeClient = undefined;
        }
    };
    // [connector] P0-A-3：单实例锁——固定端口启动时独占 CONFIG_DIR 的 runtime.lock；
    // 已有存活实例则拒绝启动（防端口漂移），close 时释放锁。
    let lockRelease: (() => void) | undefined;
    if (requestedPort !== 0) {
        const lockResult = acquireRuntimeLock({
            lockFilePath: path.join(CONFIG_DIR, "runtime.lock"),
            port: requestedPort,
            token: config.token,
            endpoint,
            log,
        });
        if (!lockResult.acquired) throw new RuntimeAlreadyRunningError(lockResult.existing);
        lockRelease = lockResult.release;
    }
    let modulesDisposed = false;
    const disposeModules = async () => {
        if (modulesDisposed) return;
        modulesDisposed = true;
        const errors: unknown[] = [];
        for (const module of modules) {
            try {
                await module.dispose?.();
            } catch (error) {
                errors.push(error);
            }
        }
        if (errors.length) throw new AggregateError(errors, "Local Runtime module disposal failed");
    };
    const ready = (async () => {
        try {
            for (const module of modules) await module.start?.();
            server.listen(requestedPort, listenHost);
            await listening(server);
            log("Framefield Local Runtime");
            log(`Runtime is listening on ${listenHost}:${requestedPort}`);
            log(`Runtime authorities: ${authorities.join(", ")}`);
            log("Codex MCP: codex mcp add yingce -- npx -y @ddcat666/open-ai-canvas-agent mcp");
            await startBridge();
        } catch (startupError) {
            sessions.dispose();
            lockRelease?.();
            const cleanupErrors: unknown[] = [];
            try { await closeServer(server); } catch (error) { cleanupErrors.push(error); }
            try { await disposeModules(); } catch (error) { cleanupErrors.push(error); }
            if (cleanupErrors.length) {
                throw new AggregateError([startupError, ...cleanupErrors], "Local Runtime startup failed");
            }
            throw startupError;
        }
    })();
    let closePromise: Promise<void> | undefined;
    const close = () => {
        closePromise ??= (async () => {
            try { await ready; } catch { /* Startup error remains observable through ready. */ }
            await stopBridge();
            sessions.dispose();
            const errors: unknown[] = [];
            try { await closeServer(server); } catch (error) { errors.push(error); }
            try { await disposeModules(); } catch (error) { errors.push(error); }
            lockRelease?.();
            if (errors.length) throw new AggregateError(errors, "Local Runtime shutdown failed");
        })();
        return closePromise;
    };
    return { app, server, sessions, ready, close };
}

function listening(server: Server) {
    if (server.listening) return Promise.resolve();
    return new Promise<void>((resolve, reject) => {
        server.once("listening", resolve);
        server.once("error", reject);
    });
}

function closeServer(server: Server) {
    if (!server.listening) return Promise.resolve();
    return new Promise<void>((resolve, reject) => {
        server.close((error) => error ? reject(error) : resolve());
    });
}
