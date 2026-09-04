// [connector] P2 §9.4 可观测性：轻量 metrics registry
//
// 提供计数器/时延采样/状态 gauge，供 /metrics 端点（JSON + Prometheus 文本）输出：
//   - 工具调用计数与时延（canvas/channel/媒体/渠道生成）
//   - 错误率（工具失败、channel 业务错误）
//   - 在线画布、活跃连接器（bridge）等实时状态
// 单例 getMetricsRegistry() 供各模块注入，进程内内存态，无需外部依赖。

export interface MetricsRegistry {
    incCounter(name: string, by?: number): void;
    observeLatency(name: string, ms: number): void;
    setGauge(name: string, value: number): void;
    snapshot(): Record<string, number | string>;
    toPrometheus(): string;
}

class Registry implements MetricsRegistry {
    private counters = new Map<string, number>();
    private latencies = new Map<string, { count: number; sumMs: number }>();
    private gauges = new Map<string, number>();

    incCounter(name: string, by = 1): void {
        this.counters.set(name, (this.counters.get(name) ?? 0) + by);
    }

    observeLatency(name: string, ms: number): void {
        const entry = this.latencies.get(name) ?? { count: 0, sumMs: 0 };
        entry.count += 1;
        entry.sumMs += ms;
        this.latencies.set(name, entry);
    }

    setGauge(name: string, value: number): void {
        this.gauges.set(name, value);
    }

    snapshot(): Record<string, number | string> {
        const out: Record<string, number | string> = {};
        for (const [name, value] of this.counters) out[`counter.${name}`] = value;
        for (const [name, entry] of this.latencies) {
            out[`latency.${name}.count`] = entry.count;
            out[`latency.${name}.avg_ms`] = entry.count ? Math.round((entry.sumMs / entry.count) * 10) / 10 : 0;
        }
        for (const [name, value] of this.gauges) out[`gauge.${name}`] = value;
        return out;
    }

    toPrometheus(): string {
        const lines: string[] = ["# HELP yingce_metrics 影策画布连接器可观测指标", "# TYPE yingce_metrics gauge"];
        const snapshot = this.snapshot();
        for (const [name, value] of Object.entries(snapshot)) {
            lines.push(`yingce_${name.replace(/[^A-Za-z0-9_-]/g, "_")} ${value}`);
        }
        return lines.join("\n") + "\n";
    }
}

let singleton: MetricsRegistry | null = null;

export function getMetricsRegistry(): MetricsRegistry {
    if (!singleton) singleton = new Registry();
    return singleton;
}

export function resetMetricsRegistryForTest(): void {
    singleton = null;
}

export function createMetricsRegistry(): MetricsRegistry {
    return new Registry();
}
