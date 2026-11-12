package videoparser

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"yingce/backend/internal/outbound"
	"yingce/backend/internal/platform"
)

// Options 是解析服务的部署配置；全部来自服务端环境变量，浏览器拿不到任何代理地址或密钥。
type Options struct {
	// HTTPClient 返回带 SSRF 防护的出站客户端；proxy 非空时走部署者配置的代理（海外平台）。
	HTTPClient func(timeout time.Duration, proxy string) *http.Client
	// ResolveHost 解析并校验主机只指向公网地址，供 yt-dlp 出口代理复用同一套出站策略。
	ResolveHost func(ctx context.Context, host string) ([]string, error)
	YTDLPPath   string
	FFmpegPath  string
	// ChromiumPath 是抖音官方嵌入播放器解析用的无头浏览器；为空时自动查找，NoBrowser 为 true 时不使用浏览器。
	ChromiumPath       string
	NoBrowser          bool
	BrowserConcurrency int
	// Platforms 是开放的平台白名单；为空表示开放全部已注册平台。
	Platforms []string
	// OverseasProxy 是访问 TikTok / YouTube / Instagram / X 的代理；为空时这些平台标记为需要代理。
	OverseasProxy       string
	Concurrency         int
	PlatformConcurrency int
	QueueTimeout        time.Duration
	PageTimeout         time.Duration
	ProviderTimeout     time.Duration
	DownloadTimeout     time.Duration
	CacheTTL            time.Duration
	MaxBytes            int64
	MaxDurationMs       int64
	// Priority 覆盖平台的供应商顺序，例如 {"bilibili": {"yt-dlp"}}。
	Priority map[string][]string
	Disabled map[string]bool
	// ProviderCostMicros 是每次调用的单价（微元），用于成本统计；自建供应商默认 0。
	ProviderCostMicros map[string]int64
	Runner             CommandRunner
	Logger             *slog.Logger
	// egressPortAllowed 仅供测试放行本地测试服务器端口；生产固定为 80 / 443。
	egressPortAllowed func(string) bool
}

// OptionsFromEnv 读取部署配置：
//
//	CANVAS_VIDEO_PARSER_YTDLP                yt-dlp 可执行文件（默认在 PATH 中查找）
//	CANVAS_VIDEO_PARSER_OVERSEAS_PROXY       海外平台代理，如 socks5://user:pass@host:1080
//	CANVAS_VIDEO_PARSER_CONCURRENCY          全局并发解析数（默认 3）
//	CANVAS_VIDEO_PARSER_PLATFORM_CONCURRENCY 单平台并发解析数（默认 2）
//	CANVAS_VIDEO_PARSER_MAX_MB               单个视频下载上限（默认 100）
//	CANVAS_VIDEO_PARSER_PRIORITY             供应商顺序，如 "bilibili=yt-dlp;kuaishou=kuaishou-share-page"
//	CANVAS_VIDEO_PARSER_DISABLED             停用的平台，如 "douyin,xigua"
//	CANVAS_VIDEO_PARSER_PROVIDER_COSTS       供应商单价（微元/次），如 "some-api=2000"
//	CANVAS_VIDEO_PARSER_PLATFORMS            开放的平台，默认 "douyin,kuaishou,xiaohongshu,channels"；"all" 表示全部
//	CANVAS_VIDEO_PARSER_CHROMIUM             无头浏览器路径（默认在 PATH 中查找 chromium）
//	CANVAS_VIDEO_PARSER_BROWSER_CONCURRENCY  单个进程同时运行的无头浏览器数（默认 1）
func OptionsFromEnv() Options {
	opts := Options{
		YTDLPPath:          strings.TrimSpace(os.Getenv("CANVAS_VIDEO_PARSER_YTDLP")),
		ChromiumPath:       strings.TrimSpace(os.Getenv("CANVAS_VIDEO_PARSER_CHROMIUM")),
		OverseasProxy:      strings.TrimSpace(os.Getenv("CANVAS_VIDEO_PARSER_OVERSEAS_PROXY")),
		Priority:           map[string][]string{},
		Disabled:           map[string]bool{},
		ProviderCostMicros: map[string]int64{},
		Platforms:          DefaultPlatforms(),
	}
	if raw := strings.TrimSpace(os.Getenv("CANVAS_VIDEO_PARSER_PLATFORMS")); raw == "all" {
		opts.Platforms = nil
	} else if raw != "" {
		opts.Platforms = nil
		for _, key := range strings.Split(raw, ",") {
			if key = strings.TrimSpace(key); key != "" {
				opts.Platforms = append(opts.Platforms, key)
			}
		}
	}
	opts.BrowserConcurrency = envInt("CANVAS_VIDEO_PARSER_BROWSER_CONCURRENCY", 1)
	opts.Concurrency = envInt("CANVAS_VIDEO_PARSER_CONCURRENCY", 3)
	opts.PlatformConcurrency = envInt("CANVAS_VIDEO_PARSER_PLATFORM_CONCURRENCY", 2)
	if mb := envInt("CANVAS_VIDEO_PARSER_MAX_MB", 0); mb > 0 {
		opts.MaxBytes = int64(mb) << 20
	}
	for _, item := range strings.Split(os.Getenv("CANVAS_VIDEO_PARSER_PRIORITY"), ";") {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		var providers []string
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				providers = append(providers, name)
			}
		}
		if key = strings.TrimSpace(key); key != "" && len(providers) > 0 {
			opts.Priority[key] = providers
		}
	}
	for _, key := range strings.Split(os.Getenv("CANVAS_VIDEO_PARSER_DISABLED"), ",") {
		if key = strings.TrimSpace(key); key != "" {
			opts.Disabled[key] = true
		}
	}
	for _, item := range strings.Split(os.Getenv("CANVAS_VIDEO_PARSER_PROVIDER_COSTS"), ",") {
		name, value, ok := strings.Cut(item, "=")
		if cost, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); ok && err == nil && cost >= 0 {
			opts.ProviderCostMicros[strings.TrimSpace(name)] = cost
		}
	}
	return opts
}

// DefaultPlatforms 是产品当前开放的四个平台；其他已注册平台需要部署者显式开启。
func DefaultPlatforms() []string {
	return []string{PlatformDouyin, PlatformKuaishou, PlatformXiaohongshu, PlatformChannels}
}

func envInt(name string, fallback int) int {
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && value > 0 {
		return value
	}
	return fallback
}

func (o *Options) applyDefaults() {
	if o.HTTPClient == nil {
		o.HTTPClient = func(timeout time.Duration, proxy string) *http.Client {
			if proxy != "" {
				return outbound.OutboundHTTPClientWithProxy(timeout, proxy)
			}
			return outbound.OutboundHTTPClient(timeout)
		}
	}
	if o.ResolveHost == nil {
		o.ResolveHost = func(ctx context.Context, host string) ([]string, error) {
			ips, err := outbound.ResolveOutboundHost(ctx, host)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(ips))
			for _, ip := range ips {
				out = append(out, ip.String())
			}
			return out, nil
		}
	}
	if o.YTDLPPath == "" {
		if path, err := exec.LookPath("yt-dlp"); err == nil {
			o.YTDLPPath = path
		}
	}
	if o.FFmpegPath == "" {
		if path, err := exec.LookPath("ffmpeg"); err == nil {
			o.FFmpegPath = path
		}
	}
	if o.NoBrowser {
		o.ChromiumPath = ""
	} else if o.ChromiumPath == "" {
		o.ChromiumPath = findChromium()
	}
	if o.BrowserConcurrency <= 0 {
		o.BrowserConcurrency = 1
	}
	defaults := []struct {
		target   *time.Duration
		fallback time.Duration
	}{{&o.QueueTimeout, 20 * time.Second}, {&o.PageTimeout, 20 * time.Second}, {&o.ProviderTimeout, 60 * time.Second}, {&o.DownloadTimeout, 3 * time.Minute}, {&o.CacheTTL, 10 * time.Minute}}
	for _, item := range defaults {
		if *item.target <= 0 {
			*item.target = item.fallback
		}
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 3
	}
	if o.PlatformConcurrency <= 0 {
		o.PlatformConcurrency = 2
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 100 << 20
	}
	if o.MaxDurationMs <= 0 {
		o.MaxDurationMs = 10 * 60 * 1000
	}
	if o.Runner == nil {
		o.Runner = execRunner{}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Priority == nil {
		o.Priority = map[string][]string{}
	}
	if o.Disabled == nil {
		o.Disabled = map[string]bool{}
	}
	if o.ProviderCostMicros == nil {
		o.ProviderCostMicros = map[string]int64{}
	}
}

// Service 是统一解析调度中心。
type Service struct {
	opts      Options
	registry  *Registry
	providers map[string]Provider
	global    chan struct{}
	perMu     sync.Mutex
	perPlat   map[string]chan struct{}
	cache     *platform.BoundedReadCache[string, *Result]
	stats     *healthBook
	egressMu  sync.Mutex
	egress    *egressProxy
}

func New(opts Options, registry *Registry, providers ...Provider) *Service {
	opts.applyDefaults()
	if registry == nil {
		registry = DefaultRegistry()
	}
	s := &Service{
		opts:      opts,
		registry:  registry,
		providers: map[string]Provider{},
		global:    make(chan struct{}, opts.Concurrency),
		perPlat:   map[string]chan struct{}{},
		cache:     platform.NewBoundedReadCache[string, *Result](512, 4<<20, 64, opts.CacheTTL),
		stats:     newHealthBook(),
	}
	if len(providers) == 0 {
		providers = s.defaultProviders()
	}
	for _, provider := range providers {
		s.providers[provider.Name()] = provider
	}
	return s
}

// NewDefault 使用十个平台与内置供应商（各平台的公开分享页解析 + 自建 yt-dlp）。
func NewDefault(opts Options) *Service { return New(opts, nil) }

func (s *Service) defaultProviders() []Provider {
	return []Provider{
		&douyinEmbedProvider{svc: s, browser: newChromiumCapturer(s)},
		&douyinSharePageProvider{svc: s},
		&kuaishouSharePageProvider{svc: s},
		&xiaohongshuPageProvider{svc: s},
		&channelsPreviewProvider{svc: s},
		&ytdlpProvider{svc: s},
	}
}

func (s *Service) Registry() *Registry { return s.registry }

func (s *Service) httpClient(timeout time.Duration, proxy string) *http.Client {
	return s.opts.HTTPClient(timeout, proxy)
}

// proxyFor 只给海外平台使用部署者配置的代理；国内平台始终直连并经过 SSRF 校验。
func (s *Service) proxyFor(platform PlatformAdapter) string {
	if platform != nil && platform.Info().Region == RegionOverseas {
		return s.opts.OverseasProxy
	}
	return ""
}

// providerChain 返回平台实际要尝试的供应商：配置优先级 > 适配器默认顺序，跳过未注册的名字。
func (s *Service) providerChain(platform PlatformAdapter) []Provider {
	names := platform.Providers()
	if override := s.opts.Priority[platform.Info().Key]; len(override) > 0 {
		names = override
	}
	chain := make([]Provider, 0, len(names))
	for _, name := range names {
		if provider, ok := s.providers[name]; ok {
			chain = append(chain, provider)
		}
	}
	return chain
}

// EffectiveCapabilities 在真实测试得出的静态能力上叠加当前部署条件（停用、缺少 yt-dlp、未配置代理）。
func (s *Service) EffectiveCapabilities(platform PlatformAdapter) Capabilities {
	caps := platform.Capabilities()
	key := platform.Info().Key
	if !s.platformEnabled(key) {
		caps.Support, caps.ConfigMissing = SupportUnavailable, "当前未开放（CANVAS_VIDEO_PARSER_PLATFORMS）"
	} else if s.opts.Disabled[key] {
		caps.Support, caps.ConfigMissing = SupportUnavailable, "已被管理员停用"
	} else if caps.NeedsProxy && s.opts.OverseasProxy == "" {
		caps.Support, caps.ConfigMissing = SupportNeedsAuth, "服务器未配置海外代理（CANVAS_VIDEO_PARSER_OVERSEAS_PROXY）"
	} else if caps.Support.Usable() {
		available := false
		var missing []string
		for _, provider := range s.providerChain(platform) {
			if err := provider.Available(); err != nil {
				missing = append(missing, provider.Name()+"："+err.Error())
				continue
			}
			available = true
		}
		if !available {
			caps.Support, caps.ConfigMissing = SupportUnavailable, strings.Join(missing, "；")
		}
	}
	caps.SupportLabel = caps.Support.Label()
	return caps
}

// Stage 是解析过程中的阶段事件，用于前端展示进度。
type Stage struct {
	Name          string  `json:"stage"`
	Platform      string  `json:"platform,omitempty"`
	PlatformLabel string  `json:"platformLabel,omitempty"`
	Provider      string  `json:"provider,omitempty"`
	Progress      float64 `json:"progress,omitempty"`
	Message       string  `json:"message,omitempty"`
}

// Parse 是 parse(url) 的统一入口：限流 → 展开短链 → 缓存 → 按优先级调用供应商（失败重试、熔断降级）。
func (s *Service) Parse(ctx context.Context, link *Link, onStage func(Stage)) (*Result, error) {
	if link == nil || link.URL == nil || link.Platform == nil {
		return nil, newError(ErrInvalidLink, "链接无效", nil)
	}
	emit := func(stage Stage) {
		if onStage != nil {
			stage.Platform, stage.PlatformLabel = link.Platform.Info().Key, link.Platform.Info().Label
			onStage(stage)
		}
	}
	caps := s.EffectiveCapabilities(link.Platform)
	info := link.Platform.Info()
	if !caps.Support.Usable() {
		message := info.Label + "链接解析" + caps.SupportLabel + "：" + caps.Notes
		if caps.ConfigMissing != "" {
			message = info.Label + "链接解析" + caps.SupportLabel + "：" + caps.ConfigMissing
		}
		code := ErrUnavailable
		if caps.Support == SupportNeedsAuth {
			code = ErrNeedsLogin
		}
		return nil, &Error{Code: code, Message: message + "。请下载后用本地上传", Platform: info.Key}
	}
	release, err := s.acquire(ctx, info.Key)
	if err != nil {
		return nil, err
	}
	defer release()

	emit(Stage{Name: "resolving", Message: "正在展开链接"})
	expanded, err := s.expand(ctx, link)
	if err != nil {
		s.stats.recordParse(info.Key, false)
		return nil, withPlatform(err, info.Key)
	}
	link = expanded
	info = link.Platform.Info()
	id, canonical := link.Platform.Normalize(link.URL)
	key := info.Key + ":" + id
	if id == "" {
		key = info.Key + ":" + canonical
	}
	loaded := false
	result, err := s.cache.Get(ctx, key, func(ctx context.Context) (*Result, int, error) {
		loaded = true
		emit(Stage{Name: "parsing", Message: "正在解析作品信息"})
		result, err := s.resolve(ctx, link, id, canonical, emit)
		if err != nil {
			return nil, 0, err
		}
		return result, 2048 + len(result.Meta.Title)*4 + len(result.Media)*1024, nil
	})
	if errors.Is(err, platform.ErrReadCacheBusy) {
		return nil, newError(ErrBusy, "解析请求较多，请稍后重试", err)
	}
	if err != nil {
		s.stats.recordParse(info.Key, false)
		return nil, withPlatform(err, info.Key)
	}
	s.stats.recordParse(info.Key, true)
	result = result.clone()
	if !loaded {
		result.Cached = true
		s.stats.recordCacheHit(info.Key)
	}
	return result, nil
}

func withPlatform(err error, platform string) error {
	parsed := AsError(err)
	if parsed.Platform == "" {
		copied := *parsed
		copied.Platform = platform
		return &copied
	}
	return parsed
}

// resolve 依次尝试平台的供应商。可重试错误重试一次；连续失败的供应商被熔断一段时间后再试。
func (s *Service) resolve(ctx context.Context, link *Link, id, canonical string, emit func(Stage)) (*Result, error) {
	info := link.Platform.Info()
	var lastErr *Error
	tried := 0
	for _, provider := range s.providerChain(link.Platform) {
		if err := provider.Available(); err != nil {
			continue
		}
		if !s.stats.allow(provider.Name(), info.Key) {
			continue
		}
		tried++
		for attempt := 0; attempt < 2; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return nil, classifyTransportError(ctx.Err(), "解析超时")
				case <-time.After(600 * time.Millisecond):
				}
			}
			emit(Stage{Name: "parsing", Provider: provider.Name(), Message: "正在解析作品信息"})
			started := time.Now()
			pctx, cancel := context.WithTimeout(ctx, s.opts.ProviderTimeout)
			result, err := provider.Resolve(pctx, Request{Platform: link.Platform, URL: link.URL, ID: id, Canonical: canonical})
			cancel()
			elapsed := time.Since(started)
			if err == nil {
				err = s.finishResult(result, link, id, canonical, provider)
			}
			parsed := AsError(err)
			s.stats.recordProvider(provider.Name(), info.Key, parsed, elapsed, s.opts.ProviderCostMicros[provider.Name()])
			s.logAttempt(info.Key, provider.Name(), link.URL.String(), attempt, elapsed, parsed)
			if parsed == nil {
				return result, nil
			}
			copied := *parsed
			copied.Platform, copied.Provider = info.Key, provider.Name()
			if parsed.Code.terminal() {
				return nil, &copied
			}
			lastErr = preferError(lastErr, &copied)
			if !parsed.Retryable || ctx.Err() != nil {
				break
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if tried == 0 {
		return nil, &Error{Code: ErrUnavailable, Message: info.Label + "解析服务暂时不可用（供应商连续失败已自动熔断），请稍后重试或下载后本地上传", Retryable: true, Platform: info.Key}
	}
	return nil, &Error{Code: ErrUpstream, Message: info.Label + "解析失败，请稍后重试", Retryable: true, Platform: info.Key}
}

// preferError 在多个供应商都失败时保留对用户最有用的原因：作品本身的问题（不存在、需登录）优先于超时，再优先于通用上游错误；同级保留先出现的。
func preferError(current, next *Error) *Error {
	rank := func(err *Error) int {
		switch err.Code {
		case ErrNotFound, ErrNeedsLogin, ErrDRM, ErrNotVideo:
			return 3
		case ErrTimeout, ErrBusy:
			return 2
		}
		return 1
	}
	if current == nil || rank(next) > rank(current) {
		return next
	}
	return current
}

// finishResult 统一校验供应商结果：必须有视频、时长不超限，并补齐平台与来源信息。
func (s *Service) finishResult(result *Result, link *Link, id, canonical string, provider Provider) error {
	if result == nil || len(result.Media) == 0 {
		return newError(ErrUpstream, "没有解析到可下载的视频地址", nil)
	}
	if _, video, _ := pickMedia(result.Media); video == nil {
		return newError(ErrNotVideo, "这条作品没有可用的视频画面", nil)
	}
	info := link.Platform.Info()
	result.Provider = provider.Name()
	result.Meta.Platform, result.Meta.PlatformLabel = info.Key, info.Label
	if result.Meta.ID == "" {
		result.Meta.ID = id
	}
	if result.Meta.SourceURL == "" {
		result.Meta.SourceURL = canonical
	}
	result.Meta.Title = clip(result.Meta.Title, 200)
	result.Meta.Author = clip(result.Meta.Author, 80)
	if result.Meta.DurationMs > s.opts.MaxDurationMs+1000 {
		return newError(ErrTooLong, "视频时长超过 10 分钟，请先在本地剪短后上传", nil)
	}
	return nil
}

func (s *Service) logAttempt(platform, provider, link string, attempt int, elapsed time.Duration, err *Error) {
	attrs := []any{"platform", platform, "provider", provider, "attempt", attempt + 1, "elapsedMs", elapsed.Milliseconds(), "link", redactURL(link)}
	if err == nil {
		s.opts.Logger.Info("video link parse ok", attrs...)
		return
	}
	attrs = append(attrs, "code", string(err.Code), "retryable", err.Retryable)
	if err.Err != nil {
		attrs = append(attrs, "cause", clip(err.Err.Error(), 240))
	}
	s.opts.Logger.Warn("video link parse failed", attrs...)
}

// acquire 先占全局并发，再占平台并发；排队超过 QueueTimeout 返回忙碌（可重试）。
func (s *Service) acquire(ctx context.Context, platform string) (func(), error) {
	s.perMu.Lock()
	slots := s.perPlat[platform]
	if slots == nil {
		slots = make(chan struct{}, s.opts.PlatformConcurrency)
		s.perPlat[platform] = slots
	}
	s.perMu.Unlock()
	timer := time.NewTimer(s.opts.QueueTimeout)
	defer timer.Stop()
	take := func(ch chan struct{}) error {
		select {
		case ch <- struct{}{}:
			return nil
		case <-ctx.Done():
			return classifyTransportError(ctx.Err(), "排队等待解析")
		case <-timer.C:
			return newError(ErrBusy, "当前解析的人较多，请稍后重试", nil)
		}
	}
	if err := take(s.global); err != nil {
		return nil, err
	}
	if err := take(slots); err != nil {
		<-s.global
		return nil, err
	}
	return func() { <-slots; <-s.global }, nil
}

// PlatformStatus 是平台列表接口的一项：能力（真实测试）+ 当前部署条件 + 运行时健康。
type PlatformStatus struct {
	PlatformInfo
	Capabilities Capabilities     `json:"capabilities"`
	Health       []ProviderHealth `json:"health,omitempty"`
	Usage        *PlatformUsage   `json:"usage,omitempty"`
}

// Platforms 返回全部平台的能力与健康状态；includeHealth=false 时只给用户端展示能力。
func (s *Service) Platforms(includeHealth bool) []PlatformStatus {
	statuses := make([]PlatformStatus, 0, len(s.registry.adapters))
	for _, adapter := range s.registry.adapters {
		if !includeHealth && !s.platformEnabled(adapter.Info().Key) {
			continue
		}
		status := PlatformStatus{PlatformInfo: adapter.Info(), Capabilities: s.EffectiveCapabilities(adapter)}
		if includeHealth {
			status.Health = s.stats.providerHealth(adapter.Info().Key, s.providerChain(adapter))
			usage := s.stats.platformUsage(adapter.Info().Key)
			status.Usage = &usage
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// RecordDownload 由下载流程调用，累计每个平台的下载次数与流量（带宽成本统计）。
func (s *Service) RecordDownload(platform string, bytes int64, ok bool) {
	s.stats.recordDownload(platform, bytes, ok)
}
