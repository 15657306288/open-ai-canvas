package doubao

// 无水印母片提取（fallback_api 通道）。
//
// 原理（参考开源项目 ihmily/doubao-nomark 的逆向实现）：
//   - 会话页 SSR 数据里每个视频都带 fallback_api（vas-*.snssdk.com/video/fplay/...）；
//   - 把请求参数改成 logo_type=unwatermarked&codec_type=8，返回的 video_list.main_url
//     是 qAAB 标记的 AES-128-CBC 加密 token，明文就是无水印母片直链；
//   - 密钥由响应里的 key_seed 派生：SHA512(seed[:32]) 再拼固定盐值做第二次 SHA512，
//     前 16 字节为 key、次 16 字节为 iv。
//
// 该通道拿到的是豆包服务端保存的原始合成片（无「豆包AI生成」角标），
// 与 get_play_info 返回的水印播放转码是两个对象。

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const qaabSaltHex = "4dd4c2e6b83162090e52b3c7a6733ba4" +
	"1cb2462b829ab58a196b39db57177524" +
	"f49baf7f08e8d68d26a72e37c1a95a2f" +
	"1f05a51892aef2949732b62a38aadd58"

var qaabSalt = mustHex(qaabSaltHex)

func mustHex(s string) []byte {
	out := make([]byte, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		out[i/2] = hexVal(s[i])<<4 | hexVal(s[i+1])
	}
	return out
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// ---------------------------------------------------------------- 提取 fallback_api

var (
	// 会话页 SSR 里斜杠以 \u002F 或 \/ 形式出现，先归一再匹配。
	fallbackURLPattern = regexp.MustCompile(
		`https://[a-z0-9.-]*(?:\.snssdk\.com|\.douyinvod\.com|\.dola\.com|\.byteintlapi\.com)/video/fplay/[^"'\\\s<>]+`)
	// fallback_api 路径最后一段是视频 vid（v0d…/v02… 长串）。
	fallbackVidPattern = regexp.MustCompile(`/video/fplay/[0-9]+/[0-9a-f]+/(v[0-9a-zA-Z_-]{10,})`)
)

// normalizePageEscapes 把 SSR 里的多层转义折叠成普通字符。
func normalizePageEscapes(s string) string {
	out := strings.ReplaceAll(s, `\u002F`, "/")
	out = strings.ReplaceAll(out, `\u002f`, "/")
	out = strings.ReplaceAll(out, `\/`, "/")
	out = html.UnescapeString(out)
	return out
}

// ExtractFallbackAPIs 从会话页 HTML 提取 vid -> fallback_api 映射。
func ExtractFallbackAPIs(pageHTML string) map[string]string {
	normalized := normalizePageEscapes(pageHTML)
	out := map[string]string{}
	for _, m := range fallbackURLPattern.FindAllString(normalized, -1) {
		u := strings.TrimRight(m, `\,;)]}`)
		if vm := fallbackVidPattern.FindStringSubmatch(u); vm != nil {
			if _, exists := out[vm[1]]; !exists {
				out[vm[1]] = u
			}
		}
	}
	return out
}

// buildUnwatermarkedFallbackURL 替换 fallback_api 的水印参数。
func buildUnwatermarkedFallbackURL(fallbackAPI string) (string, error) {
	u, err := url.Parse(fallbackAPI)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(u.Hostname())
	trusted := false
	for _, suffix := range []string{".snssdk.com", ".douyinvod.com", ".dola.com", ".byteintlapi.com"} {
		if host == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(host, suffix) {
			trusted = true
			break
		}
	}
	if u.Scheme != "https" || !trusted || !strings.HasPrefix(u.Path, "/video/fplay/") {
		return "", fmt.Errorf("fallback_api 不是受信任的豆包视频接口")
	}
	q := u.Query()
	q.Del("codec_type")
	q.Del("logo_type")
	q.Set("codec_type", "8")
	q.Set("logo_type", "unwatermarked")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ---------------------------------------------------------------- qAAB 解密

// decodeBase64Loose 兼容豆包 token 的非标准 base64 变体。
func decodeBase64Loose(value string) []byte {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil
	}
	variants := []string{
		text,
		strings.NewReplacer("$", "_", "@", "/", "#", ".").Replace(text),
		strings.NewReplacer("$", "+", "@", "/", "#", "=").Replace(text),
	}
	seen := map[string]bool{}
	for _, candidate := range variants {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		normalized := strings.NewReplacer("-", "+", "_", "/").Replace(candidate)
		normalized += strings.Repeat("=", (4-len(normalized)%4)%4)
		if data, err := base64.StdEncoding.DecodeString(normalized); err == nil {
			return data
		}
	}
	return nil
}

func isPlainASCIIURL(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, b := range data {
		if b != 9 && b != 10 && b != 13 && (b < 32 || b > 126) {
			return false
		}
	}
	return true
}

func urlFromBytes(data []byte) string {
	if !isPlainASCIIURL(data) {
		return ""
	}
	u := strings.TrimSpace(string(data))
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return ""
}

func stripPKCS7(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	pad := int(data[len(data)-1])
	if pad < 1 || pad > 16 || pad > len(data) {
		return data
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return data
		}
	}
	return data[:len(data)-pad]
}

func decryptAESCBCURL(payload, key, iv []byte) string {
	if len(payload) == 0 || len(payload)%16 != 0 {
		return ""
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(payload))
	mode.CryptBlocks(plaintext, payload)
	if u := urlFromBytes(plaintext); u != "" {
		return u
	}
	return urlFromBytes(stripPKCS7(plaintext))
}

// DecodeQAABToken 解密 qAAB token，返回明文直链（失败返回空串）。
func DecodeQAABToken(token, keySeed string) string {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "qAAB") {
		return ""
	}
	data := decodeBase64Loose(token)
	seed := decodeBase64Loose(keySeed)
	if len(data) == 0 || len(seed) == 0 {
		return ""
	}
	first := sha512.Sum512(seed[:min(32, len(seed))])
	material := sha512.Sum512(append(first[:], qaabSalt...))
	key, iv := material[:16], material[16:32]

	type attempt struct {
		payload []byte
		key     []byte
		iv      []byte
	}
	attempts := []attempt{}
	if len(data) > 4 && data[0] == 0xa8 && data[1] == 0x00 && data[2] == 0x01 && data[3] == 0x00 {
		attempts = append(attempts,
			attempt{data[4:], key, iv},
			attempt{data[4:], iv, key},
		)
		if len(data) > 36 {
			attempts = append(attempts,
				attempt{data[36:], key, data[20:36]},
				attempt{data[36:], key, iv},
			)
		}
	} else {
		attempts = append(attempts, attempt{data, key, iv})
	}
	for _, a := range attempts {
		if u := decryptAESCBCURL(a.payload, a.key, a.iv); u != "" {
			return u
		}
	}
	return ""
}

// ---------------------------------------------------------------- 对外入口

type fallbackVideoInfo struct {
	Vid    string
	URL    string
	Width  int
	Height int
}

// fetchFallbackResponse 请求改参后的 fallback_api，返回原始 JSON。
func fetchFallbackResponse(ctx context.Context, fallbackAPI string) ([]byte, error) {
	unwatermarked, err := buildUnwatermarkedFallbackURL(fallbackAPI)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, unwatermarked, nil)
	if err != nil {
		return nil, err
	}
	h := buildBrowserHeaders(ctx, "")
	h.Set("Accept", "application/json, text/plain, */*")
	h.Del("Cookie")
	h.Del("Origin")
	h.Set("Referer", originFromCtx(ctx)+"/")
	req.Header = h
	res, err := ctxHTTPClient(req.Context()).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("fallback_api HTTP %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 8<<20))
}

// parseFallbackPayload 从 fallback_api 响应解析出无水印直链。
// 结构：video_info.data.video_list{...}.main_url（qAAB token）+ data.key_seed。
func parseFallbackPayload(raw []byte) (*fallbackVideoInfo, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	var keySeed string
	videoList := []any{}
	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if ks, ok := v["key_seed"].(string); ok && ks != "" && keySeed == "" {
				keySeed = ks
			}
			if vl, ok := v["video_list"]; ok {
				switch list := vl.(type) {
				case map[string]any:
					for _, item := range list {
						if m, ok := item.(map[string]any); ok {
							videoList = append(videoList, m)
						}
					}
				case []any:
					for _, item := range list {
						if m, ok := item.(map[string]any); ok {
							videoList = append(videoList, m)
						}
					}
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(payload)
	if len(videoList) == 0 {
		return nil, fmt.Errorf("fallback_api 响应中没有 video_list")
	}

	type entryInfo struct {
		token   string
		width   float64
		height  float64
		bitrate float64
	}
	var best *entryInfo
	for _, item := range videoList {
		m := item.(map[string]any)
		token := ""
		if v, ok := m["main_url"].(string); ok && v != "" {
			token = v
		} else if v, ok := m["play_url"].(string); ok && v != "" {
			token = v
		}
		if token == "" {
			continue
		}
		num := func(keys ...string) float64 {
			for _, k := range keys {
				if v, ok := m[k].(float64); ok {
					return v
				}
			}
			return 0
		}
		e := &entryInfo{
			token:   token,
			width:   num("vwidth", "width"),
			height:  num("vheight", "height"),
			bitrate: num("bitrate", "real_bitrate"),
		}
		if best == nil || e.width*e.height > best.width*best.height ||
			(e.width*e.height == best.width*best.height && e.bitrate > best.bitrate) {
			best = e
		}
	}
	if best == nil {
		return nil, fmt.Errorf("fallback_api 响应中没有 main_url")
	}

	// token 可能是明文 URL、base64 包裹的 URL 或 qAAB 加密 token。
	token := best.token
	if !strings.HasPrefix(token, "http") {
		if data := decodeBase64Loose(token); data != nil {
			if u := urlFromBytes(data); u != "" {
				token = u
			}
		}
	}
	if strings.HasPrefix(token, "qAAB") {
		if keySeed == "" {
			return nil, fmt.Errorf("qAAB token 缺少 key_seed")
		}
		token = DecodeQAABToken(token, keySeed)
		if token == "" {
			return nil, fmt.Errorf("qAAB token 解密失败")
		}
	}
	if !strings.HasPrefix(token, "http") {
		return nil, fmt.Errorf("fallback_api 未得到有效视频直链")
	}
	return &fallbackVideoInfo{
		URL:    token,
		Width:  int(best.width),
		Height: int(best.height),
	}, nil
}

// ResolveFallbackVideoURL 通过会话页 fallback_api 通道获取指定 vid 的无水印母片直链。
// cookieHeader 用于拉取会话页；fallback_api 本身无需登录态。
func ResolveFallbackVideoURL(ctx context.Context, cookieHeader, convID, vid string) (string, error) {
	if strings.TrimSpace(convID) == "" || strings.TrimSpace(vid) == "" {
		return "", fmt.Errorf("缺少会话或视频标识")
	}
	tabID := randomUUID()
	pageHTML, err := fetchConversationPage(ctx, cookieHeader, convID, tabID)
	if err != nil || pageHTML == "" {
		return "", fmt.Errorf("会话页拉取失败")
	}
	return ResolveFallbackVideoURLFromPage(ctx, pageHTML, vid)
}

// ResolveFallbackVideoURLFromPage 从已拉取的会话页 HTML 解析无水印直链。
func ResolveFallbackVideoURLFromPage(ctx context.Context, pageHTML, vid string) (string, error) {
	apis := ExtractFallbackAPIs(pageHTML)
	fallbackAPI, ok := apis[vid]
	if !ok {
		return "", fmt.Errorf("会话页中未找到视频 %s 的 fallback_api", vid)
	}
	raw, err := fetchFallbackResponse(ctx, fallbackAPI)
	if err != nil {
		return "", err
	}
	info, err := parseFallbackPayload(raw)
	if err != nil {
		return "", err
	}
	return info.URL, nil
}

// applyFallbackNoWatermark 就地把 VideoResult 的地址升级为 fallback_api 无水印母片直链。
// 成功返回 true 并置 WatermarkFree；任何一步失败都保留原地址返回 false，不影响出片。
func applyFallbackNoWatermark(ctx context.Context, cookieHeader string, result *VideoResult) bool {
	if result == nil || strings.TrimSpace(cookieHeader) == "" || strings.TrimSpace(result.ConvID) == "" || len(result.URLs) == 0 {
		return false
	}
	vid := ""
	for _, u := range result.URLs {
		// 先查 URL，再读视频文件头 2MB（新版 CDN 地址 v*-vdl.doubao.com/.../video/tos/cn/...
		// 的 URI 段不以 v 开头且无 vid 参数，纯 URL 正则提取不出——2026-09 真机实测）。
		if v := extractVid(ctx, cookieHeader, u); v != "" {
			vid = v
			break
		}
	}
	if vid == "" {
		log.Printf("[doubao] fallback_api 通道放弃：所有地址均提取不到 vid")
		return false
	}
	pageHTML, err := fetchConversationPage(ctx, cookieHeader, result.ConvID, randomUUID())
	if err != nil || pageHTML == "" {
		log.Printf("[doubao] fallback_api 会话页拉取失败 vid=%s err=%v", vid, err)
		return false
	}
	videoURL, err := ResolveFallbackVideoURLFromPage(ctx, pageHTML, vid)
	if err != nil {
		log.Printf("[doubao] fallback_api 母片提取失败 vid=%s: %v", vid, err)
		return false
	}
	result.URLs = append([]string{videoURL}, result.URLs...)
	result.WatermarkFree = true
	log.Printf("[doubao] fallback_api 无水印母片提取成功 vid=%s", vid)
	return true
}
