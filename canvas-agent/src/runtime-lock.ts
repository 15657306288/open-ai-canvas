// [connector] P0-A-3 单实例锁 + 权威运行时入口
//
// 背景：masterToken 链接"不稳定"的根因之一是多实例端口漂移——同一 config 目录下若同时
// 启动多个 Runtime 进程（codex / 前端 / 其他 agent 各自拉起），它们共享同一 token 却可能
// 监听不同端口，浏览器或 agent 配置的 URL 一旦指向旧端口就 404/401。
//
// 本模块用"独占锁文件 + pid 存活检测"保证同一 config 目录同时只有一个 Runtime 实例：
//  - 第二个实例启动时若检测到存活实例则明确拒绝（而非静默漂移到新端口）；
//  - 锁文件同时记录权威的 { endpoint, token, port, pid }，任何外部进程/脚本都可读取
//    锁文件获得"当前真正在跑的实例地址"，不再依赖猜测或环境变量。
//
// 锁文件位置：<CONFIG_DIR>/runtime.lock

import fs from "node:fs";
import os from "node:os";
import path from "node:path";

export type RuntimeLockInfo = {
    pid: number;
    port: number;
    token: string;
    endpoint: string;
    startedAt: number;
    hostname: string;
};

export type AcquireRuntimeLockOptions = {
    /** 锁文件绝对路径（测试可指向临时目录） */
    lockFilePath: string;
    port: number;
    token: string;
    endpoint: string;
    now?: () => number;
    log?: (line: string) => void;
};

export type AcquireRuntimeLockResult =
    | { acquired: true; release: () => void }
    | { acquired: false; existing: RuntimeLockInfo };

/** 读取当前锁信息；文件不存在或损坏返回 null */
export function readRuntimeLock(lockFilePath: string): RuntimeLockInfo | null {
    try {
        const raw = JSON.parse(fs.readFileSync(lockFilePath, "utf8")) as RuntimeLockInfo;
        if (typeof raw.pid !== "number" || typeof raw.port !== "number"
            || typeof raw.token !== "string" || typeof raw.endpoint !== "string") {
            return null;
        }
        return raw;
    } catch {
        return null;
    }
}

/**
 * 尝试获取单实例锁。
 * - 成功：返回 { acquired: true, release }，调用方负责在进程退出时 release。
 * - 失败：已有存活实例占用，返回其信息；调用方应复用该地址而非再起新实例。
 * - 陈旧锁（pid 已死或文件损坏）：自动覆盖后重新获取。
 */
export function acquireRuntimeLock(options: AcquireRuntimeLockOptions): AcquireRuntimeLockResult {
    const { lockFilePath, port, token, endpoint } = options;
    const now = options.now ?? Date.now;
    const log = options.log ?? (() => undefined);
    fs.mkdirSync(path.dirname(lockFilePath), { recursive: true });
    const info: RuntimeLockInfo = { pid: process.pid, port, token, endpoint, startedAt: now(), hostname: os.hostname() };
    try {
        writeLock(lockFilePath, info);
        log(`[runtime-lock] acquired ${lockFilePath}`);
        return { acquired: true, release: () => releaseRuntimeLock(lockFilePath, process.pid) };
    } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
        const existing = readRuntimeLock(lockFilePath);
        if (existing && isProcessAlive(existing.pid)) {
            // 已有存活实例：不覆盖，返回其信息让调用方复用
            return { acquired: false, existing };
        }
        // 陈旧锁（进程已死/文件损坏）→ 移除后重试一次
        log(`[runtime-lock] stale lock detected, replacing ${lockFilePath}`);
        try { fs.unlinkSync(lockFilePath); } catch { /* ignore */ }
        try {
            writeLock(lockFilePath, info);
            return { acquired: true, release: () => releaseRuntimeLock(lockFilePath, process.pid) };
        } catch (retryError) {
            if ((retryError as NodeJS.ErrnoException).code === "EEXIST") {
                const raced = readRuntimeLock(lockFilePath);
                if (raced && isProcessAlive(raced.pid)) return { acquired: false, existing: raced };
            }
            throw retryError;
        }
    }
}

/** 释放锁（仅当锁仍归属本 pid，避免误删他人锁） */
export function releaseRuntimeLock(lockFilePath: string, ownerPid = process.pid) {
    const existing = readRuntimeLock(lockFilePath);
    if (!existing || existing.pid !== ownerPid) return;
    try { fs.unlinkSync(lockFilePath); } catch { /* ignore */ }
}

function writeLock(lockFilePath: string, info: RuntimeLockInfo) {
    const fd = fs.openSync(lockFilePath, "wx");
    try {
        fs.writeFileSync(fd, JSON.stringify(info, null, 2));
    } finally {
        fs.closeSync(fd);
    }
}

function isProcessAlive(pid: number) {
    if (!Number.isInteger(pid) || pid <= 0) return false;
    try {
        process.kill(pid, 0);
        return true;
    } catch (error) {
        // EPERM：进程存在但当前用户无权发信号 → 视为存活
        return (error as NodeJS.ErrnoException).code === "EPERM";
    }
}
