// [connector] L3 broker 独立 server 入口 —— 云端/服务器侧中继
//
// 独立可运行：node dist/bridge/broker-server.js
// 环境变量：
//   CANVAS_BROKER_PORT  监听端口（默认 17800）
//   CANVAS_BROKER_HOST  监听地址（默认 0.0.0.0）
// 说明：Broker 只做转发与队列，不持有画布状态；本地 Runtime 的 BridgeClient
//       主动外连注册（register/heartbeat/poll/result），远程 Agent 经
//       /api/canvas-bridge/request 提交工具调用、/request/:id 查询结果。

import http from "node:http";
import { createCanvasBridgeBroker } from "./broker.js";

const PORT = Number(process.env.CANVAS_BROKER_PORT ?? 17800);
const HOST = process.env.CANVAS_BROKER_HOST ?? "0.0.0.0";

const broker = createCanvasBridgeBroker();

const server = http.createServer((req, res) => {
    const url = new URL(req.url ?? "/", `http://${HOST}`);
    // 健康/管理信息：GET / → 在线 bridge 列表
    if (url.pathname === "/" && req.method === "GET") {
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify({
            service: "canvas-bridge-broker",
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
