import path from "node:path";

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
import { createCanvasAgentHttpModule } from "./modules/canvas-agent-http.js";
import { createDreaminaHttpModule } from "./modules/dreamina-http.js";
import { createPortraitClearanceHttpModule } from "./modules/portrait-clearance-http.js";

export type StartLocalRuntimeOptions = {
    config?: LocalRuntimeConfig;
    modules?: readonly LocalRuntimeModule[];
    port?: number;
    log?: (line: string) => void;
    persistConfig?: (config: LocalRuntimeConfig) => void;
    /** [connector] P0-B-1 MCP HTTP 门面开关；默认开启（Q1 拍板），可传 { enabled: false } 关闭 */
    mcp?: { enabled?: boolean; canvasOnly?: boolean; maxSessions?: number };
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
    const authority = requestedPort === 0 ? "127.0.0.1:0" : `127.0.0.1:${requestedPort}`;
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
        authority,
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
    });
    const server = createServer(app);
    const log = options.log ?? console.log;
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
            server.listen(requestedPort, "127.0.0.1");
            await listening(server);
            log("Framefield Local Runtime");
            log("Runtime is listening on 127.0.0.1");
            log("Codex MCP: codex mcp add yingce -- npx -y @ddcat666/open-ai-canvas-agent mcp");
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
