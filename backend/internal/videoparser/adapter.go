package videoparser

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

// PlatformAdapter 是单个平台的适配器：detect / getCapabilities / 规范化作品 ID，并声明默认的解析供应商顺序。
// parse(url) 由 Service.Parse 统一调度（限流、缓存、重试、降级），结果的 Metadata() / MediaAssets()
// 对应 getMetadata() / getMediaAssets()。平台特有的解析逻辑写成该平台自己的 Provider。
type PlatformAdapter interface {
	Info() PlatformInfo
	Detect(u *url.URL) bool
	Capabilities() Capabilities
	// Normalize 返回作品 ID 与去掉跟踪参数的公开地址，用于缓存键、去重和素材来源记录。
	Normalize(u *url.URL) (id string, canonical string)
	// Providers 是默认的供应商优先级；部署时可用 CANVAS_VIDEO_PARSER_PRIORITY 覆盖。
	Providers() []string
}

// basePlatform 实现各平台共有的部分；平台文件只填写域名、ID 规则、能力说明与供应商顺序。
type basePlatform struct {
	info      PlatformInfo
	caps      Capabilities
	providers []string
	idPattern *regexp.Regexp
	canonical func(id string, u *url.URL) string
}

func (p *basePlatform) Info() PlatformInfo { return p.info }

func (p *basePlatform) Detect(u *url.URL) bool {
	return u != nil && hostMatches(u.Hostname(), p.info.Domains)
}

func (p *basePlatform) Capabilities() Capabilities {
	caps := p.caps
	caps.SupportLabel = caps.Support.Label()
	caps.Providers = append([]string(nil), p.providers...)
	return caps
}

func (p *basePlatform) Providers() []string { return append([]string(nil), p.providers...) }

func (p *basePlatform) Normalize(u *url.URL) (string, string) {
	if u == nil {
		return "", ""
	}
	id := ""
	if p.idPattern != nil {
		if match := p.idPattern.FindStringSubmatch(u.String()); len(match) > 1 {
			id = match[1]
		}
	}
	if id != "" && p.canonical != nil {
		return id, p.canonical(id, u)
	}
	stripped := *u
	stripped.RawQuery, stripped.Fragment, stripped.User = "", "", nil
	return id, stripped.String()
}

// hostMatches 判断主机是否等于某个平台域名或是它的子域名；IP 字面量一律不算平台域名。
func hostMatches(host string, domains []string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || net.ParseIP(host) != nil {
		return false
	}
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// Registry 保存已注册的平台适配器；识别顺序即注册顺序。
type Registry struct {
	adapters []PlatformAdapter
	byKey    map[string]PlatformAdapter
}

func NewRegistry(adapters ...PlatformAdapter) *Registry {
	registry := &Registry{byKey: map[string]PlatformAdapter{}}
	for _, adapter := range adapters {
		key := adapter.Info().Key
		if _, exists := registry.byKey[key]; exists {
			continue
		}
		registry.adapters = append(registry.adapters, adapter)
		registry.byKey[key] = adapter
	}
	return registry
}

// DefaultRegistry 注册全部已实现的平台；默认开放哪些由 Options.Platforms 决定（见 DefaultPlatforms）。
func DefaultRegistry() *Registry {
	return NewRegistry(
		newDouyinAdapter(), newKuaishouAdapter(), newXiaohongshuAdapter(), newChannelsAdapter(),
		newBilibiliAdapter(), newWeiboAdapter(), newXiguaAdapter(),
		newTikTokAdapter(), newYouTubeAdapter(), newInstagramAdapter(), newXAdapter(),
	)
}

func (r *Registry) Detect(u *url.URL) (PlatformAdapter, bool) {
	for _, adapter := range r.adapters {
		if adapter.Detect(u) {
			return adapter, true
		}
	}
	return nil, false
}

func (r *Registry) Get(key string) (PlatformAdapter, bool) {
	adapter, ok := r.byKey[key]
	return adapter, ok
}

func (r *Registry) Adapters() []PlatformAdapter { return append([]PlatformAdapter(nil), r.adapters...) }

// AllowedHost 是全局域名白名单：用户提交的链接与短链每一跳都必须落在某个已注册平台的域名内。
func (r *Registry) AllowedHost(host string) bool {
	for _, adapter := range r.adapters {
		if hostMatches(host, adapter.Info().Domains) {
			return true
		}
	}
	return false
}
