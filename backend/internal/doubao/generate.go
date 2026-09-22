package doubao

// 账号池驱动的生成编排：取号 → 生成 → 失败标记并自动切换下一账号重试。

import (
	"context"
	"errors"
	"fmt"
	"infinite-canvas/backend/internal/outbound"
	"log"
	"math/rand"
	"strings"
	"time"
)

const maxAccountAttempts = 5

// poolError 区分「账号问题（可切换重试）」与「请求/协议问题（换号无意义）」。
type poolError struct {
	classified *ClassifyError // 非 nil 表示是账号类失败
	err        error
}

func (e *poolError) Error() string {
	if e.classified != nil {
		return e.classified.Message
	}
	return e.err.Error()
}

// ImageRequest 文生图入参。
type ImageRequest struct {
	Prompt string
	Model  string
	Ratio  string
	Style  string
	// RefImages 非空时走图生图：参考图按当次取到的账号 Cookie 上传后随请求提交。
	RefImages []RefImage
	// OnCredential 可选：取号成功后的回调，用于把「当前使用的账号」上报给任务面板。
	OnCredential func(cred ActiveCredential)
}

// ImageResult 文生图结果。
type ImageResult struct {
	URLs    []string `json:"urls"`
	Account string   `json:"account"`
	Text    string   `json:"text,omitempty"`
}

// RefImage 图生视频参考图（原片帧）：提交前按取到账号的 Cookie 上传。
type RefImage struct {
	Filename string
	Data     []byte
}

// VideoRequest 视频生成入参。Site 非空时只从该站点取号
// （doubao / dola，即梦协议不同不参与视频生成），空则跨站取号。
// RefImages 非空时走图生视频：参考图上传失败会降级为纯文生视频（不阻塞任务）。
type VideoRequest struct {
	Prompt    string
	Model     string
	Duration  int
	Ratio     string
	Site      string
	RefImages []RefImage
	// OnLive 可选：取号后 / 认出会话后回调，供任务面板实时展示当前账号
	// 与生成对话直链（豆包 / Dola 网页会话页）。可能被调用多次（换号重试时
	// 会以新账号再次回调），以最后一次为准。
	OnLive func(LiveInfo)
}

// LiveInfo 是一次视频生成进行中的账号实时信息；ConversationURL 为空表示
// 尚未认出本次会话（提交后 thread/list 认出会话才会填充）。
type LiveInfo struct {
	Label           string `json:"account,omitempty"`
	Site            string `json:"site,omitempty"`
	ConversationURL string `json:"conversationUrl,omitempty"`
}

// pickCredential 走 doubao2API 式调度取号（Acquire 内部处理并发上限、
// 同账号间隔 + 抖动；必要时等待，尊重 ctx 取消）。
// site 非空时锁定站点取号，空则按豆包优先跨站。
func pickCredential(ctx context.Context, s *Service, site, preferID string) (*ActiveCredential, error) {
	return s.Acquire(ctx, site, preferID)
}

// pickWithRetry 取号增强：账号都在短冷却（≤75s）时等冷却恢复再取一次，
// 避免后台任务因一分钟的冷却直接失败。
func pickWithRetry(ctx context.Context, s *Service, prefer string, pickFn func(string) (*ActiveCredential, error)) (*ActiveCredential, error) {
	cred, err := pickFn(prefer)
	if err == nil {
		return cred, nil
	}
	if remain, ok := s.ShortestCooldown(); ok && remain <= 75*time.Second {
		select {
		case <-time.After(remain + 1500*time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if next, retryErr := pickFn(prefer); retryErr == nil {
			return next, nil
		}
	}
	return nil, err
}

func classifyOnce(err error) *poolError {
	var ce *ClassifyError
	if errors.As(err, &ce) {
		return &poolError{classified: ce}
	}
	return &poolError{err: err}
}

// GenerateImageWithPool 从账号池取号生成图片，账号类失败自动切换下一账号。
func GenerateImageWithPool(ctx context.Context, s *Service, req ImageRequest) (*ImageResult, error) {
	return generateImageAttemptsWithPool(ctx, s, req, maxAccountAttempts)
}

// generateImageAttemptsWithPool 是「取号 → 生成 → 账号类失败切号」的公共实现。
// 文生图、图生图（RefImages 非空）与逐层图层拆分都收敛到这里，避免各自维护一套重试语义。
func generateImageAttemptsWithPool(ctx context.Context, s *Service, req ImageRequest, attempts int) (*ImageResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("prompt 不能为空")
	}
	if attempts < 1 {
		attempts = 1
	}
	prefer := ""
	var lastErr error = errors.New("账号池中没有可用账号")
	for attempt := 0; attempt < attempts; attempt++ {
		cred, err := pickWithRetry(ctx, s, prefer, func(p string) (*ActiveCredential, error) { return pickCredential(ctx, s, SiteDoubao, p) })
		if err != nil {
			// 已有真实上游失败时保留原始错误，不被「无号可取」覆盖。
			if attempt > 0 {
				return nil, fmt.Errorf("%s（账号池已无可用账号）", lastErr.Error())
			}
			return nil, err
		}
		// 按取到账号的站点切换域名（豆包 / Dola 同协议双站点）；代理按账号绑定注入本次请求。
		attemptCtx := WithOrigin(ctx, siteOrigin(cred.Site))
		if cred.ProxyURL != "" {
			attemptCtx = outbound.WithProxyURL(attemptCtx, cred.ProxyURL)
		}
		if req.OnCredential != nil {
			req.OnCredential(*cred)
		}
		// 参考图必须按「当次取到的账号」上传：TOS key 与账号 Cookie 绑定，
		// 换号重试时旧 key 对新账号无效（单张上传失败只跳过该张）。
		var refKeys []string
		if len(req.RefImages) > 0 {
			refKeys = uploadRefImages(attemptCtx, cred.CookieHeader, req.RefImages, "图片")
		}
		urls, text, err := GenerateImageWithRefsOnce(attemptCtx, cred.CookieHeader, req.Prompt, req.Model, req.Ratio, req.Style, refKeys)
		if err == nil {
			s.Release(cred.ID)
			_ = s.MarkSuccess(cred.ID)
			// SSE 下发的图片 URL 全部带水印模板（cthumb_wm1/cpreview_wm1/cdld_wm3），
			// 凭裸 key 走 get_file_url 通道换无水印原片直链；失败时原样返回。
			urls = UpgradeImagesNoWatermark(attemptCtx, cred.CookieHeader, urls)
			return &ImageResult{URLs: urls, Account: cred.Label, Text: text}, nil
		}
		pe := classifyOnce(err)
		lastErr = fmt.Errorf("账号 %s：%w", cred.Label, pe)
		log.Printf("[doubao] 账号 %s 图片生成失败（第 %d/%d 次尝试）：%v", cred.Label, attempt+1, attempts, pe)
		if pe.classified != nil {
			_, _, _ = s.MarkFailed(cred.ID, MarkFailedOptions{
				Kind: pe.classified.Kind, Message: pe.classified.Message,
			})
		}
		// 未分类错误（网络抖动等）同样换下一个账号重试。
		s.Release(cred.ID)
		prefer = "" // 让池子决定下一个可用账号
	}
	return nil, lastErr
}

// ------------------------------------------------------------ 逐层图层拆分

// layerAccountAttempts 单层允许的取号次数：层内失败先换号，仍失败则只损失该层。
const layerAccountAttempts = 3

// LayerImageRequest 逐层拆分入参。
// 上游（豆包 samantha 协议）没有「一次返回图层列表」的端点，能用的等价能力是
// 图生图：每层用各自的提示词各生成一张，因此单次任务被拆成 N 次受账号池调度的请求。
type LayerImageRequest struct {
	// Prompts 每层的提示词（长度即层数），由调用方用 LayerPrompts 生成。
	Prompts []string
	Model   string
	Ratio   string
	Style   string
	// OnLive 可选：每层取号后回调当前账号（任务面板展示正在使用的账号）。
	OnLive func(LiveInfo)
	// RefImages 源图：图生图必须携带，否则每层都会变成与源图无关的新图。
	RefImages []RefImage
}

// LayerFailure 单层失败记录（Index 从 1 开始）。
type LayerFailure struct {
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// LayerImageResult 逐层生成结果：URLs 与请求层数等长，失败位为空字符串。
type LayerImageResult struct {
	URLs     []string       `json:"urls"`
	Accounts []string       `json:"accounts,omitempty"`
	Failures []LayerFailure `json:"failures,omitempty"`
}

// GenerateImageLayersWithPool 按层依次生成：每层独立取号、独立失败切号，单层失败不拖垮其他层。
// 全部层都失败时返回错误（让任务面板显示真实原因），部分成功时由调用方按空位标记失败层。
func GenerateImageLayersWithPool(ctx context.Context, s *Service, req LayerImageRequest) (*LayerImageResult, error) {
	prompts := req.Prompts
	if len(prompts) == 0 {
		return nil, errors.New("图层数量至少为 1")
	}
	result := &LayerImageResult{URLs: make([]string, len(prompts))}
	for index, prompt := range prompts {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		generated, err := generateImageAttemptsWithPool(ctx, s, ImageRequest{
			Prompt: prompt, Model: req.Model, Ratio: req.Ratio, Style: req.Style, RefImages: req.RefImages,
			OnCredential: func(cred ActiveCredential) {
				if req.OnLive != nil {
					req.OnLive(LiveInfo{Label: cred.Label, Site: cred.Site})
				}
			},
		}, layerAccountAttempts)
		if err != nil {
			result.Failures = append(result.Failures, LayerFailure{Index: index + 1, Message: err.Error()})
			log.Printf("[doubao] 图层 %d/%d 生成失败：%v", index+1, len(prompts), err)
			continue
		}
		if len(generated.URLs) == 0 {
			result.Failures = append(result.Failures, LayerFailure{Index: index + 1, Message: "上游没有返回图片"})
			continue
		}
		result.URLs[index] = generated.URLs[0]
		result.Accounts = append(result.Accounts, generated.Account)
	}
	if len(result.Failures) == len(prompts) {
		return result, fmt.Errorf("豆包账号池未能生成任何图层：%s", result.Failures[0].Message)
	}
	return result, nil
}

// GenerateVideoWithPool 从账号池取号生成视频（同步等待出片，最长约 12 分钟）。
func GenerateVideoWithPool(ctx context.Context, s *Service, req VideoRequest) (*VideoResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("prompt 不能为空")
	}
	duration := req.Duration
	if duration <= 0 {
		duration = 10
	}
	// 上游 samantha 服务端实际接受 30s（Dola/豆包网页端 30s 档即由此发出），
	// 时长上限与网页端 5/10/30 档位对齐放开到 30。
	if duration > 30 {
		duration = 30
	}
	if duration < 4 {
		duration = 4
	}
	ratio := strings.TrimSpace(req.Ratio)
	if ratio == "" {
		ratio = "16:9"
	}
	prefer := ""
	var lastErr error = errors.New("账号池中没有可用账号")
	triedLabels := make([]string, 0, maxAccountAttempts)
	// 取号函数：Site 指定站点时锁定该站，否则跨站（豆包优先，自动落 Dola）。
	pickFn := func(p string) (*ActiveCredential, error) { return pickCredential(ctx, s, "", p) }
	if site := strings.TrimSpace(req.Site); site != "" {
		pickFn = func(p string) (*ActiveCredential, error) { return pickCredential(ctx, s, NormalizeSite(site), p) }
	}
	for attempt := 0; attempt < maxAccountAttempts; attempt++ {
		cred, err := pickWithRetry(ctx, s, prefer, pickFn)
		if err != nil {
			// 已有真实上游失败时保留原始错误，不被「无号可取」覆盖。
			if attempt > 0 {
				return nil, fmt.Errorf("%s（已依次尝试账号：%s，账号池已无可用账号）", lastErr.Error(), strings.Join(triedLabels, "、"))
			}
			return nil, err
		}
		triedLabels = append(triedLabels, cred.Label)
		// 按取到账号的站点切换域名（豆包 / Dola 同协议双站点）。
		ctx = WithOrigin(ctx, siteOrigin(cred.Site))
		if cred.ProxyURL != "" {
			ctx = outbound.WithProxyURL(ctx, cred.ProxyURL)
		}
		// 实时上报当前使用的账号；认出会话后再补一次会话直链。
		if req.OnLive != nil {
			req.OnLive(LiveInfo{Label: cred.Label, Site: cred.Site})
		}
		// Dola 站账号必须走浏览器借道（直连 HTTP 会被 Akamai 边缘 504/滑块拦截），
		// 协议也是独立的 content_block 结构，见 dola_relay.go。
		var result *VideoResult
		if NormalizeSite(cred.Site) == SiteDola {
			result, err = dolaGenerateVideo(ctx, s, cred, req, duration, ratio)
		} else {
			notifyConv := func(convID string) {
				if req.OnLive == nil || strings.TrimSpace(convID) == "" {
					return
				}
				req.OnLive(LiveInfo{Label: cred.Label, Site: cred.Site, ConversationURL: siteOrigin(cred.Site) + "/chat/" + strings.TrimSpace(convID)})
			}
			result, err = generateVideoOnce(ctx, cred.CookieHeader, req.Prompt, req.Model, duration, ratio, req.RefImages, notifyConv)
		}
		if err == nil {
			s.Release(cred.ID)
			// 视频成功才扣减账号的视频额度（失败不扣）；额度用完自动停用并切换下一账号。
			if exhausted, switched, next, mErr := s.MarkVideoSuccess(cred.ID, req.Model, duration); mErr == nil && exhausted && switched && next != nil {
				log.Printf("[doubao] 账号 %s 视频额度已用完，已自动停用并切换到 %s", cred.Label, next.Label)
			}
			// 优先走会话页 fallback_api 通道换无水印母片直链（真无水印，无需遮盖）；
			// 失败再退回 get_play_info/URL 改写的播放地址升级，最终兜底是入库前 delogo。
			if !applyFallbackNoWatermark(ctx, cred.CookieHeader, result) {
				upgradeVideosNoWatermark(ctx, cred.CookieHeader, result)
			}
			result.Account = cred.Label
			return result, nil
		}
		pe := classifyOnce(err)
		lastErr = fmt.Errorf("账号 %s：%w", cred.Label, pe)
		log.Printf("[doubao] 账号 %s 视频生成失败（第 %d/%d 次尝试，分类=%v）：%v", cred.Label, attempt+1, maxAccountAttempts, pe.classified != nil, pe)
		if pe.classified != nil {
			_, _, _ = s.MarkFailed(cred.ID, MarkFailedOptions{
				Kind: pe.classified.Kind, Message: pe.classified.Message,
			})
		}
		// 未分类错误（网络抖动等）不再直接放弃：同样换下一个账号重试。
		s.Release(cred.ID)
		prefer = ""
	}
	return nil, fmt.Errorf("%s（已依次尝试账号：%s）", lastErr.Error(), strings.Join(triedLabels, "、"))
}

// 单张失败只跳过该张；全部失败返回 nil（调用方降级为纯文本生成，不阻塞任务）。
// purpose 只用于日志区分「视频 / 图片 / 图层」等场景。
func uploadRefImages(ctx context.Context, cookieHeader string, refs []RefImage, purpose string) []string {
	if len(refs) == 0 {
		return nil
	}
	label := strings.TrimSpace(purpose)
	if label == "" {
		label = "生成"
	}
	keys := make([]string, 0, len(refs))
	for index, ref := range refs {
		uploaded, err := UploadImageOnce(ctx, cookieHeader, ref.Filename, ref.Data)
		if err != nil {
			log.Printf("[doubao] 参考图 %d/%d 上传失败（跳过）：%v", index+1, len(refs), err)
			continue
		}
		keys = append(keys, uploaded.URI)
	}
	if len(keys) == 0 {
		log.Printf("[doubao] 全部参考图上传失败，本次%s任务降级为纯文本生成", label)
	}
	return keys
}

// generateVideoOnce 单账号完整视频流程：
// 提交(2020) → 已含视频直接返回 → 自动确认（会话绑定） → task_id 轮询 / 会话页轮询。
func generateVideoOnce(ctx context.Context, cookieHeader, prompt, model string, duration int, ratio string, refImages []RefImage, notifyConv func(convID string)) (*VideoResult, error) {
	if strings.TrimSpace(cookieHeader) == "" {
		return nil, errors.New("缺少 Cookie，请重新登录账号")
	}
	imageKeys := uploadRefImages(ctx, cookieHeader, refImages, "视频")
	localConversationID := fmt.Sprintf("local_%d", rand.Int63n(1e16))
	tabID := randomUUID()
	submitAtMs := time.Now().UnixMilli()

	submitRaw, err := samanthaPost(ctx, cookieHeader, chatCompletionPath, videoPayload(prompt, model, duration, ratio, localConversationID, "", imageKeys), 7*time.Minute, tabID, "text/event-stream")
	if err != nil {
		var ce *ClassifyError
		if errors.As(err, &ce) {
			return nil, ce
		}
		return nil, err
	}
	submitEvents := ParseSSEEvents(submitRaw)
	submitText := CollectText(submitEvents)
	if block := detectBlock(submitEvents, submitRaw, submitText); block != nil {
		return nil, block
	}

	videos := CollectVideos(submitEvents)
	if len(videos) == 0 {
		videos = CollectVideosLoose(submitRaw)
	}
	taskID := ExtractTaskID(submitEvents, submitRaw)

	// 提交响应恒不回传 conversation_id；用 thread/list 小步重试认出本次新会话。
	convID := ""
	var lastThreadCount int
	for i := 0; i < 4 && convID == ""; i++ {
		if i > 0 {
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		threads := listThreads(ctx, cookieHeader, tabID)
		lastThreadCount = len(threads)
		convID = pickFreshThreadID(threads, submitAtMs, i < 3)
	}
	if convID == "" {
		log.Printf("[doubao] 视频 submit 后未认出新会话：thread/list 共 %d 条，reply=%.200s", lastThreadCount, submitText)
	}
	// 会话已认出：立即上报网页会话直链（面板「生成对话」按钮用）。
	if notifyConv != nil {
		notifyConv(convID)
	}

	// 早拍旧片快照：防止把上次任务的迟到视频当成这次的成品。
	known := map[string]bool{}
	if convID != "" {
		if html, err := fetchConversationPage(ctx, cookieHeader, convID, tabID); err == nil {
			for _, v := range ExtractVideosFromPage(html) {
				known[v.URL] = true
			}
		}
	}

	if len(videos) > 0 {
		return &VideoResult{URLs: metaURLs(videos), Message: submitText, TaskID: taskID, ConvID: convID}, nil
	}

	didConfirm := false
	lastText := submitText

	// 自动确认：豆包可能反问「比例/时长确认后生成」。确认必须是 2020+skill17
	// 且绑定同一会话，否则落进空会话永远推进不下去。
	if taskID == "" && convID != "" && isConfirmAsk(lastText) {
		didConfirm = true
		confirmPrompt := fmt.Sprintf("确认，就按 %d 秒、%s 生成。%s。请立即开始生成，不要再询问。", duration, ratio, strings.TrimSpace(prompt))
		confirmRaw, err := samanthaPost(ctx, cookieHeader, chatCompletionPath, videoPayload(confirmPrompt, model, duration, ratio, localConversationID, convID, imageKeys), 7*time.Minute, tabID, "text/event-stream")
		if err == nil {
			confirmEvents := ParseSSEEvents(confirmRaw)
			confirmText := CollectText(confirmEvents)
			if block := detectBlock(confirmEvents, confirmRaw, confirmText); block != nil {
				return nil, block
			}
			lastText = confirmText
			cv := CollectVideos(confirmEvents)
			if len(cv) == 0 {
				cv = CollectVideosLoose(confirmRaw)
			}
			if len(cv) > 0 {
				return &VideoResult{URLs: metaURLs(cv), Message: lastText, ConvID: convID}, nil
			}
			if id := ExtractTaskID(confirmEvents, confirmRaw); id != "" {
				taskID = id
			}
		}
	}

	if taskID == "" {
		if convID == "" {
			if isConfirmAsk(lastText) {
				return nil, fmt.Errorf("豆包在等待参数确认（回复：%s），但自动确认所需的会话未建立，可能上游协议有调整", truncate(lastText, 160))
			}
			return nil, fmt.Errorf("视频任务提交失败：%s", truncate(lastText, 200))
		}
		if !isAcceptance(lastText) && !didConfirm {
			if hardFailurePatterns.MatchString(lastText) {
				return nil, fmt.Errorf("%s", truncate(lastText, 300))
			}
			return nil, fmt.Errorf("视频任务提交失败：%s", truncate(lastText, 200))
		}
		// 已受理但无任务号：轮询会话页面取结果（视频异步写进会话）。
		select {
		case <-time.After(20 * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		html, err := fetchConversationPage(ctx, cookieHeader, convID, tabID)
		if err == nil {
			for _, v := range ExtractVideosFromPage(html) {
				known[v.URL] = true
			}
		}
		return pollConversationVideos(ctx, cookieHeader, convID, tabID, known, lastText)
	}

	// 有 task_id：轮询 async/stream。
	deadline := time.Now().Add(20 * time.Minute)
	lastMessage := lastText
	for rounds := 1; time.Now().Before(deadline); rounds++ {
		pollRaw, err := samanthaPost(ctx, cookieHeader, asyncStreamPath, map[string]any{"task_id": taskID, "event_id": 0}, 3*time.Minute, tabID, "text/event-stream")
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		pollEvents := ParseSSEEvents(pollRaw)
		if t := CollectText(pollEvents); t != "" {
			lastMessage = t
		}
		if block := detectBlock(pollEvents, pollRaw, lastMessage); block != nil {
			return nil, block
		}
		if hardFailurePatterns.MatchString(lastMessage) {
			return nil, fmt.Errorf("%s", truncate(lastMessage, 300))
		}
		videos := CollectVideos(pollEvents)
		if len(videos) == 0 {
			videos = CollectVideosLoose(pollRaw)
		}
		if len(videos) > 0 {
			return &VideoResult{URLs: metaURLs(preferWatermarkFree(videos)), Message: lastMessage, TaskID: taskID, ConvID: convID}, nil
		}
		delay := time.Duration(3+rounds) * time.Second
		if delay > 10*time.Second {
			delay = 10 * time.Second
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("%s视频已提交但等待超时，请稍后在豆包网页端查看，或重试", prefixText(lastMessage))
}

func prefixText(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	return truncate(t, 160) + "；"
}

func metaURLs(list []videoMeta) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, v.URL)
	}
	return out
}

// pollConversationVideos 轮询会话页面直到出现新视频。
func pollConversationVideos(ctx context.Context, cookieHeader, convID, tabID string, known map[string]bool, lastText string) (*VideoResult, error) {
	// 实测豆包 Seedance 出片可达 15-20 分钟，轮询窗口给足余量。
	deadline := time.Now().Add(20 * time.Minute)
	for rounds := 1; time.Now().Before(deadline); rounds++ {
		html, err := fetchConversationPage(ctx, cookieHeader, convID, tabID)
		if err == nil && html != "" {
			var fresh []videoMeta
			for _, v := range ExtractVideosFromPage(html) {
				if !known[v.URL] {
					fresh = append(fresh, v)
				}
			}
			if len(fresh) > 0 {
				return &VideoResult{URLs: metaURLs(preferWatermarkFree(fresh)), Message: lastText, ConvID: convID}, nil
			}
		}
		delay := time.Duration(10+rounds*2) * time.Second
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("%s已等待出片但会话里仍未出现视频，视频可能仍在生成，请稍后在豆包网页端该会话查看，或重试", prefixText(lastText))
}
