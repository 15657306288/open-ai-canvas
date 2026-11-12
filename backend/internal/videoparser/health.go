package videoparser

import (
	"sync"
	"time"
)

const (
	circuitFailureThreshold = 5
	circuitOpenDuration     = 2 * time.Minute
)

// ProviderHealth 是某个平台上某个供应商的运行状态与调用成本统计（进程内，重启清零）。
type ProviderHealth struct {
	Provider            string    `json:"provider"`
	Kind                string    `json:"kind"`
	Available           bool      `json:"available"`
	Unavailable         string    `json:"unavailableReason,omitempty"`
	Attempts            int64     `json:"attempts"`
	Successes           int64     `json:"successes"`
	Failures            int64     `json:"failures"`
	ConsecutiveFailures int       `json:"consecutiveFailures"`
	CircuitOpen         bool      `json:"circuitOpen"`
	CircuitOpenUntil    time.Time `json:"circuitOpenUntil,omitempty"`
	LastErrorCode       string    `json:"lastErrorCode,omitempty"`
	LastError           string    `json:"lastError,omitempty"`
	LastSuccessAt       time.Time `json:"lastSuccessAt,omitempty"`
	LastFailureAt       time.Time `json:"lastFailureAt,omitempty"`
	AvgLatencyMs        int64     `json:"avgLatencyMs"`
	CostMicros          int64     `json:"costMicros"`
}

// PlatformUsage 是平台维度的解析、缓存与下载流量统计。
type PlatformUsage struct {
	Parses            int64 `json:"parses"`
	ParseSuccesses    int64 `json:"parseSuccesses"`
	CacheHits         int64 `json:"cacheHits"`
	Downloads         int64 `json:"downloads"`
	DownloadSuccesses int64 `json:"downloadSuccesses"`
	DownloadedBytes   int64 `json:"downloadedBytes"`
}

type providerStat struct {
	attempts, successes, failures int64
	consecutive                   int
	openUntil                     time.Time
	lastCode, lastError           string
	lastSuccess, lastFailure      time.Time
	latency                       time.Duration
	costMicros                    int64
}

type healthBook struct {
	mu        sync.Mutex
	providers map[string]*providerStat
	platforms map[string]*PlatformUsage
	now       func() time.Time
}

func newHealthBook() *healthBook {
	return &healthBook{providers: map[string]*providerStat{}, platforms: map[string]*PlatformUsage{}, now: time.Now}
}

func providerKey(provider, platform string) string { return platform + "/" + provider }

func (h *healthBook) provider(provider, platform string) *providerStat {
	key := providerKey(provider, platform)
	stat := h.providers[key]
	if stat == nil {
		stat = &providerStat{}
		h.providers[key] = stat
	}
	return stat
}

func (h *healthBook) usage(platform string) *PlatformUsage {
	usage := h.platforms[platform]
	if usage == nil {
		usage = &PlatformUsage{}
		h.platforms[platform] = usage
	}
	return usage
}

// allow 实现熔断：连续失败达到阈值后暂停调用，冷却结束后放行一次试探。
func (h *healthBook) allow(provider, platform string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	stat := h.provider(provider, platform)
	if stat.openUntil.IsZero() || !h.now().Before(stat.openUntil) {
		return true
	}
	return false
}

// recordProvider 记录一次调用。作品本身的问题（图文、已删除、超长）不计入供应商故障，不触发熔断。
func (h *healthBook) recordProvider(provider, platform string, err *Error, latency time.Duration, costMicros int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	stat := h.provider(provider, platform)
	stat.attempts++
	stat.latency += latency
	stat.costMicros += costMicros
	now := h.now()
	if err == nil {
		stat.successes++
		stat.consecutive = 0
		stat.openUntil = time.Time{}
		stat.lastSuccess = now
		return
	}
	stat.failures++
	stat.lastFailure = now
	stat.lastCode, stat.lastError = string(err.Code), clip(err.Message, 160)
	switch err.Code {
	case ErrNotVideo, ErrNotFound, ErrTooLong, ErrTooLarge, ErrDRM, ErrInvalidLink, ErrBlocked:
		return
	}
	stat.consecutive++
	if stat.consecutive >= circuitFailureThreshold {
		stat.openUntil = now.Add(circuitOpenDuration)
	}
}

func (h *healthBook) recordParse(platform string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	usage := h.usage(platform)
	usage.Parses++
	if ok {
		usage.ParseSuccesses++
	}
}

func (h *healthBook) recordCacheHit(platform string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.usage(platform).CacheHits++
}

func (h *healthBook) recordDownload(platform string, bytes int64, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	usage := h.usage(platform)
	usage.Downloads++
	if ok {
		usage.DownloadSuccesses++
	}
	if bytes > 0 {
		usage.DownloadedBytes += bytes
	}
}

func (h *healthBook) providerHealth(platform string, chain []Provider) []ProviderHealth {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ProviderHealth, 0, len(chain))
	now := h.now()
	for _, provider := range chain {
		stat := h.provider(provider.Name(), platform)
		item := ProviderHealth{
			Provider: provider.Name(), Kind: string(provider.Kind()), Available: true,
			Attempts: stat.attempts, Successes: stat.successes, Failures: stat.failures, ConsecutiveFailures: stat.consecutive,
			LastErrorCode: stat.lastCode, LastError: stat.lastError, LastSuccessAt: stat.lastSuccess, LastFailureAt: stat.lastFailure,
			CostMicros: stat.costMicros,
		}
		if err := provider.Available(); err != nil {
			item.Available, item.Unavailable = false, err.Error()
		}
		if !stat.openUntil.IsZero() && now.Before(stat.openUntil) {
			item.CircuitOpen, item.CircuitOpenUntil = true, stat.openUntil
		}
		if stat.attempts > 0 {
			item.AvgLatencyMs = stat.latency.Milliseconds() / stat.attempts
		}
		out = append(out, item)
	}
	return out
}

func (h *healthBook) platformUsage(platform string) PlatformUsage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return *h.usage(platform)
}
