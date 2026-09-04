// [connector] P0 商业化计量计费 —— 定价表 + 用量聚合 + 账单
//
// 记账单位：microcredits（微积分，正整数）。1 积分(credit) = CREDITS_SCALE = 1_000_000 microcredits。
//           全链路只传整数，不使用人民币元浮点数；人民币售价由网站充值商品侧决定，不在网关换算。
// 数据源：网关写入的用量明细 JSONL（默认 ~/.infinite-canvas/gateway-usage.jsonl）
//        每行 { ts, date, keyId, keyName, tool, ok, ms[, microcredits] }。
// 定价表：~/.infinite-canvas/gateway-pricing.json（可用 CANVAS_GATEWAY_PRICING_FILE 覆盖）
//   {
//     "unit": "microcredits",
//     "default": { "perCallMicrocredits": 10000 },   // 未命中工具的默认单价（=0.01 积分/次，占位可调）
//     "byTool": {                                     // 精确名或前缀通配（canvas_*）
//       "canvas_get_context": { "perCallMicrocredits": 20000 },
//       "video_generation":   { "perCallMicrocredits": 1500000 }
//     }
//   }
//
// 计费模型：按工具调用次数计费（per-call，整数微积分）。
//
// 用法：node dist/bridge/gateway-billing.js <cmd>
//   report [--date <YYYY-MM-DD>] [--key <id|name>]   用量聚合（calls/工具分布/失败数）
//   bill   [--date <YYYY-MM-DD>] [--key <id|name>]   按定价生成账单（明细 + 合计）
//   pricing                                        查看/初始化定价表

import fs from "node:fs";
import path from "node:path";

/** 1 积分 = 1_000_000 微积分。 */
export const CREDITS_SCALE = 1_000_000;
/** 默认每次调用单价（微积分）。占位值，商业化定价由运营确认后改定价表即可。 */
const DEFAULT_PER_CALL_MICROCREDITS = 10_000;

export interface ToolPricing {
    perCallMicrocredits: number;
}

export interface Pricing {
    unit: "microcredits";
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
    /** 本次调用记账金额（正整数微积分）；失败/未计费时可缺省。 */
    microcredits?: number;
}

export const DEFAULT_PRICING_FILE = path.join(process.env.HOME ?? ".", ".infinite-canvas", "gateway-pricing.json");
export const DEFAULT_USAGE_FILE = path.join(process.env.HOME ?? ".", ".infinite-canvas", "gateway-usage.jsonl");

const DEFAULT_PRICING: Pricing = {
    unit: "microcredits",
    default: { perCallMicrocredits: DEFAULT_PER_CALL_MICROCREDITS },
    byTool: {},
};

/** 强制为非负整数微积分；非法/浮点/负数归零（定价表不允许脏值进入计费）。 */
function toMicrocredits(v: unknown): number {
    const n = Number(v);
    if (!Number.isFinite(n) || n <= 0) return 0;
    return Math.trunc(n);
}

export function loadPricing(file?: string): Pricing {
    const p = file ?? process.env.CANVAS_GATEWAY_PRICING_FILE ?? DEFAULT_PRICING_FILE;
    try {
        const raw = fs.readFileSync(p, "utf8");
        const parsed = JSON.parse(raw) as Partial<Pricing> & {
            // 兼容旧人民币定价文件字段（perCall 元），但不做汇率换算：缺新字段时回退整数默认值。
            default?: { perCallMicrocredits?: number };
            byTool?: Record<string, { perCallMicrocredits?: number }>;
        };
        const byTool: Record<string, ToolPricing> = {};
        for (const [k, v] of Object.entries(parsed.byTool ?? {})) {
            const mc = toMicrocredits(v?.perCallMicrocredits);
            if (mc > 0) byTool[k] = { perCallMicrocredits: mc };
        }
        return {
            unit: "microcredits",
            default: { perCallMicrocredits: toMicrocredits(parsed.default?.perCallMicrocredits) || DEFAULT_PER_CALL_MICROCREDITS },
            byTool,
        };
    } catch {
        // 不存在则创建默认定价
        ensurePricingFile(p);
        return { ...DEFAULT_PRICING, byTool: {} };
    }
}

function ensurePricingFile(file: string): void {
    try {
        fs.mkdirSync(path.dirname(file), { recursive: true });
        fs.writeFileSync(file, JSON.stringify(DEFAULT_PRICING, null, 2), { mode: 0o600 });
    } catch { /* ignore */ }
}

/** 命中工具单价（正整数微积分）：精确名 → 前缀通配（canvas_*）→ 默认 */
export function priceFor(pricing: Pricing, tool: string): number {
    const exact = pricing.byTool[tool];
    if (exact) return toMicrocredits(exact.perCallMicrocredits);
    for (const [pattern, p] of Object.entries(pricing.byTool)) {
        if (pattern.endsWith("*") && tool.startsWith(pattern.slice(0, -1))) {
            return toMicrocredits(p.perCallMicrocredits);
        }
    }
    return toMicrocredits(pricing.default.perCallMicrocredits);
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

/** 按 Key 聚合用量（金额全部为整数微积分累加） */
export interface KeyAgg {
    keyId: string;
    keyName: string;
    calls: number;
    okCalls: number;
    failCalls: number;
    byTool: Record<string, { calls: number; microcredits: number }>;
    totalMicrocredits: number;
}

export function aggregate(entries: UsageEntry[], pricing: Pricing): KeyAgg[] {
    const map = new Map<string, KeyAgg>();
    for (const e of entries) {
        let agg = map.get(e.keyId);
        if (!agg) {
            agg = { keyId: e.keyId, keyName: e.keyName, calls: 0, okCalls: 0, failCalls: 0, byTool: {}, totalMicrocredits: 0 };
            map.set(e.keyId, agg);
        }
        agg.calls += 1;
        if (e.ok) agg.okCalls += 1; else agg.failCalls += 1;
        const mc = e.microcredits ?? (e.ok ? priceFor(pricing, e.tool) : 0);
        agg.totalMicrocredits += mc;
        const t = (agg.byTool[e.tool] ??= { calls: 0, microcredits: 0 });
        t.calls += 1;
        t.microcredits += mc;
    }
    return [...map.values()].sort((a, b) => b.totalMicrocredits - a.totalMicrocredits);
}

// ---------------- 展示层换算（仅用于人类可读输出，绝不回传计费链路） ----------------

/** 微积分 → 积分数值（展示用）。 */
export function formatCredits(microcredits: number): string {
    return (microcredits / CREDITS_SCALE).toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
}

function fmtAmount(microcredits: number): string {
    return `${formatCredits(microcredits)} 积分`;
}

// ---------------- CLI ----------------

function cliMain(): void {
    const [, , cmd, ...rest] = process.argv;
    const argValue = (flag: string): string | undefined => {
        const i = rest.indexOf(flag);
        return i >= 0 ? rest[i + 1] : undefined;
    };
    const pricing = loadPricing();

    if (cmd === "pricing") {
        console.log("记账单位: microcredits（1 积分 = 1,000,000 microcredits）");
        console.log(`默认单价: ${pricing.default.perCallMicrocredits} microcredits（${fmtAmount(pricing.default.perCallMicrocredits)}）/ 次`);
        console.log("按工具定价：");
        const tools = Object.entries(pricing.byTool);
        if (!tools.length) console.log("  （未配置，全部走默认单价）");
        for (const [tool, p] of tools) {
            console.log(`  ${tool} → ${p.perCallMicrocredits} microcredits（${fmtAmount(p.perCallMicrocredits)}）/ 次`);
        }
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
                console.log(`\n${a.keyName} (${a.keyId})  ——  合计 ${fmtAmount(a.totalMicrocredits)}`);
                const tools = Object.entries(a.byTool).sort((x, y) => y[1].microcredits - x[1].microcredits);
                for (const [tool, t] of tools) {
                    const unit = pricing.byTool[tool]?.perCallMicrocredits ?? pricing.default.perCallMicrocredits;
                    console.log(`    ${tool} × ${t.calls}  @ ${unit} = ${t.microcredits} microcredits（${fmtAmount(t.microcredits)}）`);
                }
                grand += a.totalMicrocredits;
            }
            console.log(`\n账单合计: ${fmtAmount(grand)}（${grand} microcredits）`);
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
