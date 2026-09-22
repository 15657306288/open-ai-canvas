import { create } from "zustand";

import {
    batchDoubaoAccounts,
    clearAllDoubaoCooldowns,
    cooldownDoubaoAccount,
    fetchDoubaoPoolStatus,
    addDoubaoAccount,
    bulkImportDoubaoAccounts,
    removeDoubaoAccount,
    updateDoubaoAccount,
    type DoubaoAccountState,
    type DoubaoAccountView,
    type DoubaoBatchAction,
    type DoubaoBulkImportResult,
    type DoubaoPoolStatus,
    type PoolSite,
} from "@/services/api/doubao-accounts";

/**
 * 多站点账号池（豆包 / Dola / 即梦，后端 API 驱动）。
 *
 * 完整 Cookie 只保存在后端账号池服务（云端 PostgreSQL），浏览器端只持有脱敏视图。
 * 状态分桶（ready / cooling / expired / disabled）由后端统一计算，
 * 取号、冷却、自动换号语义与豆包创作工作台参考实现一致；
 * 列表、统计、批量操作均按当前站点（store.site）隔离。
 */

export type DoubaoAccount = DoubaoAccountView;
export type DoubaoAccountStatus = DoubaoAccountState;
export type { DoubaoPoolStatus, DoubaoBulkImportResult, PoolSite };

export const DOUBAO_COOLDOWN_MINUTES = 30;

type DoubaoAccountStore = {
    site: PoolSite;
    accounts: DoubaoAccount[];
    stats: DoubaoPoolStatus | null;
    loading: boolean;
    loaded: boolean;
    setSite: (site: PoolSite) => Promise<void>;
    refresh: () => Promise<void>;
    addAccount: (input: { displayName?: string; cookieText: string; note?: string; setAsCurrent?: boolean }) => Promise<void>;
    bulkImport: (input: { text: string; tags?: string[]; setActive?: boolean }) => Promise<DoubaoBulkImportResult>;
    updateAccount: (id: string, patch: { label?: string; note?: string; tags?: string[]; enabled?: boolean }) => Promise<void>;
    removeAccounts: (ids: string[]) => Promise<void>;
    setCurrentAccount: (id: string) => Promise<void>;
    batch: (action: DoubaoBatchAction, ids: string[], tags?: string[]) => Promise<number>;
    clearCooldown: (ids: string[]) => Promise<void>;
    clearAllCooldowns: () => Promise<void>;
    cooldown: (id: string, minutes?: number) => Promise<void>;
};

function applyStatus(status: DoubaoPoolStatus) {
    return { accounts: status.accounts, stats: status, loading: false, loaded: true };
}

export const useDoubaoAccountStore = create<DoubaoAccountStore>()((set, get) => ({
    site: "doubao",
    accounts: [],
    stats: null,
    loading: false,
    loaded: false,

    setSite: async (site) => {
        if (get().site === site) {
            return;
        }
        set({ site, accounts: [], stats: null, loaded: false });
        await get().refresh();
    },

    refresh: async () => {
        set({ loading: true });
        try {
            const status = await fetchDoubaoPoolStatus(get().site);
            set(applyStatus(status));
        } catch (error) {
            set({ loading: false });
            throw error;
        }
    },

    addAccount: async ({ displayName, cookieText, note = "", setAsCurrent = false }) => {
        await addDoubaoAccount({ cookie: cookieText, site: get().site, label: displayName?.trim() || undefined, note, setActive: setAsCurrent });
        await get().refresh();
    },

    bulkImport: async ({ text, tags = [], setActive = false }) => {
        const { result, status } = await bulkImportDoubaoAccounts({ text, site: get().site, tags, setActive });
        set(applyStatus(status));
        return result;
    },

    updateAccount: async (id, patch) => {
        await updateDoubaoAccount(id, patch);
        await get().refresh();
    },

    removeAccounts: async (ids) => {
        for (const id of ids) {
            await removeDoubaoAccount(id);
        }
        await get().refresh();
    },

    setCurrentAccount: async (id) => {
        await batchDoubaoAccounts("activate", [id], undefined, get().site);
        await get().refresh();
    },

    batch: async (action, ids, tags) => {
        const { affected, status } = await batchDoubaoAccounts(action, ids, tags, get().site);
        set(applyStatus(status));
        return affected;
    },

    clearCooldown: async (ids) => {
        await batchDoubaoAccounts("clear-cooldown", ids, undefined, get().site);
        await get().refresh();
    },

    clearAllCooldowns: async () => {
        await clearAllDoubaoCooldowns();
        await get().refresh();
    },

    cooldown: async (id, minutes = DOUBAO_COOLDOWN_MINUTES) => {
        await cooldownDoubaoAccount(id, minutes * 60_000);
        await get().refresh();
    },
}));

/** 后端已统一计算有效状态，这里直通保留兼容旧的调用点。 */
export function effectiveStatus(account: DoubaoAccount): DoubaoAccountStatus {
    return account.state;
}

export const DOUBAO_STATUS_META: Record<DoubaoAccountStatus, { label: string; color: string; kind: "success" | "warning" | "error" | "default" }> = {
    ready: { label: "可用", color: "green", kind: "success" },
    cooling: { label: "冷却中", color: "gold", kind: "warning" },
    expired: { label: "登录失效", color: "red", kind: "error" },
    disabled: { label: "已停用", color: "default", kind: "default" },
};

