import { afterEach, describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { Window } from "happy-dom";

const html = readFileSync(new URL("../index.html", import.meta.url), "utf8");
const script = html.match(/<script id="boot-watchdog">([\s\S]*?)<\/script>/)![1];
const windows: Window[] = [];
afterEach(async () => { for (const dom of windows.splice(0)) await dom.happyDOM.abort(); });

function boot(query = "", stored: string | Error = "") {
    const dom = new Window({ url: `https://canvas.example/canvas?mode=recent${query}#board` });
    windows.push(dom);
    const { document } = dom;
    document.body.innerHTML = '<div id="root"></div>';
    let destination = "";
    let next = 0;
    const timers = new Map<number, { callback: () => void; delay: number }>();
    runInNewContext(script, {
        window: dom, document, URL, MutationObserver: dom.MutationObserver,
        sessionStorage: { getItem: () => { if (stored instanceof Error) throw stored; return stored; } },
        setTimeout: (callback: () => void, delay: number) => { timers.set(++next, { callback, delay }); return next; },
        clearTimeout: (id: number) => timers.delete(id),
        location: { href: dom.location.href, replace: (value: string) => { destination = value; } },
    });
    return {
        dom, timers, notice: () => document.getElementById("boot-recovery"), destination: () => new URL(destination),
        fire(delay: number) { for (const [id, task] of timers) if (task.delay === delay) { timers.delete(id); task.callback(); } },
        fail: () => dom.dispatchEvent(new dom.Event("vite:preloadError")),
        async mount() { document.getElementById("root")!.append(document.createElement("main")); await dom.happyDOM.whenAsyncComplete(); },
    };
}

describe("入口加载与恢复", () => {
    test("慢连接单独提示，手动重试保留查询和锚点，延迟挂载后清理", async () => {
        const page = boot();
        page.fire(20000);
        expect(page.notice()).toBeNull();
        page.fire(45000);
        expect(page.notice()?.getAttribute("role")).toBe("status");
        page.notice()!.querySelector("button")!.click();
        expect(page.destination().searchParams.get("mode")).toBe("recent");
        expect(page.destination().searchParams.has("__chunk_recovery")).toBe(true);
        expect(page.destination().hash).toBe("#board");
        await page.mount();
        page.fail();
        expect(page.timers.size).toBe(0);
        expect(page.notice()).toBeNull();
    });
    test("脚本失败与 Vite 信号去重，可从慢连接升级为加载失败", () => {
        const page = boot();
        page.fire(45000);
        const script = page.dom.document.createElement("script");
        page.dom.document.head.append(script);
        script.dispatchEvent(new page.dom.Event("error"));
        page.fail();
        expect(page.timers.size).toBe(1);
        page.fire(1800);
        expect(page.notice()?.getAttribute("role")).toBe("alert");
        expect(page.dom.document.querySelectorAll("#boot-recovery")).toHaveLength(1);
    });
    test("已自动重试的标签页立即提示，包括禁用存储时的 URL 标记", () => {
        for (const page of [boot("", "1"), boot("&__chunk_recovery=1", new Error())]) {
            page.fail();
            expect(page.notice()?.textContent).toContain("重试后仍无法加载");
            expect(page.timers.size).toBe(0);
        }
    });
    test("非启动图片错误不报错，首次错误宽限期内成功挂载不闪提示", async () => {
        const page = boot();
        const image = page.dom.document.createElement("img");
        page.dom.document.body.append(image);
        image.dispatchEvent(new page.dom.Event("error"));
        expect([...page.timers.values()].map((item) => item.delay)).toEqual([45000]);
        page.fail();
        expect(page.notice()).toBeNull();
        await page.mount();
        expect(page.timers.size).toBe(0);
        expect(page.notice()).toBeNull();
    });
});
