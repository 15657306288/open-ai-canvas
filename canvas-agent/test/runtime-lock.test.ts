import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { acquireRuntimeLock, readRuntimeLock, releaseRuntimeLock } from "../src/runtime-lock.js";

function tempLockFile(prefix: string) {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), `runtime-lock-${prefix}-`));
    return { dir, file: path.join(dir, "runtime.lock") };
}

test("[connector] P0-A-3 acquire writes authoritative endpoint/token and release removes it", () => {
    const { file } = tempLockFile("basic");
    const result = acquireRuntimeLock({ lockFilePath: file, port: 17371, token: "t-abc", endpoint: "http://127.0.0.1:17371" });
    assert.equal(result.acquired, true);
    if (!result.acquired) return;
    const info = readRuntimeLock(file);
    assert.ok(info);
    assert.equal(info.port, 17371);
    assert.equal(info.token, "t-abc");
    assert.equal(info.endpoint, "http://127.0.0.1:17371");
    assert.equal(info.pid, process.pid);
    result.release();
    assert.equal(readRuntimeLock(file), null, "release 后锁应被移除");
});

test("[connector] P0-A-3 a second acquire is rejected with the existing instance info", () => {
    const { file } = tempLockFile("contend");
    const first = acquireRuntimeLock({ lockFilePath: file, port: 17371, token: "t-abc", endpoint: "http://127.0.0.1:17371" });
    assert.equal(first.acquired, true);
    if (!first.acquired) return;
    const second = acquireRuntimeLock({ lockFilePath: file, port: 18000, token: "t-drift", endpoint: "http://127.0.0.1:18000" });
    assert.equal(second.acquired, false, "已有存活实例时第二个实例必须被拒绝（防止端口漂移）");
    if (second.acquired) return;
    assert.equal(second.existing.pid, process.pid);
    assert.equal(second.existing.endpoint, "http://127.0.0.1:17371");
    // 第一个实例释放后，第二个实例可重新获取
    first.release();
    const third = acquireRuntimeLock({ lockFilePath: file, port: 17371, token: "t-abc", endpoint: "http://127.0.0.1:17371" });
    assert.equal(third.acquired, true);
    if (third.acquired) third.release();
});

test("[connector] P0-A-3 a stale lock from a dead pid is replaced", () => {
    const { file } = tempLockFile("stale");
    // 模拟陈旧锁：pid 是大概率已不存在的进程
    const deadPid = 999_999_999;
    fs.writeFileSync(file, JSON.stringify({ pid: deadPid, port: 17371, token: "dead", endpoint: "http://127.0.0.1:17371", startedAt: 0, hostname: "x" }));
    const result = acquireRuntimeLock({ lockFilePath: file, port: 17371, token: "t-fresh", endpoint: "http://127.0.0.1:17371" });
    assert.equal(result.acquired, true, "陈旧锁应被覆盖");
    if (!result.acquired) return;
    const info = readRuntimeLock(file);
    assert.equal(info?.pid, process.pid);
    assert.equal(info?.token, "t-fresh");
    result.release();
});

test("[connector] P0-A-3 release only removes a lock owned by the same pid", () => {
    const { file } = tempLockFile("owner");
    const result = acquireRuntimeLock({ lockFilePath: file, port: 17371, token: "t-abc", endpoint: "http://127.0.0.1:17371" });
    assert.equal(result.acquired, true);
    if (!result.acquired) return;
    // 模拟他人锁（不同 pid）不应被误删
    releaseRuntimeLock(file, 12345);
    assert.ok(readRuntimeLock(file), "他人 pid 不应删除本实例锁");
    result.release();
    assert.equal(readRuntimeLock(file), null);
});

test("[connector] P0-A-3 readRuntimeLock returns null for missing or corrupt files", () => {
    const { file } = tempLockFile("missing");
    assert.equal(readRuntimeLock(file), null);
    fs.writeFileSync(file, "{ not json");
    assert.equal(readRuntimeLock(file), null);
    fs.writeFileSync(file, JSON.stringify({ pid: "not-a-number" }));
    assert.equal(readRuntimeLock(file), null);
});
