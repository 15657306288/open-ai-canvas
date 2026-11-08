package videoparser

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// 快手：2026-11-05 实测，PC 页 www.kuaishou.com/short-video 多数时候只返回脚本外壳，
// 移动分享页 v.m.chenzhongtech.com/fw/photo/<id> 的 window.INIT_STATE 稳定带有作品对象（mainMvUrls、caption、duration）。
// 不需要登录或 Cookie；yt-dlp 没有快手解析器。

var (
	kuaishouIDPattern = regexp.MustCompile(`(?:/short-video/|/fw/photo/|/fw/long-video/|/photo/\d+/|photoId=)([0-9A-Za-z]{8,32})`)
	kuaishouInitState = regexp.MustCompile(`window\.INIT_STATE\s*=\s*`)
	kuaishouMvURL     = regexp.MustCompile(`"mainMvUrls"\s*:\s*\[\s*\{[^\]]*?"url"\s*:\s*"([^"]+)"`)
	kuaishouCaption   = regexp.MustCompile(`"caption"\s*:\s*"([^"]{0,600})"`)
	kuaishouDuration  = regexp.MustCompile(`"duration"\s*:\s*(\d{3,9})`)
)

type kuaishouAdapter struct{ basePlatform }

func newKuaishouAdapter() PlatformAdapter {
	return &kuaishouAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformKuaishou, Label: "快手", Region: RegionDomestic,
			Domains: []string{"kuaishou.com", "chenzhongtech.com", "gifshow.com", "kuaishouapp.com"}, ShortDomains: []string{"v.kuaishou.com", "v.kuaishouapp.com"}},
		caps: Capabilities{Support: SupportSupported, Video: true, Metadata: true, VerifiedOn: "2026-11-05",
			Notes: "公开作品经移动分享页解析，无需登录；私密、下架或图集作品会给出明确提示"},
		providers: []string{"kuaishou-share-page"},
		idPattern: kuaishouIDPattern,
		canonical: func(id string, _ *url.URL) string { return "https://www.kuaishou.com/short-video/" + id },
	}}
}

type kuaishouSharePageProvider struct{ svc *Service }

func (p *kuaishouSharePageProvider) Name() string       { return "kuaishou-share-page" }
func (p *kuaishouSharePageProvider) Kind() ProviderKind { return ProviderSelfHosted }
func (p *kuaishouSharePageProvider) Available() error   { return nil }

func (p *kuaishouSharePageProvider) Resolve(ctx context.Context, req Request) (*Result, error) {
	if req.ID == "" {
		return nil, newError(ErrInvalidLink, "没有从快手链接里识别到作品编号", nil)
	}
	_, body, err := p.svc.fetchPage(ctx, req.Platform, "https://v.m.chenzhongtech.com/fw/photo/"+req.ID, mobileUserAgent, "https://v.m.chenzhongtech.com/", 6<<20)
	if err != nil {
		return nil, err
	}
	return parseKuaishouPage(body, req.ID)
}

// parseKuaishouPage 从 INIT_STATE 里找到带 mainMvUrls 的作品对象；结构变化时退回正则读取同名字段。
func parseKuaishouPage(body []byte, id string) (*Result, error) {
	if photo := kuaishouPhoto(body); photo != nil {
		return kuaishouResult(photo, id)
	}
	match := kuaishouMvURL.FindSubmatch(body)
	if len(match) < 2 {
		if bytes.Contains(body, []byte(`"photoType":"`)) && !bytes.Contains(body, []byte(`"photoType":"VIDEO"`)) {
			return nil, newError(ErrNotVideo, "这条快手是图集作品，没有可用的视频", nil)
		}
		return nil, newError(ErrUpstream, "快手分享页没有返回视频地址，作品可能已设为私密或下架", nil)
	}
	result := &Result{Meta: Metadata{ID: id}, Media: []MediaAsset{{Kind: MediaMuxed, URL: unescapeJSONText(string(match[1])), Ext: "mp4", Headers: kuaishouHeaders()}}}
	if caption := kuaishouCaption.FindSubmatch(body); len(caption) > 1 {
		result.Meta.Title = unescapeJSONText(string(caption[1]))
	}
	if duration := kuaishouDuration.FindSubmatch(body); len(duration) > 1 {
		result.Meta.DurationMs, _ = strconv.ParseInt(string(duration[1]), 10, 64)
	}
	return result, nil
}

func kuaishouHeaders() map[string]string {
	return map[string]string{"User-Agent": mobileUserAgent, "Referer": "https://v.m.chenzhongtech.com/"}
}

func kuaishouPhoto(body []byte) map[string]any {
	loc := kuaishouInitState.FindIndex(body)
	if loc == nil {
		return nil
	}
	rest := body[loc[1]:]
	if end := bytes.Index(rest, []byte("</script>")); end >= 0 {
		rest = rest[:end]
	}
	rest = bytes.TrimRight(bytes.TrimSpace(rest), ";")
	decoder := json.NewDecoder(bytes.NewReader(rest))
	decoder.UseNumber()
	var state any
	if decoder.Decode(&state) != nil {
		return nil
	}
	return findMapWithKey(state, "mainMvUrls", 0)
}

// findMapWithKey 深度优先查找第一个含指定键的对象（INIT_STATE 的外层键是混淆过的，不能按路径取）。
func findMapWithKey(node any, key string, depth int) map[string]any {
	if depth > 12 {
		return nil
	}
	switch value := node.(type) {
	case map[string]any:
		if _, ok := value[key]; ok {
			return value
		}
		for _, child := range value {
			if found := findMapWithKey(child, key, depth+1); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range value {
			if found := findMapWithKey(child, key, depth+1); found != nil {
				return found
			}
		}
	}
	return nil
}

func kuaishouResult(photo map[string]any, id string) (*Result, error) {
	if photoType := stringField(photo, "photoType"); photoType != "" && photoType != "VIDEO" {
		return nil, newError(ErrNotVideo, "这条快手是图集作品，没有可用的视频", nil)
	}
	result := &Result{Meta: Metadata{ID: id, Title: stringField(photo, "caption"), Author: firstNonEmpty(stringField(photo, "userName"), stringField(photo, "authorName"))}}
	result.Meta.DurationMs = int64Field(photo, "duration")
	result.Meta.CoverURL = firstURL(photo["coverUrls"])
	width, height := int(int64Field(photo, "width")), int(int64Field(photo, "height"))
	urls, _ := photo["mainMvUrls"].([]any)
	for _, item := range urls {
		entry, _ := item.(map[string]any)
		if raw := stringField(entry, "url"); strings.HasPrefix(raw, "http") {
			result.Media = append(result.Media, MediaAsset{Kind: MediaMuxed, URL: raw, Ext: "mp4", Width: width, Height: height, Headers: kuaishouHeaders()})
		}
	}
	if len(result.Media) == 0 {
		return nil, newError(ErrUpstream, "快手分享页没有返回视频地址，作品可能已设为私密或下架", nil)
	}
	return result, nil
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, _ := m[key].(string)
	return strings.TrimSpace(value)
}

func int64Field(m map[string]any, key string) int64 {
	switch value := m[key].(type) {
	case json.Number:
		if n, err := value.Int64(); err == nil {
			return n
		}
		if f, err := value.Float64(); err == nil {
			return int64(f)
		}
	case float64:
		return int64(value)
	case string:
		n, _ := strconv.ParseInt(value, 10, 64)
		return n
	}
	return 0
}

func firstURL(value any) string {
	items, _ := value.([]any)
	for _, item := range items {
		if entry, ok := item.(map[string]any); ok {
			if raw := stringField(entry, "url"); strings.HasPrefix(raw, "http") {
				return raw
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
