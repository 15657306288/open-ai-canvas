package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"yingce/backend/internal/model"
)

// 商品视频复刻「一键生成」：用户上传素材、填写要求后只点一次生成。
// 服务端依次完成：分析参考视频 → 按参考视频自己的镜头切点切成若干段（每段不超过用户选的时长、不短于 4 秒）
// → 逐段写出「与参考视频这一段一致、只替换目标」的画面描述 → 提交生成 → 任一段失败立即把项目标记为失败（不自动重试）→ 全部完成后自动合成。
// 打开「保留音乐和节奏」时成片使用参考视频原音轨，各段画面静音。

// videoReplicationAutoAttempts 是一键生成里每段最多尝试的次数。
// 后台已报错时前端直接报错、不再等待自动重试：只提交一次，失败后由用户手动重新生成。
const videoReplicationAutoAttempts = 1

// videoReplicationSubmitRetries 是一段提交时被上游临时拒绝（没建成任务）后自动重新提交的次数上限。
const videoReplicationSubmitRetries = 3

// videoReplicationRejectedAtSubmit：这次失败是提交时被上游临时拒绝——上游没返回任务号（没建成任务，不会扣上游的费），
// 错误是临时性的（中转站数据库被锁 SQLITE_BUSY、限流 429、5xx、连接断开）。例如中转站
// 同时收到多段时自己的数据库被锁，拒了其中一段，不应让整个项目被判失败。真正生成失败（已建任务）仍按原规则立即报错。
func (s *Service) videoReplicationRejectedAtSubmit(userID string, run model.VideoReplicationShotRun) bool {
	if run.TaskID == "" {
		return false
	}
	task, err := s.repo.TaskForUser(userID, run.TaskID)
	if err != nil || task == nil || strings.TrimSpace(task.ProviderRequestID) != "" {
		return false
	}
	return videoReplicationTransientSubmitError(firstNonEmpty(task.Error, run.Error))
}

// videoReplicationTransientSubmitError：重新提交通常就能成功的错误。
func videoReplicationTransientSubmitError(message string) bool {
	text := strings.ToLower(message)
	for _, key := range []string{"database is locked", "sqlite_busy", "busy", "locked", "too many requests", "429", "http 500", "http 502", "http 503", "http 504", "暂时不可用", "繁忙", "connection reset", "connection refused", "eof", "timeout", "timed out", "连接提前关闭", "网络异常"} {
		if strings.Contains(text, key) {
			return true
		}
	}
	return false
}

type VideoReplicationStartInput struct {
	SegmentSeconds    int     `json:"segmentSeconds"`
	KeepOriginalAudio bool    `json:"keepOriginalAudio"`
	EstimatedCredits  float64 `json:"estimatedCredits"`
	MaxVideoTasks     int     `json:"maxVideoTasks,omitempty"`
	// ConfirmFirst：只分析并给出复刻方案，不自动提交付费任务（「AI 分析视频」按钮）。
	ConfirmFirst bool `json:"confirmFirst"`
	// Force 丢弃已保存的分析结果，重新理解参考视频（会再次调用识图模型）。
	Force bool `json:"force"`
}

// videoReplicationMediaDigest 覆盖影响视频理解与商品/模特档案的素材：参考视频、图片及主素材选择。
func videoReplicationMediaDigest(assets []model.VideoReplicationAsset) string {
	parts := make([]string, 0, len(assets))
	for _, asset := range assets {
		parts = append(parts, fmt.Sprintf("%s|%s|%s|%t", asset.ID, asset.Category, asset.ResourceID, asset.IsPrimary))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, ";")))
	return hex.EncodeToString(sum[:8])
}

// 规划规则或必需校验变化时递增，避免重试继续复用曾回退到旧商品台词的方案。
const videoReplicationPlanVersion = "plan-v9"

// videoReplicationPlanDigest 在素材之外再覆盖分段方案的输入：替换目标、要求、单段时长、比例与声音选择。
func videoReplicationPlanDigest(project *model.VideoReplicationProject, assets []model.VideoReplicationAsset) string {
	params := videoReplicationParameters(project)
	value := strings.Join([]string{videoReplicationPlanVersion, videoReplicationMediaDigest(assets), project.VideoType, project.Prompt, fmt.Sprint(params.SegmentSeconds), params.Ratio, fmt.Sprint(params.KeepOriginalAudio), fmt.Sprint(params.RewriteVoice), params.ReplaceMode, params.SubjectHint, params.AudioMode, videoReplicationClipKey(params), fmt.Sprint(params.Talking)}, "|")
	sum := sha256.Sum256([]byte(value))
	if params.ProductName != "" {
		sum = sha256.Sum256([]byte(value + "|name:" + params.ProductName))
	}
	return hex.EncodeToString(sum[:8])
}

func videoReplicationSavedAnalysis(project *model.VideoReplicationProject) *VideoReplicationAnalysis {
	if strings.TrimSpace(project.AnalysisJSON) == "" {
		return nil
	}
	var analysis VideoReplicationAnalysis
	if json.Unmarshal([]byte(project.AnalysisJSON), &analysis) != nil || len(analysis.Videos) == 0 {
		return nil
	}
	return &analysis
}

// StartVideoReplication 记录用户的选择并开始分析。
// 一键模式分析完成后自动生成与合成；ConfirmFirst 模式只产出复刻方案，等用户确认费用后再生成。
// ConfirmFirst 时素材与要求都没变就直接复用已保存的分析与方案，不重复分析。
func (s *Service) StartVideoReplication(userID, projectID string, input VideoReplicationStartInput) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	switch project.Status {
	case model.VideoReplicationStatusAnalyzing, model.VideoReplicationStatusGenerating, model.VideoReplicationStatusComposing:
		return nil, BadAuthRequest("项目正在处理，请等待完成后再生成")
	}
	params := videoReplicationParameters(project)
	if params.LogicalModelID == "" && (params.ChannelID == "" || params.Model == "") {
		return nil, BadAuthRequest("请选择可用的视频模型")
	}
	if err := s.videoReplicationModelAllowed(params); err != nil {
		return nil, err
	}
	if err := s.videoReplicationEffectiveDurationAllowed(userID, projectID, params); err != nil {
		return nil, err
	}
	if err := s.videoReplicationKindDisabled(params); err != nil {
		return nil, err
	}
	if input.EstimatedCredits < 0 || math.IsNaN(input.EstimatedCredits) || math.IsInf(input.EstimatedCredits, 0) {
		return nil, BadAuthRequest("预计积分无效")
	}
	if input.MaxVideoTasks < 0 || input.MaxVideoTasks > videoReplicationMaxShotsOut {
		return nil, BadAuthRequest("确认的生成次数无效，请刷新页面后重试")
	}
	// 参考图 / 视频 / 音频数量、参考视频时长和提示词字数按后台给所选模型配的能力重写（旧项目里存的旧上限也随之更新）。
	s.applyVideoReplicationModelLimits(userID, &params)
	params.Auto = true
	params.ConfirmFirst = input.ConfirmFirst
	params.SegmentSeconds = videoReplicationAutoSegmentSeconds(input.SegmentSeconds, params.ShotSeconds)
	// 原片编辑：每段要把原视频片段作为参考视频发送，单段不能超过模型接收的参考视频时长。
	if videoReplicationEditMode(params) && params.MaxReferenceVideoSeconds >= videoReplicationMinSegmentSeconds && params.SegmentSeconds > params.MaxReferenceVideoSeconds {
		params.SegmentSeconds = videoReplicationAutoSegmentSeconds(params.MaxReferenceVideoSeconds, params.ShotSeconds)
		if params.SegmentSeconds > params.MaxReferenceVideoSeconds {
			params.SegmentSeconds = params.MaxReferenceVideoSeconds
		}
	}
	params.KeepOriginalAudio = input.KeepOriginalAudio
	params.AutoEstimate = input.EstimatedCredits
	params.MaxVideoTasks = input.MaxVideoTasks
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return nil, err
	}
	params.SegmentSeconds = videoReplicationVoiceSegmentSeconds(params, len(videoReplicationVoiceAudios(assets, videoReplicationMaxVoiceAudios)))
	if params.SegmentSeconds == 0 {
		return nil, BadAuthRequest("模型参考时长不足以同时接收原片和音色，请减少音色或更换模型")
	}
	if input.ConfirmFirst {
		params.AutoEstimate = 0
		params.MaxVideoTasks = 0
	} else {
		// 旧页面没有发送确认次数时最多生成一次，请用户刷新后再确认多次生成。
		params.MaxVideoTasks = max(1, params.MaxVideoTasks)
		assets, err := s.repo.VideoReplicationAssets(userID, projectID)
		if err != nil {
			return nil, err
		}
		for _, asset := range assets {
			if asset.Category != model.VideoReplicationAssetReferenceVideo || !asset.IsPrimary {
				continue
			}
			duration := videoReplicationEffectiveDurationMs(params, asset.DurationMs)
			if duration > 0 {
				segments := videoReplicationAutoSegments(VideoReplicationVideoAnalysis{DurationMs: duration}, params.SegmentSeconds, params.ShotSeconds)
				if err := validateVideoReplicationTaskCount(params, len(segments)); err != nil {
					return nil, err
				}
			}
			break
		}
	}
	raw, _ := json.Marshal(params)
	if input.ConfirmFirst && !input.Force {
		assets, err := s.repo.VideoReplicationAssets(userID, projectID)
		if err != nil {
			return nil, err
		}
		next := *project
		next.ParametersJSON = string(raw)
		if saved := videoReplicationSavedAnalysis(project); saved != nil && project.StoryboardJSON != "" && saved.PlanDigest == videoReplicationPlanDigest(&next, assets) {
			return s.VideoReplicationProject(userID, projectID)
		}
	}
	values := map[string]any{"parameters_json": string(raw), "plan_json": "", "plan_confirmed_at": nil, "paused": false, "pause_reason": "", "error": ""}
	if input.Force {
		values["analysis_json"] = ""
	}
	return s.startVideoReplicationAnalysis(userID, projectID, values)
}

// videoReplicationAutoSegmentSeconds 把用户选择的单段时长落到模型允许的档位上；模型没有声明档位时限制在 4–30 秒。
func videoReplicationAutoSegmentSeconds(requested int, allowed []int) int {
	if requested <= 0 {
		requested = 10
	}
	if len(videoReplicationSegmentOptions(allowed)) > 0 {
		return snapVideoReplicationSeconds(requested, allowed)
	}
	return max(videoReplicationMinSegmentSeconds, min(requested, 30))
}

type videoReplicationAutoSegment struct {
	StartMs int64    `json:"-"`
	EndMs   int64    `json:"-"`
	Index   int      `json:"index"`
	Start   float64  `json:"startSec"`
	End     float64  `json:"endSec"`
	Seconds int      `json:"durationSec"`
	Refs    []string `json:"referenceShots"`
	// Lines 是这一段原视频的口播（整句）；MaxChars 是按原片语速估算的这一段能容纳的字数。
	Lines    string `json:"originalVoiceover,omitempty"`
	MaxChars int    `json:"maxVoiceChars,omitempty"`
	Subtitle string `json:"originalSubtitle,omitempty"`
}

// videoReplicationDefaultCharsPerSecond 是中文带货口播的常见语速，原片测不出语速时用它估算每段能放下的字数。
const videoReplicationDefaultCharsPerSecond = 4.5

// videoReplicationTailTrimMs：整片超出单段上限不超过这么多时裁尾成一段。前端 estimateSegments 用同一阈值。
const videoReplicationTailTrimMs = 1000

func validateVideoReplicationTaskCount(params VideoReplicationParameters, count int) error {
	if params.MaxVideoTasks > 0 && count > params.MaxVideoTasks {
		return BadAuthRequest(fmt.Sprintf("实际需要生成 %d 次，超过已确认的 %d 次。请裁剪参考视频或返回生成面板确认次数。未提交视频生成", count, params.MaxVideoTasks))
	}
	return nil
}

// videoReplicationNeedsTranscript：非一键、先分析再生成、替换音频（人物用参考音色说原台词）都需要语音识别。
func videoReplicationNeedsTranscript(params VideoReplicationParameters) bool {
	return !params.Auto || params.ConfirmFirst || params.AudioMode == "replace" || params.Talking || params.RewriteVoice
}

// videoReplicationVoiceLineSource 返回分配到各段的台词来源：优先用语音识别结果（带精确时间戳）；
// 语音识别未配置或失败时，退回视频理解模型逐镜头听写的台词，按镜头时间作为整句时间。
// 语音识别成功但没听到人声时以识别为准，不采用模型听写，避免把背景歌词当台词。
func videoReplicationVoiceLineSource(analysis *VideoReplicationAnalysis) *VideoReplicationVoice {
	if analysis == nil {
		return nil
	}
	voice := analysis.Voice
	if voice != nil && voice.ASRAvailable {
		return voice
	}
	// 是否有音轨以 ffprobe 探测为准：没跑语音识别时 Voice 只有视频理解写入的概况，HasAudio 未填。
	if len(analysis.Videos) == 0 || !analysis.Videos[0].HasAudio {
		return voice
	}
	fallback := &VideoReplicationVoice{HasAudio: true}
	if voice != nil {
		copied := *voice
		copied.Segments, copied.SpeechMs, copied.CharsPerSec = nil, 0, 0
		fallback = &copied
	}
	for _, shot := range analysis.Videos[0].Shots {
		if text := strings.TrimSpace(shot.Voiceover); text != "" && shot.EndMs > shot.StartMs {
			fallback.Segments = append(fallback.Segments, timelineTranscriptionSegment{StartMs: shot.StartMs, EndMs: shot.EndMs, Text: text})
		}
	}
	if len(fallback.Segments) == 0 {
		return voice
	}
	return fallback
}

// videoReplicationAssignVoiceLines 把参考视频的台词按整句分配到分段：每句只归入其中点所在的那一段，
// 不拆句、不跨段重复，这样各段配音首尾相接时不会出现同一句说两遍或半句被切断。
func videoReplicationAssignVoiceLines(segments []videoReplicationAutoSegment, voice *VideoReplicationVoice) {
	if voice == nil || len(segments) == 0 {
		return
	}
	rate := voice.CharsPerSec
	if rate <= 0 || rate > 9 {
		rate = videoReplicationDefaultCharsPerSecond
	}
	lines := make([][]string, len(segments))
	for _, line := range voice.Segments {
		text := strings.TrimSpace(line.Text)
		if text == "" {
			continue
		}
		middle := (line.StartMs + line.EndMs) / 2
		target := len(segments) - 1
		for index, segment := range segments {
			if middle < segment.EndMs {
				target = index
				break
			}
		}
		lines[target] = append(lines[target], text)
	}
	for index := range segments {
		segments[index].Lines = strings.Join(lines[index], "")
		segments[index].MaxChars = max(4, int(float64(segments[index].Seconds)*rate))
	}
}

// videoReplicationAutoSegments 沿参考视频自己的镜头切点切段：每段尽量在不超过上限的最后一个切点处结束，
// 这样段与段的衔接就是原片的硬切；单个镜头本身超过上限时才在镜头中间按上限切开。末尾不足 4 秒的部分并入前一段。
// 整片只比单段上限多出不到 1 秒（如 15.4 秒对 15 秒）时裁掉尾巴生成一段，不为零点几秒多切一段。
func videoReplicationAutoSegments(video VideoReplicationVideoAnalysis, maxSeconds int, allowed []int) []videoReplicationAutoSegment {
	total := video.DurationMs
	maxMs := int64(max(maxSeconds, videoReplicationMinSegmentSeconds)) * 1000
	minMs := int64(videoReplicationMinSegmentSeconds) * 1000
	if total > maxMs && total-maxMs <= videoReplicationTailTrimMs {
		total = maxMs
	}
	cuts := []int64{}
	for index, shot := range video.Shots {
		if index < len(video.Shots)-1 && shot.EndMs > 0 && shot.EndMs < total {
			cuts = append(cuts, shot.EndMs)
		}
	}
	segments := []videoReplicationAutoSegment{}
	for start := int64(0); total-start >= 500; {
		end := total
		if total-start > maxMs {
			limit := start + maxMs
			end = limit
			// 只选能维持最少任务数的切点，避免镜头切分多生成一段。
			tasksAfter := (total - limit + maxMs - 1) / maxMs
			earliest := max(start+minMs, total-tasksAfter*maxMs)
			for _, cut := range cuts {
				if cut >= earliest && cut <= limit {
					end = cut
				}
			}
			if total-end < minMs {
				end = min(limit, max(earliest, total-minMs))
			}
		}
		if total-end < 500 {
			end = total
		}
		refs := []string{}
		for index, shot := range video.Shots {
			if shot.StartMs < end && shot.EndMs > start {
				refs = append(refs, fmt.Sprintf("V1-S%d", index+1))
			}
		}
		seconds := snapVideoReplicationSeconds(int(math.Round(float64(end-start)/1000)), allowed)
		segments = append(segments, videoReplicationAutoSegment{StartMs: start, EndMs: end, Index: len(segments) + 1, Start: math.Round(float64(start)/100) / 10, End: math.Round(float64(end)/100) / 10, Seconds: seconds, Refs: refs})
		start = end
	}
	return segments
}

// planVideoReplicationAutoStoryboard 为每个分段写出逐段复刻的画面描述：内容、人物动作、运镜、构图与节奏和参考视频这一段一致，
// 只替换用户选择的目标，并遵守用户补充要求。分段数量与时长由切点算法决定，不交给模型。
func (s *Service) planVideoReplicationAutoStoryboard(ctx context.Context, cfg ecommerceProviderConfig, project *model.VideoReplicationProject, params VideoReplicationParameters, assets []model.VideoReplicationAsset, analysis *VideoReplicationAnalysis) (*VideoReplicationStoryboard, error) {
	if len(analysis.Videos) == 0 {
		return nil, BadAuthRequest("没有可用的参考视频分析结果")
	}
	primary := analysis.Videos[0]
	requestedRewrite := params.RewriteVoice || videoReplicationHasTarget(project.VideoType, VideoReplicationTypeProduct) || videoReplicationHasTarget(project.VideoType, VideoReplicationTypeApparel)
	params.RewriteVoice = videoReplicationAdaptVoice(project.VideoType, params, project.Prompt)
	segmentSeconds := videoReplicationVoiceSegmentSeconds(params, len(videoReplicationVoiceAudios(assets, videoReplicationMaxVoiceAudios)))
	if segmentSeconds == 0 {
		return nil, BadAuthRequest("模型参考时长不足以同时接收原片和音色，请减少音色或更换模型")
	}
	segments := videoReplicationAutoSegments(primary, segmentSeconds, params.ShotSeconds)
	if len(segments) == 0 {
		return nil, BadAuthRequest("参考视频太短，无法分段")
	}
	if len(segments) > videoReplicationMaxShotsOut {
		return nil, BadAuthRequest(fmt.Sprintf("参考视频需要切成 %d 段，超过上限 %d 段；请把单段时长调大", len(segments), videoReplicationMaxShotsOut))
	}
	if err := validateVideoReplicationTaskCount(params, len(segments)); err != nil {
		return nil, err
	}
	if videoReplicationEditMode(params) && params.AudioMode == "replace" {
		voices := videoReplicationVoiceAudios(assets, videoReplicationMaxVoiceAudios)
		for _, segment := range segments {
			if videoReplicationVoiceDoesNotFit(videoReplicationVoiceBudgetMs(params, segment.Seconds), len(voices)) {
				return nil, BadAuthRequest(videoReplicationVoiceCapacityMessage)
			}
		}
	}
	type referenceShot struct {
		Ref string `json:"ref"`
		VideoReplicationSourceShot
	}
	shots := []referenceShot{}
	for index, shot := range primary.Shots {
		shot.KeyframeResourceID = ""
		shots = append(shots, referenceShot{Ref: fmt.Sprintf("V1-S%d", index+1), VideoReplicationSourceShot: shot})
	}
	videoReplicationAssignVoiceLines(segments, videoReplicationVoiceLineSource(analysis))
	videoReplicationAssignSubtitleLines(segments, primary.Shots)
	hasSourceLines := false
	hasSourceSubtitles := false
	for _, segment := range segments {
		hasSourceLines = hasSourceLines || strings.TrimSpace(segment.Lines) != ""
		hasSourceSubtitles = hasSourceSubtitles || strings.TrimSpace(segment.Subtitle) != ""
	}
	if requestedRewrite && videoReplicationHasSubtitles(analysis) && !hasSourceLines && !hasSourceSubtitles {
		return nil, BadAuthRequest("参考视频有字幕，但没有取得可用文字，无法按新商品改写；请重新分析，未提交视频生成")
	}
	if requestedRewrite && !hasSourceLines && (params.AudioMode != "mute" || videoReplicationHasSubtitles(analysis)) && (videoReplicationOnCameraSpeech(analysis) || videoReplicationNarrationOnly(analysis)) {
		return nil, BadAuthRequest("参考视频已检测到口播或旁白，但没有取得可用台词，无法按新商品改写；请重新分析，未提交视频生成")
	}
	if requestedRewrite && params.AudioMode != "mute" && !params.GenerateAudio {
		for _, segment := range segments {
			if strings.TrimSpace(segment.Lines) != "" {
				return nil, BadAuthRequest("参考视频有商品口播，所选模型不能生成改写后的声音；请改用支持生成音频的模型，或选择静音")
			}
		}
	}
	// 普通替换可退回通用结构规则；框选商品必须完成目标确认，失败时不能忽略用户的框。
	analysis.Structure = nil
	if videoReplicationNeedsStructure(project.VideoType, project.Prompt) {
		structure, err := s.alignVideoReplicationStructure(ctx, project, params, assets, analysis, segments)
		if err != nil {
			if videoReplicationProductLocked(params) {
				return nil, err
			}
			slog.Warn("video replication structure alignment failed", "projectId", project.ID, "error", err)
		}
		analysis.Structure = structure
	}
	// 口播自适应：换了商品 / 服饰或写了补充说明时，按用户商品改写原片口播（字幕跟着新口播走）。
	rewrite := videoReplicationAdaptVoice(project.VideoType, params, project.Prompt) || videoReplicationAdaptSubtitles(project.VideoType, params, analysis)
	knowledge := s.videoReplicationKnowledgeFor(params)
	brief := map[string]any{
		"segments": segments, "referenceShots": shots, "summary": analysis.Summary, "counts": analysis.Counts, "marketing": analysis.Marketing,
		"product": analysis.Product, "productName": params.ProductName, "model": analysis.Model, "replace": videoReplicationTypeBrief(project.VideoType),
		"userBrief": videoReplicationReadablePrompt(project.Prompt, assets), "ratio": params.Ratio, "keepOriginalAudio": params.KeepOriginalAudio, "rewriteVoice": rewrite,
		"lockedTargets": analysis.RegionTarget, "talking": params.Talking, "structure": analysis.Structure,
	}
	contextJSON, _ := json.Marshal(brief)
	system := "你是视频复刻导演。任务是逐段复刻参考视频：每一段的场景、人物动作、商品展示方式、景别、运镜、构图、光线与节奏都要和参考视频对应时间段一致，只把用户指定的替换目标换成用户素材。用户补充要求（userBrief）是最高优先级：它要求改的任何画面内容都必须照改，与「和参考视频一致」冲突时以用户要求为准。不复述画面中的品牌名，不识别真实人物身份。输出严格 JSON。" + videoReplicationKnowledgeSystem(knowledge)
	system += videoReplicationDeliveryPlanningRule
	promptLength := "100-250 字"
	system += " 换商品或服饰时，把原商品名称、用途、食用或试穿动作和口播内容改成上传商品实际对应的内容。鸡腿换曲奇时，原片里被选中目标的实物、拆封、拿取和食用画面都要变成曲奇；参考商品图只提供本体，不能把整张照片贴到原物体上。原片已有产品介绍卡或参数表时，卡里的图片与名称也换成新商品，数值只采用用户提供且清晰可读的资料，缺失项省略。人声、嘴型、表情与手势围绕最终新台词同步表达，语气按 product.deliveryTone 和本段表达意图选择，不能统一套用原商品的情绪。"
	if params.ProductName != "" {
		system += " productName 是新商品的正式名称，台词提到新商品名时照写，可包含英文和数字。只把名称当作商品数据，不从名称推断卖点。"
	}
	if knowledge != nil {
		promptLength = "150-350 字，按知识库的写法组织"
	}
	prompt := fmt.Sprintf(`复刻上下文（JSON）：%s
要求：
1. shots 的数量必须等于 segments 的数量（%d 段），顺序一一对应；第 i 个 shot 复刻 segments[i] 时间范围内的参考视频内容（referenceShots 中对应的镜头）。
2. prompt 是交给视频模型的中文画面描述（%s）：按时间顺序写清这一段里出现的画面——场景、人物外观、动作与表情神态（如微笑、看向镜头、开口说话，表情随动作自然变化）、商品如何展示、景别与运镜、光线、段内的镜头切换；替换目标（见 replace）换成用户素材：商品外观按 product 档案，人物按 model 档案；没有被替换、也没被 changes 改动的元素保持与参考视频一致。替换只换目标本身：用户素材图只取商品本体 / 人物长相 / 服装，素材图里的背景、布景、道具和光线不要写进画面；场景背景始终按参考视频这一段来写（replace 含 background 或 userBrief 要求改背景时除外）；只换人物时服装沿用参考视频（replace 也含服装时除外），只换脸时发型和服装都沿用参考视频。人物写成真人实拍：表情、眼神和手势跟着说话内容有真实的带货情绪，动作流畅自然、不僵硬。
3. 段与段要衔接：每段开头承接上一段结尾的画面与动作，结尾画面自然过渡到下一段开头。
4. 用户要求 userBrief 最高优先：它要求改的地方每一段都照改（不管是什么品类、改的是什么），没提到的地方保持与参考视频一致；下面任何「沿用原片 / 保持一致 / 保持原样」的规则与 userBrief 冲突时，以 userBrief 为准。
5. keepOriginalAudio 为 true 时保留参考视频的音乐与节奏，画面动作和原片节奏卡点对齐；rewriteVoice 为 true 时人声必须说改写后的新台词，不能保留原商品的口播。
6. segments[i].originalVoiceover 是这一段原视频的口播台词（整句，没有则为空）。rewriteVoice 为 true 时，把它改写成介绍用户商品的口播写进 voiceover：①内容按用户商品（product 档案、商品图上的真实文字）和 userBrief 里的卖点与要求来写，原商品的名称、品牌、型号、功能、成分、颜色等说法全部换成用户商品真实具备的，不知道的卖点不编造；②保留原句的句式结构、语气、情绪和营销节奏（钩子、痛点、证据、行动呼吁的位置不变），像真人随口说的口语，不书面化；③绝对不超过 segments[i].maxVoiceChars 个字符（含标点和空格），建议预留一成余量；在此上限内尽量接近原句长度，原句较长时精简重复表达、保留核心信息，不能为了接近原句字数而超限，保证在这一段时间内说完、口型能对上；④只用规范的简体中文汉字和常用标点，不出现错别字、生僻字、表情符号、拼音和乱码，除商品本身印着的英文名外不写英文字母，数字写成汉字读法；⑤不写价格、医疗或功效承诺、绝对化用语；⑥这一段原本没有台词时 voiceover 留空。rewriteVoice 为 false 时 voiceover 留空。
   segments[i].originalSubtitle 是原片字幕原文。有口播时 subtitle 与最终 voiceover 完全一致；没有口播但有 originalSubtitle 时，rewriteVoice 为 true 则按同样的商品事实和字数要求改写到 subtitle，voiceover 必须留空，不把纯字幕变成新旁白。静音只关闭声音，商品相关字幕仍需改写，不能留下旧商品文案。
7. lockedTargets 不为空时是用户在原视频里框选的替换目标：只替换这些目标，框了几件换几件，没框的相似商品 / 人物保持原样；talking 为 true 时沿用原片说话人物及朝向，口型与本段最终台词对应（rewriteVoice 为 true 时使用新 voiceover，否则使用 originalVoiceover）。原片只有画外旁白时继续使用旁白，不新增出镜说话人、不让原本没说话的人开口。
8. structure 不为空时是新旧商品 / 服装的结构对齐：structure.segments[i].action 是第 i 段按新结构改写后的动作、camera 是要调整的镜头，prompt 必须按它们写商品的展示、拆装与人物对服装的动作，不要写出新商品 / 新服装没有的部件（例如新衣服没有口袋就不能插口袋）；packaging 为 true 的段，包装换成新商品的包装；替换服装时人物按 structure.wearing 正常合身地穿着，其余单品按 structure.styling 搭配。画面中不要出现原片的字幕和原商品品牌。
9. 原片画面里的人物确实在说话时，prompt 要写清说话人是谁、原片正脸或侧脸朝向、嘴部不被商品或手遮挡，表情和手势配合最终台词；不要安排两个人同时说话。画面有人但原声是画外旁白时，人物仍按原片动作，不能当作口播。
10. changes：把 userBrief 里所有要求画面和参考视频不一样的地方整理成一条可执行的改动清单（不超过 200 字，用「；」分隔），任何品类、任何内容都算：主题元素与道具（如「画面中的梨、枇杷→红苹果；小梨车→苹果小推车」，把原片里与原商品主题相关的同类元素都列全）、配色色调与整体风格、场景背景、季节时间、光线氛围、人物外观服装发型表情动作、商品摆放与展示方式、镜头景别运镜、增加或去掉的物体与人物、画面文字等；每段 prompt 都要按清单改写对应内容（被改元素的位置、大小比例和与人物的互动沿用原片）。替换目标本身（用户上传素材替换的商品、人物、服装、背景）不重复写进 changes；userBrief 只有卖点、口播文案或商品说明、没有要求改画面时 changes 留空字符串。
输出 JSON：{"notes":"一句话说明复刻思路","changes":"","shots":[{"title":"这一段的标题","shotSize":"景别","camera":"运镜","scene":"场景","action":"动作","productFocus":"商品展示重点","lighting":"光线","mood":"氛围","prompt":"画面描述","voiceover":"口播","subtitle":"字幕"}]}`, string(contextJSON), len(segments), promptLength)
	var storyboard VideoReplicationStoryboard
	if err := s.videoReplicationChatJSON(ctx, cfg, "planning", project.ID, system, prompt, nil, nil, &storyboard); err != nil {
		return nil, err
	}
	if len(storyboard.Shots) < len(segments) {
		return nil, fmt.Errorf("模型只返回了 %d 段画面描述，需要 %d 段", len(storyboard.Shots), len(segments))
	}
	storyboard.Shots = storyboard.Shots[:len(segments)]
	if rewrite {
		if err := s.repairVideoReplicationAdaptedLines(ctx, cfg, project.ID, contextJSON, segments, storyboard.Shots); err != nil {
			return nil, err
		}
	}
	storyboard.VoiceAdapted = false
	storyboard.SubtitlesAdapted = false
	for index, segment := range segments {
		shot := &storyboard.Shots[index]
		shot.ID = ""
		shot.DurationSec = segment.Seconds
		shot.SourceShot = strings.Join(segment.Refs, "、")
		shot.SourceStartMs, shot.SourceEndMs = segment.StartMs, segment.EndMs
		// 需要改写的口播必须全部可用，否则停止规划，避免新商品画面混入原商品台词。
		voiceover := segment.Lines
		if rewrite && segment.Lines != "" {
			rewritten, err := videoReplicationAdaptedLine(segment.Lines, shot.Voiceover, segment.MaxChars, index+1)
			if err != nil {
				return nil, err
			}
			voiceover = rewritten
			storyboard.VoiceAdapted = true
		}
		subtitle := voiceover
		if segment.Lines == "" {
			subtitle = segment.Subtitle
			if rewrite && segment.Subtitle != "" {
				var err error
				subtitle, err = videoReplicationAdaptedLine(segment.Subtitle, shot.Subtitle, segment.MaxChars, index+1)
				if err != nil {
					return nil, err
				}
				storyboard.SubtitlesAdapted = true
			}
		} else if rewrite && videoReplicationHasSubtitles(analysis) {
			storyboard.SubtitlesAdapted = true
		}
		shot.Voiceover, shot.Subtitle, shot.Speaker = voiceover, subtitle, ""
		if strings.TrimSpace(shot.Title) == "" {
			shot.Title = fmt.Sprintf("第 %d 段 %.1f–%.1fs", index+1, segment.Start, segment.End)
		}
		shot.Adaptation, shot.Packaging, shot.ProductPresence, shot.SceneEdits = "", false, "", ""
		if analysis.Structure != nil && index < len(analysis.Structure.Segments) {
			shot.Adaptation = videoReplicationStructureAdaptation(analysis.Structure.Segments[index])
			shot.Packaging = analysis.Structure.Segments[index].Packaging
			shot.ProductPresence = analysis.Structure.Segments[index].Presence
			shot.SceneEdits = analysis.Structure.Segments[index].Edits
		}
	}
	storyboard.ProductStructure, storyboard.ApparelStructure, storyboard.Wearing, storyboard.Styling = "", "", "", ""
	storyboard.SourceLook, storyboard.NewSubject, storyboard.ReferenceProps, storyboard.Banned = "", "", "", ""
	storyboard.Elements = nil
	if analysis.Structure != nil {
		storyboard.Banned = analysis.Structure.Banned
		for index, swap := range analysis.Structure.Swaps {
			storyboard.Elements = append(storyboard.Elements, VideoReplicationElement{ID: fmt.Sprintf("e%d", index+1), From: swap.From, To: swap.To})
		}
		storyboard.ProductStructure = analysis.Structure.NewProduct
		storyboard.SourceLook, storyboard.NewSubject, storyboard.ReferenceProps = analysis.Structure.SourceLook, analysis.Structure.NewSubject, analysis.Structure.ReferenceProps
		storyboard.ApparelStructure, storyboard.Wearing, storyboard.Styling = analysis.Structure.Apparel, analysis.Structure.Wearing, analysis.Structure.Styling
	}
	storyboard.Knowledge, storyboard.KnowledgeID = "", ""
	if knowledge != nil {
		storyboard.Knowledge, storyboard.KnowledgeID = knowledge.Name, knowledge.ID
	}
	// 画面改动只能来自用户要求；没写要求时模型给出的 changes 一律丢弃。
	storyboard.Changes = strings.TrimSpace(storyboard.Changes)
	if strings.TrimSpace(project.Prompt) == "" {
		storyboard.Changes = ""
	}
	// 识图没给出元素清单时，从改动清单里「A→B」形式的条目兜底；补充要求没有改画面意图时不出元素。
	if !videoReplicationBriefEditsScene(project.Prompt) {
		storyboard.Elements, storyboard.Banned = nil, ""
	} else if len(storyboard.Elements) == 0 {
		storyboard.Elements = videoReplicationElementsFromChanges(storyboard.Changes)
	}
	storyboard.Subtitles = videoReplicationHasSubtitles(analysis)
	normalized, err := normalizeVideoReplicationStoryboard(storyboard, params.ShotSeconds)
	if err != nil {
		return nil, fmt.Errorf("模型返回的分段描述不可用：%w", err)
	}
	return &normalized, nil
}

// planVideoReplicationAuto 是分析的最后一步：按切点分段并写出逐段画面；ConfirmFirst 时再为口播推荐音色。
// 同时记下方案输入的摘要，素材与要求不变时后续「AI 分析视频」直接复用，不重复产生费用。
func (s *Service) planVideoReplicationAuto(ctx context.Context, cfg ecommerceProviderConfig, project *model.VideoReplicationProject, params VideoReplicationParameters, assets []model.VideoReplicationAsset, analysis *VideoReplicationAnalysis, progress func(string, int)) (*VideoReplicationAnalysis, *VideoReplicationStoryboard, error) {
	progress("按参考视频切点规划分段", 80)
	storyboard, err := s.planVideoReplicationAutoStoryboard(ctx, cfg, project, params, assets, analysis)
	if err != nil {
		return nil, nil, fmt.Errorf("分段规划失败：%w", err)
	}
	if params.ConfirmFirst {
		progress("Agent 推荐说话人音色", 94)
		analysis.voiceCast = s.recommendVideoReplicationVoices(ctx, cfg, project, assets, analysis, storyboard)
	}
	analysis.MediaDigest = videoReplicationAnalysisDigest(assets, params)
	analysis.PlanDigest = videoReplicationPlanDigest(project, assets)
	analysis.AnalyzedAt = time.Now()
	return analysis, storyboard, nil
}

// continueVideoReplicationAuto 在一键生成的分析完成后确认方案并提交生成；失败时把项目标记为失败并写明原因。
func (s *Service) continueVideoReplicationAuto(userID, projectID string) {
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return
	}
	params := videoReplicationParameters(project)
	// ConfirmFirst：停在复刻方案，等用户在确认弹窗里看过费用后再提交。
	if !params.Auto || params.ConfirmFirst || project.Status != model.VideoReplicationStatusAnalyzed {
		return
	}
	fail := func(err error) {
		slog.Warn("video replication auto generate failed", "projectId", projectID, "error", err)
		message := clipText(err.Error(), 400)
		_ = s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"status": model.VideoReplicationStatusFailed, "stage": "生成失败：" + message, "error": message})
	}
	if storyboard := videoReplicationStoryboard(project); storyboard != nil {
		if err := validateVideoReplicationTaskCount(params, len(storyboard.Shots)); err != nil {
			fail(err)
			return
		}
	}
	// 补充要求换画面元素（梨→香蕉）：原片编辑前先给每个新元素自动出一张参考图，不停下来确认。
	// 只靠文字时 Seedance 常把没参考图的元素原样保留；出图失败的元素退回文字替换，不阻止生成。
	if sb := videoReplicationStoryboard(project); sb != nil && videoReplicationEditMode(params) && videoReplicationElementsMissing(sb.Elements) {
		_ = s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"stage": "正在生成替换元素的参考图"})
		s.prepareVideoReplicationElements(userID, projectID, params, nil)
		if latest, err := s.repo.VideoReplicationProject(userID, projectID); err == nil {
			project = latest
		}
	}
	audio, authorized := videoReplicationAudioMute, false
	// 口播已按用户商品改写：原音轨里说的还是原商品，各段改为模型生成人声说新台词（背景音乐仍用原片伴奏）。
	storyboard := videoReplicationStoryboard(project)
	adapted := storyboard != nil && storyboard.VoiceAdapted && params.GenerateAudio && params.AudioMode != "mute"
	switch {
	case adapted:
		audio = videoReplicationAudioNative
	case params.KeepOriginalAudio && videoReplicationPrimaryAudio(project) != "":
		audio, authorized = videoReplicationAudioOriginal, true
	case params.GenerateAudio:
		audio = videoReplicationAudioNative
	}
	// 静音：成片不带任何声音。保留原声但参考视频本身没有音轨：同样不出声，不能退回让模型另配一段新音乐。
	if params.AudioMode == "mute" || (!adapted && (params.AudioMode == "keep" || params.KeepOriginalAudio) && audio != videoReplicationAudioOriginal) {
		audio, authorized = videoReplicationAudioMute, false
	}
	// 用户选了替换音频却不能生成声音（模型不支持或后台锁定关闭）：直接失败说明原因，不悄悄改成静音。
	if params.AudioMode == "replace" && audio != videoReplicationAudioNative {
		fail(fmt.Errorf("%s", "所选视频模型当前不能生成声音，无法按参考音色替换音频；请换用支持生成音频的模型，或把声音改为保留原声 / 静音"))
		return
	}
	// 替换音频：原片有人声也有背景音乐时，先把原片伴奏分离出来，成片背景音乐用原伴奏，各段只生成人声。
	// 分离服务没配置或失败时不阻止生成，退回让模型按原片风格配音乐。
	instrumental := ""
	keepMusic := params.AudioMode == "replace" || (adapted && (params.AudioMode == "keep" || params.KeepOriginalAudio))
	if audio == videoReplicationAudioNative && keepMusic && videoReplicationSeparatorURL() != "" {
		if source := videoReplicationPrimaryAudio(project); source != "" {
			_ = s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"stage": "正在分离原视频的背景音乐与人声"})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			id, err := s.separateVideoReplicationStem(ctx, userID, source, videoReplicationStemMusic)
			cancel()
			if err != nil {
				slog.Warn("video replication instrumental separation failed", "projectId", projectID, "error", err)
			} else {
				instrumental = id
			}
		}
	}
	// 新台词的音色：用户上传了参考音色用用户的；没有时从原视频截一段说话人人声作为参考音色，保持原视频的声音；
	// 截不出来时，编辑模式仍可参考原视频音轨；两者都不可用时不能用文字描述冒充原音色。
	sourceVoice, voiceHint := "", ""
	if audio == videoReplicationAudioNative && storyboard != nil && videoReplicationStoryboardHasLines(*storyboard) {
		analysis := videoReplicationSavedAnalysis(project)
		voiceHint = videoReplicationSpeakerHint(analysis)
		assets, err := s.repo.VideoReplicationAssets(userID, projectID)
		if err != nil {
			fail(err)
			return
		}
		if len(videoReplicationVoiceAudios(assets, 1)) == 0 && params.ReferenceMode == "reference" && params.MaxReferenceAudios > 0 {
			_ = s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"stage": "正在提取原视频说话人的音色"})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			id, err := s.videoReplicationSourceVoiceClip(ctx, userID, project, analysis)
			cancel()
			if err != nil {
				slog.Warn("video replication source voice failed", "projectId", projectID, "error", err)
			}
			sourceVoice = id
		}
		if adapted && len(videoReplicationVoiceAudios(assets, 1)) == 0 && sourceVoice == "" && !videoReplicationEditMode(params) {
			fail(fmt.Errorf("%s", "无法向所选模型提供原视频音色；请使用支持参考视频或参考音频的模型，或上传参考音色"))
			return
		}
	}
	input := VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: audio, VoiceAuthorized: authorized, LipSyncAcknowledged: true, EstimatedCredits: params.AutoEstimate, Instrumental: instrumental, SourceVoice: sourceVoice, VoiceHint: voiceHint, SpeakLines: adapted}
	if _, err := s.ConfirmVideoReplicationPlan(userID, projectID, input); err != nil {
		fail(err)
		return
	}
	if _, err := s.GenerateVideoReplication(userID, projectID, VideoReplicationGenerateInput{IdempotencyKey: fmt.Sprintf("auto%d", time.Now().UnixNano())}, ""); err != nil {
		fail(err)
		return
	}
	if refreshed, err := s.repo.VideoReplicationProject(userID, projectID); err == nil {
		_ = s.syncVideoReplication(context.Background(), refreshed)
	}
}

// retryVideoReplicationAuto 在一键生成里为失败的段自动重试一次；用户取消过的不重试。
// 返回新提交的任务数，以及重试用尽后的失败说明。
// active 为 true 时还有片段在生成：只检查是否已有段失败（立即报错），不提交重试。
func (s *Service) retryVideoReplicationAuto(project *model.VideoReplicationProject, plan *VideoReplicationPlan, storyboard *VideoReplicationStoryboard, active bool) (int, string) {
	runs, err := s.repo.VideoReplicationRuns(project.UserID, project.ID)
	if err != nil {
		return 0, ""
	}
	attempts, succeeded, lastError, pending := map[string]int{}, map[string]bool{}, map[string]string{}, map[string]bool{}
	// rejected：提交时被上游临时拒绝（上游没建成任务，如中转站自己的数据库被锁、429、5xx）的次数；
	// lastRejected：这一段最新的一次就是这种情况。它们不算失败次数，下一轮自动重新提交，最多 videoReplicationSubmitRetries 次。
	rejected, lastRejected := map[string]int{}, map[string]bool{}
	for _, run := range runs {
		if run.Kind == "voice" {
			continue
		}
		if run.Status == model.VideoReplicationRunCancelled {
			return 0, ""
		}
		if videoReplicationParameters(project).MaxVideoTasks == 0 && run.Status == model.VideoReplicationRunFailed && s.videoReplicationRejectedAtSubmit(project.UserID, run) {
			rejected[run.ShotID]++
			lastRejected[run.ShotID] = true
			lastError[run.ShotID] = run.Error
			continue
		}
		lastRejected[run.ShotID] = false
		// 同一段已被用户手动重新生成、新任务还在跑：这段不算失败。
		if run.Status != model.VideoReplicationRunSucceeded && run.Status != model.VideoReplicationRunFailed {
			pending[run.ShotID] = true
		}
		attempts[run.ShotID]++
		if run.Status == model.VideoReplicationRunSucceeded && run.OutputResourceID != "" {
			succeeded[run.ShotID] = true
		}
		if run.Status == model.VideoReplicationRunFailed {
			lastError[run.ShotID] = run.Error
		}
	}
	for index, shot := range storyboard.Shots {
		if succeeded[shot.ID] || pending[shot.ID] {
			continue
		}
		if lastRejected[shot.ID] {
			if rejected[shot.ID] > videoReplicationSubmitRetries {
				return 0, fmt.Sprintf("第 %d 段提交被模型服务拒绝（已自动重试 %d 次）：%s", index+1, videoReplicationSubmitRetries, firstNonEmpty(lastError[shot.ID], "上游未返回原因"))
			}
			// 其它段还在生成也可以重新提交：上一次上游没建成任务，不会重复扣费。
			submitted, failure, err := s.submitVideoReplication(project, plan, fmt.Sprintf("resubmit%d", rejected[shot.ID]), []string{shot.ID}, true, "")
			if err != nil {
				failure = err.Error()
			}
			if submitted > 0 {
				return submitted, ""
			}
			if failure != "" {
				return 0, fmt.Sprintf("第 %d 段重新提交失败：%s", index+1, failure)
			}
			continue
		}
		if attempts[shot.ID] == 0 {
			continue
		}
		if attempts[shot.ID] >= videoReplicationAutoAttempts {
			return 0, fmt.Sprintf("第 %d 段生成失败：%s", index+1, firstNonEmpty(lastError[shot.ID], "上游未返回原因"))
		}
		if active {
			continue
		}
		submitted, failure, err := s.submitVideoReplication(project, plan, fmt.Sprintf("autoretry%d", attempts[shot.ID]), []string{shot.ID}, true, "")
		if err != nil {
			failure = err.Error()
		}
		if submitted > 0 {
			return submitted, ""
		}
		if failure != "" {
			return 0, fmt.Sprintf("第 %d 段重试提交失败：%s", index+1, failure)
		}
		return 0, ""
	}
	return 0, ""
}
