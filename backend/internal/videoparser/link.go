package videoparser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	maxShareTextRunes = 4000
	maxLinkLength     = 2048
	maxRedirectHops   = 6
	mobileUserAgent   = "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1"
	desktopUserAgent  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

var (
	linkURLPattern = regexp.MustCompile(`(?i)https?://[a-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+`)
	// 分享文案里常见不带协议的短链，例如「v.douyin.com/AbCd/」「b23.tv/xxxx」。
	bareLinkPattern = regexp.MustCompile(`(?i)(?:^|[^a-z0-9@./])((?:[a-z0-9-]+\.)+[a-z]{2,10}/[a-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]*)`)
)

// Link 是从分享文案里识别出的链接与对应平台。
type Link struct {
	URL      *url.URL
	Platform PlatformAdapter
}

// Identify 从完整分享文案或 URL 中取出第一个属于已支持平台的链接并识别平台；不发起任何网络请求。
func (s *Service) Identify(text string) (*Link, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, newError(ErrInvalidLink, "请粘贴视频分享链接或分享文案", nil)
	}
	if len([]rune(text)) > maxShareTextRunes {
		return nil, newError(ErrInvalidLink, "分享内容过长，请只粘贴分享链接", nil)
	}
	candidates := extractLinks(text)
	if len(candidates) == 0 {
		return nil, newError(ErrInvalidLink, "没有识别到链接，请粘贴视频分享链接或分享文案", nil)
	}
	var firstErr error
	for _, raw := range candidates {
		u, err := parseShareURL(raw)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if adapter, ok := s.registry.Detect(u); ok {
			if s.platformEnabled(adapter.Info().Key) {
				return &Link{URL: u, Platform: adapter}, nil
			}
			if firstErr == nil {
				firstErr = &Error{Code: ErrUnsupported, Message: "暂不支持" + adapter.Info().Label + "链接，目前支持：" + strings.Join(s.enabledLabels(), "、"), Platform: adapter.Info().Key}
			}
			continue
		}
		if firstErr == nil {
			firstErr = newError(ErrUnsupported, "暂不支持这个网站的链接，目前支持："+strings.Join(s.enabledLabels(), "、"), nil)
		}
	}
	return nil, firstErr
}

// platformEnabled：部署配置了平台白名单（CANVAS_VIDEO_PARSER_PLATFORMS）时只开放名单里的平台。
func (s *Service) platformEnabled(key string) bool {
	if len(s.opts.Platforms) == 0 {
		return true
	}
	for _, item := range s.opts.Platforms {
		if item == key {
			return true
		}
	}
	return false
}

func (s *Service) enabledLabels() []string {
	labels := []string{}
	for _, adapter := range s.registry.Adapters() {
		if s.platformEnabled(adapter.Info().Key) && !s.opts.Disabled[adapter.Info().Key] {
			labels = append(labels, adapter.Info().Label)
		}
	}
	return labels
}

// extractLinks 依次返回文案中带协议的链接，以及不带协议、但域名属于常见短链格式的链接。
func extractLinks(text string) []string {
	var links []string
	seen := map[string]bool{}
	add := func(raw string) {
		raw = strings.TrimRight(raw, ".,;!?)]}'\"")
		if raw != "" && !seen[raw] {
			seen[raw] = true
			links = append(links, raw)
		}
	}
	for _, raw := range linkURLPattern.FindAllString(text, 8) {
		add(raw)
	}
	stripped := linkURLPattern.ReplaceAllString(text, " ")
	for _, match := range bareLinkPattern.FindAllStringSubmatch(stripped, 8) {
		add("https://" + match[1])
	}
	return links
}

func parseShareURL(raw string) (*url.URL, error) {
	if len(raw) > maxLinkLength {
		return nil, newError(ErrInvalidLink, "链接过长", nil)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, newError(ErrInvalidLink, "链接格式无效", err)
	}
	if err := checkLinkShape(u); err != nil {
		return nil, err
	}
	u.Fragment = ""
	return u, nil
}

// checkLinkShape 只接受 http/https、默认端口、无账号信息的地址；每一跳重定向都会再检查一次。
func checkLinkShape(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return newError(ErrBlocked, "只支持 http / https 链接", nil)
	}
	if u.User != nil {
		return newError(ErrBlocked, "链接不能包含账号信息", nil)
	}
	switch u.Port() {
	case "", "80", "443":
	default:
		return newError(ErrBlocked, "链接端口不受支持", nil)
	}
	return nil
}

// pageClient 返回用于打开平台页面的客户端：出站拨号由 outbound 校验公网地址（防 SSRF），
// 每一跳重定向还必须留在已注册的平台域名内，最多 maxRedirectHops 跳。
func (s *Service) pageClient(timeout time.Duration, proxy string) *http.Client {
	base := s.httpClient(timeout, proxy)
	client := *base
	inner := base.CheckRedirect
	client.Jar = nil
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirectHops {
			return newError(ErrBlocked, "链接跳转次数过多", nil)
		}
		if err := checkLinkShape(req.URL); err != nil {
			return err
		}
		if !s.registry.AllowedHost(req.URL.Hostname()) {
			return newError(ErrBlocked, "链接跳转到了不受支持的网站", fmt.Errorf("redirect host %s", req.URL.Hostname()))
		}
		req.Header.Del("Cookie")
		if inner != nil {
			return inner(req, via)
		}
		return nil
	}
	return &client
}

// fetchPage 打开平台页面并返回最终地址与最多 limit 字节的内容。
func (s *Service) fetchPage(ctx context.Context, platform PlatformAdapter, target, userAgent, referer string, limit int64) (*url.URL, []byte, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, nil, newError(ErrInvalidLink, "链接格式无效", err)
	}
	if err := checkLinkShape(u); err != nil {
		return nil, nil, err
	}
	if !s.registry.AllowedHost(u.Hostname()) {
		return nil, nil, newError(ErrBlocked, "链接不属于受支持的平台", nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, newError(ErrInvalidLink, "链接格式无效", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.6")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := s.pageClient(s.opts.PageTimeout, s.proxyFor(platform)).Do(req)
	if err != nil {
		return nil, nil, classifyTransportError(err, "打开分享页失败")
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return resp.Request.URL, body, newError(ErrNotFound, "作品不存在或已删除", nil)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return resp.Request.URL, body, newError(ErrNeedsLogin, "平台要求登录后才能查看这条作品", nil)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return resp.Request.URL, body, newError(ErrUpstream, fmt.Sprintf("平台暂时不可用（%d），请稍后重试", resp.StatusCode), nil)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return resp.Request.URL, body, newError(ErrUpstream, fmt.Sprintf("分享页返回 %d", resp.StatusCode), nil)
	}
	if readErr != nil {
		return resp.Request.URL, body, newError(ErrUpstream, "读取分享页失败", readErr)
	}
	return resp.Request.URL, body, nil
}

// expand 展开短链：只对平台声明的短链域名发请求，每一跳都校验域名与地址。
func (s *Service) expand(ctx context.Context, link *Link) (*Link, error) {
	info := link.Platform.Info()
	if !hostMatches(link.URL.Hostname(), info.ShortDomains) {
		return link, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.PageTimeout)
	defer cancel()
	final, body, err := s.fetchPage(ctx, link.Platform, link.URL.String(), mobileUserAgent, "", 256<<10)
	if final == nil {
		return nil, err
	}
	var parsed *Error
	// 短链落地页本身返回非 2xx 也可能已经拿到了真实地址（例如跳到需要脚本渲染的页面），以地址为准。
	if err != nil && errors.As(err, &parsed) && parsed.Code != ErrUpstream && parsed.Code != ErrNotFound {
		return nil, err
	}
	if hostMatches(final.Hostname(), info.ShortDomains) {
		// 部分短链（如小红书 xhslink.com/m/）落地页用脚本跳转，从页面里取出作品地址再识别。
		if target := shareTargetFromPage(info.Key, body); target != "" {
			if next, parseErr := parseShareURL(target); parseErr == nil && !hostMatches(next.Hostname(), info.ShortDomains) {
				if adapter, ok := s.registry.Detect(next); ok {
					return &Link{URL: next, Platform: adapter}, nil
				}
			}
		}
		if err != nil {
			return nil, err
		}
		return nil, newError(ErrNotFound, info.Label+"短链没有跳转到作品页，可能已失效", nil)
	}
	adapter, ok := s.registry.Detect(final)
	if !ok {
		return nil, newError(ErrBlocked, "短链跳转到了不受支持的网站", nil)
	}
	final.Fragment = ""
	return &Link{URL: final, Platform: adapter}, nil
}

func shareTargetFromPage(platform string, body []byte) string {
	switch platform {
	case PlatformXiaohongshu:
		return xhsShareTarget(body)
	}
	return ""
}

// postJSON 以 JSON 请求平台的公开接口；地址与每一跳重定向都要留在平台域名内，响应最多读 limit 字节。
func (s *Service) postJSON(ctx context.Context, platform PlatformAdapter, target, referer, origin string, payload any, limit int64) ([]byte, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, newError(ErrInvalidLink, "接口地址无效", err)
	}
	if err := checkLinkShape(u); err != nil {
		return nil, err
	}
	if !s.registry.AllowedHost(u.Hostname()) {
		return nil, newError(ErrBlocked, "接口地址不属于受支持的平台", nil)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(data))
	if err != nil {
		return nil, newError(ErrInvalidLink, "接口地址无效", err)
	}
	req.Header.Set("User-Agent", mobileUserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := s.pageClient(s.opts.PageTimeout, s.proxyFor(platform)).Do(req)
	if err != nil {
		return nil, classifyTransportError(err, "请求平台接口失败")
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, newError(ErrUpstream, fmt.Sprintf("平台暂时不可用（%d），请稍后重试", resp.StatusCode), nil)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, newError(ErrUpstream, fmt.Sprintf("平台接口返回 %d", resp.StatusCode), nil)
	}
	if readErr != nil {
		return nil, newError(ErrUpstream, "读取平台接口失败", readErr)
	}
	return body, nil
}

// classifyTransportError 区分超时、被拦截的内网地址与普通网络错误。
func classifyTransportError(err error, message string) error {
	var parsed *Error
	if errors.As(err, &parsed) {
		return parsed
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return newError(ErrTimeout, message+"：请求超时", err)
	}
	if errors.Is(err, context.Canceled) {
		return newError(ErrTimeout, message+"：请求已取消", err)
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "private") || strings.Contains(lower, "内网") || strings.Contains(lower, "不允许访问") || strings.Contains(lower, "blocked") {
		return newError(ErrBlocked, "链接指向的地址不允许访问", err)
	}
	return newError(ErrUpstream, message+"，请稍后重试", err)
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
