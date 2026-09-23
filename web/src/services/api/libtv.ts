import type { CanvasConnection } from "@/types/canvas";
import { http } from "@/services/api/request";

export type AdminLibTVSetting = {
    enabled: boolean;
    hasToken: boolean;
    updatedAt?: string;
};

export type LibTVImportIssue = { id?: string; name?: string; reason: string };
export type LibTVImportWarning = { id?: string; message: string };

export type LibTVImportNode = {
    id: string;
    type: "image" | "video";
    title: string;
    x: number;
    y: number;
    width: number;
    height: number;
    content: string;
    prompt?: string;
    model?: string;
    naturalWidth?: number;
    naturalHeight?: number;
    durationMs?: number;
    mimeType?: string;
    status?: "idle" | "success" | "error";
    errorDetails?: string;
    metadata: {
        provider: "libtv";
        projectUuid: string;
        nodeKey: string;
        batchId: string;
        sourceType?: string;
        styleAssetUuid?: string;
        styleVersionUuid?: string;
        styleName?: string;
    };
};

export type LibTVImportResult = {
    batchId: string;
    batchCreatedAt: string;
    projectUuid: string;
    projectName: string;
    nodes: LibTVImportNode[];
    connections: CanvasConnection[];
    importedNodeCount: number;
    importedConnectionCount: number;
    skippedNodes: LibTVImportIssue[];
    skippedConnections: LibTVImportIssue[];
    warnings: LibTVImportWarning[];
    multiResultNodeCount: number;
    staleNodeCount: number;
    reusedFailedNodeCount: number;
    placeholderNodeCount: number;
    convertedSpecialCount: number;
};

export function getAdminLibTVSetting() {
    return http.get<{ setting: AdminLibTVSetting }>("/admin/settings/libtv");
}

export function updateAdminLibTVSetting(input: { enabled: boolean; token?: string; clearToken?: boolean }) {
    return http.patch<{ setting: AdminLibTVSetting }>("/admin/settings/libtv", input);
}

export function testAdminLibTV(uuid: string) {
    return http.post<{ ok: boolean }>("/admin/settings/libtv/test", { uuid });
}

export function importLibTVCanvas(projectId: string, uuid: string) {
    return http.post<LibTVImportResult>(`/canvas-projects/${encodeURIComponent(projectId)}/import/libtv`, { uuid });
}
