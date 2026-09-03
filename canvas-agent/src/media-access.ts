// [connector] P1-Q5 画布媒体读取（用户拍板 Q5：允许外部 Agent 读画布媒体内容）
//
// 安全模型（roadmap §8）：
//   - block 模式：图片等以 base64 返回，默认上限 8MB，超限提示改用 url
//   - url 模式：短 TTL（默认 5min）单次签名 URL，绑定 nodeId，消费即失效
//   - 不落地、不缓存、不进日志；全部读取经 onAudit 审计
// 媒体引用来自节点 metadata（dataUrl/url/previewUrl/coverUrl），外部投影（compactNode）不含。

import crypto from "node:crypto";

import { agentFetch } from "./agent-fetch.js";
import type { CanvasNode } from "./types.js";

export type MediaReadMode = "block" | "url";

export interface MediaReadInput {
    mode?: MediaReadMode;
    maxBytes?: number;
}

export type MediaReadResult =
    | { mode: "block"; mimeType: string; dataBase64: string; bytes: number }
    | { mode: "url"; url: string; token: string; expiresAtMs: number };

export interface MediaAuditEntry {
    nodeId: string;
    mode: MediaReadMode;
    bytes: number;
    atMs: number;
}

export interface CanvasMediaAccessOptions {
    /** block 返回的字节上限，默认 8MB */
    maxBlockBytes?: number;
    /** 签名 URL 有效时长，默认 5 分钟 */
    urlTtlMs?: number;
    onAudit?: (entry: MediaAuditEntry) => void;
}

type MediaRef =
    | { kind: "dataUrl"; value: string }
    | { kind: "http"; value: string };

export class CanvasMediaAccess {
    private readonly signed = new Map<string, { nodeId: string; expiresAtMs: number; remaining: number }>();

    constructor(private readonly opts: CanvasMediaAccessOptions = {}) {}

    get maxBlockBytes(): number {
        return this.opts.maxBlockBytes ?? 8 * 1024 * 1024;
    }
    get urlTtlMs(): number {
        return this.opts.urlTtlMs ?? 5 * 60_000;
    }

    /** 读取节点媒体：mode=block 返回 base64；mode=url 生成短 TTL 单次签名 URL */
    async getNodeMedia(node: CanvasNode, input: MediaReadInput = {}): Promise<MediaReadResult> {
        const ref = findMediaRef(node);
        if (!ref) throw new Error(`节点 ${node.id} 没有可读媒体`);
        if (input.mode === "url") return this.createSignedUrl(node.id);
        const { bytes, mimeType } = await this.loadNodeMedia(node);
        const max = input.maxBytes ?? this.maxBlockBytes;
        if (bytes.length > max) {
            throw new Error(`媒体 ${bytes.length} 字节超过 block 上限 ${max}（${Math.floor(max / 1024 / 1024)}MB），请改用 mode=url`);
        }
        this.opts.onAudit?.({ nodeId: node.id, mode: "block", bytes: bytes.length, atMs: Date.now() });
        return { mode: "block", mimeType, dataBase64: bytes.toString("base64"), bytes: bytes.length };
    }

    /** 加载节点媒体字节（签名 URL 消费与 block 共用） */
    async loadNodeMedia(node: CanvasNode): Promise<{ bytes: Buffer; mimeType: string }> {
        const ref = findMediaRef(node);
        if (!ref) throw new Error(`节点 ${node.id} 没有可读媒体`);
        return loadBytes(ref);
    }

    createSignedUrl(nodeId: string): MediaReadResult {
        const token = crypto.randomBytes(16).toString("hex");
        const expiresAtMs = Date.now() + this.urlTtlMs;
        this.signed.set(token, { nodeId, expiresAtMs, remaining: 1 });
        this.opts.onAudit?.({ nodeId, mode: "url", bytes: 0, atMs: Date.now() });
        return { mode: "url", url: `/api/media/${token}`, token, expiresAtMs };
    }

    /** 消费签名 token：有效期内且未用过则返回 nodeId 并标记已用；否则 undefined */
    consumeToken(token: string): { nodeId: string } | undefined {
        const entry = this.signed.get(token);
        if (!entry) return undefined;
        if (entry.expiresAtMs < Date.now() || entry.remaining <= 0) {
            this.signed.delete(token);
            return undefined;
        }
        entry.remaining -= 1;
        if (entry.remaining <= 0) this.signed.delete(token);
        return { nodeId: entry.nodeId };
    }

    /** 清理已过期 token（可周期性调用） */
    sweepExpired(): number {
        const now = Date.now();
        let removed = 0;
        for (const [token, entry] of this.signed) {
            if (entry.expiresAtMs < now) {
                this.signed.delete(token);
                removed++;
            }
        }
        return removed;
    }
}

function findMediaRef(node: CanvasNode): MediaRef | undefined {
    const metadata = node.metadata ?? {};
    for (const key of ["dataUrl", "url", "previewUrl", "coverUrl"] as const) {
        const value = metadata[key];
        if (typeof value === "string" && value) {
            if (value.startsWith("data:")) return { kind: "dataUrl", value };
            if (/^https?:\/\//i.test(value)) return { kind: "http", value };
        }
    }
    return undefined;
}

async function loadBytes(ref: MediaRef): Promise<{ bytes: Buffer; mimeType: string }> {
    if (ref.kind === "dataUrl") {
        const match = /^data:([^;,]*)?(;base64)?,(.*)$/s.exec(ref.value);
        if (!match) throw new Error("无效的 dataUrl");
        const mimeType = match[1] || "application/octet-stream";
        const base64 = match[2] ? match[3] : Buffer.from(match[3], "utf8").toString("base64");
        const bytes = Buffer.from(base64, "base64");
        return { bytes, mimeType };
    }
    // http(s)：GET 只读，agentFetch 自带 keepalive 与只读重试
    const res = await agentFetch(ref.value, { method: "GET", timeoutMs: 30_000 });
    if (!res.ok) throw new Error(`媒体加载失败：HTTP ${res.status}`);
    const mimeType = res.headers.get("content-type") ?? "application/octet-stream";
    const bytes = Buffer.from(await res.arrayBuffer());
    return { bytes, mimeType };
}
