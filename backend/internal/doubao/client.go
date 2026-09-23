package doubao

// 豆包网页协议（samantha）客户端，移植自豆包创作工作台 src/image-client.js 与 video-client.js。
// 协议要点（结论来自参考项目实测，勿轻改）：
//   - 提交路径必须是 /samantha/chat/completion（漏掉 /samantha 前缀会被网关拒绝）；
//   - SSE 事件类型在 `event:` 行上（STREAM_CHUNK / STREAM_ERROR / SSE_REPLY_END…）；
//   - 文生图 content_type=2009 + skill_type=3；文生视频 content_type=2020 + skill_type=17；
//   - 视频结果 content_type=2021，或异步写进会话（GET /chat/{id} 页面内嵌 _ROUTER_DATA）；
//   - 错误码：710022002 顶点限流、710022004 风控、710012000/710012001 登录态失效。

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"infinite-canvas/backend/internal/outbound"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	assistantID = "497858"
	versionCode = "20800"
	// pcVersion 与参考项目对齐（3.22.5）：version_code=20800 配 pc_version=2.44.0
	// 是自相矛盾的版本组合，属于风控评分的负向信号。
	pcVersion          = "3.22.5"
	chatCompletionPath = "/samantha/chat/completion"
	asyncStreamPath    = "/samantha/chat/async/stream"
	threadListPath     = "/samantha/thread/list"
	doubaoOrigin       = "https://www.doubao.com"
	userAgent          = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	secChUA            = `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`
)

// ClassifyError 上游失败分类，驱动账号池的冷却策略。
type ClassifyError struct {
	Kind    string // rate_limited | quota_exhausted | session_expired
	Code    int64
	Message string
}

func (e *ClassifyError) Error() string { return e.Message }

var (
	quotaPatterns          = regexp.MustCompile(`免费次数已用完|额度已用完|额度用尽|次数已用完|开通豆包专业版|积分不足|余额不足|今日额度|已达上限`)
	rateLimitPatterns      = regexp.MustCompile(`rate\s*limited|shark_admin|710022004|710022002|710012001|verify_scene|系统错误|操作过于频繁|请求过于频繁|当前服务访问频繁|服务访问频繁|请稍后重试`)
	sessionExpiredPatterns = regexp.MustCompile(`user invalid|login invalid|登录态失效|Invalid User ID`)
	hardFailurePatterns    = regexp.MustCompile(`生成失败|内容不适宜|违规|敏感内容|无法完成该任务|已被拦截`)
	acceptancePatterns     = regexp.MustCompile(`正在为您生成视频|视频生成好后|生成好后|预计等待|预计等|大约需要|我会主动发送|消耗每日免费额度|视频生成已提交`)
	confirmAskExplicit     = regexp.MustCompile(`确认后|确认了就|是否确认|确认按|请确认|待你确认|等你确认|你确认|确认一下|确认无误|生成吗|我就直接生成|按这个生成|如果你确认|如确认|确认我就`)
)

// ---------------------------------------------------------------- 设备指纹

// deviceFingerprint 每账号固定指纹。与参考实现不同，这里直接从 sessionid
// 哈希确定性派生（无需落盘）：同一账号永远得到同一套 id，重启后一致。
type deviceFingerprint struct {
	DeviceID string
	WebID    string
	TeaUUID  string
}

func fingerprintFor(sessionID string) deviceFingerprint {
	sum := sha256.Sum256([]byte("doubao-fp:" + sessionID))
	digit := func(offset int) string {
		out := make([]byte, 0, 19)
		for i := 0; i < 18; i++ {
			out = append(out, '0'+byte(sum[(offset+i)%len(sum)])%10)
		}
		return "7" + string(out)
	}
	return deviceFingerprint{DeviceID: digit(0), WebID: digit(8), TeaUUID: digit(16)}
}

func randomUUID() string {
	// 时间+随机来源足够做本地消息 id；无需严格 RFC4122 版本位。
	buf := make([]byte, 16)
	now := time.Now().UnixNano()
	for i := 0; i < 8; i++ {
		buf[i] = byte(now >> (i * 8))
	}
	if _, err := rand.Read(buf[8:]); err != nil {
		for i := 8; i < 16; i++ {
			buf[i] = byte(now >> (i * 4))
		}
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func buildQuery(sessionID, tabID string) url.Values {
	fp := fingerprintFor(sessionID)
	q := url.Values{}
	q.Set("aid", assistantID)
	q.Set("device_id", fp.DeviceID)
	q.Set("web_id", fp.WebID)
	q.Set("tea_uuid", fp.TeaUUID)
	q.Set("web_tab_id", tabID)
	q.Set("device_platform", "web")
	q.Set("language", "zh")
	q.Set("pc_version", pcVersion)
	q.Set("pkg_type", "release_version")
	q.Set("real_aid", assistantID)
	q.Set("region", "CN")
	q.Set("samantha_web", "1")
	q.Set("sys_region", "CN")
	q.Set("use-olympus-account", "1")
	q.Set("version_code", versionCode)
	return q
}

func cookieValue(cookieHeader, name string) string {
	target := strings.ToLower(name)
	for _, part := range strings.Split(cookieHeader, ";") {
		idx := strings.Index(part, "=")
		if idx <= 0 {
			continue
		}
		if strings.ToLower(strings.TrimSpace(part[:idx])) == target {
			v, _ := url.QueryUnescape(strings.TrimSpace(part[idx+1:]))
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------- 站点域名

// 同一套 samantha 协议承载豆包（www.doubao.com）与 Dola（www.dola.com）两个站点，
// 账号池按站点取号后，通过 ctx 把站点域名传给底层请求函数；缺省仍是豆包。
type originCtxKey struct{}

func siteOrigin(site string) string {
	if NormalizeSite(site) == SiteDola {
		return "https://www.dola.com"
	}
	return doubaoOrigin
}

// WithOrigin 把站点域名注入 ctx（生成编排取号后调用）。
func WithOrigin(ctx context.Context, origin string) context.Context {
	if strings.TrimSpace(origin) == "" {
		return ctx
	}
	return context.WithValue(ctx, originCtxKey{}, origin)
}

func originFromCtx(ctx context.Context) string {
	if ctx != nil {
		if o, ok := ctx.Value(originCtxKey{}).(string); ok && strings.TrimSpace(o) != "" {
			return o
		}
	}
	return doubaoOrigin
}

func buildBrowserHeaders(ctx context.Context, cookieHeader string) http.Header {
	origin := originFromCtx(ctx)
	h := http.Header{}
	h.Set("Accept", "text/event-stream")
	h.Set("Accept-Language", "zh-CN,zh;q=0.9")
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Type", "application/json")
	h.Set("Cookie", cookieHeader)
	h.Set("Origin", origin)
	h.Set("Pragma", "no-cache")
	h.Set("Referer", origin+"/chat/")
	h.Set("Sec-Ch-Ua", secChUA)
	h.Set("Sec-Ch-Ua-Mobile", "?0")
	h.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	h.Set("Sec-Fetch-Dest", "empty")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Site", "same-origin")
	h.Set("User-Agent", userAgent)
	h.Set("Priority", "u=1, i")
	h.Set("Agw-Js-Conv", "str")
	if csrf := cookieValue(cookieHeader, "passport_csrf_token"); csrf != "" {
		h.Set("X-Tt-Passport-Csrf-Token", csrf)
	}
	return h
}

var httpClient = &http.Client{}

// ctxHTTPClient 遵循账号绑定的代理（ctx 注入）；未注入时与 httpClient 行为一致。
func ctxHTTPClient(ctx context.Context) *http.Client {
	return outbound.HTTPClientFromContext(ctx, 0)
}

// samanthaPost 发起一次 samantha 请求，返回完整响应文本（SSE 或 JSON）。
func samanthaPost(ctx context.Context, cookieHeader, apiPath string, body any, timeout time.Duration, tabID string, accept string) (string, error) {
	qs := buildQuery(cookieHeader, tabID).Encode()
	reqURL := originFromCtx(ctx) + apiPath + "?" + qs
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header = buildBrowserHeaders(ctx, cookieHeader)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := ctxHTTPClient(req.Context()).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	text, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return "", err
	}
	raw := string(text)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if block := detectBlockRaw(raw); block != nil {
			log.Printf("[doubao] HTTP %d %s 被识别为 %s（code=%d msg=%s）raw=%.500s",
				res.StatusCode, apiPath, block.Kind, block.Code, block.Message, raw)
			return raw, block
		}
		return raw, fmt.Errorf("豆包接口失败 HTTP %d: %.300s", res.StatusCode, raw)
	}
	return raw, nil
}

// ---------------------------------------------------------------- SSE 解析

// SSEEvent 保留事件名与 JSON body。
type SSEEvent struct {
	Name string
	Data map[string]any
	Raw  string
}

// ParseSSEEvents 解析 SSE 文本。豆包把事件类型放在 `event:` 行，JSON body 里没有。
func ParseSSEEvents(rawText string) []SSEEvent {
	var events []SSEEvent
	eventName := ""
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 && eventName == "" {
			return
		}
		data := strings.Join(dataLines, "\n")
		ev := SSEEvent{Name: eventName, Raw: data}
		if data != "" {
			var m map[string]any
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				ev.Data = m
			}
		}
		if ev.Data != nil || ev.Name != "" {
			events = append(events, ev)
		}
		eventName = ""
		dataLines = nil
	}
	for _, line := range strings.Split(rawText, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			flush()
		}
	}
	flush()
	return events
}

// ------------------------------------------------------------ 事件取值工具

func evNum(m map[string]any, key string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case string:
		if parsed, err := strconv.ParseInt(n, 10, 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func evStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func unwrapEventData(ev SSEEvent) map[string]any {
	if ev.Data == nil {
		return nil
	}
	ed, ok := ev.Data["event_data"]
	if !ok {
		return nil
	}
	switch v := ed.(type) {
	case map[string]any:
		return v
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(v), &m) == nil {
			return m
		}
	}
	return nil
}

var streamErrorReasons = map[int64]string{
	710022002: "当前服务访问频繁（顶点限流）",
	// 710022004 实为 shark_admin 内容安全验证（decision.type=verify，滑块/语义审核），
	// HTTP 直连无法代替完成，需该账号在豆包网页端过一次验证后才能继续生成。
	710022004: "豆包要求安全验证（710022004）：请用该账号登录 www.doubao.com 网页版，随便发一条消息并完成弹出的滑块/安全验证，然后回来重试",
	710012001: "登录态失效（login invalid）",
	710012000: "登录态失效（user invalid，Cookie 已过期）",
	710082041: "创作任务需要澄清确认",
	710020202: "请求参数被网关拒绝（接口路径或报文字段不合法）",
}

func classifyCode(code int64, msg string) string {
	if code == 710012000 || code == 710012001 {
		return FailKindSessionExpired
	}
	if quotaPatterns.MatchString(msg) {
		return FailKindQuotaExhausted
	}
	return FailKindRateLimited
}

// finalizeErrorMsg 上游错误文案对用户可见，英文占位（rate limited / block 等）
// 对用户无意义；已知错误码用中文指引覆盖，未知码保留原文。
func finalizeErrorMsg(code int64, msg string) string {
	reason := streamErrorReasons[code]
	hasCJK := strings.ContainsFunc(msg, func(r rune) bool { return r >= 0x4E00 && r <= 0x9FFF })
	if reason != "" && (msg == "" || !hasCJK) {
		return reason
	}
	if msg == "" {
		if reason != "" {
			return reason
		}
		return "上游返回未知错误"
	}
	return msg
}

// DetectStreamError 捕获 STREAM_ERROR 与 event_type=2005 两种错误形态。
func DetectStreamError(events []SSEEvent) *ClassifyError {
	for _, ev := range events {
		if ev.Name == "STREAM_ERROR" {
			code, _ := evNum(ev.Data, "error_code")
			msg := strings.TrimSpace(evStr(ev.Data, "error_msg"))
			log.Printf("[doubao] STREAM_ERROR code=%d msg=%s raw=%.500s", code, msg, ev.Raw)
			msg = finalizeErrorMsg(code, msg)
			return &ClassifyError{Kind: classifyCode(code, msg), Code: code, Message: msg}
		}
		if code, _ := evNum(ev.Data, "event_type"); code == 2005 {
			ed := unwrapEventData(ev)
			c := int64(0)
			msg := ""
			if ed != nil {
				c, _ = evNum(ed, "code")
				msg = strings.TrimSpace(evStr(ed, "message"))
				if detail, ok := ed["error_detail"].(map[string]any); ok {
					if c == 0 {
						c, _ = evNum(detail, "code")
					}
					if msg == "" {
						msg = strings.TrimSpace(evStr(detail, "message"))
					}
				}
			}
			if c == 0 && msg == "" {
				continue
			}
			log.Printf("[doubao] EVENT_2005 code=%d msg=%s raw=%.500s", c, msg, ev.Raw)
			msg = finalizeErrorMsg(c, msg)
			return &ClassifyError{Kind: classifyCode(c, msg), Code: c, Message: msg}
		}
	}
	return nil
}

// detectBlockRaw 从原始报文兜底识别风控/额度/失效信号。
func detectBlockRaw(raw string) *ClassifyError {
	if m := sessionExpiredPatterns.FindString(raw); m != "" {
		return &ClassifyError{Kind: FailKindSessionExpired, Message: "登录态失效（Cookie 已过期）"}
	}
	if m := quotaPatterns.FindString(raw); m != "" {
		return &ClassifyError{Kind: FailKindQuotaExhausted, Message: "额度已用完（" + m + "）"}
	}
	if m := rateLimitPatterns.FindString(raw); m != "" {
		return &ClassifyError{Kind: FailKindRateLimited, Message: "上游风控限流（" + m + "）"}
	}
	return nil
}

// detectBlock 综合事件流与原文识别阻断。
func detectBlock(events []SSEEvent, raw, text string) *ClassifyError {
	if err := DetectStreamError(events); err != nil {
		return err
	}
	blob := truncate(raw, 8000) + "\n" + text
	if sessionExpiredPatterns.MatchString(blob) {
		return &ClassifyError{Kind: FailKindSessionExpired, Message: "登录态失效（Cookie 已过期）"}
	}
	if m := quotaPatterns.FindString(blob); m != "" {
		return &ClassifyError{Kind: FailKindQuotaExhausted, Message: "额度已用完（" + m + "）"}
	}
	if m := rateLimitPatterns.FindString(blob); m != "" {
		return &ClassifyError{Kind: FailKindRateLimited, Message: "上游风控限流（" + m + "）"}
	}
	return nil
}

// ------------------------------------------------------------ 文本收集

func parseJSONObj(v any) map[string]any {
	if s, ok := v.(string); ok {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			return m
		}
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// CollectText 收集事件流中的文本回复。
func CollectText(events []SSEEvent) string {
	var b strings.Builder
	for _, ev := range events {
		ed := unwrapEventData(ev)
		var msg map[string]any
		if ed != nil {
			msg = parseJSONObj(ed["message"])
		}
		if msg == nil {
			msg = ev.Data
		}
		content := msg["content"]
		if m := parseJSONObj(content); m != nil {
			if t, ok := m["text"].(string); ok {
				b.WriteString(t)
			}
			continue
		}
		if t, ok := content.(string); ok {
			b.WriteString(t)
		}
	}
	return b.String()
}

// ------------------------------------------------------------ 文生图

var imageURLPattern = regexp.MustCompile(`https?://[^\s)"']+`)
var imageLikePattern = regexp.MustCompile(`(?i)(\.png|\.jpe?g|\.webp|byteimg|imagex|tos-cn)`)

// cleanImageURL 清洗图片地址上的水印痕迹：
//   - image_dld_watermark（下载版，带水印）→ image_pre_watermark（预览版，
//     无水印高清原图，与豆包前端「列表缩略图干净、下载才加水印」的行为一致）；
//     命中该规则时保留模板段（预览模板本身就是无水印版，剥掉反而可能失效）；
//   - 其余情况剥 ~tplv-*watermark* 模板段与 logo= 参数。
//   - image_ori_raw 本就是无水印原图，此清洗只作用于回退变体。
func cleanImageURL(imageURL string) string {
	if imageURL == "" {
		return imageURL
	}
	u := imageURL
	if strings.Contains(u, "image_dld_watermark") {
		u = strings.ReplaceAll(u, "image_dld_watermark", "image_pre_watermark")
		return u
	}
	u = tplvWatermarkPattern.ReplaceAllString(u, "")
	if strings.Contains(u, "logo=") {
		u = logoParamPattern.ReplaceAllString(u, "")
		u = strings.ReplaceAll(u, "?&", "?")
		u = strings.ReplaceAll(u, "&&", "&")
		if strings.HasSuffix(u, "?") || strings.HasSuffix(u, "&") {
			u = u[:len(u)-1]
		}
	}
	return u
}

func addImageURL(list *[]string, seen map[string]bool, raw, key string) {
	u := strings.ReplaceAll(raw, `\u0026`, "&")
	u = strings.ReplaceAll(u, "&amp;", "&")
	if !strings.HasPrefix(u, "http") {
		return
	}
	if !imageLikePattern.MatchString(u) {
		return
	}
	u = cleanImageURL(u)
	dedupe := u
	if key != "" {
		dedupe = key
	} else if idx := strings.Index(u, "?"); idx > 0 {
		dedupe = u[:idx]
	}
	if seen[dedupe] || seen[u] {
		return
	}
	seen[dedupe] = true
	seen[u] = true
	*list = append(*list, u)
}

// harvestCreationBlocks 递归收集 creation_block.creations 里的图片节点。
// 豆包会把生成结果包在 content.creation_block.creations / patch_op.patch_value
// 等多层结构里下发（无水印原图 image_ori_raw 挂在 creations[].image 上）；
// 这里不假设层级，任何带 creations 数组的节点都视为候选，与插件的
// harvest（walkJSON）扫描面保持一致。
func harvestCreationBlocks(node any, pickFn func(map[string]any, string), depth int) {
	if depth > 24 {
		return
	}
	switch v := node.(type) {
	case map[string]any:
		if creations, ok := v["creations"].([]any); ok {
			for _, it := range creations {
				c := parseJSONObj(it)
				if c == nil {
					continue
				}
				img := parseJSONObj(c["image"])
				if img == nil {
					continue
				}
				k, _ := img["key"].(string)
				pickFn(img, k)
			}
		}
		for _, vv := range v {
			harvestCreationBlocks(vv, pickFn, depth+1)
		}
	case []any:
		for _, item := range v {
			harvestCreationBlocks(item, pickFn, depth+1)
		}
	}
}

// CollectImages 从事件流提取图片地址（content_type 2010 data[] / 2074 creations[] / 文本兜底）。
func CollectImages(events []SSEEvent) []string {
	images := []string{}
	seen := map[string]bool{}
	pick := func(item map[string]any, key string) {
		// image_ori_raw 优先：无水印原图（插件 doubao-international 的提取路径），
		// 其后才是 image_ori / preview / thumb 等展示变体。
		for _, k := range []string{"image_ori_raw", "image_ori", "image_raw", "image_preview", "image_thumb"} {
			if img := parseJSONObj(item[k]); img != nil {
				if u, ok := img["url"].(string); ok {
					addImageURL(&images, seen, u, key)
				}
			}
		}
		if u, ok := item["url"].(string); ok {
			addImageURL(&images, seen, u, key)
		}
	}
	for _, ev := range events {
		// 补丁流兜底：creations 可能嵌在任何事件的任意层（patch_op / creation_block），
		// 不受 event_type/content_type 门控约束，去重由 seen 保证不重复收图。
		harvestCreationBlocks(ev.Data, pick, 0)
		if ct, ok := evNum(ev.Data, "event_type"); ok && ct != 2001 {
			continue
		}
		ed := unwrapEventData(ev)
		var msg map[string]any
		if ed != nil {
			msg = parseJSONObj(ed["message"])
		}
		if msg == nil {
			if ed != nil && (ed["content"] != nil || ed["content_block"] != nil) {
				msg = ed
			} else {
				continue
			}
		}
		ct, _ := evNum(msg, "content_type")
		content := msg["content"]
		if cm := parseJSONObj(content); cm != nil {
			switch ct {
			case 2010:
				if data, ok := cm["data"].([]any); ok {
					for _, it := range data {
						if item := parseJSONObj(it); item != nil {
							k, _ := item["key"].(string)
							pick(item, k)
						}
					}
				}
			case 2074:
				if creations, ok := cm["creations"].([]any); ok {
					for _, it := range creations {
						if c := parseJSONObj(it); c != nil {
							img := parseJSONObj(c["image"])
							if img != nil {
								k, _ := img["key"].(string)
								pick(img, k)
							}
							if u, ok := c["url"].(string); ok {
								addImageURL(&images, seen, u, "")
							}
						}
					}
				}
			}
			if t, ok := cm["text"].(string); ok {
				for _, u := range imageURLPattern.FindAllString(t, -1) {
					addImageURL(&images, seen, u, "")
				}
			}
			continue
		}
		if t, ok := content.(string); ok {
			for _, u := range imageURLPattern.FindAllString(t, -1) {
				addImageURL(&images, seen, u, "")
			}
		}
	}
	return images
}

// GenerateImageOnce 用固定 Cookie 发起一次文生图。
func GenerateImageOnce(ctx context.Context, cookieHeader, prompt, model, ratio, style string) ([]string, string, error) {
	return GenerateImageWithRefsOnce(ctx, cookieHeader, prompt, model, ratio, style, nil)
}

// GenerateImageWithRefsOnce 用固定 Cookie 发起一次图片生成；imageKeys 非空时把参考图
// 作为 attachments 一并提交（图生图 / 文字编辑 / 标注编辑 / 逐层图层拆分都收敛到这一条）。
//
// 事实边界：attachments=[{type:image,key:TOS uri}] 这套结构在 content_type=2020 的视频消息上
// 已实测可用（见 videoPayload / generateVideoOnce）。图片消息（content_type=2009）沿用同一结构，
// 属于同协议推断，尚未与真实上游联调；若上游忽略参考图，这里会以「已提交参考图但未回图」
// 的显式错误暴露，而不是静默产出与源图无关的新图。
func GenerateImageWithRefsOnce(ctx context.Context, cookieHeader, prompt, model, ratio, style string, imageKeys []string) ([]string, string, error) {
	if strings.TrimSpace(cookieHeader) == "" {
		return nil, "", errors.New("缺少 Cookie，请重新登录账号")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, "", errors.New("prompt 不能为空")
	}
	payload := imageGenerationPayload(prompt, model, ratio, style, imageKeys)
	tabID := randomUUID()
	raw, err := samanthaPost(ctx, cookieHeader, chatCompletionPath, payload, 5*time.Minute, tabID, "*/*")
	if err != nil {
		var ce *ClassifyError
		if errors.As(err, &ce) {
			return nil, "", ce
		}
		return nil, "", err
	}
	events := ParseSSEEvents(raw)
	text := CollectText(events)
	if block := detectBlock(events, raw, text); block != nil {
		return nil, "", block
	}
	images := CollectImages(events)
	if len(images) == 0 {
		// CollectText 偶尔带上 "{}" JSON 噪声，剥掉后再判断；模型明确回复时多为内容风控拒绝。
		clean := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "{}"))
		if clean != "" {
			return nil, clean, fmt.Errorf("豆包拒绝生成或未返回图片：%.200s（若提示词含版权角色/真人等敏感内容会被上游风控拒绝，请调整提示词重试）", clean)
		}
		if len(imageKeys) > 0 {
			return nil, "", fmt.Errorf("生图未返回图片（空响应）：已随请求提交 %d 张参考图，但上游没有回图（参考图通道可能未生效或图片被风控），请切换账号重试", len(imageKeys))
		}
		return nil, "", errors.New("生图未返回图片（空响应），可能被风控，请切换账号重试")
	}
	return images, text, nil
}

// imageGenerationPayload 构建 content_type=2009（SamanthaImageGenerationInput）消息。
// imageKeys 非空时写入 attachments：key 为 /samantha/pages/upload_image 返回的 TOS uri，
// 结构与 2020 视频消息的参考图完全一致（videoPayload 已在真实上游验证）。
func imageGenerationPayload(prompt, model, ratio, style string, imageKeys []string) map[string]any {
	if model == "" {
		model = "Seedream 4.5"
	}
	if ratio == "" {
		ratio = "1:1"
	}
	// 画布尺寸选择器可能保存像素格式（如 1824x1024 = 16:9 的 1K 档），
	// 豆包只认标准比例语义，先归一成 16:9 这类标准比例再写正文。
	ratio = normalizeDoubaoImageRatio(ratio)
	// 豆包无视结构化 ratio 字段，只认正文语义（与 videoPayload 同一结论），
	// 必须把比例以方位词形式写进正文才会生效；结构化 ratio 保留作双保险。
	textData := prompt
	if spec := ratioSpecText(ratio); spec != "" {
		textData = prompt + "，" + spec
	}
	contentData := map[string]any{
		"text": textData, "model": model, "template_type": "placeholder", "use_creation": false, "ratio": ratio,
	}
	if style != "" && style != "默认" {
		contentData["text"] = textData + "\n风格：" + style
	}
	attachments := make([]any, 0, len(imageKeys))
	for _, key := range imageKeys {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			attachments = append(attachments, map[string]any{"type": "image", "key": trimmed})
		}
	}
	return map[string]any{
		"messages": []any{map[string]any{
			"content":      mustJSON(contentData),
			"content_type": 2009,
			"attachments":  attachments,
			"references":   []any{},
			"skill": map[string]any{
				"skill_type": 3, "skill_type_no_default": 3, "skill_id": "3", "skill_id_no_default": "3",
			},
		}},
		"completion_option": map[string]any{
			"is_regen": false, "with_suggest": true, "need_create_conversation": true,
			"launch_stage": 1, "is_replace": false, "is_delete": false, "is_ai_playground": false,
			"memory_type": 2, "message_from": 0, "use_deep_think": false, "use_auto_cot": false,
			"resend_for_regen": false, "enable_commerce_credit": false, "action_bar_skill_id": 3,
		},
		"evaluate_option":       map[string]any{"web_ab_params": ""},
		"local_conversation_id": randomUUID(),
		"local_message_id":      randomUUID(),
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ------------------------------------------------------------ 文生视频

// VideoResult 文生视频结果。
type VideoResult struct {
	URLs    []string `json:"urls"`
	Covers  []string `json:"covers,omitempty"`
	Message string   `json:"message,omitempty"`
	TaskID  string   `json:"taskId,omitempty"`
	ConvID  string   `json:"conversationId,omitempty"`
	Account string   `json:"account,omitempty"`
	// WatermarkFree 表示 URLs[0] 已是服务端无水印母片（fallback_api 通道），
	// 下载入库后无需再做 delogo 水印遮盖。
	WatermarkFree bool `json:"watermarkFree,omitempty"`
}

func mapAbilityModel(model string) string {
	s := strings.TrimSpace(model)
	if s == "" {
		return "seedance_v2.0"
	}
	if strings.Contains(s, "seedance_v") {
		return s
	}
	if regexp.MustCompile(`(?i)2\.5`).MatchString(s) {
		return "seedance_v2.5"
	}
	if regexp.MustCompile(`(?i)1\.0|seedance_v1`).MatchString(s) {
		return "seedance_v1.0"
	}
	if regexp.MustCompile(`(?i)fast|mini`).MatchString(s) {
		return "seedance_v2.0_fast"
	}
	if strings.Contains(s, "2.0") {
		return "seedance_v2.0"
	}
	return s
}

// normalizeDoubaoImageRatio 把画布保存的 size（比例或像素尺寸）归一成豆包认的标准比例。
// 画布尺寸选择器对部分协议保存像素（如 1824x1024），豆包正文语义需要 16:9 这类标准比例。
func normalizeDoubaoImageRatio(value string) string {
	v := strings.TrimSpace(strings.ToLower(strings.ReplaceAll(value, "×", "x")))
	if v == "" || v == "auto" || strings.Contains(v, ":") {
		return v
	}
	parts := strings.Split(v, "x")
	if len(parts) != 2 {
		return ""
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return ""
	}
	type stdRatio struct {
		name string
		w, h int
	}
	standards := []stdRatio{
		{"1:1", 1, 1}, {"16:9", 16, 9}, {"9:16", 9, 16}, {"4:3", 4, 3}, {"3:4", 3, 4},
		{"3:2", 3, 2}, {"2:3", 2, 3}, {"21:9", 21, 9}, {"4:5", 4, 5}, {"5:4", 5, 4},
	}
	best := ""
	bestDiff := 0.02 // 2% 容差：1824x1024(1.78125) 命中 16:9(1.7778)
	for _, s := range standards {
		diff := math.Abs(float64(w*s.h)/float64(h*s.w) - 1)
		if diff < bestDiff {
			best = s.name
			bestDiff = diff
		}
	}
	if best != "" {
		return best
	}
	ow, oh := w, h
	divisor := w
	for h != 0 {
		divisor, h = h, divisor%h
	}
	if divisor < 1 {
		divisor = 1
	}
	return strconv.Itoa(ow/divisor) + ":" + strconv.Itoa(oh/divisor)
}

// ratioSpecText 把比例写成方位词（豆包无视结构化 ratio，只认正文语义）。
func ratioSpecText(ratio string) string {
	v := strings.TrimSpace(ratio)
	if v == "" || v == "auto" {
		return ""
	}
	parts := strings.SplitN(v, ":", 2)
	if len(parts) != 2 {
		return "比例 " + v
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w == 0 || h == 0 {
		return "比例 " + v
	}
	orient := "方形"
	if w > h {
		orient = "横屏"
	} else if h > w {
		orient = "竖屏"
	}
	return orient + " " + v
}

// videoPayload 构建 content_type=2020（SamanthaVideoGenerationInput）消息。
// imageKeys 非空时写入 attachments（图生视频参考图，key 为 /samantha/pages/upload_image 返回的 TOS uri）。
func videoPayload(prompt, model string, duration int, ratio, localConversationID, conversationID string, imageKeys []string) map[string]any {
	ratio = normalizeDoubaoImageRatio(ratio)
	spec := ratioSpecText(ratio)
	textPrompt := strings.TrimSpace(prompt)
	if spec != "" {
		textPrompt = textPrompt + "，" + spec
	}
	contentDict := map[string]any{"text": textPrompt}
	if ratio != "" && ratio != "auto" {
		contentDict["ratio"] = ratio
	}
	if m := mapAbilityModel(model); m != "" {
		contentDict["model"] = m
	}
	if duration > 0 {
		contentDict["duration"] = duration
	}
	attachments := make([]any, 0, len(imageKeys))
	for _, key := range imageKeys {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			attachments = append(attachments, map[string]any{"type": "image", "key": trimmed})
		}
	}
	needCreate := conversationID == ""
	launchStage := 1
	if conversationID != "" {
		launchStage = 0
	}
	payload := map[string]any{
		"messages": []any{map[string]any{
			"content":      mustJSON(contentDict),
			"content_type": 2020,
			"attachments":  attachments,
			"references":   []any{},
			"skill": map[string]any{
				"skill_type": 17, "skill_type_no_default": 17, "skill_id": "17", "skill_id_no_default": "17",
			},
		}},
		"completion_option": map[string]any{
			"is_regen": false, "with_suggest": true, "need_create_conversation": needCreate,
			"launch_stage": launchStage, "is_replace": false, "is_delete": false, "is_ai_playground": false,
			"memory_type": 2, "message_from": 0, "use_deep_think": false, "use_auto_cot": false,
			"resend_for_regen": false, "enable_commerce_credit": false, "action_bar_skill_id": 17,
		},
		"evaluate_option":       map[string]any{"web_ab_params": ""},
		"local_conversation_id": localConversationID,
		"local_message_id":      randomUUID(),
	}
	if conversationID != "" {
		payload["conversation_id"] = conversationID
		if co := parseJSONObj(payload["completion_option"]); co != nil {
			co["conversation_id"] = conversationID
		}
		payload["ext"] = map[string]any{"sub_conv_firstmet_type": "0"}
	}
	return payload
}

// VideoLikeURL 判断 URL 是否像视频地址。
func VideoLikeURL(u string) bool {
	if u == "" || !strings.HasPrefix(u, "http") {
		return false
	}
	if regexp.MustCompile(`(?i)\.(png|jpe?g|webp|gif)(~|\?|$)`).MatchString(u) {
		return false
	}
	return regexp.MustCompile(`(?i)(\.mp4|\.m3u8|/video/|video)`).MatchString(u)
}

type videoMeta struct {
	URL      string
	Cover    string
	Width    int
	Height   int
	Duration int
}

func decodeMaybeB64URL(v any) string {
	s, ok := v.(string)
	if !ok || len(s) < 16 {
		return ""
	}
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		if u := string(decoded); strings.HasPrefix(u, "http") {
			return u
		}
	}
	return ""
}

// CollectVideos 从事件流提取视频（content_type 2021 或 URL 特征兜底）。
func CollectVideos(events []SSEEvent) []videoMeta {
	videos := []videoMeta{}
	seen := map[string]bool{}
	add := func(m videoMeta) {
		if !VideoLikeURL(m.URL) || seen[m.URL] {
			return
		}
		seen[m.URL] = true
		videos = append(videos, m)
	}
	for _, ev := range events {
		if ct, ok := evNum(ev.Data, "event_type"); ok && ct != 2001 {
			continue
		}
		ed := unwrapEventData(ev)
		var msg map[string]any
		if ed != nil {
			msg = parseJSONObj(ed["message"])
		}
		if msg == nil {
			if ed != nil && (ed["content"] != nil || ed["content_block"] != nil) {
				msg = ed
			} else {
				continue
			}
		}
		if ct, ok := evNum(msg, "content_type"); ok && ct != 2021 {
			continue
		}
		content := parseJSONObj(msg["content"])
		if content == nil {
			continue
		}
		items, ok := content["data"].([]any)
		if !ok {
			items = []any{content}
		}
		for _, it := range items {
			item := parseJSONObj(it)
			if item == nil {
				continue
			}
			videoURL, _ := item["video_url"].(string)
			if videoURL == "" {
				videoURL, _ = item["url"].(string)
			}
			if videoURL == "" {
				if vm := parseJSONObj(item["video_model"]); vm != nil {
					if vl, ok := vm["video_list"].(map[string]any); ok {
						for _, vinfo := range vl {
							if vi := parseJSONObj(vinfo); vi != nil {
								if u := decodeMaybeB64URL(vi["main_url"]); u != "" {
									videoURL = u
									break
								}
							}
						}
					}
				}
			}
			cover, _ := item["cover_url"].(string)
			w, _ := evNum(item, "width")
			h, _ := evNum(item, "height")
			d, _ := evNum(item, "duration")
			add(videoMeta{URL: videoURL, Cover: cover, Width: int(w), Height: int(h), Duration: int(d)})
		}
	}
	return videos
}

var looseVideoURLPattern = regexp.MustCompile(`https?:\\?/\\?/[^\s"'\\]+`)

// CollectVideosLoose 从原始报文兜底提取视频地址。
func CollectVideosLoose(raw string) []videoMeta {
	videos := []videoMeta{}
	seen := map[string]bool{}
	for _, u := range looseVideoURLPattern.FindAllString(raw, -1) {
		u = strings.ReplaceAll(u, `\/`, "/")
		u = strings.ReplaceAll(u, `\u0026`, "&")
		if !regexp.MustCompile(`(?i)(\.mp4|\.m3u8|/video/)`).MatchString(u) || seen[u] {
			continue
		}
		seen[u] = true
		videos = append(videos, videoMeta{URL: u})
	}
	return videos
}

// task id 候选键
var taskIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`"task_id"\s*:\s*"?(\d{6,})"?`),
	regexp.MustCompile(`"taskId"\s*:\s*"?(\d{6,})"?`),
	regexp.MustCompile(`"async_task_id"\s*:\s*"?(\d{6,})"?`),
	regexp.MustCompile(`"asyncTaskId"\s*:\s*"?(\d{6,})"?`),
}

func looksLikeTaskID(v string) bool {
	if len(v) < 6 {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ExtractTaskID 从事件流 / 原文中提取异步任务号（fin_reason.async_task.id 优先）。
func ExtractTaskID(events []SSEEvent, raw string) string {
	var walk func(node any) string
	walk = func(node any) string {
		switch v := node.(type) {
		case map[string]any:
			if fr := parseJSONObj(v["fin_reason"]); fr != nil {
				at := fr["async_task"]
				if at == nil {
					at = fr["asyncTask"]
				}
				if atm := parseJSONObj(at); atm != nil {
					for _, k := range []string{"id", "task_id"} {
						if id, ok := atm[k].(string); ok && looksLikeTaskID(id) {
							return id
						}
					}
					if id, ok := atm["id"].(float64); ok && looksLikeTaskID(fmt.Sprintf("%.0f", id)) {
						return fmt.Sprintf("%.0f", id)
					}
				}
			}
			if at := parseJSONObj(v["async_task"]); at != nil {
				if id, ok := at["id"].(string); ok && looksLikeTaskID(id) {
					return id
				}
			}
			if tasks, ok := v["tasks"].([]any); ok {
				for _, t := range tasks {
					if tm := parseJSONObj(t); tm != nil {
						if id, ok := tm["id"].(string); ok && looksLikeTaskID(id) {
							return id
						}
					}
				}
			}
			for _, k := range []string{"task_id", "taskId", "async_task_id", "asyncTaskId", "video_task_id", "gen_task_id"} {
				if s, ok := v[k].(string); ok && looksLikeTaskID(s) {
					return s
				}
			}
			// map 遍历不稳定，但只要存在唯一 task id 就能找到
			for _, vv := range v {
				if hit := walk(vv); hit != "" {
					return hit
				}
			}
		case []any:
			for _, item := range v {
				if hit := walk(item); hit != "" {
					return hit
				}
			}
		}
		return ""
	}
	for _, ev := range events {
		if hit := walk(ev.Data); hit != "" {
			return hit
		}
	}
	for _, re := range taskIDPatterns {
		if m := re.FindStringSubmatch(raw); m != nil && looksLikeTaskID(m[1]) {
			return m[1]
		}
	}
	return ""
}

// walkFindString 递归找指定键的字符串值。
func walkFindString(node any, keys ...string) string {
	switch v := node.(type) {
	case map[string]any:
		for _, k := range keys {
			if s, ok := v[k].(string); ok && s != "" {
				return s
			}
		}
		for _, vv := range v {
			if hit := walkFindString(vv, keys...); hit != "" {
				return hit
			}
		}
	case []any:
		for _, item := range v {
			if hit := walkFindString(item, keys...); hit != "" {
				return hit
			}
		}
	}
	return ""
}

func isConfirmAsk(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || isAcceptance(t) {
		return false
	}
	if confirmAskExplicit.MatchString(t) {
		return true
	}
	return (strings.Contains(t, "确认") || strings.Contains(t, "确定好") || strings.Contains(t, "定下来")) &&
		regexp.MustCompile(`生成|参数|比例|时长|画幅|分辨率`).MatchString(t)
}

func isAcceptance(text string) bool {
	if hardFailurePatterns.MatchString(text) {
		return false
	}
	return acceptancePatterns.MatchString(text)
}

// ------------------------------------------------------------ 会话解析与结果抓取

type threadInfo struct {
	ID       string
	UpdateMs int64
	Type     int64
}

func listThreads(ctx context.Context, cookieHeader, tabID string) []threadInfo {
	raw, err := samanthaPost(ctx, cookieHeader, threadListPath, map[string]any{}, 30*time.Second, tabID, "application/json")
	if err != nil {
		return nil
	}
	var parsed struct {
		Data struct {
			ThreadList []struct {
				Conversation struct {
					ConversationID   string  `json:"conversation_id"`
					UpdateTime       float64 `json:"update_time"`
					ConversationType float64 `json:"conversation_type"`
				} `json:"conversation"`
				// thread_id 上游返回的是 JSON 数字（超出 int32），必须用 Number/string 接，
				// 否则整包 Unmarshal 类型错误 → 会话永远解析不到（自动确认随之失效）。
				ThreadID    json.Number `json:"thread_id"`
				ThreadIDStr string      `json:"thread_id_str"`
			} `json:"thread_list"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		log.Printf("[doubao] thread/list 解析失败：%v raw=%.300s", err, raw)
		return nil
	}
	out := []threadInfo{}
	for _, t := range parsed.Data.ThreadList {
		id := t.Conversation.ConversationID
		if id == "" {
			id = t.ThreadIDStr
		}
		if id == "" {
			id = t.ThreadID.String()
		}
		if id == "" || id == "0" {
			continue
		}
		out = append(out, threadInfo{ID: id, UpdateMs: int64(t.Conversation.UpdateTime * 1000), Type: int64(t.Conversation.ConversationType)})
	}
	return out
}

// pickFreshThreadID 找出本次提交刚创建/更新的会话（strict 时不回退旧会话）。
func pickFreshThreadID(threads []threadInfo, sinceMs int64, strict bool) string {
	if len(threads) == 0 {
		return ""
	}
	var fresh, pool []threadInfo
	for _, t := range threads {
		if t.UpdateMs >= sinceMs-5000 {
			fresh = append(fresh, t)
		}
	}
	if strict && len(fresh) == 0 {
		return ""
	}
	pool = fresh
	if len(pool) == 0 {
		pool = threads
	}
	// 更新时间倒排，优先「AI 创作」会话（type=5）
	best := pool[0]
	for _, t := range pool {
		if t.UpdateMs > best.UpdateMs {
			best = t
		}
	}
	for _, t := range pool {
		if t.Type == 5 && t.UpdateMs >= best.UpdateMs-60000 {
			return t.ID
		}
	}
	return best.ID
}

func fetchConversationPage(ctx context.Context, cookieHeader, conversationID, tabID string) (string, error) {
	qs := buildQuery(cookieHeader, tabID).Encode()
	reqURL := fmt.Sprintf("%s/chat/%s?%s", originFromCtx(ctx), url.PathEscape(conversationID), qs)
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header = buildBrowserHeaders(ctx, cookieHeader)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Del("Origin")
	res, err := ctxHTTPClient(req.Context()).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", nil
	}
	text, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return "", err
	}
	return string(text), nil
}

// extractSSRJSON 从页面内嵌 _ROUTER_DATA 里配平解析出大 JSON。
func extractSSRJSON(html string) map[string]any {
	for _, marker := range []string{"window._ROUTER_DATA", "_ROUTER_DATA", "window.__INITIAL_STATE__", "_SSR_DATA", "__ROUTE_DATA__"} {
		idx := strings.Index(html, marker)
		if idx < 0 {
			continue
		}
		start := strings.Index(html[idx:], "{")
		if start < 0 {
			continue
		}
		start += idx
		depth, inStr, esc := 0, false, false
		for i := start; i < len(html) && i < start+30_000_000; i++ {
			ch := html[i]
			if inStr {
				if esc {
					esc = false
				} else if ch == '\\' {
					esc = true
				} else if ch == '"' {
					inStr = false
				}
				continue
			}
			switch ch {
			case '"':
				inStr = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					var m map[string]any
					if json.Unmarshal([]byte(html[start:i+1]), &m) == nil {
						return m
					}
					return nil
				}
			}
		}
	}
	return nil
}

func collectVideoNodes(node any, out *[]map[string]any, depth int) {
	// json.Unmarshal 产物是树（无环），无需按节点去重——map 节点不可哈希，
	// 用 map[any]bool 记录访问会直接 panic（hash of unhashable type）。
	if depth > 64 {
		return
	}
	switch v := node.(type) {
	case map[string]any:
		if v["download_url"] != nil || v["video_list"] != nil || v["video_model"] != nil || v["main_url"] != nil || v["backup_url_1"] != nil {
			*out = append(*out, v)
		}
		for _, vv := range v {
			collectVideoNodes(vv, out, depth+1)
		}
	case []any:
		for _, item := range v {
			collectVideoNodes(item, out, depth+1)
		}
	}
}

// Go RE2 重复上限 1000；URL 超长场景由 collectVideoNodes 的 SSR 解析兜底。
var pageDownloadURLPattern = regexp.MustCompile(`"download_url":"(https:[^"]{20,1000})"`)
var pageMainURLPattern = regexp.MustCompile(`"(?:main_url|backup_url_1|backup_url_2)":"(https:[^"]{20,1000})"`)

// preferWatermarkFree 同一视频常同时带水印/无水印两个变体 URL；
// 有干净变体时只返回干净变体，避免把带水印版当结果。
// 判定复用 nomark.go 的水印特征（含 lr=video_gen_watermark_dyn）。
func preferWatermarkFree(list []videoMeta) []videoMeta {
	clean := make([]videoMeta, 0, len(list))
	for _, m := range list {
		if !isLikelyMarkedURL(m.URL, "") {
			clean = append(clean, m)
		}
	}
	if len(clean) > 0 {
		return clean
	}
	return list
}

// ExtractVideosFromPage 从会话页面 SSR 数据/正则兜底提取视频。
// 节点内多个 URL 变体按无水印优先评分选主，其余变体仍加入结果用于「已见过」标记覆盖。
func ExtractVideosFromPage(html string) []videoMeta {
	videos := []videoMeta{}
	seen := map[string]bool{}
	add := func(m videoMeta) {
		if !VideoLikeURL(m.URL) || seen[m.URL] {
			return
		}
		seen[m.URL] = true
		videos = append(videos, m)
	}
	str := func(node map[string]any, key string) string {
		s, _ := node[key].(string)
		return s
	}
	if html != "" {
		if ssr := extractSSRJSON(html); ssr != nil {
			var nodes []map[string]any
			collectVideoNodes(ssr, &nodes, 0)
			for _, node := range nodes {
				w, _ := evNum(node, "width")
				h, _ := evNum(node, "height")
				d, _ := evNum(node, "duration")
				meta := videoMeta{Width: int(w), Height: int(h), Duration: int(d)}

				type cand struct {
					key string
					url string
				}
				var cands []cand
				push := func(key, u string) {
					if u = strings.TrimSpace(u); u != "" {
						cands = append(cands, cand{key, u})
					}
				}
				push("no_watermark_url", str(node, "no_watermark_url"))
				push("original_url", str(node, "original_url"))
				if om := parseJSONObj(node["original_media_info"]); om != nil {
					push("main_url", str(om, "main_url"))
				}
				push("main_url", str(node, "main_url"))
				push("play_url", str(node, "play_url"))
				push("download_url", str(node, "download_url"))
				collectListURLs := func(raw any) {
					vl, ok := raw.(map[string]any)
					if !ok {
						return
					}
					for _, vinfo := range vl {
						vi := parseJSONObj(vinfo)
						if vi == nil {
							continue
						}
						if u := decodeMaybeB64URL(vi["main_url"]); u != "" {
							push("video_list.main_url", u)
							continue
						}
						push("video_list.main_url", str(vi, "main_url"))
					}
				}
				if vm := parseJSONObj(node["video_model"]); vm != nil {
					collectListURLs(vm["video_list"])
				}
				collectListURLs(node["video_list"])

				// 候选按无水印优先级顺序 push；先取首个不带水印特征的，
				// 全部带水印时取首个（download_url 兜底，generate 侧还有 nomark 升级）。
				best := ""
				for _, c := range cands {
					if !isLikelyMarkedURL(c.url, c.key) {
						best = c.url
						break
					}
					if best == "" {
						best = c.url
					}
				}
				// 其余变体也加入：轮询前用整页结果构建「已见过」集合时覆盖所有变体，
				// 防止旧视频的水印变体被误判为新片。
				for _, c := range cands {
					if c.url != best {
						add(videoMeta{URL: c.url, Width: meta.Width, Height: meta.Height, Duration: meta.Duration})
					}
				}
				meta.URL = best
				add(meta)
			}
			if len(videos) > 0 {
				return videos
			}
		}
	}
	flat := strings.ReplaceAll(html, `\u002F`, "/")
	flat = strings.ReplaceAll(flat, `\/`, "/")
	for _, re := range []*regexp.Regexp{pageDownloadURLPattern, pageMainURLPattern} {
		for _, m := range re.FindAllStringSubmatch(flat, -1) {
			add(videoMeta{URL: strings.ReplaceAll(m[1], `\"`, `"`)})
		}
	}
	return videos
}
