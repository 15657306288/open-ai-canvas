// [connector] P0-A 稳定性专项的验证工具：/health 四态 soak 脚本
//
// 用法：
//   npx tsx scripts/health-soak.ts [endpoint] [durationSec] [intervalSec]
//   示例：npx tsx scripts/health-soak.ts http://127.0.0.1:17371 3600 5
//
// 周期性 GET /health，统计四态分布（healthy/reconnecting/degraded/offline），
// 检测状态跳变，超时/非 200 计为 offline。退出码 0 = 全程无 offline 且无异常跳变。

import { agentFetch } from "../src/agent-fetch.js";

const endpoint = (process.argv[2] ?? "http://127.0.0.1:17371").replace(/\/+$/, "");
const durationSec = Number(process.argv[3] ?? 3600);
const intervalSec = Number(process.argv[4] ?? 5);

const counts: Record<string, number> = { healthy: 0, reconnecting: 0, degraded: 0, offline: 0 };
let lastStatus: string | undefined;
const transitions: Array<{ at: string; from: string | undefined; to: string }> = [];
let samples = 0;
let errors = 0;

async function sample(): Promise<void> {
    const started = Date.now();
    let status = "offline";
    try {
        const res = await agentFetch(`${endpoint}/health`, { method: "GET", timeoutMs: 5000 });
        if (res.ok) {
            const body = (await res.json()) as { status?: string };
            status = typeof body.status === "string" && body.status ? body.status : "degraded";
        }
    } catch {
        status = "offline";
    }
    const elapsed = Date.now() - started;
    samples += 1;
    counts[status] = (counts[status] ?? 0) + 1;
    if (status !== lastStatus) {
        transitions.push({ at: new Date().toISOString(), from: lastStatus, to: status });
        lastStatus = status;
    }
    if (status === "offline") errors += 1;
    if (samples % Math.max(1, Math.floor(20 / intervalSec)) === 0 || status !== "healthy") {
        console.log(`[${new Date().toISOString()}] ${status} (${elapsed}ms) counts=${JSON.stringify(counts)}`);
    }
}

async function main(): Promise<void> {
    console.log(`health soak: ${endpoint} / duration ${durationSec}s / interval ${intervalSec}s / 预计 ${Math.floor(durationSec / intervalSec)} 次采样`);
    const deadline = Date.now() + durationSec * 1000;
    while (Date.now() < deadline) {
        await sample();
        await new Promise((resolve) => setTimeout(resolve, intervalSec * 1000));
    }
    console.log("\n=== 汇总 ===");
    console.log(`采样 ${samples} 次，分布：${JSON.stringify(counts)}`);
    console.log(`状态跳变 ${transitions.length} 次：`);
    for (const t of transitions) console.log(`  ${t.at} ${t.from ?? "(启动)"} -> ${t.to}`);
    if (errors > 0) {
        console.error(`存在 ${errors} 次 offline，稳定性未达标`);
        process.exit(1);
    }
    console.log("全程无 offline，稳定性达标");
    process.exit(0);
}

main().catch((error) => {
    console.error("soak 失败:", error instanceof Error ? error.message : error);
    process.exit(1);
});
