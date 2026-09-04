import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { KeyStore, generateKey, hashKey, today } from "../src/bridge/gateway-keys.js";

function makeStore(): { store: KeyStore; file: string } {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "gwkeys-"));
    const file = path.join(dir, "gateway-keys.json");
    return { store: new KeyStore(file), file };
}

test("[connector] P1 网关 Key：颁发格式、仅存哈希、校验通过/失败", () => {
    const { store, file } = makeStore();
    const { key, record } = store.createKey({ name: "客户A" });

    // ① ak_ 前缀 + 256 位熵
    assert.ok(key.startsWith("ak_"), "key 应以 ak_ 开头");
    assert.equal(key.length, 3 + 32, "ak_ + 32 位 hex");
    assert.ok(record.id.startsWith("k_"));

    // ② 磁盘只存哈希，绝不落明文
    const raw = fs.readFileSync(file, "utf8");
    assert.ok(!raw.includes(key), "磁盘不得包含明文 key");
    assert.ok(raw.includes(record.keyHash), "磁盘应包含哈希");

    // ③ 正确 key 通过；错误 key 拒绝
    const ok = store.verify(key);
    assert.equal(ok.ok, true);
    assert.equal(ok.key?.name, "客户A");
    assert.equal(store.verify("ak_" + "0".repeat(32)).ok, false);
});

test("[connector] P1 网关 Key：停用/启用、日配额 429、用量累计", () => {
    const { store } = makeStore();
    const { key } = store.createKey({ name: "限量客户", dailyCalls: 2 });

    // 配额内两次通过（verify 通过 + 调用后计数，模拟真实 MCP 调用流）
    const id = store.list()[0].id;
    for (let i = 0; i < 2; i += 1) {
        assert.equal(store.verify(key).ok, true);
        store.recordUsage(id, "canvas_get_context");
    }
    // 第三次 → 配额耗尽
    const third = store.verify(key);
    assert.equal(third.ok, false);
    assert.equal(third.reason, "quota");

    // 用量记录（配额内 2 次 canvas_get_context + 后续 channel_list + canvas_get_context 各 1）
    store.recordUsage(id, "channel_list");
    const usage = store.recordUsage(id, "canvas_get_context");
    assert.equal(usage.calls, 4);
    assert.equal(usage.totalCalls, 4);
    assert.equal(usage.byTool["canvas_get_context"], 3);
    assert.equal(usage.byTool["channel_list"], 1);

    // 停用 → disabled
    assert.equal(store.revoke(id), true);
    const afterRevoke = store.verify(key);
    assert.equal(afterRevoke.ok, false);
    assert.equal(afterRevoke.reason, "disabled");

    // 启用 → 恢复（日配额已用满，仍 quota）
    assert.equal(store.enable(id), true);
    assert.equal(store.verify(key).reason, "quota");
    // 重置当日 → 重新可用
    assert.equal(store.resetDaily(id), true);
    assert.equal(store.verify(key).ok, true);
});

test("[connector] P1 网关 Key：createKey 默认不限量、recordUsage 跨天按新的一天计", () => {
    const { store } = makeStore();
    const { key } = store.createKey({ name: "无限客户" });
    // 默认 quota 0 = 不限
    assert.equal(store.verify(key).ok, true);
    const id = store.list()[0].id;
    // 模拟跨天：把 usage.date 改为昨天（仅内部测试可直改）
    const k = store.get(id)!;
    k.usage = { date: "2000-01-01", calls: 999, totalCalls: 999, byTool: { x: 999 } };
    // verify 遇到非今天会自动重置日计数
    const v = store.verify(key);
    assert.equal(v.ok, true, "跨天后日计数应重置");
    assert.equal(v.key!.usage.calls, 0);
    assert.equal(v.key!.usage.totalCalls, 999, "累计不清零");
});

test("[connector] P1 网关 Key：哈希与日期工具", () => {
    assert.equal(hashKey("abc"), hashKey("abc"));
    assert.notEqual(hashKey("abc"), hashKey("abd"));
    assert.equal(today(), new Date().toISOString().slice(0, 10));
    assert.ok(generateKey().startsWith("ak_"));
});

test("[connector] P1 网关 Key：文件热重载——外部 CLI 颁发后网关无需重启即可校验", () => {
    const { store, file } = makeStore();
    // 模拟 CLI 进程：另一个 KeyStore 实例写入同一文件（如同 gateway-keys.js 命令行）
    const cliStore = new KeyStore(file);
    const { key } = cliStore.createKey({ name: "热重载客户" });

    // 网关侧 store 不重启，仅靠热重载即可识别新 Key
    assert.equal(store.verify(key).ok, true, "网关侧应自动热加载 CLI 新颁发的 key");
    assert.equal(store.list().length, 1);

    // 模拟 CLI 吊销后，网关侧也应自动感知
    cliStore.revoke(cliStore.list()[0].id);
    assert.equal(store.verify(key).ok, false, "网关侧应自动热加载 CLI 的吊销");
});

test("[connector] P1 网关 Key：损坏文件自动备份并空库启动", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "gwkeys-"));
    const file = path.join(dir, "gateway-keys.json");
    fs.writeFileSync(file, "{broken json");
    const store = new KeyStore(file);
    assert.equal(store.list().length, 0);
    assert.ok(fs.existsSync(`${file}.corrupt`), "损坏文件应备份为 .corrupt");
});
