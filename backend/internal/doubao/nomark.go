package doubao

// 无水印视频地址解析，核心逻辑移植自浏览器插件 doubao-international
// （https://github.com/chuansd/doubao-international，MIT License）。
//
// 原理（与插件一致）：
//   - 拿到视频 vid 后，凭登录态调 get_play_info {key, type: "video"} 择优；
//   - 对接口返回的候选 URL 评分择优（original_media_info.main_url 权重最高，
//     带 watermark/logo 特征的重罚），全部失败时退化为直接改写 URL 参数。
//   - 注意：播放转码层的水印是烧录的（实测 URL 改写前后字节一致），本文件
//     全部手段都只是降级兜底；真无水印原片只能走 fallback.go 的母片通道。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const playInfoPath = "/samantha/media/get_play_info"

// 2026-09-18 真机实测结论（勿轻改）：
//   - 播放页/转码层的水印是烧录的：把 CDN 地址 lr=video_gen_watermark_dyn 改成
//     video_gen_no_watermark 后字节完全一致；get_play_info（带不带 lr 均试过）
//     返回的 original_media_info 也是同一份水印转码——播放转码层面拿不到原片；
//   - /samantha/media/get_download_info 在 www.doubao.com 域 HTTP 404（其他产品线的接口）；
//   - 唯一拿到真无水印原片的通道是会话页 fallback_api 改参母片（见 fallback.go），
//     本文件的 get_play_info 择优 + URL 改写只作为母片通道失败后的降级兜底。

var (
	// URL 查询参数里的 vid（vid=/video_id=/uri=）
	vidParamPattern = regexp.MustCompile(`[?&](?:vid|video_id|uri)=(v[0-9a-zA-Z_-]{10,})`)
	// URL 路径里以 v 开头的长段（豆包 CDN 视频 URI，如 v0d00fg1000c…）
	vidSegmentPattern = regexp.MustCompile(`(?:^|/)(v[0-9][0-9a-zA-Z_-]{18,})(?:[/?~]|$)`)
	// 视频文件头部元数据里的 vid:xxx（插件读取前 2MB 后正则提取）
	vidBodyPattern = regexp.MustCompile(`vid:(v[0-9a-zA-Z_-]{10,})`)

	noMarkPattern = regexp.MustCompile(`no[_-]?watermark|without[_-]?watermark|video_gen_no_watermark|original|origin|raw|watermark=0`)
	markedPattern = regexp.MustCompile(`watermark=1|water_mark|watermark|logo=|watermark_logo|wm_|lr=cici_ai`)

	tplvWatermarkPattern = regexp.MustCompile(`~tplv-[^.?&]*watermark[^.?&]*`)
	logoParamPattern     = regexp.MustCompile(`[&?]logo=[^&]*`)
)

func safeDecodeURL(text string) string {
	if v, err := url.QueryUnescape(text); err == nil {
		return v
	}
	return text
}

// ---------------------------------------------------------------- vid 提取

// extractVidFromURL 从视频地址的查询参数与路径段提取 vid（纯字符串解析，不发请求）。
func extractVidFromURL(videoURL string) string {
	if m := vidParamPattern.FindStringSubmatch(videoURL); m != nil {
		return m[1]
	}
	if m := vidSegmentPattern.FindStringSubmatch(videoURL); m != nil {
		return m[1]
	}
	return ""
}

// extractVid 从视频地址提取 vid：先查 URL，失败则读视频文件头 2MB。
func extractVid(ctx context.Context, cookieHeader, videoURL string) string {
	if vid := extractVidFromURL(videoURL); vid != "" {
		return vid
	}
	return extractVidFromHead(ctx, cookieHeader, videoURL)
}

// extractVidFromHead 下载视频前 2MB，从文件元数据里正则提取 vid。
func extractVidFromHead(ctx context.Context, cookieHeader, videoURL string) string {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, videoURL, nil)
	if err != nil {
		return ""
	}
	h := buildBrowserHeaders(ctx, cookieHeader)
	h.Set("Accept", "*/*")
	h.Set("Range", "bytes=0-2097151")
	h.Del("Content-Type")
	req.Header = h
	res, err := ctxHTTPClient(req.Context()).Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil && len(data) == 0 {
		return ""
	}
	if m := vidBodyPattern.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// ---------------------------------------------------------------- URL 清洗

// cleanVideoURL 直接改写 URL 参数去水印（插件 makeNoWatermarkUrl / cleanVideoUrl 的移植）：
// lr= 替换为 video_gen_no_watermark、watermark=1 改 0、删水印模板段与 logo= 参数。
func cleanVideoURL(videoURL string) string {
	if videoURL == "" {
		return videoURL
	}
	u := videoURL
	if strings.Contains(u, "lr=") {
		u = regexp.MustCompile(`lr=[^&]+`).ReplaceAllString(u, "lr=video_gen_no_watermark")
	}
	if strings.Contains(u, "watermark") {
		u = strings.ReplaceAll(u, "watermark=1", "watermark=0")
		u = tplvWatermarkPattern.ReplaceAllString(u, "")
	}
	if strings.Contains(u, "logo=") {
		u = logoParamPattern.ReplaceAllString(u, "")
		// 删参后修整可能残留的悬空分隔符
		u = strings.ReplaceAll(u, "?&", "?")
		u = strings.ReplaceAll(u, "&&", "&")
		if strings.HasSuffix(u, "?") || strings.HasSuffix(u, "&") {
			u = u[:len(u)-1]
		}
	}
	return u
}

func isLikelyNoMarkURL(u, key string) bool {
	return noMarkPattern.MatchString(strings.ToLower(safeDecodeURL(key + " " + u)))
}

func isLikelyMarkedURL(u, key string) bool {
	if isLikelyNoMarkURL(u, key) {
		return false
	}
	return markedPattern.MatchString(strings.ToLower(safeDecodeURL(key + " " + u)))
}

// ---------------------------------------------------------------- 候选评分

type videoCandidate struct {
	key    string
	source string
	url    string
	width  int64
	height int64
}

var (
	mainURLWordPattern  = regexp.MustCompile(`\bmain(_url)?\b`)
	noMarkWordPattern   = regexp.MustCompile(`\b(no[_-]?watermark|no_watermark_url)\b`)
	originalWordPattern = regexp.MustCompile(`\b(original|origin|raw)\b`)
)

// scoreVideoCandidate 候选地址打分，移植自插件 scoreVideoUrlCandidate。
func scoreVideoCandidate(c videoCandidate) int {
	if c.url == "" || !strings.HasPrefix(c.url, "http") {
		return -1 << 30
	}
	text := strings.ToLower(c.key + " " + c.source + " " + safeDecodeURL(c.url))
	score := 0
	if strings.Contains(c.source, "original_media_info") {
		score += 120
	}
	if noMarkWordPattern.MatchString(text) {
		score += 110
	}
	if originalWordPattern.MatchString(text) {
		score += 90
	}
	if strings.Contains(text, "video_gen_no_watermark") {
		score += 80
	}
	if mainURLWordPattern.MatchString(text) {
		score += 20
	}
	if strings.Contains(c.url, ".mp4") {
		score += 12
	}
	if c.width > 0 && c.height > 0 {
		pixels := c.width * c.height
		if pixels > 6_000_000 {
			pixels = 6_000_000
		}
		score += int(pixels / 300000)
	}
	if isLikelyMarkedURL(c.url, c.key+" "+c.source) {
		score -= 160
	}
	return score
}

func chooseBestVideoURL(candidates []videoCandidate) string {
	best := ""
	bestScore := -1 << 30
	for _, c := range candidates {
		if s := scoreVideoCandidate(c); s > bestScore {
			best = c.url
			bestScore = s
		}
	}
	return best
}

// ---------------------------------------------------------------- get_play_info

type playMediaInfo struct {
	MainURL string  `json:"main_url"`
	Width   flexInt `json:"width"`
	Height  flexInt `json:"height"`
	Meta    *struct {
		Width  flexInt `json:"width"`
		Height flexInt `json:"height"`
	} `json:"meta"`
}

// flexInt 兼容豆包接口里宽高在数字与字符串之间摇摆的返回（1080 / "1080"）。
// 解析失败按 0 处理，绝不让单个字段类型抖动毁掉整个候选列表。
type flexInt int64

func (f *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt(v)
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexInt(v)
		return nil
	}
	*f = 0
	return nil
}

// postMediaInfo 调一次豆包媒体接口，返回原始响应体。
func postMediaInfo(ctx context.Context, cookieHeader, apiPath string, body map[string]any) ([]byte, error) {
	tabID := randomUUID()
	reqURL := originFromCtx(ctx) + apiPath + "?" + buildQuery(cookieHeader, tabID).Encode()
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	h := buildBrowserHeaders(ctx, cookieHeader)
	h.Set("Accept", "application/json")
	req.Header = h
	res, err := ctxHTTPClient(req.Context()).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("豆包媒体接口 %s HTTP %d", apiPath, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 8<<20))
}

// fetchPlayInfoCandidatesFromJSON 从 get_play_info 原始响应解析候选列表（拆出便于对字段类型抖动做回归测试）。
func fetchPlayInfoCandidatesFromJSON(raw []byte) ([]videoCandidate, error) {
	var parsed struct {
		Code int64 `json:"code"`
		Data struct {
			OriginalMediaInfo *playMediaInfo   `json:"original_media_info"`
			NoWatermarkURL    string           `json:"no_watermark_url"`
			OriginalURL       string           `json:"original_url"`
			MainURL           string           `json:"main_url"`
			VideoURL          string           `json:"video_url"`
			Width             flexInt          `json:"width"`
			Height            flexInt          `json:"height"`
			PlayInfos         []map[string]any `json:"play_infos"`
			PlayInfo          map[string]any   `json:"play_info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return candidatesFromPlayInfo(parsed.Code, parsed.Data.OriginalMediaInfo, parsed.Data.NoWatermarkURL, parsed.Data.OriginalURL, parsed.Data.MainURL, parsed.Data.VideoURL, parsed.Data.Width, parsed.Data.Height, parsed.Data.PlayInfos, parsed.Data.PlayInfo)
}

// candidatesFromPlayInfo 把 get_play_info 响应字段折叠成候选列表（拆出便于对字段类型抖动做回归测试）。
func candidatesFromPlayInfo(
	code int64,
	om *playMediaInfo,
	noWatermarkURL, originalURL, mainURL, videoURL string,
	width, height flexInt,
	playInfos []map[string]any,
	playInfo map[string]any,
) ([]videoCandidate, error) {
	if code != 0 || om == nil &&
		noWatermarkURL == "" && mainURL == "" &&
		videoURL == "" && originalURL == "" &&
		len(playInfos) == 0 && playInfo == nil {
		return nil, nil
	}
	w, hgt := int64(width), int64(height)
	candidates := []videoCandidate{}
	if om != nil && om.MainURL != "" {
		cw, ch := int64(om.Width), int64(om.Height)
		if cw == 0 && om.Meta != nil {
			cw = int64(om.Meta.Width)
		}
		if ch == 0 && om.Meta != nil {
			ch = int64(om.Meta.Height)
		}
		if cw == 0 {
			cw = w
		}
		if ch == 0 {
			ch = hgt
		}
		candidates = append(candidates, videoCandidate{key: "main_url", source: "original_media_info", url: om.MainURL, width: cw, height: ch})
	}
	candidates = append(candidates,
		videoCandidate{key: "no_watermark_url", source: "data", url: noWatermarkURL, width: w, height: hgt},
		videoCandidate{key: "original_url", source: "data", url: originalURL, width: w, height: hgt},
		videoCandidate{key: "main_url", source: "data", url: mainURL, width: w, height: hgt},
		videoCandidate{key: "video_url", source: "data", url: videoURL, width: w, height: hgt},
	)
	playInfos = append([]map[string]any{}, playInfos...)
	if playInfo != nil {
		playInfos = append(playInfos, playInfo)
	}
	for _, pi := range playInfos {
		if pi == nil {
			continue
		}
		w, _ := evNum(pi, "width")
		hgt, _ := evNum(pi, "height")
		for _, pair := range []struct{ key, field string }{
			{"main", "main"}, {"main_url", "main_url"}, {"play_url", "play_url"}, {"url", "url"},
		} {
			if u, ok := pi[pair.field].(string); ok && u != "" {
				candidates = append(candidates, videoCandidate{key: pair.key, source: "play_info", url: u, width: w, height: hgt})
			}
		}
	}
	return candidates, nil
}

// ---------------------------------------------------------------- 通用响应遍历

// mediaURLFields 宽容遍历认得的地址字段（对齐豆包前端劫持插件 captureVideoUrls 的取值面）。
var mediaURLFields = []string{
	"main_url", "main", "play_url", "download_url", "video_url",
	"no_watermark_url", "original_url", "url", "src", "mp4_url",
}

var mediaLikeURLPattern = regexp.MustCompile(`(?i)(\.mp4|\.m3u8|video|media)`)

// candidatesFromMediaJSON 对 get_download_info / get_play_info 响应做宽容遍历：
// 不假设字段布局，递归收集所有「像视频直链」的 URL 字段（含 base64 包裹的 main_url），
// 交给 scoreVideoCandidate 统一评分。接口字段类型/嵌套抖动不会让整个响应作废。
func candidatesFromMediaJSON(payload []byte) []videoCandidate {
	var root map[string]any
	if json.Unmarshal(payload, &root) != nil {
		return nil
	}
	candidates := []videoCandidate{}
	seen := map[string]bool{}
	add := func(c videoCandidate) {
		if c.url == "" || !strings.HasPrefix(c.url, "http") || !mediaLikeURLPattern.MatchString(c.url) {
			return
		}
		if seen[c.url] {
			return
		}
		seen[c.url] = true
		candidates = append(candidates, c)
	}
	var walk func(node any, source string)
	walk = func(node any, source string) {
		switch v := node.(type) {
		case map[string]any:
			w, _ := evNum(v, "width")
			if w == 0 {
				w, _ = evNum(v, "vwidth")
			}
			if w == 0 {
				w, _ = evNum(v, "video_width")
			}
			h, _ := evNum(v, "height")
			if h == 0 {
				h, _ = evNum(v, "vheight")
			}
			if h == 0 {
				h, _ = evNum(v, "video_height")
			}
			for _, k := range mediaURLFields {
				val, ok := v[k]
				if !ok {
					continue
				}
				s, isStr := val.(string)
				if !isStr || s == "" {
					continue
				}
				u := s
				if dec := decodeMaybeB64URL(val); dec != "" {
					u = dec
				}
				add(videoCandidate{key: k, source: source, url: u, width: w, height: h})
			}
			for kk, vv := range v {
				next := source
				switch kk {
				case "original_media_info":
					next = "original_media_info"
				case "play_info", "play_infos":
					next = "play_info"
				case "download_info":
					next = "download_info"
				}
				walk(vv, next)
			}
		case []any:
			for _, item := range v {
				walk(item, source)
			}
		}
	}
	walk(root, "data")
	return candidates
}

func hasUnmarkedCandidate(cands []videoCandidate) bool {
	for _, c := range cands {
		if !isLikelyMarkedURL(c.url, c.key+" "+c.source) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 对外入口

// fetchPlayInfoCandidates 调 get_play_info 换取候选地址列表（需有效登录态）。
// 实测（2026-09-18，见文件头注释）该通道返回的都是水印转码，仅作 fallback
// 母片通道失败后的降级兜底；true 无水印原片只能走 fallback.go 的母片通道。
func fetchMediaCandidates(ctx context.Context, cookieHeader, vid string) []videoCandidate {
	var cands []videoCandidate
	appendFrom := func(payload []byte, err error) {
		if err != nil || len(payload) == 0 {
			return
		}
		if parsed, perr := fetchPlayInfoCandidatesFromJSON(payload); perr == nil && len(parsed) > 0 {
			cands = append(cands, parsed...)
			return
		}
		cands = append(cands, candidatesFromMediaJSON(payload)...)
	}
	appendFrom(postMediaInfo(ctx, cookieHeader, playInfoPath, map[string]any{"key": vid, "type": "video"}))
	return cands
}

// resolveNoWatermarkVideoURL 把一个播放地址升级为无水印地址：
// vid → get_play_info / get_download_info（带 lr 无水印标记）多通道择优；
// 失败退化为 URL 参数清洗；再失败保留原地址。
func resolveNoWatermarkVideoURL(ctx context.Context, cookieHeader, videoURL string) string {
	if videoURL == "" || !strings.HasPrefix(videoURL, "http") {
		return videoURL
	}
	best := ""
	if cookieHeader != "" {
		if vid := extractVid(ctx, cookieHeader, videoURL); vid != "" {
			if cands := fetchMediaCandidates(ctx, cookieHeader, vid); len(cands) > 0 {
				best = chooseBestVideoURL(cands)
			}
		}
	}
	if best == "" {
		best = cleanVideoURL(videoURL)
	}
	if best == "" || !strings.HasPrefix(best, "http") {
		return videoURL
	}
	return best
}

// upgradeVideosNoWatermark 就地把 VideoResult 里的视频地址升级为无水印版本。
// 单条失败不影响其余地址，也不影响生成结果本身（保留原地址兜底）。
func upgradeVideosNoWatermark(ctx context.Context, cookieHeader string, result *VideoResult) {
	if result == nil || cookieHeader == "" {
		return
	}
	for i, u := range result.URLs {
		result.URLs[i] = resolveNoWatermarkVideoURL(ctx, cookieHeader, u)
	}
}
