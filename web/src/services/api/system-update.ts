import { apiClient, request } from "./request";

export type UpdatePhase =
    | "idle"
    | "checking"
    | "ready"
    | "no_update"
    | "preflight"
    | "backing_up"
    | "pulling"
    | "draining"
    | "migrating"
    | "switching"
    | "verifying"
    | "succeeded"
    | "rolling_back"
    | "rolled_back"
    | "failed"
    | "manual_intervention";

export type SystemUpdateRelease = {
    version: string;
    name: string;
    body: string;
    url: string;
    publishedAt: string;
    prerelease: boolean;
};

export type SystemUpdateCheck = {
    key: string;
    label: string;
    status: "passed" | "pending" | "failed" | string;
    detail?: string;
    blocking: boolean;
};

export type SystemUpdateBackup = {
    id: string;
    path: string;
    checksum: string;
    size: number;
    createdAt: string;
    version: string;
};

export type SystemUpdateLog = {
    at: string;
    phase: UpdatePhase;
    message: string;
};

export type SystemUpdateOperation = {
    id?: string;
    phase: UpdatePhase;
    fromVersion?: string;
    targetVersion?: string;
    startedAt?: string;
    finishedAt?: string;
    error?: string;
    rollbackError?: string;
    automaticRollback: boolean;
    logs: SystemUpdateLog[];
};

export type SystemUpdateStatus = {
    supported: boolean;
    connected: boolean;
    repository: string;
    deployment: string;
    currentVersion: string;
    latestRelease?: SystemUpdateRelease;
    updateAvailable: boolean;
    checks: SystemUpdateCheck[];
    lastBackup?: SystemUpdateBackup;
    rollbackVersion?: string;
    operation: SystemUpdateOperation;
};

export function getSystemUpdateStatus(signal?: AbortSignal) {
    return request<SystemUpdateStatus>(apiClient.get("/admin/system-update", { signal }));
}

export function checkSystemUpdate() {
    return request<SystemUpdateStatus>(apiClient.post("/admin/system-update/check"));
}

export function startSystemUpdate(targetVersion: string) {
    return request<SystemUpdateStatus>(apiClient.post("/admin/system-update/start", { targetVersion }));
}

export function rollbackSystemUpdate(reason: string) {
    return request<SystemUpdateStatus>(apiClient.post("/admin/system-update/rollback", { reason }));
}
