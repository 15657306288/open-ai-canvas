// [connector] P0-A-2 断线宽限追踪器
//
// 背景：SSE 连接瞬断（网络抖动、切换标签、前端重连）不应立刻判定画布"离线"并清空
// canvasState —— 那会导致所有只读工具在断网 1 秒后报"当前没有已连接画布"。
// 本类为每个断开连接的 clientId 开一个 grace 窗口（默认 8s），窗口内允许同 clientId 重连
// 并恢复；超时仍未重连才触发 onExpired 回调，由调用方清理画布状态与 pending 请求。

export type GraceTrackerTimers = {
    setTimeout(callback: () => void, delayMs: number): unknown;
    clearTimeout(handle: unknown): void;
};

export type GraceTrackerOptions = {
    /** 断线宽限时长（毫秒），默认 8000 */
    graceMs?: number;
    now?: () => number;
    timers?: GraceTrackerTimers;
    /** 宽限超时（且未被 cancel）时回调，参数为 clientId */
    onExpired: (clientId: string) => void;
};

export class GraceTracker {
    private readonly graceMs: number;
    private readonly now: () => number;
    private readonly timers: GraceTrackerTimers;
    private readonly onExpired: (clientId: string) => void;
    private readonly expirations = new Map<string, number>();
    private readonly timerHandles = new Map<string, unknown>();

    constructor(options: GraceTrackerOptions) {
        this.graceMs = options.graceMs ?? 8_000;
        this.now = options.now ?? Date.now;
        this.timers = options.timers ?? {
            setTimeout(callback, delayMs) {
                const timer = setTimeout(callback, delayMs);
                timer.unref();
                return timer;
            },
            clearTimeout(handle) {
                clearTimeout(handle as NodeJS.Timeout);
            },
        };
        this.onExpired = options.onExpired;
    }

    /** 进入宽限期；若该 clientId 已在宽限期则重置计时（连续断线窗口不叠加） */
    enter(clientId: string) {
        if (this.timerHandles.has(clientId)) this.cancel(clientId);
        this.expirations.set(clientId, this.now() + this.graceMs);
        const handle = this.timers.setTimeout(() => this.expire(clientId), this.graceMs);
        this.timerHandles.set(clientId, handle);
    }

    /** 是否处于宽限期（供"画布重连中"判定） */
    isGrace(clientId: string) {
        return this.expirations.has(clientId);
    }

    /** 重连成功后清除宽限，不触发过期回调 */
    cancel(clientId: string) {
        const handle = this.timerHandles.get(clientId);
        if (handle !== undefined) this.timers.clearTimeout(handle);
        this.timerHandles.delete(clientId);
        this.expirations.delete(clientId);
    }

    /** 当前处于宽限期的连接数 */
    activeCount() {
        return this.expirations.size;
    }

    private expire(clientId: string) {
        this.expirations.delete(clientId);
        this.timerHandles.delete(clientId);
        this.onExpired(clientId);
    }
}
