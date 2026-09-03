// [connector] P0-B-4 渠道/模型目录 —— 数据层（"模型目录是动态数据，工具集保持稳定"）
//
// 来源：CONFIG_DIR/channel-catalog.json（声明式目录文件，含渠道连接信息，本机专属不进 git；
//       仓库内置 examples/channel-catalog.example.json 无密钥示例）。
// 密钥隔离：listChannels/listModels 等只读视图绝不返回 apiKey；resolveChannel 仅 generate 内部用。
// 三层更新机制（roadmap §7.3）：
//   层1 拉取即最新：每次调用 stat 检查 mtime，变化才重解析（无进程内常驻过期缓存）
//   层2 版本探测：channel_catalog_version 返回 version/updatedAt/hash/counts 供比对
//   层3 变更推送：fs.watch 监听目录文件，变更触发 onChange → MCP notifications/tools/list_changed

import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

export type ModelCapability = "text" | "image" | "video" | "audio";

export interface ChannelDef {
    id: string;
    name: string;
    protocol: "openai-compatible";
    baseUrl: string;
    apiKey?: string;
    enabled: boolean;
    /** 视频/任务型渠道的任务提交地址（如 /v1/video/generations），缺省时 generate 提示未配置 */
    videoUrl?: string;
    /** 任务查询地址模板，{requestId} 占位；缺省时任务型渠道不支持主动查询 */
    taskUrl?: string;
}

export interface ModelTaskParameterDef {
    name: string;
    label?: string;
    type: string;
    required?: boolean;
    options?: Array<{ label: string; value: string }>;
    defaultValue?: unknown;
    min?: number;
    max?: number;
}

export interface ModelTaskDef {
    kind: string;
    parameters?: ModelTaskParameterDef[];
}

export interface ChannelModelDef {
    key: string;
    channelId: string;
    name?: string;
    capability: ModelCapability;
    enabled: boolean;
    pricing?: { type: "per_call" | "per_second"; amount: number; currency: string };
    tasks?: ModelTaskDef[];
    extras?: Record<string, unknown>;
}

export interface LogicalModelDef {
    id: string;
    name: string;
    capability: ModelCapability;
    lines: string[];
}

export interface ChannelCatalogFile {
    version?: number;
    updatedAt?: string;
    channels: ChannelDef[];
    models: ChannelModelDef[];
    logicalModels?: LogicalModelDef[];
}

// ---------------- 只读视图（不含密钥） ----------------
export interface ChannelView {
    id: string;
    name: string;
    protocol: string;
    enabled: boolean;
    modelCount: number;
}
export interface ModelView {
    key: string;
    channelId: string;
    name: string;
    capability: ModelCapability;
    enabled: boolean;
    pricing?: { type: string; amount: number; currency: string };
    tasks?: ModelTaskDef[];
}
export interface LogicalModelView {
    id: string;
    name: string;
    capability: ModelCapability;
    lines: string[];
}
export interface CatalogVersionView {
    version: string;
    updatedAt: number;
    hash: string;
    counts: { channels: number; models: number; logicalModels: number };
}

export interface ChannelCatalogProvider {
    catalogVersion(): CatalogVersionView;
    listChannels(): ChannelView[];
    listModels(filter?: { channelId?: string; capability?: ModelCapability; enabled?: boolean }): ModelView[];
    listLogicalModels(): LogicalModelView[];
    getModel(modelKey: string): ModelView | undefined;
    getCapability(modelKey: string): ModelView | undefined;
    /** 含密钥渠道信息，仅 channel_generate 内部使用；绝不通过只读工具返回 */
    resolveChannel(channelId: string): ChannelDef | undefined;
    onChange(cb: () => void): () => void;
    close(): void;
}

export interface JsonCatalogProviderOptions {
    /** 解析失败时是否抛出；默认 false（返回空目录，日志记录） */
    strict?: boolean;
}

export function createJsonCatalogProvider(filePath: string, options: JsonCatalogProviderOptions = {}): ChannelCatalogProvider {
    let cached: ChannelCatalogFile | undefined;
    let cachedStat: { mtimeMs: number; size: number } | undefined;
    let cachedHash = "";
    let lastError: string | undefined;
    const listeners = new Set<() => void>();
    let watcher: fs.FSWatcher | undefined;

    const statFile = (): { mtimeMs: number; size: number } | undefined => {
        try {
            const stat = fs.statSync(filePath);
            return { mtimeMs: stat.mtimeMs, size: stat.size };
        } catch {
            return undefined;
        }
    };

    const ensureLoaded = (): ChannelCatalogFile => {
        const stat = statFile();
        const key = stat ? `${stat.mtimeMs}:${stat.size}` : "missing";
        if (cached && cachedStat && `${cachedStat.mtimeMs}:${cachedStat.size}` === key) {
            return cached;
        }
        if (!stat) {
            cached = undefined;
            cachedStat = undefined;
            cachedHash = "";
            lastError = undefined;
            return emptyCatalog();
        }
        try {
            const raw = fs.readFileSync(filePath, "utf8");
            const parsed = JSON.parse(raw) as ChannelCatalogFile;
            const hash = crypto.createHash("sha256").update(raw).digest("hex").slice(0, 12);
            cached = normalize(parsed);
            cachedStat = stat;
            cachedHash = hash;
            lastError = undefined;
            return cached;
        } catch (error) {
            cached = undefined;
            cachedStat = stat;
            cachedHash = "";
            lastError = error instanceof Error ? error.message : String(error);
            return emptyCatalog();
        }
    };

    const emptyCatalog = (): ChannelCatalogFile => ({ channels: [], models: [], logicalModels: [] });

    function normalize(file: ChannelCatalogFile): ChannelCatalogFile {
        return {
            version: file.version,
            updatedAt: file.updatedAt,
            channels: Array.isArray(file.channels) ? file.channels : [],
            models: Array.isArray(file.models) ? file.models : [],
            logicalModels: Array.isArray(file.logicalModels) ? file.logicalModels : [],
        };
    }

    const tryWatch = () => {
        if (watcher) return;
        try {
            // watch 目录而非单文件：macOS 上覆盖写入（writeFileSync）对单文件 fs.watch
            // 不可靠，目录 watch 能同时捕获 change 与原子替换（rename）两种写法。
            watcher = fs.watch(path.dirname(filePath), () => {
                cached = undefined; // 强制下次重读（层1）
                for (const cb of listeners) cb(); // 层3：变更推送
            });
            // 不阻止进程退出（测试/短生命周期进程可正常结束；常驻 Runtime 无影响）
            watcher.unref?.();
        } catch {
        // 目录不存在或平台不支持时降级为"仅拉取即最新"（层1/层2 仍有效）
        }
    };
    tryWatch();

    const provider: ChannelCatalogProvider = {
        catalogVersion() {
            const file = ensureLoaded();
            const stat = cachedStat;
            return {
                version: file.version !== undefined ? String(file.version) : (stat ? String(Math.floor(stat.mtimeMs)) : "empty"),
                updatedAt: stat ? Math.floor(stat.mtimeMs) : 0,
                hash: cachedHash || (file.channels.length || file.models.length ? "dirty" : "empty"),
                counts: {
                    channels: file.channels.length,
                    models: file.models.length,
                    logicalModels: file.logicalModels?.length ?? 0,
                },
            };
        },
        listChannels() {
            const file = ensureLoaded();
            return file.channels.map((channel) => ({
                id: channel.id,
                name: channel.name,
                protocol: channel.protocol,
                enabled: channel.enabled,
                modelCount: file.models.filter((m) => m.channelId === channel.id).length,
            }));
        },
        listModels(filter) {
            const file = ensureLoaded();
            return file.models
                .filter((m) => {
                    if (filter?.channelId && m.channelId !== filter.channelId) return false;
                    if (filter?.capability && m.capability !== filter.capability) return false;
                    if (filter?.enabled !== undefined && m.enabled !== filter.enabled) return false;
                    return true;
                })
                .map((m) => ({
                    key: m.key,
                    channelId: m.channelId,
                    name: m.name ?? m.key,
                    capability: m.capability,
                    enabled: m.enabled,
                    pricing: m.pricing,
                    tasks: m.tasks,
                }));
        },
        listLogicalModels() {
            const file = ensureLoaded();
            return (file.logicalModels ?? []).map((m) => ({ id: m.id, name: m.name, capability: m.capability, lines: m.lines }));
        },
        getModel(modelKey) {
            return this.listModels().find((m) => m.key === modelKey);
        },
        getCapability(modelKey) {
            return this.getModel(modelKey);
        },
        resolveChannel(channelId) {
            const file = ensureLoaded();
            return file.channels.find((c) => c.id === channelId);
        },
        onChange(cb) {
            listeners.add(cb);
            return () => listeners.delete(cb);
        },
        close() {
            watcher?.close();
            watcher = undefined;
            listeners.clear();
        },
    };
    return provider;
}

/** 便捷：默认目录文件路径 */
export function channelCatalogPath(configDir: string): string {
    return path.join(configDir, "channel-catalog.json");
}

/** 便捷：provider 是否存在（供检测是否可生成） */
export function hasChannelCatalog(filePath: string): boolean {
    try {
        const stat = fs.statSync(filePath);
        return stat.isFile();
    } catch {
        return false;
    }
}
