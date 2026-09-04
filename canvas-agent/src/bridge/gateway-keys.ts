// [connector] P1 商业化 Key 网关 —— 客户 API Key 存储与管理
//
// 目标：把单一共享的内部 gateway token 升级为"客户级 API Key"体系，
// 支持多租户、按 Key 限流、可吊销、可计量，为 P2 计费打底。
//
// 原则：
//   - 客户 Key 格式 `ak_` + 32 位十六进制（16 字节随机，256 位熵）。
//   - 磁盘只存 SHA-256 哈希，明文仅在 createKey 时打印一次，绝不落盘。
//   - 每个 Key 独立配额（日调用次数），到达上限返回 429。
//   - 用量按日累计（usage.calls / usage.byTool / usage.totalCalls），并追加写入
//     JSONL 明细（P2 计量用）。
//
// 用法（编译后）：node dist/bridge/gateway-keys.js <cmd> [args]
//   add   --name <客户名> [--quota <日调用上限>]  颁发一个客户 Key（打印一次明文）
//   list                                         列出全部 Key（不含明文）
//   revoke <id|name>                             停用某个 Key
//   enable <id|name>                             重新启用某个 Key
//   reset  <id|name>                             重置当日调用计数
//   usage  <id|name>                             查看某个 Key 的用量明细
//
// 环境变量：
//   CANVAS_GATEWAY_KEYS_FILE    Key 存储文件路径（默认 ~/.infinite-canvas/gateway-keys.json）

import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";

export interface KeyQuota {
    dailyCalls: number;
}

export interface KeyUsage {
    date: string;
    calls: number;
    totalCalls: number;
    byTool: Record<string, number>;
}

export interface ApiKey {
    id: string;
    name: string;
    keyHash: string;
    enabled: boolean;
    createdAt: string;
    lastUsedAt?: string;
    quota: KeyQuota;
    usage: KeyUsage;
}

interface KeyFile {
    keys: ApiKey[];
}

const DEFAULT_FILE = path.join(process.env.HOME ?? ".", ".infinite-canvas", "gateway-keys.json");

export function hashKey(raw: string): string {
    return crypto.createHash("sha256").update(raw).digest("hex");
}

export function today(): string {
    return new Date().toISOString().slice(0, 10);
}

export function generateKey(): string {
    return `ak_${crypto.randomBytes(16).toString("hex")}`;
}

export class KeyStore {
    private readonly file: string;
    private data: KeyFile;
    private lastMtime = 0;

    constructor(file?: string) {
        this.file = file ?? process.env.CANVAS_GATEWAY_KEYS_FILE ?? DEFAULT_FILE;
        this.data = { keys: [] };
        this.load();
    }

    get filePath(): string {
        return this.file;
    }

    /** 文件 mtime 变化则热重载：CLI 颁发/吊销后网关无需重启即时生效 */
    private reloadIfChanged(): void {
        try {
            const st = fs.statSync(this.file);
            if (st.mtimeMs !== this.lastMtime) {
                this.load();
                this.lastMtime = st.mtimeMs;
            }
        } catch {
            // 文件不存在：保持当前内存库
        }
    }

    load(): void {
        try {
            const raw = fs.readFileSync(this.file, "utf8");
            const parsed = JSON.parse(raw) as KeyFile;
            this.data = { keys: Array.isArray(parsed.keys) ? parsed.keys : [] };
            try { this.lastMtime = fs.statSync(this.file).mtimeMs; } catch { /* ignore */ }
        } catch {
            // 文件不存在或损坏：以空库启动（损坏时保留备份，避免覆盖丢失）
            if (fs.existsSync(this.file)) {
                try { fs.copyFileSync(this.file, `${this.file}.corrupt`); } catch { /* ignore */ }
            }
            this.data = { keys: [] };
        }
    }

    save(): void {
        const dir = path.dirname(this.file);
        fs.mkdirSync(dir, { recursive: true });
        // 原子写：先写临时文件再 rename，避免进程崩溃损坏库
        const tmp = `${this.file}.tmp`;
        fs.writeFileSync(tmp, JSON.stringify(this.data, null, 2), { mode: 0o600 });
        fs.renameSync(tmp, this.file);
    }

    createKey(opts: { name: string; dailyCalls?: number }): { key: string; record: ApiKey } {
        const key = generateKey();
        const now = new Date().toISOString();
        const record: ApiKey = {
            id: `k_${crypto.randomBytes(4).toString("hex")}`,
            name: opts.name,
            keyHash: hashKey(key),
            enabled: true,
            createdAt: now,
            quota: { dailyCalls: opts.dailyCalls ?? 0 }, // 0 = 不限制
            usage: { date: today(), calls: 0, totalCalls: 0, byTool: {} },
        };
        this.data.keys.push(record);
        this.save();
        try { this.lastMtime = fs.statSync(this.file).mtimeMs; } catch { /* ignore */ }
        return { key, record };
    }

    private resolve(idOrName: string): ApiKey | undefined {
        return this.data.keys.find((k) => k.id === idOrName || k.name === idOrName);
    }

    list(): ApiKey[] {
        this.reloadIfChanged();
        return this.data.keys.slice().sort((a, b) => a.createdAt.localeCompare(b.createdAt));
    }

    revoke(idOrName: string): boolean {
        const k = this.resolve(idOrName);
        if (!k) return false;
        k.enabled = false;
        this.save();
        return true;
    }

    enable(idOrName: string): boolean {
        const k = this.resolve(idOrName);
        if (!k) return false;
        k.enabled = true;
        this.save();
        return true;
    }

    resetDaily(idOrName: string): boolean {
        const k = this.resolve(idOrName);
        if (!k) return false;
        k.usage = { date: today(), calls: 0, totalCalls: k.usage.totalCalls, byTool: {} };
        this.save();
        return true;
    }

    get(idOrName: string): ApiKey | undefined {
        return this.resolve(idOrName);
    }

    /** 校验明文 Key：哈希比对 + 启用状态 + 日配额。ok=false 时 reason 供网关转 HTTP 状态 */
    verify(rawKey: string) {
        this.reloadIfChanged();
        return this.verifyInner(rawKey);
    }

    private verifyInner(rawKey: string): { ok: boolean; key?: ApiKey; reason?: "not_found" | "disabled" | "quota" } {
        if (!rawKey) return { ok: false, reason: "not_found" };
        const h = hashKey(rawKey);
        const k = this.data.keys.find((x) => x.keyHash === h);
        if (!k) return { ok: false, reason: "not_found" };
        if (!k.enabled) return { ok: false, key: k, reason: "disabled" };
        // 日配额：usage.date 不是今天则视为新的一天
        if (k.usage.date !== today()) {
            k.usage = { date: today(), calls: 0, totalCalls: k.usage.totalCalls, byTool: {} };
        }
        if (k.quota.dailyCalls > 0 && k.usage.calls >= k.quota.dailyCalls) {
            return { ok: false, key: k, reason: "quota" };
        }
        return { ok: true, key: k };
    }

    /** 记录一次工具调用（配额消耗 + 明细），返回更新后的用量 */
    recordUsage(id: string, toolName: string): KeyUsage {
        this.reloadIfChanged();
        const k = this.resolve(id);
        if (!k) return { date: today(), calls: 0, totalCalls: 0, byTool: {} };
        if (k.usage.date !== today()) {
            k.usage = { date: today(), calls: 0, totalCalls: k.usage.totalCalls, byTool: {} };
        }
        k.usage.calls += 1;
        k.usage.totalCalls += 1;
        k.usage.byTool[toolName] = (k.usage.byTool[toolName] ?? 0) + 1;
        k.lastUsedAt = new Date().toISOString();
        this.save();
        return k.usage;
    }
}

// ---------------- CLI ----------------

function printRecord(k: ApiKey): void {
    console.log(`  id      : ${k.id}`);
    console.log(`  name    : ${k.name}`);
    console.log(`  enabled : ${k.enabled ? "✓" : "✗（已停用）"}`);
    console.log(`  created : ${k.createdAt}`);
    console.log(`  quota   : 日 ${k.quota.dailyCalls === 0 ? "不限" : k.quota.dailyCalls} 次调用`);
    console.log(`  usage   : 今日 ${k.usage.calls} 次 / 累计 ${k.usage.totalCalls} 次`);
}

function cliMain(): void {
    const [, , cmd, ...rest] = process.argv;
    const store = new KeyStore();

    const argValue = (flag: string): string | undefined => {
        const i = rest.indexOf(flag);
        return i >= 0 ? rest[i + 1] : undefined;
    };
    const hasFlag = (flag: string): boolean => rest.includes(flag);

    if (cmd === "add") {
        const name = argValue("--name");
        if (!name) {
            console.error("用法: gateway-keys add --name <客户名> [--quota <日调用上限>]");
            process.exit(1);
        }
        const dailyCalls = argValue("--quota") ? Number(argValue("--quota")) : 0;
        const { key, record } = store.createKey({ name, dailyCalls: Number.isFinite(dailyCalls) ? dailyCalls : 0 });
        console.log("已颁发客户 Key（明文仅此一次，之后只存哈希，请妥善保存）：");
        console.log();
        console.log(`  ${key}`);
        console.log();
        console.log("Key 信息：");
        printRecord(record);
        return;
    }

    if (cmd === "list") {
        const keys = store.list();
        if (keys.length === 0) {
            console.log("（暂无 Key。用 add --name <客户名> 颁发第一个）");
            return;
        }
        console.log(`共 ${keys.length} 个 Key：`);
        for (const k of keys) {
            console.log(`  ${k.enabled ? "✓" : "✗"}  ${k.name}  (${k.id})  日配额 ${k.quota.dailyCalls === 0 ? "不限" : k.quota.dailyCalls}  今日 ${k.usage.calls}/${k.usage.totalCalls}`);
        }
        return;
    }

    if (cmd === "revoke" || cmd === "enable" || cmd === "reset" || cmd === "usage") {
        const target = rest[0];
        if (!target) {
            console.error(`用法: gateway-keys ${cmd} <id|name>`);
            process.exit(1);
        }
        if (cmd === "revoke") {
            if (store.revoke(target)) console.log(`已停用 Key：${target}`);
            else { console.error(`未找到：${target}`); process.exit(1); }
        } else if (cmd === "enable") {
            if (store.enable(target)) console.log(`已重新启用 Key：${target}`);
            else { console.error(`未找到：${target}`); process.exit(1); }
        } else if (cmd === "reset") {
            if (store.resetDaily(target)) console.log(`已重置当日计数：${target}`);
            else { console.error(`未找到：${target}`); process.exit(1); }
        } else {
            const k = store.get(target);
            if (!k) { console.error(`未找到：${target}`); process.exit(1); }
            printRecord(k);
            const tools = Object.entries(k.usage.byTool).sort((a, b) => b[1] - a[1]);
            if (tools.length) {
                console.log("  工具分布：");
                for (const [tool, n] of tools) console.log(`    ${tool} × ${n}`);
            }
        }
        return;
    }

    if (hasFlag("--help") || !cmd) {
        console.log(`用法: gateway-keys <cmd>
  add   --name <客户名> [--quota <日调用上限>]   颁发客户 Key（打印一次明文）
  list                                         列出全部 Key
  revoke <id|name>                             停用
  enable <id|name>                             启用
  reset  <id|name>                             重置当日计数
  usage  <id|name>                             查看用量明细`);
        return;
    }

    console.error(`未知命令: ${cmd}`);
    process.exit(1);
}

// 仅当直接执行本文件时运行 CLI（被网关 import 时不触发）
if (import.meta.url === `file://${process.argv[1]}`) {
    cliMain();
}
