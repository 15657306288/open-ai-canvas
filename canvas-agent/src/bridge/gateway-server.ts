// [connector] L3 broker MCP 网关 —— 对外标准 MCP 端点（P1 商业化 Key 网关版）
//
// 独立可运行：node dist/bridge/gateway-server.js
// 让外部 agent（Codex/豆包/WorkBuddy/任意 MCP 客户端）只配一个固定 URL 即可调用
// 通过 broker 注册的画布 Runtime（本机或远端任意一台）。
//
// P1 商业化 Key 体系：
//   - 认证优先级：① 内部 master token（CANVAS_GATEWAY_TOKEN）② 客户 API Key。
//   - 客户 Key：`Authorization: Bearer ak_...` 或 `X-Api-Key: ak_...`，
//     由 KeyStore（gateway-keys.ts）哈希校验，支持按 Key 停用/日配额（超额 429）。
//   - 每次工具调用写入 JSONL 明细（~/.infinite-canvas/gateway-usage.jsonl），供 P2 计量计费。
//
// 工作原理：
//   1) 启动时连接 Schema Runtime（默认本机 127.0.0.1:17371/mcp）拉取工具定义，
//      在 tools/list 时原样下发（影策各 Runtime 工具集一致，schema 通用）。
//   2) tools/call 时把 {name, input} 经 Broker /api/canvas-bridge/request 转发到
//      目标 bridge（默认 CANVAS_BRIDGE_ID，未指定则取第一个在线 bridge），
//      轮询 /request/:id 直至本地 Runtime 回传结果。
//
// 环境变量：
//   CANVAS_GATEWAY_PORT          监听端口（默认 17801）
//   CANVAS_GATEWAY_HOST          监听地址（默认 0.0.0.0）
//   CANVAS_GATEWAY_TOKEN         内部 master Bearer 凭据（建议设置）
//   CANVAS_GATEWAY_KEYS_FILE     客户 Key 存储文件（默认 ~/.infinite-canvas/gateway-keys.json）
//   CANVAS_GATEWAY_USAGE_LOG     用量明细 JSONL（默认 ~/.infinite-canvas/gateway-usage.jsonl）
//   CANVAS_BROKER_URL            Broker 地址（默认 http://127.0.0.1:17800）
//   CANVAS_BROKER_AGENT_TOKEN    网关调用 Broker 的 Bearer 凭据
//   CANVAS_BROKER_GATEWAY_TOKEN  旧名称兼容项
//   CANVAS_BRIDGE_ID             默认目标 bridgeId（空=取第一个在线 bridge）
//   CANVAS_SCHEMA_RUNTIME_URL    Schema Runtime 地址（默认 http://127.0.0.1:17371）
//   CANVAS_SCHEMA_RUNTIME_TOKEN  Schema Runtime 的 /mcp 凭据（默认空）
//   CANVAS_TOOL_TIMEOUT_MS       单次工具调用超时（默认 120000）

import http from "node:http";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import type { IncomingMessage, ServerResponse } from "node:http";

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { ListToolsRequestSchema, CallToolRequestSchema } from "@modelcontextprotocol/sdk/types.js";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

import { AGENT_PROMPT, VERSION } from "../config.js";
import { agentFetch } from "../agent-fetch.js";
import { assertTokenForHost } from "./server-security.js";
import { today } from "./gateway-keys.js";
import { createAccountProvider, type AccountProvider } from "./account-provider.js";
import { runBilledCall } from "./billing-lifecycle.js";
import { loadPricing, priceFor, type Pricing } from "./gateway-billing.js";
import { OAuthManager } from "./gateway-oauth.js";

const env = process.env;
const PORT = Number(env.CANVAS_GATEWAY_PORT ?? 17801);
const HOST = env.CANVAS_GATEWAY_HOST ?? "0.0.0.0";
const GATEWAY_TOKEN = env.CANVAS_GATEWAY_TOKEN ?? "";
const BROKER_URL = (env.CANVAS_BROKER_URL ?? "http://127.0.0.1:17800").replace(/\/+$/, "");
const BRIDGE_ID = env.CANVAS_BRIDGE_ID ?? "";
const SCHEMA_URL = ((env.CANVAS_SCHEMA_RUNTIME_URL ?? "http://127.0.0.1:17371").replace(/\/+$/, "")) + "/mcp";
const SCHEMA_TOKEN = env.CANVAS_SCHEMA_RUNTIME_TOKEN ?? "";
const BROKER_AGENT_TOKEN = env.CANVAS_BROKER_AGENT_TOKEN ?? env.CANVAS_BROKER_GATEWAY_TOKEN ?? "";
const TOOL_TIMEOUT_MS = Number(env.CANVAS_TOOL_TIMEOUT_MS ?? 120_000);
const MAX_BODY_BYTES = 2 * 1024 * 1024;
const USAGE_LOG = env.CANVAS_GATEWAY_USAGE_LOG ?? path.join(process.env.HOME ?? ".", ".infinite-canvas", "gateway-usage.jsonl");

// 账户来源：local=本地 KeyStore（默认/内测）；remote=网站后端钱包（CANVAS_ACCOUNT_PROVIDER 切换）
const account: AccountProvider = createAccountProvider();

// 标准 MCP OAuth 2.1（发现/动态注册/PKCE 授权码/刷新）；对外公网基址用于 metadata 绝对 URL
const PUBLIC_BASE_URL = (env.CANVAS_GATEWAY_PUBLIC_BASE_URL ?? "https://yingce.cc.cd").replace(/\/+$/, "");
const OAUTH_STORE_FILE = env.CANVAS_OAUTH_STORE_FILE ?? path.join(process.env.HOME ?? ".", ".infinite-canvas", "oauth-store.json");
const oauth = new OAuthManager({ accounts: account, publicBaseUrl: PUBLIC_BASE_URL, storeFile: OAUTH_STORE_FILE });

// P2 定价缓存：启动加载一次，mtime 变化自动重载（改价无需重启）
const PRICING_FILE = process.env.CANVAS_GATEWAY_PRICING_FILE ?? path.join(process.env.HOME ?? ".", ".infinite-canvas", "gateway-pricing.json");
let pricing: Pricing = loadPricing();
let pricingMtime = statMtimeMs(PRICING_FILE);

function statMtimeMs(file: string): number {
    try { return fs.statSync(file).mtimeMs; } catch { return 0; }
}

function currentPricing(): Pricing {
    const m = statMtimeMs(PRICING_FILE);
    if (m !== pricingMtime) {
        pricing = loadPricing();
        pricingMtime = m;
    }
    return pricing;
}

assertTokenForHost(HOST, GATEWAY_TOKEN, "CANVAS_GATEWAY_TOKEN");

/** 追加一行用量明细（P2 计量计费数据源；追加写，不覆盖） */
function appendUsageLog(entry: Record<string, unknown>): void {
    try {
        fs.mkdirSync(path.dirname(USAGE_LOG), { recursive: true });
        fs.appendFileSync(USAGE_LOG, `${JSON.stringify(entry)}\n`);
    } catch {
        // 日志写入失败不影响主流程
    }
}

async function brokerFetch(path: string, init: RequestInit = {}, timeoutMs = 10_000): Promise<Response> {
    const method = String(init.method ?? "GET").toUpperCase();
    const headers = new Headers(init.headers);
    if (method === "POST" && !headers.has("content-type")) headers.set("content-type", "application/json");
    if (BROKER_AGENT_TOKEN) headers.set("authorization", `Bearer ${BROKER_AGENT_TOKEN}`);
    return agentFetch(`${BROKER_URL}${path}`, {
        ...init,
        headers,
        timeoutMs,
        retries: method === "GET" ? 1 : 0,
    });
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

interface ToolMeta {
    name: string;
    description?: string;
    inputSchema?: unknown;
}

/** 从 Schema Runtime 拉取工具定义 */
async function loadTools(): Promise<ToolMeta[]> {
    const client = new Client({ name: "canvas-gateway", version: VERSION });
    const headers: Record<string, string> = SCHEMA_TOKEN ? { authorization: `Bearer ${SCHEMA_TOKEN}` } : {};
    const transport = new StreamableHTTPClientTransport(new URL(SCHEMA_URL), { requestInit: { headers } });
    try {
        await client.connect(transport);
        const result = await client.listTools();
        return result.tools.map((tool) => ({
            name: tool.name,
            description: tool.description,
            inputSchema: tool.inputSchema as unknown,
        }));
    } finally {
        await client.close();
    }
}

/** 带重试的 schema 拉取：launchd 同时拉起三件套时等待 Runtime 就绪（启动解耦） */
async function loadToolsWithRetry(): Promise<ToolMeta[]> {
    let lastError: unknown;
    for (let i = 0; i < 10; i += 1) {
        try {
            return await loadTools();
        } catch (error) {
            lastError = error;
            console.log(`[canvas-gateway] Schema Runtime 未就绪（${i + 1}/10），2s 后重试…`);
            await sleep(2000);
        }
    }
    throw lastError instanceof Error ? lastError : new Error("Schema Runtime 不可达");
}

/** 获取在线 bridge 列表 */
async function listBridges(): Promise<Array<{ bridgeId: string; online: boolean }>> {
    const res = await brokerFetch("/api/canvas-bridge/bridges");
    const body = (await res.json()) as { code?: number; data?: { bridges?: Array<{ bridgeId: string; online: boolean }> } };
    if (body.code !== 0 || !body.data?.bridges) return [];
    return body.data.bridges;
}

/** 选择目标 bridge：显式配置优先，否则第一个在线 */
async function pickBridgeId(): Promise<string | undefined> {
    if (BRIDGE_ID) return BRIDGE_ID;
    const bridges = await listBridges();
    const online = bridges.find((b) => b.online) ?? bridges[0];
    return online?.bridgeId;
}

/** 经 Broker 转发工具调用到目标 bridge，轮询直至完成 */
async function callViaBridge(name: string, input: Record<string, unknown>): Promise<unknown> {
    const bridgeId = await pickBridgeId();
    if (!bridgeId) throw new Error("没有在线的画布 bridge（请先启动本地 Runtime 并启用 bridge 外连）");

    const submitRes = await brokerFetch("/api/canvas-bridge/request", {
        method: "POST",
        body: JSON.stringify({ bridgeId, name, input }),
    });
    const submitBody = (await submitRes.json()) as { code?: number; data?: { requestId: string }; msg?: string };
    if (submitBody.code !== 0) throw new Error(submitBody.msg || `bridge 提交失败（HTTP ${submitRes.status}）`);
    const requestId = submitBody.data!.requestId;

    const deadline = Date.now() + TOOL_TIMEOUT_MS;
    while (Date.now() < deadline) {
        const resultRes = await brokerFetch(`/api/canvas-bridge/request/${encodeURIComponent(requestId)}`, {}, Math.max(1, Math.min(10_000, deadline - Date.now())));
        const resultBody = (await resultRes.json()) as { code?: number; msg?: string; data?: { status: string; result?: unknown; error?: string } };
        if (resultBody.code !== 0) throw new Error(resultBody.msg || "查询 bridge 结果失败");
        const data = resultBody.data!;
        if (data.status === "succeeded") return data.result;
        if (data.status === "failed") throw new Error(data.error || "画布工具执行失败");
        await sleep(300);
    }
    throw new Error(`画布工具调用超时（${TOOL_TIMEOUT_MS}ms）`);
}

/** 认证主体：内部 master 或某个客户 Key */
interface GwAuth {
    type: "master" | "key";
    keyId?: string;
    keyName?: string;
    /** 认证失败时的 HTTP 状态：401 凭据无效 / 429 配额耗尽 */
    rejectStatus?: number;
    rejectReason?: string;
}

/**
 * 解析并校验请求凭据。
 * 优先内部 master token（Authorization Bearer / x-canvas-agent-token），
 * 其次客户 API Key（Authorization Bearer ak_ 或 X-Api-Key）。
 * 返回 { type, ... } 表示通过；返回 { rejectStatus, rejectReason } 表示拒绝。
 */
async function authenticate(req: IncomingMessage): Promise<GwAuth> {
    const auth = typeof req.headers.authorization === "string" ? req.headers.authorization : "";
    const bearer = auth.startsWith("Bearer ") ? auth.slice("Bearer ".length).trim() : "";
    const alt = typeof req.headers["x-canvas-agent-token"] === "string" ? req.headers["x-canvas-agent-token"] : "";
    const apiKeyHeader = typeof req.headers["x-api-key"] === "string" ? req.headers["x-api-key"].trim() : "";

    // ① 内部 master
    if (GATEWAY_TOKEN && (bearer === GATEWAY_TOKEN || alt === GATEWAY_TOKEN)) {
        return { type: "master" };
    }

    // ② OAuth2 access_token（授权码 PKCE / client_credentials 换取，短期，OAuthManager 统一签发）
    if (bearer.startsWith("at_")) {
        const rec = oauth.verifyAccessToken(bearer);
        if (!rec) return { type: "key", rejectStatus: 401, rejectReason: "invalid or expired access token" };
        const p = await account.resolveSubject(rec.keyId);
        if (!p) return { type: "key", rejectStatus: 401, rejectReason: "invalid or disabled account" };
        return { type: "key", keyId: p.subjectId, keyName: p.displayName };
    }

    // ③ 客户 API Key
    const candidate = bearer.startsWith("ak_") ? bearer : apiKeyHeader;
    if (candidate) {
        const v = await account.authenticateByKey(candidate);
        if (v.ok) {
            return { type: "key", keyId: v.principal.subjectId, keyName: v.principal.displayName };
        }
        if (v.status === 429) {
            return { type: "key", rejectStatus: 429, rejectReason: "quota exceeded: daily call limit reached" };
        }
        return { type: "key", rejectStatus: 401, rejectReason: "invalid or disabled API key" };
    }

    // ③ 未提供任何凭据
    return { type: "key", rejectStatus: 401, rejectReason: "Unauthorized: missing credentials" };
}

// ---------------- OAuth2 授权服务器（标准 MCP OAuth，见 gateway-oauth.ts） ----------------
// token 签发/校验、动态注册、PKCE 授权码、client_credentials 全部由 OAuthManager 承担；
// 端点：/.well-known/oauth-*、/register、/authorize、/token（/auth/token 作兼容别名）。

/** 为单个 MCP 会话创建低层 Server（inputSchema 原样透传 JSON Schema） */
function createSessionServer(tools: ToolMeta[], auth: GwAuth): Server {
    const server = new Server(
        { name: "canvas-gateway", version: VERSION },
        { capabilities: { tools: { listChanged: false } }, instructions: AGENT_PROMPT },
    );
    server.setRequestHandler(ListToolsRequestSchema, async () => ({
        tools: tools.map((t) => ({
            name: t.name,
            description: t.description ?? "",
            inputSchema: t.inputSchema as never,
        })),
    }));
    server.setRequestHandler(CallToolRequestSchema, async (request) => {
        const name = request.params.name;
        const args = (request.params.arguments ?? {}) as Record<string, unknown>;
        return runBilledCall(name, args, auth, {
            account,
            callTool: callViaBridge,
            priceOf: (tool) => priceFor(currentPricing(), tool),
            log: appendUsageLog,
        });
    });
    return server;
}

async function main() {
    // 拉取工具 schema（带重试，等待 Runtime 就绪）
    const tools = await loadToolsWithRetry();
    console.log(`[canvas-gateway] 已从 ${SCHEMA_URL} 拉取 ${tools.length} 个工具定义`);

    // MCP Streamable HTTP 会话管理（P1：master token 或客户 API Key 鉴权）
    const sessions = new Map<string, { transport: StreamableHTTPServerTransport; server: Server }>();
    const httpServer = http.createServer((req, res) => {
        void (async () => {
            // 标准 MCP OAuth 端点（发现/动态注册/授权/token），不经过 MCP 鉴权
            if (await oauth.handle(req, res)) return;
            const auth = await authenticate(req);
            if (auth.rejectStatus) {
                const code = auth.rejectStatus === 429 ? -32029 : -32001;
                res.statusCode = auth.rejectStatus;
                res.setHeader("content-type", "application/json");
                res.setHeader("x-canvas-auth", auth.rejectReason ?? "denied");
                // 401 时按 MCP OAuth 规范下发受保护资源元数据指针，供客户端自动发起 mcp login
                if (auth.rejectStatus === 401) {
                    res.setHeader("WWW-Authenticate",
                        `Bearer resource_metadata="${PUBLIC_BASE_URL}/.well-known/oauth-protected-resource"`);
                }
                res.end(JSON.stringify({ jsonrpc: "2.0", error: { code, message: auth.rejectReason }, id: null }));
                return;
            }
            await handleMcpRequest(req, res, auth);
        })();
    });

    async function handleMcpRequest(req: IncomingMessage, res: ServerResponse, auth: GwAuth) {
        const headerSessionId = typeof req.headers["mcp-session-id"] === "string" ? req.headers["mcp-session-id"] : undefined;
        let session = headerSessionId ? sessions.get(headerSessionId) : undefined;
        if (headerSessionId && !session) {
            jsonRpcError(res, 404, -32001, "Session not found");
            return;
        }
        if (!session) {
            if (req.method !== "POST") {
                jsonRpcError(res, 404, -32001, "Session not found");
                return;
            }
            const sessionServer = createSessionServer(tools, auth);
            const transport = new StreamableHTTPServerTransport({
                sessionIdGenerator: () => crypto.randomUUID(),
                enableJsonResponse: true,
                onsessioninitialized: (sessionId) => { sessions.set(sessionId, { transport, server: sessionServer }); },
                onsessionclosed: (sessionId) => { sessions.delete(sessionId); },
            });
            session = { transport, server: sessionServer };
            await sessionServer.connect(transport);
        }
        let parsedBody: unknown;
        if (req.method === "POST") {
            try {
                parsedBody = await readJsonBody(req);
            } catch (error) {
                if (error instanceof BodyTooLargeError) {
                    jsonRpcError(res, 413, -32013, "Request body too large");
                } else {
                    jsonRpcError(res, 400, -32700, "Parse error: Invalid JSON");
                }
                return;
            }
        }
        try {
            await session.transport.handleRequest(req as never, res as never, parsedBody);
        } catch {
            if (!res.headersSent) jsonRpcError(res, 500, -32603, "Internal error");
        }
    }

    httpServer.listen(PORT, HOST, () => {
        console.log(`[canvas-gateway] MCP gateway listening on ${HOST}:${PORT}/mcp`);
        console.log(`[canvas-gateway] broker=${BROKER_URL} defaultBridge=${BRIDGE_ID || "(auto: 第一个在线)"}`);
        console.log(`[canvas-gateway] auth=master(${GATEWAY_TOKEN ? "on" : "off"}) + account-provider=${account.kind}`);
        console.log(`[canvas-gateway] usage-log=${USAGE_LOG}`);
        if (account.kind === "remote") {
            console.log(`[canvas-gateway] pricing=后端定价（影策画布 mcp_tool_pricing，连接器不参与定价）`);
        } else {
            console.log(`[canvas-gateway] pricing=${PRICING_FILE} (default ${priceFor(currentPricing(), "default")} microcredits/call) — local 内测占位价，上线 remote 后由后端定价`);
        }
        console.log(`[canvas-gateway] oauth=标准 MCP OAuth2.1（discovery/register/authorize/token）public=${PUBLIC_BASE_URL} store=${OAUTH_STORE_FILE}`);
    });

    function shutdown() {
        for (const s of sessions.values()) {
            try { void s.transport.close(); } catch { /* ignore */ }
        }
        httpServer.close(() => process.exit(0));
        setTimeout(() => process.exit(0), 2000).unref();
    }
    process.on("SIGINT", shutdown);
    process.on("SIGTERM", shutdown);
}

async function readJsonBody(req: IncomingMessage): Promise<unknown> {
    const chunks: Buffer[] = [];
    let size = 0;
    for await (const chunk of req) {
        const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
        size += buffer.length;
        if (size <= MAX_BODY_BYTES) chunks.push(buffer);
    }
    if (size > MAX_BODY_BYTES) throw new BodyTooLargeError();
    const raw = Buffer.concat(chunks).toString("utf8");
    return raw.trim() ? JSON.parse(raw) : undefined;
}

class BodyTooLargeError extends Error {}

function jsonRpcError(res: ServerResponse, status: number, code: number, message: string) {
    if (res.headersSent) return;
    res.statusCode = status;
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify({ jsonrpc: "2.0", error: { code, message }, id: null }));
}

void main();
