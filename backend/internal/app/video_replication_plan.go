package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

// 商品视频复刻 2.0 的双引擎：
//   - native（原生直出）：一个视频生成任务直接输出完整视频，不拆分、不拼接、不剪辑；FFmpeg 只做 ffprobe/黑场等只读检查。
//   - segmented（智能分段）：按分镜生成多段，FFmpeg 时间线渲染合成完整视频。
// 任何付费调用都必须基于用户确认过的方案（任务数、模型、预算）；方案对应的分镜或参数变化后需要重新确认。

const (
	VideoReplicationStrategyNative    = "native"
	VideoReplicationStrategySegmented = "segmented"
	videoReplicationNativeShotID      = "__full__"

	videoReplicationAudioNative   = "model_native"
	videoReplicationAudioOriginal = "keep_original"
	videoReplicationAudioMute     = "mute"
	videoReplicationAudioTTS      = "tts_voiceover"
)

type VideoReplicationPlanSegment struct {
	ShotID  string `json:"shotId"`
	Seconds int    `json:"seconds"`
}

type VideoReplicationPlan struct {
	Strategy           string                        `json:"strategy"`
	Model              string                        `json:"model"`
	ModelLabel         string                        `json:"modelLabel,omitempty"`
	TargetSeconds      int                           `json:"targetSeconds"`
	TaskCount          int                           `json:"taskCount"`
	SegmentMaxSeconds  int                           `json:"segmentMaxSeconds,omitempty"`
	Segments           []VideoReplicationPlanSegment `json:"segments"`
	Continuity         string                        `json:"continuity"`
	AudioMode          string                        `json:"audioMode"`
	VoiceAuthorized    bool                          `json:"voiceAuthorized"`
	UsesFFmpegCompose  bool                          `json:"usesFfmpegCompose"`
	UsesTTS            bool                          `json:"usesTts"`
	UsesDigitalHuman   bool                          `json:"usesDigitalHuman"`
	LipSyncRequired    bool                          `json:"lipSyncRequired"`
	LipSyncSupported   bool                          `json:"lipSyncSupported"`
	EstimatedCredits   float64                       `json:"estimatedCredits"`
	BudgetCredits      float64                       `json:"budgetCredits"`
	StoryboardRevision int                           `json:"storyboardRevision"`
	NoSplice           bool                          `json:"noSplice"`
	Digest             string                        `json:"digest"`
	ConfirmedAt        time.Time                     `json:"confirmedAt"`
	Notes              []string                      `json:"notes,omitempty"`
	// Instrumental 是原视频分离出的伴奏（背景音乐）资源：替换音频时成片 = 各段新人声 + 这条原伴奏，背景音乐保持原片。
	Instrumental string `json:"instrumentalResourceId,omitempty"`
	// SourceVoice 是从原视频截出的说话人人声（≤15 秒）：用户没上传参考音色时，新台词用它作为参考音色，保持原视频的声音。
	SourceVoice string `json:"sourceVoiceResourceId,omitempty"`
	// VoiceHint 是原视频说话人的音色文字描述，模型收不了参考音频时写进提示词。
	VoiceHint string `json:"voiceHint,omitempty"`
	// SpeakLines：口播已按用户商品改写，各段要让人物说出新台词（不再沿用原音轨）。
	SpeakLines bool `json:"speakLines,omitempty"`
}

type VideoReplicationPlanInput struct {
	Strategy            string  `json:"strategy"`
	AudioMode           string  `json:"audioMode"`
	VoiceAuthorized     bool    `json:"voiceAuthorized"`
	LipSyncAcknowledged bool    `json:"lipSyncAcknowledged"`
	EstimatedCredits    float64 `json:"estimatedCredits"`
	BudgetCredits       float64 `json:"budgetCredits"`
	// NoSplice（禁止剪辑拼接）：只接受原生直出；模型做不到时直接拒绝，不偷偷改成智能分段。
	NoSplice bool `json:"noSplice"`
	// Instrumental 只由一键生成流程在服务端写入（分离原片伴奏成功时），不接受前端传入。
	Instrumental string `json:"-"`
	SourceVoice  string `json:"-"`
	VoiceHint    string `json:"-"`
	SpeakLines   bool   `json:"-"`
}

type VideoReplicationQuality struct {
	Passed     bool                           `json:"passed"`
	DurationMs int64                          `json:"durationMs"`
	ExpectedMs int64                          `json:"expectedMs"`
	Width      int                            `json:"width"`
	Height     int                            `json:"height"`
	HasAudio   bool                           `json:"hasAudio"`
	BlackMs    int64                          `json:"blackMs"`
	Checks     []VideoReplicationQualityCheck `json:"checks"`
	CheckedAt  time.Time                      `json:"checkedAt"`
}

type VideoReplicationQualityCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail | skipped
	Detail string `json:"detail,omitempty"`
}

func videoReplicationPlan(project *model.VideoReplicationProject) *VideoReplicationPlan {
	if strings.TrimSpace(project.PlanJSON) == "" {
		return nil
	}
	var plan VideoReplicationPlan
	if json.Unmarshal([]byte(project.PlanJSON), &plan) != nil {
		return nil
	}
	return &plan
}

// videoReplicationDigest 覆盖影响付费结果的全部输入：参数、视频类型、描述与分镜修订号。
func videoReplicationDigest(project *model.VideoReplicationProject) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{project.ParametersJSON, project.VideoType, project.Prompt, strconv.Itoa(project.StoryboardRevision), project.VoiceCastJSON}, "|")))
	return hex.EncodeToString(sum[:8])
}

func videoReplicationSecondsAllowed(allowed []int, seconds int) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, value := range allowed {
		if value == seconds {
			return true
		}
	}
	return false
}

func videoReplicationOnCameraSpeech(analysis *VideoReplicationAnalysis) bool {
	return analysis != nil && analysis.Voice != nil && (analysis.Voice.Type == "on_camera" || analysis.Voice.Type == "dialogue")
}

// 原生直出直接发布模型资源；只有 TTS 方案会实际进入后期字幕合成。
func videoReplicationValidateNativeSubtitles(plan *VideoReplicationPlan, storyboard VideoReplicationStoryboard, params VideoReplicationParameters) error {
	adaptedSubtitles := storyboard.SubtitlesAdapted || (storyboard.VoiceAdapted && (storyboard.Subtitles || params.Subtitles))
	if plan.Strategy == VideoReplicationStrategyNative && plan.AudioMode != videoReplicationAudioTTS && adaptedSubtitles {
		return BadAuthRequest("原生直出不能叠加改写后的商品字幕，请选择智能分段后重新确认；已开启「禁止剪辑拼接」时需先取消，尚未提交视频生成")
	}
	return nil
}

// buildVideoReplicationPlan 根据分镜、参数与用户选择计算真实任务计划；不信任前端传入的任务数。
func buildVideoReplicationPlan(project *model.VideoReplicationProject, input VideoReplicationPlanInput) (*VideoReplicationPlan, error) {
	storyboard := videoReplicationStoryboard(project)
	if storyboard == nil || len(storyboard.Shots) == 0 {
		return nil, BadAuthRequest("请先完成参考视频分析并生成分镜")
	}
	params := videoReplicationParameters(project)
	if params.LogicalModelID == "" && (params.ChannelID == "" || params.Model == "") {
		return nil, BadAuthRequest("请选择可用的系统视频模型")
	}
	if (params.ReferenceMode == "" || params.ReferenceMode == "none") && len(videoReplicationTargets(project.VideoType)) > 0 {
		return nil, BadAuthRequest("所选视频模型不支持参考图输入，无法完成商品/人物替换")
	}
	var analysis *VideoReplicationAnalysis
	if strings.TrimSpace(project.AnalysisJSON) != "" {
		analysis = &VideoReplicationAnalysis{}
		_ = json.Unmarshal([]byte(project.AnalysisJSON), analysis)
	}
	plan := &VideoReplicationPlan{Strategy: input.Strategy, Model: params.Model, ModelLabel: params.ModelLabel, AudioMode: input.AudioMode, VoiceAuthorized: input.VoiceAuthorized, EstimatedCredits: input.EstimatedCredits, BudgetCredits: input.BudgetCredits, StoryboardRevision: project.StoryboardRevision, NoSplice: input.NoSplice, Instrumental: input.Instrumental, SourceVoice: input.SourceVoice, VoiceHint: input.VoiceHint, SpeakLines: input.SpeakLines, Digest: videoReplicationDigest(project), ConfirmedAt: time.Now()}
	if err := videoReplicationValidateNativeSubtitles(plan, *storyboard, params); err != nil {
		return nil, err
	}
	if storyboard.VoiceAdapted {
		if plan.AudioMode == videoReplicationAudioOriginal {
			return nil, BadAuthRequest("商品口播已经改写，不能用原视频旧人声覆盖新台词；请选择模型原生音频、AI 配音或静音")
		}
		plan.SpeakLines = true
	}
	if input.NoSplice && input.Strategy != VideoReplicationStrategyNative {
		return nil, BadAuthRequest("已选择禁止剪辑拼接，只能使用原生直出；请换用支持参考视频时长的模型，或取消「禁止剪辑拼接」")
	}
	if len(params.ShotSeconds) > 0 {
		plan.SegmentMaxSeconds = params.ShotSeconds[len(params.ShotSeconds)-1]
	}
	switch plan.AudioMode {
	case videoReplicationAudioNative, videoReplicationAudioMute:
	case videoReplicationAudioTTS:
		hasLines := false
		if storyboard := videoReplicationStoryboard(project); storyboard != nil {
			for _, shot := range storyboard.Shots {
				hasLines = hasLines || strings.TrimSpace(shot.Voiceover) != ""
			}
		}
		if !hasLines {
			return nil, BadAuthRequest("分镜里没有口播文案，无法生成 AI 配音；可以在分镜里填写口播后再选择")
		}
		plan.UsesTTS = true
		plan.Notes = append(plan.Notes, "AI 配音由 TTS 生成，按镜头起点铺成一条连续音轨；属于音频后处理，不是视频模型原生音频")
		// 每段配音最长只放到该段结束，超过的部分会被截掉；按常见语速提前提示，避免成片里半句话被切断。
		for index, shot := range storyboard.Shots {
			if chars := utf8.RuneCountInString(strings.TrimSpace(shot.Voiceover)); chars > 0 && float64(chars) > float64(shot.DurationSec)*videoReplicationDefaultCharsPerSecond*1.3 {
				plan.Notes = append(plan.Notes, fmt.Sprintf("第 %d 段口播 %d 字，%d 秒内按正常语速读不完，结尾可能被截断；建议在分镜里删减", index+1, chars, shot.DurationSec))
			}
		}
	case videoReplicationAudioOriginal:
		if !input.VoiceAuthorized {
			return nil, BadAuthRequest("保留原视频声音前，请确认已获得声音与内容授权")
		}
	default:
		return nil, BadAuthRequest("声音处理方式无效")
	}
	if plan.AudioMode == videoReplicationAudioNative && !params.GenerateAudio {
		return nil, BadAuthRequest("「模型原生音频」需要先打开生成音频（所选模型需支持）")
	}
	total := 0
	for _, shot := range storyboard.Shots {
		total += snapVideoReplicationSeconds(shot.DurationSec, params.ShotSeconds)
	}
	switch plan.Strategy {
	case VideoReplicationStrategyNative:
		// 成片时长不由用户选择：跟随主参考视频真实时长；在模型单次上限内时取最接近的可用档位。
		target := params.TargetSeconds
		if target <= 0 {
			target = videoReplicationReferenceSeconds(analysis)
		}
		if target <= 0 {
			target = total
		}
		if plan.SegmentMaxSeconds > 0 && target <= plan.SegmentMaxSeconds {
			target = snapVideoReplicationSeconds(target, params.ShotSeconds)
		}
		if !videoReplicationSecondsAllowed(params.ShotSeconds, target) {
			return nil, BadAuthRequest(fmt.Sprintf("所选模型单次最长 %d 秒，不能原生直出 %d 秒的完整视频；请换用支持该时长的模型，或改用智能分段", plan.SegmentMaxSeconds, target))
		}
		if plan.AudioMode == videoReplicationAudioOriginal {
			return nil, BadAuthRequest("原生直出不对成片做音轨剪辑；保留原声请改用智能分段，或选择模型原生音频/关闭人声")
		}
		plan.TargetSeconds, plan.TaskCount = target, 1
		plan.Segments = []VideoReplicationPlanSegment{{ShotID: videoReplicationNativeShotID, Seconds: target}}
		plan.Continuity = "single_task"
	case VideoReplicationStrategySegmented:
		for _, shot := range storyboard.Shots {
			plan.Segments = append(plan.Segments, VideoReplicationPlanSegment{ShotID: shot.ID, Seconds: snapVideoReplicationSeconds(shot.DurationSec, params.ShotSeconds)})
		}
		plan.TargetSeconds, plan.TaskCount, plan.UsesFFmpegCompose = total, len(plan.Segments), true
		// 只支持首帧图生视频的模型用上一段尾帧作为下一段首帧；参考图生视频模型靠共享的商品/人物参考图保持一致。
		plan.Continuity = "shared_references"
		if params.ReferenceMode == "first_frame" {
			plan.Continuity = "tail_frame"
		}
	default:
		return nil, BadAuthRequest("生成方式无效")
	}
	if (videoReplicationHasTarget(project.VideoType, VideoReplicationTypeModel) || videoReplicationHasTarget(project.VideoType, VideoReplicationTypeFace)) && videoReplicationOnCameraSpeech(analysis) {
		plan.LipSyncRequired = true
		plan.Notes = append(plan.Notes, "参考视频是人物对镜口播，替换人物后需要口型同步；当前没有接入口型同步/数字人服务，画面口型不会与台词精确对齐")
		if !input.LipSyncAcknowledged {
			return nil, BadAuthRequest("当前不支持口型同步，请确认知晓后再生成")
		}
	}
	if plan.EstimatedCredits < 0 || plan.BudgetCredits < 0 || math.IsNaN(plan.BudgetCredits) {
		return nil, BadAuthRequest("预算无效")
	}
	if plan.BudgetCredits > 0 && plan.EstimatedCredits > plan.BudgetCredits {
		return nil, BadAuthRequest(fmt.Sprintf("预计消耗 %.2f 积分超过设定预算 %.2f 积分", plan.EstimatedCredits, plan.BudgetCredits))
	}
	return plan, nil
}

func (s *Service) ConfirmVideoReplicationPlan(userID, projectID string, input VideoReplicationPlanInput) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if project.Status == model.VideoReplicationStatusAnalyzing {
		return nil, BadAuthRequest("参考视频分析中")
	}
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return nil, err
	}
	if missing := videoReplicationRequirement(project.VideoType, videoReplicationParameters(project), assets); missing != "" {
		return nil, BadAuthRequest(missing)
	}
	if err := s.videoReplicationModelAllowed(videoReplicationParameters(project)); err != nil {
		return nil, err
	}
	if input.AudioMode == videoReplicationAudioTTS {
		setting, err := s.videoReplicationSetting()
		if err != nil {
			return nil, err
		}
		if !setting.ttsReady() {
			return nil, BadAuthRequest("后台还没有配置配音模型（TTS），暂时不能使用 AI 配音")
		}
		cast := videoReplicationVoiceCast(project)
		if len(cast.Roles) == 0 || cast.Roles[0].VoiceKey == "" {
			return nil, BadAuthRequest("请先在声音配置里为说话人选择音色")
		}
	}
	plan, err := buildVideoReplicationPlan(project, input)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(plan)
	now := time.Now()
	if err := s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"plan_json": string(raw), "plan_confirmed_at": now, "paused": false, "pause_reason": ""}); err != nil {
		return nil, err
	}
	return s.VideoReplicationProject(userID, projectID)
}

// requireConfirmedVideoReplicationPlan 保证提交付费任务时，方案与当前分镜、参数一致。
func requireConfirmedVideoReplicationPlan(project *model.VideoReplicationProject) (*VideoReplicationPlan, error) {
	plan := videoReplicationPlan(project)
	if plan == nil || project.PlanConfirmedAt == nil {
		return nil, &AppError{Status: 409, Code: 409, Message: "请先确认生成方案与预计积分"}
	}
	if plan.Digest != videoReplicationDigest(project) || plan.StoryboardRevision != project.StoryboardRevision {
		return nil, &AppError{Status: 409, Code: 409, Message: "分镜或参数已变化，请重新确认生成方案与费用"}
	}
	return plan, nil
}

// videoReplicationSpentCredits 汇总本项目镜头任务的实际账单（已退款的不计）。
func (s *Service) videoReplicationSpentCredits(userID string, runs []model.VideoReplicationShotRun) (float64, error) {
	ids := []string{}
	for _, run := range runs {
		if run.TaskID != "" {
			ids = append(ids, run.TaskID)
		}
	}
	orders, err := s.repo.BillingOrdersByTaskIDs(userID, ids)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, order := range orders {
		if order.Status != model.BillingStatusRefunded {
			total += order.AmountMicrocredits
		}
	}
	return float64(total) / float64(CreditScale), nil
}

// videoReplicationTaskEstimate 按秒数把方案总预估分摊到单个任务，用于提交前的预算判断。
func videoReplicationTaskEstimate(plan *VideoReplicationPlan, seconds int) float64 {
	if plan.TargetSeconds <= 0 || plan.EstimatedCredits <= 0 {
		return 0
	}
	return plan.EstimatedCredits * float64(seconds) / float64(plan.TargetSeconds)
}

func (s *Service) PauseVideoReplication(userID, projectID string, paused bool) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	if _, err := s.repo.VideoReplicationProject(userID, projectID); err != nil {
		return nil, videoReplicationNotFound(err)
	}
	reason := ""
	if paused {
		reason = "已暂停：进行中的镜头会继续完成，后续片段不再自动提交"
	}
	if err := s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"paused": paused, "pause_reason": reason}); err != nil {
		return nil, err
	}
	return s.VideoReplicationProject(userID, projectID)
}

// videoReplicationNativePrompt 把整条分镜写成一个完整视频的生成指令（带时间轴），交给模型一次输出。
func videoReplicationNativePrompt(videoType string, params VideoReplicationParameters, storyboard VideoReplicationStoryboard, target int, brief string, references []map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "生成一条完整的 %d 秒商业广告视频，按以下时间轴连续呈现，镜头之间自然衔接，不要黑场与字幕：", target)
	cursor := 0
	for index, shot := range storyboard.Shots {
		seconds := snapVideoReplicationSeconds(shot.DurationSec, nil)
		end := cursor + seconds
		if index == len(storyboard.Shots)-1 || end > target {
			end = target
		}
		fmt.Fprintf(&b, "\n[%d-%d秒] %s", cursor, end, strings.TrimSpace(shot.Prompt))
		if language := strings.Trim(strings.Join([]string{shot.ShotSize, shot.Camera, shot.Lighting}, "，"), "，"); language != "" {
			b.WriteString("（" + language + "）")
		}
		cursor = end
		if cursor >= target {
			break
		}
	}
	tail := videoReplicationShotPrompt(videoType, params, VideoReplicationShot{}, brief, references)
	b.WriteString("\n" + strings.TrimSpace(tail))
	return videoReplicationPromptBudgetFor(params).clip(b.String())
}

var blackDurationPattern = regexp.MustCompile(`black_duration:([0-9.]+)`)

// checkVideoReplicationOutput 对成片做只读质量检查：真实时长、分辨率、音轨、黑场比例。不修改文件。
func (s *Service) checkVideoReplicationOutput(ctx context.Context, userID, resourceID string, expectedMs int64, wantAudio bool) VideoReplicationQuality {
	var quality VideoReplicationQuality
	err := s.withVideoReplicationResourceFile(userID, resourceID, 2<<30, func(_ *model.Resource, path string) error {
		quality = inspectVideoReplicationFile(ctx, path, expectedMs, wantAudio)
		return nil
	})
	if err != nil {
		quality = VideoReplicationQuality{ExpectedMs: expectedMs, CheckedAt: time.Now(), Checks: []VideoReplicationQualityCheck{{Name: "视频文件有效性", Status: "fail", Detail: clipText(err.Error(), 300)}}}
	}
	return quality
}

// inspectVideoReplicationFile 对本地成片文件做只读检查。
func inspectVideoReplicationFile(ctx context.Context, path string, expectedMs int64, wantAudio bool) VideoReplicationQuality {
	quality := VideoReplicationQuality{ExpectedMs: expectedMs, CheckedAt: time.Now()}
	add := func(name, status, detail string) {
		quality.Checks = append(quality.Checks, VideoReplicationQualityCheck{Name: name, Status: status, Detail: detail})
	}
	err := func() error {
		probe, err := probeVideoReplicationFile(ctx, path)
		if err != nil {
			return err
		}
		quality.DurationMs, quality.Width, quality.Height, quality.HasAudio = probe.DurationMs, probe.Width, probe.Height, probe.HasAudio
		add("视频文件有效性", "pass", fmt.Sprintf("%s · %dx%d · %.2f fps", probe.VideoCodec, probe.Width, probe.Height, probe.FPS))
		tolerance := max(int64(1500), expectedMs/20)
		if expectedMs > 0 && (probe.DurationMs < expectedMs-tolerance || probe.DurationMs > expectedMs+tolerance) {
			add("视频时长", "fail", fmt.Sprintf("实际 %.1f 秒，目标 %.1f 秒", float64(probe.DurationMs)/1000, float64(expectedMs)/1000))
		} else {
			add("视频时长", "pass", fmt.Sprintf("%.1f 秒", float64(probe.DurationMs)/1000))
		}
		if wantAudio && !probe.HasAudio {
			add("音轨", "fail", "方案要求有声音，但成片没有音轨")
		} else if probe.HasAudio {
			add("音轨", "pass", "包含音轨")
		} else {
			add("音轨", "pass", "无音轨（方案未要求声音）")
		}
		if ffmpegBin, err := renderFfmpegBinary(); err == nil {
			callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(callCtx, ffmpegBin, "-hide_banner", "-nostdin", "-i", path, "-an", "-vf", "blackdetect=d=0.5:pix_th=0.10", "-f", "null", "-")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if cmd.Run() == nil {
				for _, match := range blackDurationPattern.FindAllStringSubmatch(stderr.String(), -1) {
					if seconds, err := strconv.ParseFloat(match[1], 64); err == nil {
						quality.BlackMs += int64(seconds * 1000)
					}
				}
				ratio := float64(quality.BlackMs) / math.Max(1, float64(probe.DurationMs))
				switch {
				case ratio > 0.3:
					add("黑屏", "fail", fmt.Sprintf("黑场 %.1f 秒（%.0f%%）", float64(quality.BlackMs)/1000, ratio*100))
				case quality.BlackMs > 1500:
					add("黑屏", "warn", fmt.Sprintf("黑场 %.1f 秒", float64(quality.BlackMs)/1000))
				default:
					add("黑屏", "pass", "未发现长黑场")
				}
			} else {
				add("黑屏", "skipped", "黑场检测执行失败")
			}
		}
		return nil
	}()
	if err != nil {
		add("视频文件有效性", "fail", clipText(err.Error(), 300))
	}
	quality.Passed = true
	for _, check := range quality.Checks {
		if check.Status == "fail" {
			quality.Passed = false
		}
	}
	return quality
}

// runVideoReplicationQuality 异步执行质量检查；只有通过检查的成片才标记为完成。
func (s *Service) runVideoReplicationQuality(userID string, output model.VideoReplicationOutput, wantAudio bool) {
	claimed, err := s.repo.ClaimVideoReplicationOutputCheck(output.ID)
	if err != nil || !claimed {
		return
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Warn("video replication quality panic", "outputId", output.ID, "error", recovered)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		quality := s.checkVideoReplicationOutput(ctx, userID, output.ResourceID, output.DurationMs, wantAudio)
		raw, _ := json.Marshal(quality)
		status := "succeeded"
		message := ""
		if !quality.Passed {
			status = "quality_failed"
			for _, check := range quality.Checks {
				if check.Status == "fail" {
					message = check.Name + "：" + check.Detail
					break
				}
			}
		}
		if err := s.repo.UpdateVideoReplicationOutput(output.ID, map[string]any{"status": status, "quality_json": string(raw), "error": message}); err != nil {
			slog.Warn("video replication quality save failed", "outputId", output.ID, "error", err)
		}
	}()
}

// videoReplicationPrimaryAudio 返回主参考视频分析时抽出的音轨资源。
func videoReplicationPrimaryAudio(project *model.VideoReplicationProject) string {
	if strings.TrimSpace(project.AnalysisJSON) == "" {
		return ""
	}
	var analysis VideoReplicationAnalysis
	if json.Unmarshal([]byte(project.AnalysisJSON), &analysis) != nil {
		return ""
	}
	for _, video := range analysis.Videos {
		if video.Primary {
			return video.AudioResourceID
		}
	}
	if len(analysis.Videos) > 0 {
		return analysis.Videos[0].AudioResourceID
	}
	return ""
}

// videoReplicationReferenceSeconds 返回主参考视频的真实时长（秒，四舍五入）。
func videoReplicationReferenceSeconds(analysis *VideoReplicationAnalysis) int {
	if analysis == nil {
		return 0
	}
	for _, video := range analysis.Videos {
		if video.Primary {
			return int(math.Round(float64(video.DurationMs) / 1000))
		}
	}
	if len(analysis.Videos) > 0 {
		return int(math.Round(float64(analysis.Videos[0].DurationMs) / 1000))
	}
	return 0
}

// videoReplicationWithDetectedTalking：原片识别出有人对镜说话（口播 / 对话）时按口播处理，用户没勾「是否口播视频」也一样。
// 否则换了人、换了声音后提示词里没有口型要求，新面孔的嘴型和台词对不上，一眼假。
func videoReplicationWithDetectedTalking(params VideoReplicationParameters, analysis *VideoReplicationAnalysis) VideoReplicationParameters {
	if !params.Talking && videoReplicationOnCameraSpeech(analysis) {
		params.Talking = true
	}
	return params
}
