// [connector] P2 商业化计量计费 —— 定价表 + 用量聚合 + 账单
//
// 数据源：P1 网关写入的用量明细 JSONL（默认 ~/.infinite-canvas/gateway-usage.jsonl）
//        每行 { ts, date, keyId, keyName, tool, ok, ms[, cost] }。
// 定价表：~/.infinite-canvas/gateway-pricing.json（可用 CANVAS_GATEWAY_PRICING_FILE 覆盖）
//   {
//     "currency": "CNY",
//     "default": { "perCall": 0.01 },                       // 未命中工具的默认单价（元/次）
//     "byTool": {                                            // 精确名或前缀通配（canvas_*）
//       "canvas_get_context": { "perCall": 0.02 },
//       "video_generation":   { "perCall": 1.50 }
//     }
//   }
//
// 计费模型：按工具调用次数计费（per-call）。画布/渠道操作以调用次数计价；
// 若未来要按模型 token 计费，在网关 JSONL 追加 token 字段并在本模块加对应计价器即可。
//
// 用法：node dist/bridge/gateway-billing.js <cmd>
//   report [--date <YYYY-MM-DD>] [--key <id|name>]   用量聚合（calls/工具分布/失败数）
//   bill   [--date <YYYY-MM-DD>] [--key <id|name>]   按定价生成账单（明细 + 合计）
//   pricing                                        查看/初始化定价表

import fs from "node:fs";
import path from "node:path";

export interface ToolPricing {
    perCall: number;
}

export interface Pricing {
    currency: string;
    default: ToolPricing;
    byTool: Record<string, ToolPricing>;
}

export interface UsageEntry {
    ts: string;
    date: string;
    keyId: string;
    keyName: string;
    tool: string;
    ok: boolean;
    ms: number;
    cost?: number;
}

export const DEFAULT_PRICING_FILE = path.join(process.env.HOME ?? ".", ".infinite-canvas", "gateway-pricing.json");
export const DEFAULT_USAGE_FILE = path.join(process.env.HOME ?? ".", ".infinite-canvas", "gateway-usage.jsonl");

const DEFAULT_PRICING: Pricing = {
    currency: "CNY",
    default: { perCall: 0.01 },
    byTool: {},
};

export function loadPricing(file?: string): Pricing {
    const p = file ?? process.env.CANVAS_GATEWAY_PRICING_FILE ?? DEFAULT_PRICING_FILE;
    try {
        const raw = fs.readFileSync(p, "utf8");
        const parsed = JSON.parse(raw) as Partial<Pricing>;
        return {
            currency: parsed.currency ?? "CNY",
            default: { perCall: Number(parsed.default?.perCall ?? 0.01) },
            byTool: parsed.byTool ?? {},
        };
    } catch {
        // 不存在则创建默认定价
        ensurePricingFile(p);
        return { ...DEFAULT_PRICING };
    }
}

function ensurePricingFile(file: string): void {
    try {
        fs.mkdirSync(path.dirname(file), { recursive: true });
        fs.writeFileSync(file, JSON.stringify(DEFAULT_PRICING, null, 2), { mode: 0o600 });
    } catch { /* ignore */ }
}

/** 命中工具单价：精确名 → 前缀通配（canvas_*）→ 默认 */
export function priceFor(pricing: Pricing, tool: string): number {
    const exact = pricing.byTool[tool];
    if (exact) return Number(exact.perCall) || 0;
    for (const [pattern, p] of Object.entries(pricing.byTool)) {
        if (pattern.endsWith("*") && tool.startsWith(pattern.slice(0, -1))) {
            return Number(p.perCall) || 0;
        }
    }
    return Number(pricing.default.perCall) || 0;
}

/** 读取用量明细（JSONL），可过滤日期/Key */
export function readUsage(usageFile?: string, date?: string, key?: string): UsageEntry[] {
    const file = usageFile ?? process.env.CANVAS_GATEWAY_USAGE_LOG ?? DEFAULT_USAGE_FILE;
    const entries: UsageEntry[] = [];
    try {
        const lines = fs.readFileSync(file, "utf8").split("\n");
        for (const line of lines) {
            const t = line.trim();
            if (!t) continue;
            try {
                const e = JSON.parse(t) as UsageEntry;
                if (date && e.date !== date) continue;
                if (key && e.keyId !== key && e.keyName !== key) continue;
                entries.push(e);
            } catch { /* 跳过坏行 */ }
        }
    } catch { /* 文件不存在 */ }
    return entries;
}

/** 按 Key 聚合用量 */
export interface KeyAgg {
    keyId: string;
    keyName: string;
    calls: number;
    okCalls: number;
    failCalls: number;
    byTool: Record<string, { calls: number; cost: number }>;
    totalCost: number;
}

export function aggregate(entries: UsageEntry[], pricing: Pricing): KeyAgg[] {
    const map = new Map<string, KeyAgg>();
    for (const e of entries) {
        let agg = map.get(e.keyId);
        if (!agg) {
            agg = { keyId: e.keyId, keyName: e.keyName, calls: 0, okCalls: 0, failCalls: 0, byTool: {}, totalCost: 0 };
            map.set(e.keyId, agg);
        }
        agg.calls += 1;
        if (e.ok) agg.okCalls += 1; else agg.failCalls += 1;
        const cost = e.cost ?? (e.ok ? priceFor(pricing, e.tool) : 0);
        agg.totalCost = Math.round((agg.totalCost + cost) * 100) / 100;
        const t = (agg.byTool[e.tool] ??= { calls: 0, cost: 0 });
        t.calls += 1;
        t.cost = Math.round((t.cost + cost) * 100) / 100;
    }
    return [...map.values()].sort((a, b) => b.totalCost - a.totalCost);
}

// ---------------- CLI ----------------

function fmtMoney(v: number, currency: string): string {
    return `${currency === "CNY" ? "¥" : ""}${v.toFixed(2)}`;
}

function cliMain(): void {
    const [, , cmd, ...rest] = process.argv;
    const argValue = (flag: string): string | undefined => {
        const i = rest.indexOf(flag);
        return i >= 0 ? rest[i + 1] : undefined;
    };
    const pricing = loadPricing();

    if (cmd === "pricing") {
        console.log(`币种: ${pricing.currency}`);
        console.log(`默认单价: ${fmtMoney(pricing.default.perCall, pricing.currency)} / 次`);
        console.log("按工具定价：");
        const tools = Object.entries(pricing.byTool);
        if (!tools.length) console.log("  （未配置，全部走默认单价）");
        for (const [tool, p] of tools) console.log(`  ${tool} → ${fmtMoney(p.perCall, pricing.currency)} / 次`);
        return;
    }

    if (cmd === "report" || cmd === "bill") {
        const date = argValue("--date");
        const key = argValue("--key");
        const d = date ?? new Date().toISOString().slice(0, 10);
        const entries = readUsage(undefined, d, key);
        const aggs = aggregate(entries, pricing);
        if (!aggs.length) {
            console.log(`（${d} 无用量记录）`);
            return;
        }
        console.log(`== 日期 ${d} ==`);
        if (cmd === "report") {
            for (const a of aggs) {
                console.log(`\n${a.keyName} (${a.keyId})`);
                console.log(`  调用 ${a.calls} 次（成功 ${a.okCalls} / 失败 ${a.failCalls}）`);
                const tools = Object.entries(a.byTool).sort((x, y) => y[1].calls - x[1].calls);
                for (const [tool, t] of tools) console.log(`    ${tool} × ${t.calls}`);
            }
        } else {
            let grand = 0;
            for (const a of aggs) {
                console.log(`\n${a.keyName} (${a.keyId})  ——  合计 ${fmtMoney(a.totalCost, pricing.currency)}`);
                const tools = Object.entries(a.byTool).sort((x, y) => y[1].cost - x[1].cost);
                for (const [tool, t] of tools) {
                    const unit = pricing.byTool[tool]?.perCall ?? pricing.default.perCall;
                    console.log(`    ${tool} × ${t.calls}  @ ${fmtMoney(unit, pricing.currency)} = ${fmtMoney(t.cost, pricing.currency)}`);
                }
                grand = Math.round((grand + a.totalCost) * 100) / 100;
            }
            console.log(`\n账单合计: ${fmtMoney(grand, pricing.currency)}`);
        }
        return;
    }

    if (!cmd || cmd === "--help") {
        console.log(`用法: gateway-billing <cmd>
  report [--date <YYYY-MM-DD>] [--key <id|name>]   用量聚合
  bill   [--date <YYYY-MM-DD>] [--key <id|name>]   按定价生成账单
  pricing                                        查看/初始化定价表`);
        return;
    }
    console.error(`未知命令: ${cmd}`);
    process.exit(1);
}

// 仅当直接执行本文件时运行 CLI（被网关 import 时不触发）
if (import.meta.url === `file://${process.argv[1]}`) {
    cliMain();
}
