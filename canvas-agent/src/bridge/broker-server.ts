// [connector] L3 broker 独立 server 入口 —— 云端/服务器侧中继
//
// 独立可运行：node dist/bridge/broker-server.js
// 环境变量：
//   CANVAS_BROKER_PORT  监听端口（默认 17800）
//   CANVAS_BROKER_HOST  监听地址（默认 0.0.0.0）
//   CANVAS_BROKER_AGENT_TOKEN  远程 Agent 侧（request/bridges/result）鉴权 token；
//                             网关经 Bearer 调用，公网暴露必须设置
//   CANVAS_BROKER_GATEWAY_TOKEN  旧名称兼容项（优先使用 AGENT_TOKEN）
//   CANVAS_BROKER_REGISTRATION_TOKEN  bridge 注册/换证凭据，公网暴露必须设置
// 说明：Broker 只做转发与队列，不持有画布状态；本地 Runtime 的 BridgeClient
//       主动外连注册（register/heartbeat/poll/result），远程 Agent 经
//       /api/canvas-bridge/request 提交工具调用、/request/:id 查询结果。

import http from "node:http";
import { createCanvasBridgeBroker } from "./broker.js";
import { assertTokenForHost } from "./server-security.js";

const PORT = Number(process.env.CANVAS_BROKER_PORT ?? 17800);
const HOST = process.env.CANVAS_BROKER_HOST ?? "0.0.0.0";
const AGENT_TOKEN = process.env.CANVAS_BROKER_AGENT_TOKEN ?? process.env.CANVAS_BROKER_GATEWAY_TOKEN ?? "";
const REGISTRATION_TOKEN = process.env.CANVAS_BROKER_REGISTRATION_TOKEN ?? "";

assertTokenForHost(HOST, AGENT_TOKEN, "CANVAS_BROKER_AGENT_TOKEN");
assertTokenForHost(HOST, REGISTRATION_TOKEN, "CANVAS_BROKER_REGISTRATION_TOKEN");

const broker = createCanvasBridgeBroker({ agentToken: AGENT_TOKEN, registrationToken: REGISTRATION_TOKEN });

/** 常量时间比较 Bearer token */
function bearerOk(auth: string, token: string): boolean {
    const expected = `Bearer ${token}`;
    if (auth.length !== expected.length) return false;
    let diff = 0;
    for (let i = 0; i < auth.length; i++) diff |= auth.charCodeAt(i) ^ expected.charCodeAt(i);
    return diff === 0;
}

const server = http.createServer((req, res) => {
    const url = new URL(req.url ?? "/", `http://${HOST}`);
    // 健康/管理信息：GET / → 在线 bridge 列表（配置了 Agent token 时需鉴权）
    if (url.pathname === "/" && req.method === "GET") {
        if (AGENT_TOKEN && !bearerOk(req.headers.authorization ?? "", AGENT_TOKEN)) {
            res.statusCode = 401;
            res.setHeader("content-type", "application/json");
            res.end(JSON.stringify({ code: 40100, msg: "未授权：缺少/错误的网关凭据" }));
            return;
        }
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify({
            service: "canvas-bridge-broker",
            auth: AGENT_TOKEN ? "enabled" : "disabled",
            bridges: broker.listBridges().map((b) => ({
                bridgeId: b.bridgeId,
                endpoint: b.endpoint,
                lastSeenAt: b.lastSeenAt,
                queue: b.queue.length,
            })),
        }));
        return;
    }
    void broker.handle(req, res);
});

server.listen(PORT, HOST, () => {
    console.log(`[canvas-bridge] Broker listening on ${HOST}:${PORT}`);
    console.log(`[canvas-bridge] register/poll/heartbeat/result: /api/canvas-bridge/*`);
    console.log(`[canvas-bridge] remote submit: POST /api/canvas-bridge/request`);
});

function shutdown() {
    broker.close();
    server.close(() => process.exit(0));
    setTimeout(() => process.exit(0), 2000).unref();
}
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
