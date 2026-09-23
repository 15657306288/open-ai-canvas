package doubao

// Dola 浏览器借道中继（2026-09-20 实测定稿）：
//   - dola.com 的 completion 接口有 mssdk 签名+浏览器指纹风控，Go 直连 HTTP 必被
//     Akamai 边缘 30s 504（errors.edgesuite.net）或滑块拦截；唯一可行架构 =
//     rod 常驻真实浏览器，把请求放进页面上下文发 fetch（签名与指纹由页面环境自带）。
//   - 协议与豆包 samantha 不同：顶级路径 /chat/completion（不是 /samantha/...），
//     aid=495671、region=JP&sys_region=JP；payload 是 client_meta + content_block
//     + option 结构（抓包 ground truth 见 %TEMP%/dola_probe5_result.json 结构）。
//   - 会话是「游客」时上游明确回「游客模式暂不支持生成图片和视频，请登录后再试」，
//     这里分类为登录失效，指引到账号池重新窗口登录（qrlogin 已支持 Dola）。
//   - dola 会话在浏览器侧使用后会轮换（sessionid_ss），每次任务结束把页面最新
//     Cookie 回存账号池（RefreshCookieHeader），避免下个任务变游客态。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

const dolaOrigin = "https://www.dola.com"

// 游客/风控/频控特征（上游中英文都会出现）。
var dolaGuestPatterns = regexp.MustCompile(`游客模式暂不支持生成|游客模式|Guest mode|log in to (generate|create)|please log in`)

// dolaRelayManager 常驻浏览器中继：同一时刻只跑一个任务（cookie 按账号切换）。
type dolaRelayManager struct {
	mu      sync.Mutex
	browser *rod.Browser
	page    *rod.Page
	pool    *Service
}

var dolaRelay = &dolaRelayManager{}

// dolaGenerateVideo 生成侧入口：GenerateVideoWithPool 里 dola 站账号走这里。
func dolaGenerateVideo(ctx context.Context, pool *Service, cred *ActiveCredential, req VideoRequest, duration int, ratio string) (*VideoResult, error) {
	if strings.TrimSpace(cred.CookieHeader) == "" {
		return nil, errors.New("缺少 Cookie，请重新登录 Dola 账号")
	}
	// 参考图上传是豆包 /samantha 专有通道，dola 协议不同：降级为纯文生视频。
	if len(req.RefImages) > 0 {
		log.Printf("[dola] 参考图 %d 张暂不支持上传（dola 协议不同），降级为纯文生视频", len(req.RefImages))
	}
	return dolaRelay.video(ctx, pool, cred, req.Prompt, req.Model, duration, ratio, req.OnLive)
}

func (m *dolaRelayManager) reset() {
	if m.browser != nil {
		_ = m.browser.Close()
	}
	m.browser = nil
	m.page = nil
}

// ensurePage 惰性启动常驻浏览器并复用页面；失败返回错误（下个任务重试拉起）。
func (m *dolaRelayManager) ensurePage() (*rod.Page, error) {
	if m.browser != nil && m.page != nil {
		return m.page, nil
	}
	bin := ""
	if cands := browserCandidates(); len(cands) > 0 {
		bin = cands[0]
	}
	if bin == "" {
		return nil, errors.New("本机未找到 Chrome / Edge，无法发起 Dola 浏览器中继")
	}
	profileDir := filepath.Join(os.TempDir(), "yingce-dola-relay")
	controlURL, err := launcher.New().
		Bin(bin).
		Headless(false).
		UserDataDir(profileDir).
		Set("--disable-blink-features", "AutomationControlled").
		Set("--window-size", "1280,900").
		Launch()
	if err != nil {
		return nil, fmt.Errorf("启动 Dola 中继浏览器失败：%w", err)
	}
	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("连接 Dola 中继浏览器失败：%w", err)
	}
	page, err := browser.Page(proto.TargetCreateTarget{URL: dolaOrigin + "/chat/"})
	if err != nil {
		_ = browser.Close()
		return nil, fmt.Errorf("打开 Dola 页面失败：%w", err)
	}
	m.browser = browser
	m.page = page
	return page, nil
}

// applyAccountCookies 清掉浏览器旧 Cookie 后注入指定账号的完整 Cookie 头。
func (m *dolaRelayManager) applyAccountCookies(page *rod.Page, cookieHeader string) error {
	_ = proto.NetworkClearBrowserCookies{}.Call(page)
	params := make([]*proto.NetworkCookieParam, 0, 24)
	for _, part := range strings.Split(cookieHeader, ";") {
		part = strings.TrimSpace(part)
		idx := strings.Index(part, "=")
		if idx <= 0 {
			continue
		}
		params = append(params, &proto.NetworkCookieParam{
			Name: part[:idx], Value: part[idx+1:], Domain: ".dola.com", Path: "/",
		})
	}
	if len(params) == 0 {
		return errors.New("账号 Cookie 无效（无可注入项）")
	}
	if err := m.browser.SetCookies(params); err != nil {
		return fmt.Errorf("注入 Cookie 失败：%w", err)
	}
	return nil
}

func (m *dolaRelayManager) video(ctx context.Context, pool *Service, cred *ActiveCredential, prompt, model string, duration int, ratio string, onLive func(LiveInfo)) (*VideoResult, error) {
	// 串行化：同一浏览器同一时刻只服务一个账号任务。
	m.mu.Lock()
	defer m.mu.Unlock()

	page, err := m.ensurePage()
	if err != nil {
		return nil, err
	}
	m.pool = pool
	if err := m.applyAccountCookies(page, cred.CookieHeader); err != nil {
		return nil, err
	}
	if err := page.Navigate(dolaOrigin + "/chat/"); err != nil {
		m.reset()
		return nil, fmt.Errorf("打开 Dola 聊天页失败：%w", err)
	}
	if err := dolaWaitReady(ctx, page, 20*time.Second); err != nil {
		m.reset()
		return nil, err
	}

	if onLive != nil {
		onLive(LiveInfo{Label: cred.Label, Site: cred.Site})
	}

	// 页面上下文内发视频请求（签名/指纹由页面环境处理）。
	fireJS := dolaFireJS(cred.SessionID, prompt, model, duration, ratio)
	if _, err := page.Evaluate(&rod.EvalOptions{AwaitPromise: true, ByValue: true, JS: fireJS}); err != nil {
		m.reset()
		return nil, fmt.Errorf("Dola 页面请求注入失败：%w", err)
	}

	// 轮询收流（SSE 全文落在 window.__dolaSSE）。
	sse, err := m.collectSSE(ctx, page, 20*time.Minute)
	if err != nil {
		return nil, err
	}

	events := ParseSSEEvents(sse)
	text := dolaCollectText(events)
	convID := ""
	for _, ev := range events {
		if convID = walkFindString(ev.Data, "conversation_id"); convID != "" {
			break
		}
	}
	if onLive != nil && convID != "" {
		onLive(LiveInfo{Label: cred.Label, Site: cred.Site, ConversationURL: dolaOrigin + "/chat/" + convID})
	}

	// 游客态：明确指引重新登录（这正是 dola 账号池登录问题的上游信号）。
	if dolaGuestPatterns.MatchString(sse) || dolaGuestPatterns.MatchString(text) {
		return nil, &ClassifyError{Kind: FailKindSessionExpired, Message: "Dola 账号是游客登录态（Cookie 已失效），请在账号池对 Dola 账号点击「登录」重新窗口登录后再试"}
	}
	if block := detectBlock(events, sse, text); block != nil {
		return nil, block
	}

	videos := CollectVideosLoose(sse)
	if len(videos) == 0 {
		videos = CollectVideos(events)
	}
	if len(videos) == 0 {
		videos = dolaHarvestCreationVideos(events)
	}

	// 提交流里没直接出片：轮询会话页等异步产出（Seedance 出片可达 15 分钟+）。
	if len(videos) == 0 && convID != "" {
		videos, text = m.pollConversationForVideos(ctx, page, convID, text)
	}
	if len(videos) == 0 {
		clean := strings.TrimSpace(text)
		if clean != "" {
			return nil, fmt.Errorf("Dola 未返回视频：%s", truncate(clean, 240))
		}
		return nil, errors.New("Dola 未返回视频（空响应），可能被风控，请重试或重新登录账号")
	}

	result := &VideoResult{URLs: metaURLs(preferWatermarkFree(videos)), Message: text, ConvID: convID}
	// Cookie 回存：浏览器会话可能已轮换 sessionid，写回账号池防止游客态漂移。
	m.syncCookiesBack(page, cred)
	return result, nil
}

// collectSSE 轮询页面里的 SSE 累积缓冲，流结束或超时返回全文。
func (m *dolaRelayManager) collectSSE(ctx context.Context, page *rod.Page, max time.Duration) (string, error) {
	deadline := time.Now().Add(max)
	lastLen, stable := 0, 0
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		res, err := page.Evaluate(&rod.EvalOptions{ByValue: true, JS: `() => JSON.stringify({done: !!window.__dolaDone, len: (window.__dolaSSE || '').length, status: window.__dolaStatus || 0})`})
		if err == nil && res != nil {
			var snap struct {
				Done   bool `json:"done"`
				Len    int  `json:"len"`
				Status int  `json:"status"`
			}
			if json.Unmarshal([]byte(res.Value.String()), &snap) == nil {
				if snap.Status >= 400 {
					return m.fetchSSE(page), nil
				}
				if snap.Done {
					return m.fetchSSE(page), nil
				}
				if snap.Len == lastLen && snap.Len > 0 {
					stable++
					if stable >= 8 { // 40s 无增长视为流挂起结束
						return m.fetchSSE(page), nil
					}
				} else {
					stable = 0
				}
				lastLen = snap.Len
			}
		} else if err != nil {
			// 页面可能已崩溃/被关闭
			m.reset()
			return "", fmt.Errorf("Dola 中继页面失联：%w", err)
		}
		sleepCtx(ctx, 5*time.Second)
	}
	return m.fetchSSE(page), nil
}

func (m *dolaRelayManager) fetchSSE(page *rod.Page) string {
	if res, err := page.Evaluate(&rod.EvalOptions{ByValue: true, JS: `() => window.__dolaSSE || ''`}); err == nil && res != nil {
		return res.Value.String()
	}
	return ""
}

// pollConversationForVideos 会话页轮询兜底：导航到会话页抓渲染出的视频地址。
func (m *dolaRelayManager) pollConversationForVideos(ctx context.Context, page *rod.Page, convID, lastText string) ([]videoMeta, string) {
	deadline := time.Now().Add(15 * time.Minute)
	rounds := 0
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, lastText
		}
		rounds++
		if err := page.Navigate(dolaOrigin + "/chat/" + convID); err == nil {
			sleepCtx(ctx, 8*time.Second)
			if html, err := page.HTML(); err == nil && html != "" {
				found := ExtractVideosFromPage(html)
				if len(found) > 0 {
					return found, lastText
				}
			}
		}
		log.Printf("[dola] 会话 %s 第 %d 轮未见到视频，继续等待", convID, rounds)
		sleepCtx(ctx, 15*time.Second)
	}
	return nil, lastText
}

// syncCookiesBack 任务结束把浏览器里的最新 Cookie 回存账号池。
func (m *dolaRelayManager) syncCookiesBack(page *rod.Page, cred *ActiveCredential) {
	if m.pool == nil {
		return
	}
	full, err := page.Cookies([]string{dolaOrigin})
	if err != nil || len(full) == 0 {
		return
	}
	header := buildCookieHeaderFromRod(full)
	if strings.TrimSpace(header) == "" || header == strings.TrimSpace(cred.CookieHeader) {
		return
	}
	if err := m.pool.RefreshCookieHeader(cred.ID, header); err != nil {
		log.Printf("[dola] Cookie 回存失败（账号 %s）：%v", cred.Label, err)
	} else {
		log.Printf("[dola] 账号 %s Cookie 已回存（浏览器会话发生轮换）", cred.Label)
	}
}

// ------------------------------------------------------------ payload / JS

// dolaVideoPayload 构造 dola content_block 协议 payload（对照 2026-09-20 抓包模板，
// 字段名/结构勿随意增删；视频由 chat_ability ability_type=17 触发）。
func dolaVideoPayload(sessionID, prompt, model string, duration int, ratio, conversationID string) map[string]any {
	displayText := "生成视频：" + strings.TrimSpace(prompt)
	if spec := ratioSpecText(normalizeDoubaoImageRatio(ratio)); spec != "" {
		displayText += "，" + spec
	}
	abilityParam := mustJSON(map[string]any{
		"model":             mapAbilityModel(model),
		"duration":          duration,
		"input_box_content": displayText,
	})
	block := map[string]any{
		"block_type":    10000,
		"content":       map[string]any{"text_block": map[string]any{"text": displayText, "icon_url": "", "icon_url_dark": "", "summary": ""}, "pc_event_block": ""},
		"chat_ability":  map[string]any{"ability_type": 17, "ability_param": abilityParam},
		"block_id":      randomUUID(),
		"parent_id":     "",
		"meta_info":     []any{},
		"append_fields": []any{},
	}
	needCreate := conversationID == ""
	clientMeta := map[string]any{
		"local_conversation_id": fmt.Sprintf("local_%d", randomLocalConvID()),
		"conversation_id":       conversationID,
		"bot_id":                "7339470689562525703",
		"last_section_id":       "",
		"last_message_index":    nil,
		"local_permissions": []any{
			map[string]any{"permission_name": "ACCESS_COARSE_LOCATION", "status": 3},
			map[string]any{"permission_name": "ACCESS_FINE_LOCATION", "status": 3},
			map[string]any{"permission_name": "ACCESS_BACKGROUND_LOCATION", "status": 3},
		},
	}
	if conversationID != "" {
		clientMeta["conversation_id"] = conversationID
		clientMeta["last_section_id"] = conversationID
	}
	option := map[string]any{
		"send_message_scene": "", "create_time_ms": time.Now().UnixMilli(), "collect_id": "",
		"is_audio": false, "answer_with_suggest": false, "agent_mode": 2, "tts_switch": false,
		"need_deep_think": 0, "click_clear_context": false, "from_suggest": false,
		"is_regen": false, "is_replace": false, "is_from_click_option": false,
		"is_from_click_softlink": false, "disable_sse_cache": false, "select_text_action": "",
		"is_select_text": false, "resend_for_regen": false, "scene_type": 0,
		"unique_key": randomUUID(), "start_seq": 0, "need_create_conversation": needCreate,
		"conversation_init_option": map[string]any{"need_ack_conversation": true},
		"conversation_init_ext":    map[string]any{"model_item_key": "0"},
		"regen_query_id":           []any{}, "edit_query_id": []any{}, "regen_instruction": "",
		"no_replace_for_regen": false, "message_from": 0, "shared_app_name": "", "shared_app_id": "",
		"sse_recv_event_options": map[string]any{"support_chunk_delta": true},
		"is_ai_playground":       false, "is_old_user": false,
		"recovery_option":      map[string]any{"is_recovery": false, "req_create_time_sec": time.Now().Unix(), "append_sse_event_scene": 0},
		"message_storage_type": 0, "related_deleted_message_ids": map[string]any{}, "connector_info_list": []any{},
		"model_config":     map[string]any{"model_item_key": "0", "model_extra_params": map[string]any{}},
		"aggregate_params": map[string]any{"conversation_mode": "", "mode_id": "", "model_item_key": "0", "agent_mode": "2", "reasoning_effort": "", "provider_id": ""},
	}
	return map[string]any{
		"client_meta": clientMeta,
		"messages": []any{map[string]any{
			"local_message_id": randomUUID(),
			"content_block":    []any{block},
			"message_status":   0,
		}},
		"option":       option,
		"user_context": []any{},
	}
}

// dolaFireJS 生成页面内执行的点火脚本：自带流式读取，SSE 全文累积在 window.__dolaSSE。
func dolaFireJS(sessionID, prompt, model string, duration int, ratio string) string {
	payload := dolaVideoPayload(sessionID, prompt, model, duration, ratio, "")
	return fmt.Sprintf(`async () => {
		const payload = %s;
		const uuid = () => 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, c => {
			const r = Math.random() * 16 | 0; return (c === 'x' ? r : (r & 0x3 | 0x8)).toString(16);
		});
		const rid = String(Math.floor(7600000000000000000 + Math.random() * 200000000000000000));
		const fp = 'verify_' + uuid().slice(0, 5) + '_' + uuid().replace(/-/g, '').slice(0, 8) + '_' + uuid().replace(/-/g, '').slice(0, 4) + '_' + uuid().replace(/-/g, '').slice(0, 4) + '_' + uuid().replace(/-/g, '').slice(0, 4) + '_' + uuid().replace(/-/g, '').slice(0, 12);
		const qs = 'aid=495671&device_id=' + rid + '&device_platform=web&doubao_device_platform=web&doubao_pc_version=3.36.11&fp=' + fp +
			'&language=zh&pc_version=3.36.11&pkg_type=release_version&real_aid=495671&region=JP&samantha_web=1&sys_region=JP&tea_uuid=' + rid +
			'&tz_name=Asia%%2FShanghai&use-olympus-account=1&version_code=20800&web_id=' + rid + '&web_platform=browser&web_tab_id=' + uuid();
		window.__dolaSSE = ''; window.__dolaDone = false; window.__dolaStatus = 0;
		(async () => {
			try {
				const r = await fetch('/chat/completion?' + qs, {
					method: 'POST', headers: {'Content-Type': 'application/json'},
					body: JSON.stringify(payload), credentials: 'include'});
				window.__dolaStatus = r.status;
				if (!r.body) { window.__dolaSSE += 'NO_BODY'; window.__dolaDone = true; return; }
				const reader = r.body.getReader();
				const dec = new TextDecoder();
				for (;;) {
					const {done, value} = await reader.read();
					if (done) break;
					window.__dolaSSE += dec.decode(value, {stream: true});
					if (window.__dolaSSE.length > 8000000) break;
				}
			} catch (e) { window.__dolaSSE += '\n__FETCH_ERROR__:' + String(e); }
			window.__dolaDone = true;
		})();
		return 'FIRED';
	}`, mustJSON(payload))
}

// ------------------------------------------------------------ 小工具

func dolaWaitReady(ctx context.Context, page *rod.Page, max time.Duration) error {
	// SPA 心跳请求永不静止（WaitStable 会无限等待），用 readyState + 固定缓冲。
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if res, err := page.Evaluate(&rod.EvalOptions{ByValue: true, JS: `() => document.readyState`}); err == nil && res != nil && res.Value.String() == "complete" {
			time.Sleep(4 * time.Second) // SPA 水合缓冲
			return nil
		}
		sleepCtx(ctx, 800*time.Millisecond)
	}
	return errors.New("Dola 页面加载超时")
}

// dolaCollectText 收 STREAM_MSG_NOTIFY 里的 assistant 文本（dola 内容结构与豆包不同）。
func dolaCollectText(events []SSEEvent) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Data == nil {
			continue
		}
		content := parseJSONObj(ev.Data["content"])
		if content == nil {
			continue
		}
		if inner := parseJSONObj(content["content"]); inner != nil {
			if t, ok := inner["text"].(string); ok {
				b.WriteString(t)
			}
		}
	}
	return b.String()
}

// dolaHarvestCreationVideos 从 creation/asset 节点兜底收视频（dola 产物结构未完全固定，
// 这里做宽口径收集，误收由 VideoLikeURL 过滤）。
func dolaHarvestCreationVideos(events []SSEEvent) []videoMeta {
	var out []videoMeta
	seen := map[string]bool{}
	add := func(u string) {
		u = strings.ReplaceAll(u, `\u0026`, "&")
		u = strings.ReplaceAll(u, `\/`, "/")
		if !VideoLikeURL(u) || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, videoMeta{URL: u})
	}
	var walk func(node any, depth int)
	walk = func(node any, depth int) {
		if depth > 20 {
			return
		}
		switch v := node.(type) {
		case map[string]any:
			for _, k := range []string{"video_url", "main_url", "download_url", "play_url", "no_watermark_url", "original_url"} {
				if s, ok := v[k].(string); ok && strings.HasPrefix(s, "http") {
					add(s)
				}
			}
			for _, vv := range v {
				walk(vv, depth+1)
			}
		case []any:
			for _, item := range v {
				walk(item, depth+1)
			}
		}
	}
	for _, ev := range events {
		walk(ev.Data, 0)
	}
	return out
}

func randomLocalConvID() int64 {
	return time.Now().UnixNano() % 1e15
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
