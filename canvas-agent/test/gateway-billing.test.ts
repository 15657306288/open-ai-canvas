import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { KeyStore } from "../src/bridge/gateway-keys.js";
import { aggregate, loadPricing, priceFor, type Pricing, type UsageEntry } from "../src/bridge/gateway-billing.js";

const pricing: Pricing = {
    currency: "CNY",
    default: { perCall: 0.01 },
    byTool: {
        "canvas_get_context": { perCall: 0.05 },
        "video_generation": { perCall: 1.5 },
        "canvas_*": { perCall: 0.02 },
    },
};

test("[connector] P2 定价：精确匹配优先于通配，未命中走默认", () => {
    // 精确命中（比通配更细）
    assert.equal(priceFor(pricing, "canvas_get_context"), 0.05);
    // 精确命中 video
    assert.equal(priceFor(pricing, "video_generation"), 1.5);
    // 通配 canvas_*
    assert.equal(priceFor(pricing, "canvas_create"), 0.02);
    assert.equal(priceFor(pricing, "canvas_set_context"), 0.02);
    // 未命中 → 默认
    assert.equal(priceFor(pricing, "channel_list"), 0.01);
    // 通配须是前缀
    assert.equal(priceFor(pricing, "xx_canvas"), 0.01);
});

test("[connector] P2 用量聚合：按 Key 分组、按工具计费、失败不计费", () => {
    const entries: UsageEntry[] = [
        { ts: "2026-09-04T00:00:00Z", date: "2026-09-04", keyId: "k_1", keyName: "甲", tool: "canvas_get_context", ok: true, ms: 10 },
        { ts: "2026-09-04T00:00:01Z", date: "2026-09-04", keyId: "k_1", keyName: "甲", tool: "canvas_create", ok: true, ms: 20 },
        { ts: "2026-09-04T00:00:02Z", date: "2026-09-04", keyId: "k_1", keyName: "甲", tool: "channel_list", ok: true, ms: 30 },
        { ts: "2026-09-04T00:00:03Z", date: "2026-09-04", keyId: "k_2", keyName: "乙", tool: "video_generation", ok: true, ms: 40 },
        // 失败调用：不计费（cost 默认按 ok 才计）
        { ts: "2026-09-04T00:00:04Z", date: "2026-09-04", keyId: "k_1", keyName: "甲", tool: "canvas_get_context", ok: false, ms: 50 },
        // 显式 cost（网关已扣）优先
        { ts: "2026-09-04T00:00:05Z", date: "2026-09-04", keyId: "k_2", keyName: "乙", tool: "canvas_get_context", ok: true, ms: 60, cost: 9.9 },
    ];
    const aggs = aggregate(entries, pricing);
    assert.equal(aggs.length, 2);
    const a = aggs.find((x) => x.keyId === "k_1")!;
    assert.equal(a.calls, 4);
    assert.equal(a.okCalls, 3);
    assert.equal(a.failCalls, 1);
    // 0.05 + 0.02 + 0.01 = 0.08
    assert.ok(Math.abs(a.totalCost - 0.08) < 1e-9, `甲 totalCost=${a.totalCost}`);
    assert.equal(a.byTool["canvas_get_context"].calls, 2);
    const b = aggs.find((x) => x.keyId === "k_2")!;
    // 1.5 + 9.9 = 11.4
    assert.ok(Math.abs(b.totalCost - 11.4) < 1e-9, `乙 totalCost=${b.totalCost}`);
});

test("[connector] P2 定价文件：缺省自动初始化默认定价", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "pricing-"));
    const file = path.join(dir, "pricing.json");
    const p = loadPricing(file);
    assert.equal(p.default.perCall, 0.01);
    assert.ok(fs.existsSync(file), "缺失的定价文件应自动创建");
});

test("[connector] P2 余额：充值/扣费/不足拦截", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "gwkeys-bal-"));
    const store = new KeyStore(path.join(dir, "keys.json"));
    // 无余额 → 不启用余额控制
    const { key: kNoBal } = store.createKey({ name: "无余额" });
    const idNo = store.list()[0].id;
    assert.equal(store.deduct(idNo, 5).ok, true, "未启用余额控制应放行");
    assert.equal(store.get(idNo)!.balance, undefined);

    // 有余额：topup → 扣费 → 不足拦截
    const { key: kBal } = store.createKey({ name: "有余额", balance: 1 });
    const id = store.list()[1].id;
    assert.equal(store.get(id)!.balance, 1);
    assert.equal(store.topup(id, 0.5).balance, 1.5);
    // 扣 0.3 → 1.2
    assert.equal(store.deduct(id, 0.3).ok, true);
    assert.ok(Math.abs(store.get(id)!.balance! - 1.2) < 1e-9);
    // 扣超过余额 → 拒绝且余额不变
    assert.equal(store.deduct(id, 2).ok, false);
    assert.ok(Math.abs(store.get(id)!.balance! - 1.2) < 1e-9, "不足时余额不得变动");
    // 校验仍可用
    assert.equal(store.verify(kBal).ok, true);
    assert.equal(store.verify(kNoBal).ok, true);
});
