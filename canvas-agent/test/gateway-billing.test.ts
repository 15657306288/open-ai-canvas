import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { KeyStore } from "../src/bridge/gateway-keys.js";
import { aggregate, loadPricing, priceFor, CREDITS_SCALE, type Pricing, type UsageEntry } from "../src/bridge/gateway-billing.js";

const pricing: Pricing = {
    unit: "microcredits",
    default: { perCallMicrocredits: 10_000 },
    byTool: {
        "canvas_get_context": { perCallMicrocredits: 50_000 },
        "video_generation": { perCallMicrocredits: 1_500_000 },
        "canvas_*": { perCallMicrocredits: 20_000 },
    },
};

test("[billing] 整数微积分定价：精确匹配优先于通配，未命中走默认", () => {
    assert.equal(priceFor(pricing, "canvas_get_context"), 50_000);
    assert.equal(priceFor(pricing, "video_generation"), 1_500_000);
    assert.equal(priceFor(pricing, "canvas_create"), 20_000);
    assert.equal(priceFor(pricing, "canvas_set_context"), 20_000);
    assert.equal(priceFor(pricing, "channel_list"), 10_000);
    assert.equal(priceFor(pricing, "xx_canvas"), 10_000); // 通配须是前缀
    for (const v of [priceFor(pricing, "canvas_create"), priceFor(pricing, "channel_list")]) {
        assert.ok(Number.isInteger(v), "单价必须是整数 microcredits");
    }
    assert.equal(CREDITS_SCALE, 1_000_000);
});

test("[billing] 用量聚合：按 Key 分组、整数累加、失败不计费、显式金额优先", () => {
    const entries: UsageEntry[] = [
        { ts: "2026-09-04T00:00:00Z", date: "2026-09-04", keyId: "k_1", keyName: "甲", tool: "canvas_get_context", ok: true, ms: 10 },
        { ts: "2026-09-04T00:00:01Z", date: "2026-09-04", keyId: "k_1", keyName: "甲", tool: "canvas_create", ok: true, ms: 20 },
        { ts: "2026-09-04T00:00:02Z", date: "2026-09-04", keyId: "k_1", keyName: "甲", tool: "channel_list", ok: true, ms: 30 },
        { ts: "2026-09-04T00:00:03Z", date: "2026-09-04", keyId: "k_2", keyName: "乙", tool: "video_generation", ok: true, ms: 40 },
        // 失败调用不计费
        { ts: "2026-09-04T00:00:04Z", date: "2026-09-04", keyId: "k_1", keyName: "甲", tool: "canvas_get_context", ok: false, ms: 50 },
        // 显式 microcredits（网关已记账）优先于定价表
        { ts: "2026-09-04T00:00:05Z", date: "2026-09-04", keyId: "k_2", keyName: "乙", tool: "canvas_get_context", ok: true, ms: 60, microcredits: 99_000 },
    ];
    const aggs = aggregate(entries, pricing);
    assert.equal(aggs.length, 2);
    const a = aggs.find((x) => x.keyId === "k_1")!;
    assert.equal(a.calls, 4);
    assert.equal(a.okCalls, 3);
    assert.equal(a.failCalls, 1);
    // 50000 + 20000 + 10000 = 80000（整数，无浮点误差）
    assert.equal(a.totalMicrocredits, 80_000);
    assert.equal(a.byTool["canvas_get_context"].calls, 2);
    const b = aggs.find((x) => x.keyId === "k_2")!;
    // 1_500_000 + 99_000 = 1_599_000
    assert.equal(b.totalMicrocredits, 1_599_000);
});

test("[billing] 定价文件：缺省自动初始化整数默认定价", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "pricing-"));
    const file = path.join(dir, "pricing.json");
    const p = loadPricing(file);
    assert.equal(p.unit, "microcredits");
    assert.ok(Number.isInteger(p.default.perCallMicrocredits) && p.default.perCallMicrocredits > 0);
    assert.ok(fs.existsSync(file), "缺失的定价文件应自动创建");
});

test("[billing] 本地钱包整数 microcredits：充值/扣费/不足拦截", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "gwkeys-bal-"));
    const store = new KeyStore(path.join(dir, "keys.json"));
    const { key: kNoBal } = store.createKey({ name: "无余额" });
    const idNo = store.list()[0].id;
    assert.equal(store.deduct(idNo, 5_000).ok, true, "未启用余额控制应放行");
    assert.equal(store.get(idNo)!.balance, undefined);

    const { key: kBal } = store.createKey({ name: "有余额", balance: 1_000_000 });
    const id = store.list()[1].id;
    assert.equal(store.get(id)!.balance, 1_000_000);
    assert.equal(store.topup(id, 500_000).balance, 1_500_000);
    assert.equal(store.deduct(id, 300_000).ok, true);
    assert.equal(store.get(id)!.balance, 1_200_000); // 整数无误差
    assert.equal(store.deduct(id, 9_999_999).ok, false);
    assert.equal(store.get(id)!.balance, 1_200_000, "不足时余额不得变动");
    assert.equal(store.verify(kBal).ok, true);
    assert.equal(store.verify(kNoBal).ok, true);
});
