import { create } from "zustand";
import { persist } from "zustand/middleware";
import type { CanvasProject } from "@/stores/canvas/use-canvas-store";

export type DeletedCanvasHistoryItem = {
    id: string;
    title: string;
    createdAt: string;
    updatedAt: string;
    deletedAt: string;
    nodeCount: number;
    coverUrl?: string;
};

type CanvasHistoryStore = {
    deletedProjects: DeletedCanvasHistoryItem[];
    recordDeletedProjects: (projects: CanvasProject[]) => void;
    removeDeletedHistoryItem: (id: string) => void;
    clearDeletedHistory: () => void;
};

export const useCanvasHistoryStore = create<CanvasHistoryStore>()(
    persist(
        (set, get) => ({
            deletedProjects: [],
            recordDeletedProjects: (projects) => {
                const now = new Date().toISOString();
                const newItems: DeletedCanvasHistoryItem[] = projects.map((p) => ({
                    id: p.id,
                    title: p.title || "未命名画布",
                    createdAt: p.createdAt || p.updatedAt || now,
                    updatedAt: p.updatedAt || now,
                    deletedAt: now,
                    nodeCount: p.nodes?.length || 0,
                    coverUrl: p.nodes?.find((n) => n.type === "image" && n.metadata?.content)?.metadata?.content as string | undefined,
                }));
                set((state) => {
                    const existingIds = new Set(newItems.map((item) => item.id));
                    const filtered = state.deletedProjects.filter((item) => !existingIds.has(item.id));
                    return {
                        deletedProjects: [...newItems, ...filtered].slice(0, 200),
                    };
                });
            },
            removeDeletedHistoryItem: (id) =>
                set((state) => ({
                    deletedProjects: state.deletedProjects.filter((item) => item.id !== id),
                })),
            clearDeletedHistory: () => set({ deletedProjects: [] }),
        }),
        {
            name: "infinite-canvas:deleted_history_store",
        },
    ),
);
