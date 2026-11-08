import assert from "node:assert/strict";
import { test } from "node:test";

import { linkProgressPercent, linkProgressText } from "./link-progress";

test("链接解析进度按阶段推进，下载阶段按比例插值", () => {
    assert.equal(linkProgressPercent(null), 0);
    assert.ok(linkProgressPercent({ stage: "detecting" }) < linkProgressPercent({ stage: "parsing" }));
    assert.equal(linkProgressPercent({ stage: "downloading", progress: 0 }), 35);
    assert.equal(linkProgressPercent({ stage: "downloading", progress: 0.5 }), 59);
    assert.equal(linkProgressPercent({ stage: "downloading", progress: 3 }), 82);
    assert.equal(linkProgressPercent({ stage: "done" }), 100);
});

test("链接解析进度优先显示服务端说明", () => {
    assert.equal(linkProgressText({ stage: "downloading", message: "正在下载视频 12.3 MB" }), "正在下载视频 12.3 MB");
    assert.equal(linkProgressText({ stage: "probing" }), "正在校验视频");
    assert.equal(linkProgressText(null), "");
});
