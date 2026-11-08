package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"

	"yingce/backend/internal/model"
	"yingce/backend/internal/workbench"
)

// 商品视频复刻的逐镜头生成与合成。每个镜头版本对应一个普通工作台视频任务：
// 预扣、失败退款、超时、重启恢复与取消都由任务系统负责，这里只维护镜头版本与任务的关联。
// 合成使用时间线渲染任务（本地 ffmpeg，不计费）。

var videoReplicationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// videoReplicationSubmitGrace 是提交中版本等待任务回执的时间；超时后按 RequestID 找回任务，找不到才判定未提交。
const videoReplicationSubmitGrace = 2 * time.Minute

type VideoReplicationGenerateInput struct {
	IdempotencyKey string   `json:"idempotencyKey"`
	ShotIDs        []string `json:"shotIds,omitempty"`
	// Regenerate 为 true 时为 ShotIDs 中的镜头新建版本，即使已有成功版本；否则只补齐还没有成功版本的镜头。
	Regenerate bool `json:"regenerate,omitempty"`
}

type VideoReplicationComposeInput struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

func videoReplicationRunActive(status string) bool {
	return status == model.VideoReplicationRunSubmitting || status == model.VideoReplicationRunQueued || status == model.VideoReplicationRunRunning
}

func videoReplicationOutputActive(status string) bool {
	return status == "queued" || status == "running" || status == "submitting"
}

type videoReplicationRunParameters struct {
	Seconds    int                       `json:"seconds"`
	Ratio      string                    `json:"ratio,omitempty"`
	Resolution string                    `json:"resolution,omitempty"`
	Model      string                    `json:"model,omitempty"`
	ModelLabel string                    `json:"modelLabel,omitempty"`
	VideoType  string                    `json:"videoType"`
	References []map[string]string       `json:"references"`
	Mentions   []VideoReplicationMention `json:"mentions"`
	Strategy   string                    `json:"strategy"`
	TailFrame  string                    `json:"tailFrameResourceId,omitempty"`
	// TailRun 是尾帧衔接时取尾帧的上一段版本；上一段换了版本，本段需要按新尾帧重做。
	TailRun string `json:"tailRunId,omitempty"`
}

func (s *Service) GenerateVideoReplication(userID, projectID string, input VideoReplicationGenerateInput, traceID string) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(input.IdempotencyKey)
	if !videoReplicationKeyPattern.MatchString(key) {
		return nil, BadAuthRequest("提交标识无效")
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if project.Status == model.VideoReplicationStatusAnalyzing {
		return nil, BadAuthRequest("参考视频分析中，请等待分镜生成")
	}
	// 同一提交标识重放（网络重试、双击）：已经处理过就直接返回当前状态，不继续提交后续片段。
	if used, err := s.repo.VideoReplicationKeyUsed(projectID, key); err != nil {
		return nil, err
	} else if used {
		return s.VideoReplicationProject(userID, projectID)
	}
	plan, err := requireConfirmedVideoReplicationPlan(project)
	if err != nil {
		return nil, err
	}
	if input.Regenerate && project.Paused {
		// 用户主动重新生成单个镜头视为继续；预算仍会在提交前检查。
		_ = s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"paused": false, "pause_reason": ""})
		project.Paused = false
	}
	submitted, failure, err := s.submitVideoReplication(project, plan, key, input.ShotIDs, input.Regenerate, traceID)
	if err != nil {
		return nil, err
	}
	// AI 配音与画面并行提交：每个有口播的镜头一个 TTS 任务，已成功或进行中的不重复提交。
	if plan.AudioMode == videoReplicationAudioTTS && failure == "" {
		voiced, voiceFailure, err := s.submitVideoReplicationVoiceRuns(project, plan, key)
		if err != nil {
			return nil, err
		}
		submitted += voiced
		failure = voiceFailure
	}
	view, err := s.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, err
	}
	if submitted == 0 && failure != "" {
		return nil, BadAuthRequest(failure)
	}
	return view, nil
}

type videoReplicationTarget struct {
	shotID  string
	index   int
	seconds int
	prompt  string
	tailOf  string
	// sourceStartMs / sourceEndMs：原片编辑模式下这一段对应的参考视频区间。
	sourceStartMs int64
	sourceEndMs   int64
	// references 是本段实际发送的参考图（原片编辑带元素参考图时比项目级列表多）；为空时用项目级列表。
	references []map[string]string
}

// submitVideoReplication 按已确认方案提交视频生成任务。
// 原生直出只会提交一个整片任务；智能分段在「尾帧衔接」时按顺序一次只提交下一段，否则并行提交缺失片段。
// 每次提交前按真实账单累计检查预算，超出即暂停，不会静默追加付费任务。
func (s *Service) submitVideoReplication(project *model.VideoReplicationProject, plan *VideoReplicationPlan, key string, shotIDs []string, regenerate bool, traceID string) (int, string, error) {
	userID, projectID := project.UserID, project.ID
	storyboard := videoReplicationStoryboard(project)
	if storyboard == nil || len(storyboard.Shots) == 0 {
		return 0, "", BadAuthRequest("请先分析参考视频并确认分镜")
	}
	params := videoReplicationParameters(project)
	if err := validateVideoReplicationTaskCount(params, plan.TaskCount); err != nil {
		return 0, "", err
	}
	if err := videoReplicationValidateNativeSubtitles(plan, *storyboard, params); err != nil {
		return 0, "", err
	}
	params = videoReplicationWithRegionTarget(project, params)
	params = videoReplicationWithDetectedTalking(params, videoReplicationSavedAnalysis(project))
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return 0, "", err
	}
	if missing := videoReplicationRequirement(project.VideoType, params, assets); missing != "" {
		return 0, "", BadAuthRequest(missing)
	}
	if err := s.videoReplicationModelAllowed(params); err != nil {
		return 0, "", err
	}
	mentions, err := videoReplicationMentions(project.Prompt, assets)
	if err != nil {
		return 0, "", err
	}
	referenceParams := params
	if videoReplicationProductLocked(params) {
		referenceParams = params
		if plan.Strategy != VideoReplicationStrategySegmented || plan.Continuity == "tail_frame" {
			return 0, "", BadAuthRequest("商品框选锁定需要逐段使用原视频编辑，请重新分析并使用原片分段方案")
		}
	}
	referenceParams, err = videoReplicationProductLockReferenceParams(project.VideoType, referenceParams, assets)
	if err != nil {
		return 0, "", err
	}
	references := videoReplicationReferences(project.VideoType, referenceParams, assets, mentions)
	if len(references) == 0 && len(videoReplicationTargets(project.VideoType)) > 0 {
		return 0, "", BadAuthRequest("所选视频模型不支持参考图输入，无法完成商品/人物替换。请换用支持图生视频或参考图生视频的模型")
	}
	references, err = s.videoReplicationWithProductLockReference(project, params, assets, references)
	if err != nil {
		return 0, "", err
	}
	if err := videoReplicationValidateReferences(project.VideoType, assets, references); err != nil {
		return 0, "", err
	}
	// 替换音频：参考音色随每个片段一起发送，并在提示词里绑定到画面人物；模型收不了参考音频时直接拒绝，不静默换成随机音色。
	if storyboard.VoiceAdapted && plan.AudioMode == videoReplicationAudioOriginal {
		return 0, "", BadAuthRequest("已改写的商品口播不能被原视频旧人声覆盖，请重新确认声音方案")
	}
	voices := []string{}
	if plan.AudioMode == videoReplicationAudioNative {
		voices = videoReplicationVoiceAudios(assets, videoReplicationMaxVoiceAudios)
		// 用户没上传参考音色：用一键生成时从原视频截出的说话人人声，新台词保持原视频的音色。
		if len(voices) == 0 && plan.SourceVoice != "" && params.ReferenceMode == "reference" && params.MaxReferenceAudios > 0 {
			voices = []string{plan.SourceVoice}
		}
		if len(voices) > 0 && (params.ReferenceMode != "reference" || params.MaxReferenceAudios <= 0) {
			return 0, "", BadAuthRequest("所选视频模型不支持参考音频，成片无法使用你上传的参考音色；请换用支持参考音频的模型（如 Seedance 2.0），或把声音改为保留原声 / 静音")
		}
		if len(voices) > params.MaxReferenceAudios {
			return 0, "", BadAuthRequest(fmt.Sprintf("所选视频模型最多接收 %d 段参考音频，你上传了 %d 段参考音色；请删掉多余的，或换用支持更多参考音频的模型", params.MaxReferenceAudios, len(voices)))
		}
		if storyboard.VoiceAdapted && len(voices) == 0 && !videoReplicationEditMode(params) {
			return 0, "", BadAuthRequest("改写口播缺少可用的原视频音色参考；请使用支持参考视频的模型，或上传参考音色后重新确认")
		}
	}
	runs, err := s.repo.VideoReplicationRuns(userID, projectID)
	if err != nil {
		return 0, "", err
	}
	succeeded := map[string]model.VideoReplicationShotRun{}
	active := map[string]bool{}
	for _, run := range runs {
		if run.Kind == "voice" {
			continue
		}
		if run.Status == model.VideoReplicationRunSucceeded && run.OutputResourceID != "" {
			if current, ok := succeeded[run.ShotID]; !ok || run.Selected || (!current.Selected && run.Version > current.Version) {
				succeeded[run.ShotID] = run
			}
		}
		if videoReplicationRunActive(run.Status) {
			active[run.ShotID] = true
		}
	}
	wanted := map[string]bool{}
	for _, id := range shotIDs {
		wanted[strings.TrimSpace(id)] = true
	}
	brief := videoReplicationReadablePrompt(project.Prompt, assets)
	negative := videoReplicationKnowledgeNegative(storyboard.KnowledgeID)
	targets := []videoReplicationTarget{}
	if plan.Strategy == VideoReplicationStrategyNative {
		_, done := succeeded[videoReplicationNativeShotID]
		if (!done || regenerate) && !active[videoReplicationNativeShotID] {
			targets = append(targets, videoReplicationTarget{shotID: videoReplicationNativeShotID, seconds: plan.TargetSeconds, prompt: videoReplicationWithUserDirectiveWithin(videoReplicationNativePrompt(project.VideoType, params, *storyboard, plan.TargetSeconds, "", references), brief, storyboard.Changes, videoReplicationPromptBudgetFor(params))})
		}
	} else {
		chain := plan.Continuity == "tail_frame"
		for index, shot := range storyboard.Shots {
			if chain && len(active) > 0 {
				break
			}
			if len(wanted) > 0 && !wanted[shot.ID] {
				continue
			}
			if _, done := succeeded[shot.ID]; (done && !regenerate) || active[shot.ID] {
				continue
			}
			shotPrompt := videoReplicationShotPrompt(project.VideoType, params, shot, "", references)
			// 逐段生成同样逐个点名本段要换的画面元素，并全片清除用户要求换掉的原元素。
			if clauses := videoReplicationSceneEditClauses(shot.SceneEdits, "参考画面"); len(clauses) > 0 {
				shotPrompt = "本段画面逐项改：" + strings.Join(clauses, "；") + "。\n" + shotPrompt
			}
			if rule := videoReplicationBannedRule(storyboard.Banned, "画面"); rule != "" {
				shotPrompt = rule + "\n" + shotPrompt
			}
			if videoReplicationHasTarget(project.VideoType, VideoReplicationTypeProduct) && len(references) > 0 {
				// 逐段生成同样按存在感约束商品大小，并排除商品图里的布景，免得每段都变成商品图的翻拍。
				if rule := videoReplicationPresenceRule(normalizeVideoReplicationPresence(shot.ProductPresence), "参考视频"); rule != "" {
					shotPrompt += "\n" + rule
				}
				shotPrompt += "\n" + videoReplicationReferenceOnlyRule("商品参考图", *storyboard)
			}
			target := videoReplicationTarget{shotID: shot.ID, index: index, seconds: snapVideoReplicationSeconds(shot.DurationSec, params.ShotSeconds), prompt: videoReplicationWithUserDirectiveWithin(videoReplicationPromptBudgetFor(params).clip(shotPrompt), brief, storyboard.Changes, videoReplicationPromptBudgetFor(params))}
			if videoReplicationEditMode(params) && shot.SourceEndMs > shot.SourceStartMs {
				// 原片编辑：不再按文字重画场景，以原视频片段为底片只替换目标。
				// 带上清单里新元素的参考图（排在替换素材后面，编号不打乱）。
				target.references = videoReplicationWithElements(references, storyboard.Elements, shot, params.MaxReferenceImages)
				if err := videoReplicationValidateProductLockReferences(project.VideoType, params, assets, target.references); err != nil {
					return 0, "", err
				}
				target.prompt = videoReplicationPromptBudgetFor(params).without(negative).clip(videoReplicationEditPrompt(project.VideoType, params, shot, *storyboard, brief, target.references))
				target.sourceStartMs, target.sourceEndMs = shot.SourceStartMs, shot.SourceEndMs
			}
			if chain && index > 0 {
				previous, ok := succeeded[storyboard.Shots[index-1].ID]
				if !ok {
					// 尾帧衔接必须等上一段完成；这里不提交，等上一段成功后由同步阶段继续。
					break
				}
				target.tailOf = previous.OutputResourceID
			}
			targets = append(targets, target)
			if chain {
				break
			}
		}
	}
	if len(targets) == 0 {
		return 0, "", nil
	}
	if len(videoReplicationVoiceAudios(assets, 1)) > 0 {
		for _, target := range targets {
			if target.sourceEndMs > target.sourceStartMs && videoReplicationVoiceDoesNotFit(videoReplicationVoiceBudgetMs(params, target.seconds), len(voices)) {
				return 0, "", BadAuthRequest(videoReplicationVoiceCapacityMessage)
			}
		}
	}
	if videoReplicationProductLocked(params) {
		for _, target := range targets {
			if target.sourceEndMs <= target.sourceStartMs {
				return 0, "", BadAuthRequest("商品框选锁定缺少对应原视频片段，请重新分析后再生成")
			}
		}
	}
	if storyboard.VoiceAdapted && plan.AudioMode == videoReplicationAudioNative {
		for _, target := range targets {
			if target.tailOf != "" || (len(voices) == 0 && target.sourceEndMs <= target.sourceStartMs) {
				return 0, "", BadAuthRequest("改写口播的片段缺少实际音色输入，请使用原视频分段编辑或带参考音色的生成方案")
			}
		}
	}
	spent, err := s.videoReplicationSpentCredits(userID, runs)
	if err != nil {
		return 0, "", err
	}
	submitted := 0
	for _, target := range targets {
		estimate := videoReplicationTaskEstimate(plan, target.seconds)
		if plan.BudgetCredits > 0 && spent+estimate > plan.BudgetCredits+1e-6 {
			reason := fmt.Sprintf("已暂停：已消耗约 %.2f 积分，继续提交将超过确认预算 %.2f 积分；提高预算并重新确认后继续", spent, plan.BudgetCredits)
			_ = s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"paused": true, "pause_reason": reason})
			return submitted, reason, nil
		}
		runKey := key + ":" + target.shotID
		if _, err := s.repo.VideoReplicationRunByKey(projectID, runKey); err == nil {
			continue
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return submitted, "", err
		}
		targetRefs := references
		if target.references != nil {
			targetRefs = target.references
		}
		if err := videoReplicationValidateReferences(project.VideoType, assets, targetRefs); err != nil {
			return submitted, "", err
		}
		taskReferences := make([]workbench.TaskReference, 0, len(targetRefs))
		tailFrame := ""
		targetVoices := voices
		if target.tailOf == "" && target.sourceEndMs > target.sourceStartMs && len(voices) > 0 {
			// 上游要求参考音视频合计不超过模型参考时长上限：音色按本段原片片段剩下的时长截短。
			fitted, reason, err := s.videoReplicationFitVoices(userID, voices, videoReplicationVoiceBudgetMs(params, target.seconds), plan.SourceVoice != "" && len(videoReplicationVoiceAudios(assets, 1)) == 0)
			if err != nil {
				return submitted, "截取参考音色失败：" + err.Error(), nil
			}
			if reason != "" {
				return submitted, reason, nil
			}
			targetVoices = fitted
		}
		if target.tailOf != "" {
			tail, err := s.extractDramaTailFrame(userID, target.tailOf)
			if err != nil {
				return submitted, "上一段尾帧抽取失败：" + err.Error(), nil
			}
			tailFrame = tail
			taskReferences = append(taskReferences, workbench.TaskReference{ResourceID: tail, Kind: "image", Role: "first_frame"})
		} else {
			if target.sourceEndMs > target.sourceStartMs {
				clip, err := s.videoReplicationSourceClip(project, params, target.shotID, target.sourceStartMs, target.seconds, storyboard.VoiceAdapted && plan.AudioMode == videoReplicationAudioNative && len(targetVoices) == 0)
				if err != nil {
					return submitted, "截取原视频片段失败：" + err.Error(), nil
				}
				taskReferences = append(taskReferences, workbench.TaskReference{ResourceID: clip, Kind: "video", Role: "reference"})
			}
			for _, reference := range targetRefs {
				taskReferences = append(taskReferences, workbench.TaskReference{ResourceID: reference["resourceId"], Kind: "image", Role: reference["role"]})
			}
			for _, voice := range targetVoices {
				taskReferences = append(taskReferences, workbench.TaskReference{ResourceID: voice, Kind: "audio", Role: "reference"})
			}
		}
		prompt := target.prompt
		// 声音和台词要求接在画面后面，反提示词放在最后；都按后台模型配置的提示词上限预留位置，超长时只截画面描述。
		lockRule := videoReplicationProductLockRule(params, targetRefs)
		if videoReplicationProductLocked(params) && lockRule == "" {
			return submitted, "商品框选定位图未进入本段参考素材，请重新分析后再生成", nil
		}
		visualRefs := targetRefs
		if target.tailOf != "" {
			visualRefs = nil
		}
		required := strings.TrimSpace(lockRule + "\n" + videoReplicationVisualContract(project.VideoType, params, visualRefs))
		if params.ReplaceMode != "precise-person" && target.sourceEndMs > target.sourceStartMs {
			required += "\n以 @视频1 为编辑底片，只换指定目标；镜头、机位、构图、运镜、切点与动作节奏沿用原片，未指定替换的对象保持原样。\n" + videoReplicationSubtitleRule
		}
		budget := videoReplicationPromptBudgetFor(params).without(negative).without(required)
		if budget.limit <= 0 {
			return submitted, "模型提示词上限不足以容纳商品定位和生成要求，请提高提示词上限或更换模型", nil
		}
		if plan.AudioMode == videoReplicationAudioNative {
			sentVoices := len(targetVoices)
			if target.tailOf != "" {
				sentVoices = 0
			}
			// 不勾选「保留原声」也没传参考音色的普通视频：模型按画面重新生成声音，不强行让人物说原台词；口播视频照常说原台词。
			lines := ""
			if params.AudioMode != "generate" || params.Talking || plan.SpeakLines {
				lines = videoReplicationTargetLines(*storyboard, target)
			}
			sound := videoReplicationSound{Edit: target.sourceEndMs > target.sourceStartMs, Music: videoReplicationMusicNote(project), SpeechSegment: params.AudioMode == "replace" || params.Talking || plan.SpeakLines, Stems: plan.Instrumental != "", SourceVoice: sentVoices > 0 && len(videoReplicationVoiceAudios(assets, 1)) == 0, VoiceHint: plan.VoiceHint, Subtitled: storyboard.Subtitles, Narration: videoReplicationNarrationOnly(videoReplicationSavedAnalysis(project))}
			sound.Delivery, sound.Selling = videoReplicationDeliveryNote(project)
			sound.Pace = videoReplicationPaceRule(lines, target.seconds)
			var refPace string
			deliveryStart, deliveryEnd := target.sourceStartMs, target.sourceEndMs
			for _, shot := range storyboard.Shots {
				if shot.ID == target.shotID {
					deliveryStart, deliveryEnd = shot.SourceStartMs, shot.SourceEndMs
					break
				}
			}
			sound.ReferenceDelivery, refPace = videoReplicationReferenceDelivery(project, deliveryStart, deliveryEnd)
			if refPace != "" {
				sound.Pace = refPace
			}
			prompt = videoReplicationWithVoicePrompt(prompt, lines, sentVoices, targetRefs, sound, budget)
		} else if plan.AudioMode == videoReplicationAudioOriginal && params.Talking {
			prompt = videoReplicationWithLipSyncPrompt(prompt, videoReplicationTargetLines(*storyboard, target), budget)
		} else {
			prompt = budget.clip(prompt)
		}
		if required != "" {
			prompt = required + "\n" + prompt
		}
		if negative != "" {
			prompt += "\n" + negative
		}
		core := videoReplicationReplacementCore(project.VideoType, params, targetRefs, target.sourceEndMs > target.sourceStartMs)
		if err := videoReplicationValidateReplacementPrompt(prompt, core, videoReplicationPromptBudgetFor(params)); err != nil {
			return submitted, "", err
		}
		runParams, _ := json.Marshal(videoReplicationRunParameters{Seconds: target.seconds, Ratio: params.Ratio, Resolution: params.Resolution, Model: params.Model, ModelLabel: params.ModelLabel, VideoType: project.VideoType, References: targetRefs, Mentions: mentions, Strategy: plan.Strategy, TailFrame: tailFrame})
		run := &model.VideoReplicationShotRun{ID: newID(), ProjectID: projectID, UserID: userID, ShotID: target.shotID, IdempotencyKey: runKey, Status: model.VideoReplicationRunSubmitting, Prompt: prompt, ParametersJSON: string(runParams)}
		created, err := s.repo.ReserveVideoReplicationRun(run)
		if err != nil {
			if existing, lookupErr := s.repo.VideoReplicationRunByKey(projectID, runKey); lookupErr == nil && existing != nil {
				continue
			}
			return submitted, "", err
		}
		if !created {
			continue
		}
		parameters := map[string]any{"seconds": target.seconds, "generateAudio": params.GenerateAudio && plan.AudioMode == videoReplicationAudioNative, "watermark": params.Watermark}
		if params.Ratio != "" {
			parameters["ratio"] = params.Ratio
		}
		if params.Resolution != "" {
			parameters["resolution"] = params.Resolution
		}
		request := WorkbenchTaskInput{Kind: WorkbenchKindVideo, Prompt: prompt, Model: params.Model, Parameters: parameters, References: taskReferences}
		if target.sourceEndMs > target.sourceStartMs && target.tailOf == "" && videoReplicationTopViewSourceDuration(params.Model) {
			// 只有 Seedance 2.5 原片编辑沿用 -1；2.0 和其他模型发送本段的合法秒数。
			request.ProviderOptions = map[string]map[string]any{videoReplicationTopViewProtocol: {"duration": -1}}
		}
		if params.LogicalModelID != "" {
			request.LogicalModelID = params.LogicalModelID
		} else {
			request.ChannelID = params.ChannelID
		}
		// RequestID 取镜头版本 ID：进程在建任务后、写回 TaskID 前中断时，同步阶段据此找回任务，避免误判为未提交。
		task, err := s.CreateWorkbenchTask(userID, request, traceID, run.ID)
		if err != nil {
			message := err.Error()
			_ = s.repo.UpdateVideoReplicationRun(run.ID, map[string]any{"status": model.VideoReplicationRunFailed, "error": "提交失败（未扣费）：" + clipText(message, 400)})
			// 积分不足、并发上限、模型不可用时后续任务大概率同样失败：停止提交，由用户处理后重试。
			label := "完整视频"
			if target.shotID != videoReplicationNativeShotID {
				label = fmt.Sprintf("片段 %d", target.index+1)
			}
			return submitted, label + "：" + message, nil
		}
		submitted++
		spent += estimate
		_ = s.repo.UpdateVideoReplicationRun(run.ID, map[string]any{"task_id": task.ID, "status": videoReplicationRunStatus(task.Status)})
	}
	return submitted, "", nil
}

func videoReplicationRunStatus(status model.TaskStatus) string {
	switch status {
	case model.TaskStatusQueued:
		return model.VideoReplicationRunQueued
	case model.TaskStatusRunning:
		return model.VideoReplicationRunRunning
	case model.TaskStatusSucceeded:
		return model.VideoReplicationRunSucceeded
	case model.TaskStatusCancelled:
		return model.VideoReplicationRunCancelled
	}
	return model.VideoReplicationRunFailed
}

// videoReplicationReferences 按视频类型挑选参考图：被 @ 引用的素材优先，其次主素材，再按顺序；
// 数量与角色受所选模型能力约束（参考图生视频 / 首帧图生视频）。
func videoReplicationReferences(videoType string, params VideoReplicationParameters, assets []model.VideoReplicationAsset, mentions []VideoReplicationMention) []map[string]string {
	if params.ReferenceMode == "none" || params.ReferenceMode == "" {
		return nil
	}
	mentioned := map[string]bool{}
	for _, mention := range mentions {
		mentioned[mention.AssetID] = true
	}
	ordered := func(category string) []VideoReplicationAssetView {
		items := []VideoReplicationAssetView{}
		for _, view := range videoReplicationAssetViews(assets) {
			if view.Category == category {
				items = append(items, view)
			}
		}
		rank := func(view VideoReplicationAssetView) int {
			if mentioned[view.ID] {
				return 0
			}
			if view.IsPrimary {
				return 1
			}
			return 2
		}
		for i := 1; i < len(items); i++ {
			for j := i; j > 0 && rank(items[j]) < rank(items[j-1]); j-- {
				items[j], items[j-1] = items[j-1], items[j]
			}
		}
		return items
	}
	targets := videoReplicationTargets(videoType)
	limit := params.MaxReferenceImages
	role := "reference"
	if params.ReferenceMode == "first_frame" {
		limit, role = 1, "first_frame"
	}
	if limit <= 0 {
		limit = 1
	}
	// 先保证每个目标至少一张，再保留全部商品资料；额外角度不能挤掉包装或参数表。
	queues := make([][]VideoReplicationAssetView, 0, len(targets))
	for _, target := range targets {
		queues = append(queues, ordered(videoReplicationTargetCategory[target]))
	}
	references := []map[string]string{}
	add := func(candidate VideoReplicationAssetView) {
		if len(references) < limit {
			references = append(references, map[string]string{"assetId": candidate.ID, "resourceId": candidate.ResourceID, "label": candidate.Label, "category": candidate.Category, "role": role})
		}
	}
	for _, queue := range queues {
		if len(queue) > 0 {
			add(queue[0])
		}
	}
	if containsString(targets, VideoReplicationTypeProduct) && role == "reference" {
		for _, category := range []string{model.VideoReplicationAssetProductDetailImage, model.VideoReplicationAssetPackagingImage} {
			for _, candidate := range ordered(category) {
				add(candidate)
			}
		}
	}
	for round := 1; len(references) < limit; round++ {
		added := false
		for _, queue := range queues {
			if round < len(queue) && len(references) < limit {
				add(queue[round])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return references
}

// 参考图名额不足时不能悄悄少换一类，也不能丢掉用户提供的包装或展示资料。
func videoReplicationValidateReferences(videoType string, assets []model.VideoReplicationAsset, references []map[string]string) error {
	categories, resources := map[string]bool{}, map[string]bool{}
	for _, ref := range references {
		if ref["resourceId"] != "" {
			categories[ref["category"]], resources[ref["resourceId"]] = true, true
		}
	}
	for _, target := range videoReplicationTargets(videoType) {
		if !categories[videoReplicationTargetCategory[target]] {
			return BadAuthRequest("所选模型的参考图名额不足，无法包含全部替换目标；请换用支持更多参考图的模型后重试")
		}
	}
	if videoReplicationHasTarget(videoType, VideoReplicationTypeProduct) {
		for _, asset := range assets {
			if (asset.Category == model.VideoReplicationAssetProductDetailImage || asset.Category == model.VideoReplicationAssetPackagingImage) && !resources[asset.ResourceID] {
				return BadAuthRequest("所选模型的参考图名额不足，未能包含你上传的全部细节、参数资料或包装图；请换用支持更多参考图的模型，或减少这些素材后重试")
			}
		}
	}
	return nil
}

// videoReplicationUserDirective 是所有复刻提示词（原片编辑、逐段生成、整片直出，任何品类）共用的用户要求段：
// 用户补充要求原文 + 分段规划整理出的画面改动清单，声明为最高优先级，压过「保持不变 / 与原片一致 / 其余物品保持原样」等通用规则。
// source 是原片在提示词里的称呼（原片编辑为 @视频1）。都为空时返回空串。
func videoReplicationUserDirective(brief, changes, source string) string {
	brief, changes = strings.TrimSpace(brief), strings.TrimSpace(changes)
	if brief == "" && changes == "" {
		return ""
	}
	if source == "" {
		source = "原视频"
	}
	var b strings.Builder
	if brief != "" {
		b.WriteString("用户补充要求（最高优先级，必须执行；与下文任何「保持不变 / 与原视频一致 / 保持原样」的规则冲突时以它为准）：" + brief + "\n")
	}
	if changes != "" {
		b.WriteString("画面改动清单（必须执行）：" + changes + "。" + source + "里对应的内容每一帧都按清单改干净，不残留原来的元素、配色和风格；被改元素的位置、大小比例、运动轨迹以及与人物的互动沿用" + source + "中的对应元素，光线氛围与改动后的画面协调统一。\n")
	}
	return b.String()
}

// videoReplicationWithUserDirective 把用户要求段放在逐段生成 / 整片直出提示词的开头：超长截断时先截掉末尾的通用细则，不是用户的要求。
func videoReplicationWithUserDirective(prompt, brief, changes string) string {
	return videoReplicationWithUserDirectiveWithin(prompt, brief, changes, videoReplicationPromptBudget{limit: videoReplicationDefaultPromptBytes})
}

func videoReplicationWithUserDirectiveWithin(prompt, brief, changes string, budget videoReplicationPromptBudget) string {
	directive := videoReplicationUserDirective(brief, changes, "")
	if directive == "" {
		return prompt
	}
	return budget.clip(directive + prompt)
}

func videoReplicationValidateReplacementPrompt(prompt, core string, budget videoReplicationPromptBudget) error {
	if budget.size(prompt) > budget.limit || (core != "" && !strings.Contains(prompt, core)) {
		return BadAuthRequest("所选模型的提示词上限不足，无法完整保留替换要求与台词；请提高模型提示词上限或缩短台词后重试，本段尚未提交生成")
	}
	return nil
}

func videoReplicationShotPrompt(videoType string, params VideoReplicationParameters, shot VideoReplicationShot, brief string, references []map[string]string) string {
	var b strings.Builder
	if core := videoReplicationReplacementCore(videoType, params, references, false); core != "" {
		b.WriteString(core + "\n")
	}
	language := []string{}
	for _, item := range [][2]string{{"景别", shot.ShotSize}, {"运镜", firstNonEmpty(shot.Camera, params.Camera)}, {"光线", firstNonEmpty(shot.Lighting, params.Lighting)}, {"氛围", firstNonEmpty(shot.Mood, params.Mood)}, {"节奏", params.Rhythm}} {
		if strings.TrimSpace(item[1]) != "" {
			language = append(language, item[0]+"："+strings.TrimSpace(item[1]))
		}
	}
	labels := make([]string, 0, len(references))
	for index, reference := range references {
		labels = append(labels, fmt.Sprintf("参考图%d=%s", index+1, reference["label"]))
	}
	if len(labels) > 0 {
		b.WriteString("\n参考图：" + strings.Join(labels, "，") + "。")
	}
	rules := map[string]string{
		VideoReplicationTypeProduct:    "画面中的商品必须与商品参考图完全一致，保持外观、结构、颜色与细节；只取商品本体，不用商品图里的布景、人物和道具。",
		VideoReplicationTypeModel:      "画面人物必须是模特参考图中的人物，保持面部、发型与身份一致；只取人物长相，不取图中的背景、场景、服装和道具。",
		VideoReplicationTypeApparel:    "人物穿着的服装必须与服饰参考图一致，保持款式、颜色、面料与细节；只取服装，不取服饰图里的模特和背景；正常合身地穿好，不穿模、不走光、无原服装残留；新服装没有的口袋、拉链等结构不出现，原片针对它们的动作改为自然动作；未给出的单品搭配风格颜色协调的简洁款。",
		VideoReplicationTypeFace:       "画面人物面部必须与人脸参考图保持一致；只取脸，不取图里的服饰、背景和道具。",
		VideoReplicationTypeBackground: "场景与背景必须换成背景参考图中的环境，只取环境，不引入背景图里的人物或商品；主体位置和动作节奏与原片对应，接触阴影、反射、光线和透视与新背景匹配。",
	}
	replaced := map[string]bool{}
	for _, target := range videoReplicationTargets(videoType) {
		replaced[target] = true
		b.WriteString("\n" + rules[target])
	}
	keep := []string{}
	if !replaced[VideoReplicationTypeProduct] {
		keep = append(keep, "商品")
	}
	if !replaced[VideoReplicationTypeModel] {
		if replaced[VideoReplicationTypeFace] {
			keep = append(keep, "人物发型与身材")
		} else {
			keep = append(keep, "人物长相与身材")
		}
	}
	if !replaced[VideoReplicationTypeApparel] {
		keep = append(keep, "服装与配饰")
	}
	if !replaced[VideoReplicationTypeBackground] {
		keep = append(keep, "场景背景")
	}
	if len(keep) > 0 {
		b.WriteString("\n未选择替换的" + strings.Join(keep, "、") + "沿用原视频（用户明确要求改动的除外）；已选择的类别各按对应参考图替换。")
	}
	if hint := videoReplicationReplaceModeRule(params); hint != "" {
		b.WriteString("\n" + hint)
	}
	if rule := videoReplicationRegionRule(params); rule != "" {
		b.WriteString("\n" + rule)
	}
	b.WriteString("\n" + videoReplicationLifelikeRule)
	if description := strings.TrimSpace(shot.Prompt); description != "" {
		b.WriteString("\n本段画面（与以上替换规则冲突的原对象描述按以上替换）：" + description)
	}
	if len(language) > 0 {
		b.WriteString("\n镜头语言：" + strings.Join(language, "；") + "。")
	}
	if videoReplicationHasTarget(videoType, VideoReplicationTypeModel) || videoReplicationHasTarget(videoType, VideoReplicationTypeFace) {
		source := "参考图只提供人物长相，不照搬图中的表情、姿势和头部角度"
		// 首帧模型：人物照片就是开场画面，只能要求从这一帧开始动起来，不能要求不用照片里的姿势。
		if len(references) > 0 && references[0]["role"] == "first_frame" && (references[0]["category"] == model.VideoReplicationAssetModelImage || references[0]["category"] == model.VideoReplicationAssetFaceImage) {
			source = "参考图是开场画面，人物从这一帧开始自然动起来，不要一直保持照片上的表情和姿势"
		}
		b.WriteString("\n表情：" + source + "；人物表情随动作和情绪自然变化，自然眨眼、转动眼神，" + videoReplicationNaturalFaceRule)
	}
	if adaptation := strings.TrimSpace(shot.Adaptation); adaptation != "" {
		b.WriteString("\n结构：本段手部动作按新商品 / 新服装结构调整：" + adaptation + "；镜头、景别和构图与原片一致，不要画出新商品 / 新服装没有的部件。")
	}
	if shot.Packaging && videoReplicationHasTarget(videoType, VideoReplicationTypeProduct) {
		packaging := ""
		for index, reference := range references {
			if reference["category"] == model.VideoReplicationAssetPackagingImage {
				packaging = fmt.Sprintf("参考图%d", index+1)
				break
			}
		}
		b.WriteString("\n" + videoReplicationPackagingRule(packaging))
	}
	if params.KeepOriginalAudio || params.AudioMode == "keep" {
		b.WriteString("\n节奏：成片沿用原视频的音乐，人物动作、转身、商品展示和镜头切换的时间点与原视频一致，卡准原音乐节拍，不加快也不放慢。")
	}
	if params.Talking {
		b.WriteString("\n口播：这是口播视频，人物面向镜头自然开口说话，表情、手势与说话节奏跟随原视频。")
	}
	if strings.TrimSpace(brief) != "" {
		b.WriteString("\n用户要求：" + strings.TrimSpace(brief))
	}
	b.WriteString("\n" + videoReplicationSubtitleRule)
	return videoReplicationPromptBudgetFor(params).clip(b.String())
}

// syncVideoReplication 把镜头版本与成片版本对齐到任务的真实状态，并据此刷新项目状态。
func (s *Service) syncVideoReplication(ctx context.Context, project *model.VideoReplicationProject) error {
	runs, err := s.repo.VideoReplicationRuns(project.UserID, project.ID)
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, run := range runs {
		if run.Selected {
			selected[run.Kind+":"+run.ShotID] = true
		}
	}
	active, done := 0, 0
	for index := range runs {
		run := &runs[index]
		// A failed delivery may be restored on its original task. Reconcile failed
		// runs too, so a recovered task becomes visible instead of submitting a new
		// paid generation. Explicitly cancelled runs retain the user's choice.
		if !videoReplicationRunActive(run.Status) && run.Status != model.VideoReplicationRunFailed {
			if run.Status == model.VideoReplicationRunSucceeded {
				done++
			}
			continue
		}
		var task *model.Task
		previousStatus, previousTaskID := run.Status, run.TaskID
		if run.TaskID == "" {
			if time.Since(run.CreatedAt) < videoReplicationSubmitGrace {
				active++
				continue
			}
			found, lookupErr := s.repo.TaskForUserByRequestID(project.UserID, run.ID)
			if lookupErr != nil {
				run.Status = model.VideoReplicationRunFailed
				_ = s.repo.UpdateVideoReplicationRun(run.ID, map[string]any{"status": run.Status, "error": "提交中断，未创建生成任务（未扣费），请重新生成"})
				continue
			}
			task = found
			run.TaskID = task.ID
		} else {
			found, lookupErr := s.repo.TaskForUser(project.UserID, run.TaskID)
			if lookupErr != nil {
				run.Status = model.VideoReplicationRunFailed
				_ = s.repo.UpdateVideoReplicationRun(run.ID, map[string]any{"status": run.Status, "error": "生成任务记录不存在"})
				continue
			}
			task = found
		}
		status := videoReplicationRunStatus(task.Status)
		values := map[string]any{"task_id": task.ID, "status": status}
		switch status {
		case model.VideoReplicationRunSucceeded:
			values["error"] = ""
			resourceID := dramaTaskResource(task)
			if resourceID == "" {
				status = model.VideoReplicationRunFailed
				values["status"] = status
				values["error"] = "任务已完成但没有返回媒体文件"
				break
			}
			values["output_resource_id"] = resourceID
			if !selected[run.Kind+":"+run.ShotID] {
				values["selected"] = true
				selected[run.Kind+":"+run.ShotID] = true
			}
			done++
		case model.VideoReplicationRunFailed, model.VideoReplicationRunCancelled:
			values["error"] = clipText(firstNonEmpty(task.Error, task.Stage, "生成失败"), 600)
		default:
			values["error"] = ""
			active++
		}
		if status != previousStatus || task.ID != previousTaskID {
			_ = s.repo.UpdateVideoReplicationRun(run.ID, values)
		}
	}
	plan := videoReplicationPlan(project)
	planValid := plan != nil && project.PlanConfirmedAt != nil && plan.Digest == videoReplicationDigest(project) && plan.StoryboardRevision == project.StoryboardRevision
	storyboard := videoReplicationStoryboard(project)
	// 原生直出：整片任务成功后，模型返回的视频直接成为成片版本，不经过 FFmpeg 剪辑；随后只读质量检查。
	if plan != nil && plan.Strategy == VideoReplicationStrategyNative && plan.AudioMode != videoReplicationAudioTTS {
		latest, err := s.repo.VideoReplicationRuns(project.UserID, project.ID)
		if err != nil {
			return err
		}
		for _, run := range latest {
			if run.ShotID != videoReplicationNativeShotID || run.Status != model.VideoReplicationRunSucceeded || run.OutputResourceID == "" {
				continue
			}
			key := "native:" + run.ID
			if _, err := s.repo.VideoReplicationOutputByKey(project.ID, key); err == nil {
				continue
			}
			runIDs, _ := json.Marshal([]string{run.ID})
			_ = s.repo.CreateVideoReplicationOutput(&model.VideoReplicationOutput{ID: newID(), ProjectID: project.ID, UserID: project.UserID, IdempotencyKey: key, Mode: VideoReplicationStrategyNative, TaskID: run.TaskID, Status: "checking", ResourceID: run.OutputResourceID, RunIDsJSON: string(runIDs), DurationMs: int64(plan.TargetSeconds) * 1000})
		}
	}
	// 智能分段 + 尾帧衔接：上一段成功后自动提交下一段（仍受预算检查）。失败过的片段不自动重试，由用户手动重新生成。
	if planValid && plan.Strategy == VideoReplicationStrategySegmented && plan.Continuity == "tail_frame" && !project.Paused && active == 0 && project.Status != model.VideoReplicationStatusAnalyzing && storyboard != nil {
		latest, err := s.repo.VideoReplicationRuns(project.UserID, project.ID)
		if err != nil {
			return err
		}
		tried := map[string]bool{}
		done := map[string]bool{}
		for _, run := range latest {
			if run.Kind == "voice" {
				continue
			}
			tried[run.ShotID] = true
			if run.Status == model.VideoReplicationRunSucceeded {
				done[run.ShotID] = true
			}
		}
		for index, shot := range storyboard.Shots {
			if done[shot.ID] {
				continue
			}
			if index > 0 && !tried[shot.ID] && done[storyboard.Shots[index-1].ID] {
				submitted, failure, err := s.submitVideoReplication(project, plan, fmt.Sprintf("chain-%d", plan.ConfirmedAt.Unix()), []string{shot.ID}, false, "")
				if err != nil {
					failure = err.Error()
				}
				active += submitted
				if submitted == 0 && failure != "" && !strings.HasPrefix(failure, "已暂停") {
					_ = s.repo.UpdateVideoReplicationProject(project.UserID, project.ID, map[string]any{"paused": true, "pause_reason": "自动续接下一段失败：" + clipText(failure, 240)})
				}
				if refreshed, err := s.repo.VideoReplicationProject(project.UserID, project.ID); err == nil {
					project.Paused, project.PauseReason = refreshed.Paused, refreshed.PauseReason
				}
			}
			break
		}
	}
	// 一键生成：任一段失败立即把整个项目标记为失败，并写明是哪一段、什么原因；
	// 其它段还在生成时也立即报错，不让前端继续等待。
	params := videoReplicationParameters(project)
	autoFailure := ""
	if planValid && params.Auto && storyboard != nil && !project.Paused && project.Status != model.VideoReplicationStatusAnalyzing {
		retried, failure := s.retryVideoReplicationAuto(project, plan, storyboard, active > 0)
		active += retried
		autoFailure = failure
	}
	// 替换音频且分离出了原片伴奏：每段一生成完就开始在后台分离人声，等全部完成时多数段已经分离好。
	if planValid && plan.Instrumental != "" {
		if latest, err := s.repo.VideoReplicationRuns(project.UserID, project.ID); err == nil {
			chosen, _ := videoReplicationChosenRuns(latest)
			s.videoReplicationSegmentVocals(project.UserID, chosen)
		}
	}
	// 全部片段（与配音）成功后自动合成完整视频，用户不需要再点合成；某段重新生成后版本变化会再合成一次。
	if planValid && storyboard != nil && active == 0 && !project.Paused && (plan.Strategy == VideoReplicationStrategySegmented || plan.AudioMode == videoReplicationAudioTTS) {
		if latest, err := s.repo.VideoReplicationRuns(project.UserID, project.ID); err == nil && videoReplicationComposeReady(*storyboard, plan, latest) == "" {
			if err := s.composeVideoReplicationOutput(project, plan, videoReplicationAutoComposeKey(*storyboard, plan, latest)); err != nil && err != errVideoReplicationStemsPending {
				slog.Warn("video replication auto compose failed", "projectId", project.ID, "error", err)
			}
		}
	}
	outputs, err := s.repo.VideoReplicationOutputs(project.UserID, project.ID)
	if err != nil {
		return err
	}
	wantAudio := plan != nil && (plan.AudioMode == videoReplicationAudioNative || plan.AudioMode == videoReplicationAudioOriginal || plan.AudioMode == videoReplicationAudioTTS)
	composing, checking := false, false
	for index := range outputs {
		output := &outputs[index]
		if (output.Status == "queued" || output.Status == "running") && output.TaskID != "" {
			task, lookupErr := s.repo.TaskForUser(project.UserID, output.TaskID)
			if lookupErr != nil {
				output.Status = "failed"
				_ = s.repo.UpdateVideoReplicationOutput(output.ID, map[string]any{"status": "failed", "error": "合成任务记录不存在"})
				continue
			}
			values := map[string]any{"status": string(task.Status)}
			switch task.Status {
			case model.TaskStatusSucceeded:
				var result timelineRenderResult
				if json.Unmarshal([]byte(task.ResultJSON), &result) != nil || result.ResourceID == "" {
					values["status"], values["error"] = "failed", "合成完成但没有返回成片文件"
				} else {
					// 渲染成功不等于成片合格：先进入质量检查。
					values["status"], values["resource_id"], values["subtitle_srt"], values["duration_ms"] = "checking", result.ResourceID, result.SubtitleSRT, result.DurationMs
					output.ResourceID, output.DurationMs = result.ResourceID, result.DurationMs
				}
			case model.TaskStatusFailed, model.TaskStatusCancelled:
				values["error"] = clipText(firstNonEmpty(task.Error, task.Stage, "合成失败"), 600)
			}
			output.Status = values["status"].(string)
			_ = s.repo.UpdateVideoReplicationOutput(output.ID, values)
		}
		if output.Status == "checking" || output.Status == "inspecting" {
			checking = true
			s.runVideoReplicationQuality(project.UserID, *output, wantAudio)
		}
		if output.Status == "queued" || output.Status == "running" {
			composing = true
		}
	}
	if project.Status == model.VideoReplicationStatusAnalyzing {
		return nil
	}
	native := plan != nil && plan.Strategy == VideoReplicationStrategyNative
	latestStatus := ""
	if len(outputs) > 0 {
		latestStatus = outputs[0].Status
	}
	next, stage, progress := project.Status, project.Stage, project.Progress
	switch {
	case autoFailure != "":
		next, stage, progress = model.VideoReplicationStatusFailed, clipText(autoFailure, 300), 100
	case active > 0:
		next = model.VideoReplicationStatusGenerating
		if native {
			stage, progress = "完整视频生成中（原生直出，1 个任务）", 50
		} else {
			stage = fmt.Sprintf("片段生成中：%d 个进行中", active)
			if storyboard != nil && len(storyboard.Shots) > 0 {
				videoDone := 0
				for key := range selected {
					if strings.HasPrefix(key, "video:") {
						videoDone++
					}
				}
				progress = min(99, videoDone*100/len(storyboard.Shots))
				if params.Auto {
					stage = fmt.Sprintf("视频生成中：已完成 %d/%d 段", videoDone, len(storyboard.Shots))
				}
			}
		}
	case composing:
		next, stage, progress = model.VideoReplicationStatusComposing, "自动合成中（FFmpeg 拼接片段）", 85
	case checking:
		next, stage, progress = model.VideoReplicationStatusComposing, "质量检查中", 95
	case latestStatus == "succeeded":
		next, stage, progress = model.VideoReplicationStatusCompleted, "完整视频已完成", 100
	case latestStatus == "quality_failed":
		next, stage, progress = model.VideoReplicationStatusFailed, "成片质量检查未通过："+clipText(outputs[0].Error, 200), 100
	case project.Paused:
		next, stage = model.VideoReplicationStatusAnalyzed, project.PauseReason
	case project.Status == model.VideoReplicationStatusFailed && latestStatus == "":
	case storyboard != nil:
		next = model.VideoReplicationStatusAnalyzed
		if params.Auto && planValid {
			stage = "正在提交生成任务"
		} else if done > 0 {
			stage = fmt.Sprintf("已生成 %d 个片段", done)
		} else {
			stage = "分镜已生成，等待确认方案"
		}
		progress = 100
	default:
		next, stage, progress = model.VideoReplicationStatusDraft, "", 0
	}
	// stage 列是 varchar(200)：上游英文错误、暂停原因可能超长，超长写入会让每次同步都失败。
	stage = videoReplicationClipRunes(strings.ToValidUTF8(stage, ""), 200)
	if next != project.Status || stage != project.Stage || progress != project.Progress {
		project.Status, project.Stage, project.Progress = next, stage, progress
		values := map[string]any{"status": next, "stage": stage, "progress": progress}
		if autoFailure != "" {
			values["error"] = clipText(autoFailure, 600)
		}
		return s.repo.UpdateVideoReplicationProject(project.UserID, project.ID, values)
	}
	return nil
}

func (s *Service) SelectVideoReplicationRun(userID, projectID, runID string) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	if err := s.repo.SelectVideoReplicationRun(userID, projectID, runID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("镜头版本不存在")
		}
		return nil, BadAuthRequest(err.Error())
	}
	return s.VideoReplicationProject(userID, projectID)
}

// CancelVideoReplication 取消进行中的镜头与合成任务；费用按任务系统规则结算（未执行的预扣退回）。
func (s *Service) CancelVideoReplication(ctx context.Context, userID, projectID string) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if err := s.syncVideoReplication(ctx, project); err != nil {
		return nil, err
	}
	runs, err := s.repo.VideoReplicationRuns(userID, projectID)
	if err != nil {
		return nil, err
	}
	var failures []string
	for _, run := range runs {
		if !videoReplicationRunActive(run.Status) || run.TaskID == "" {
			continue
		}
		if _, err := s.CancelTask(ctx, userID, run.TaskID); err != nil {
			failures = append(failures, err.Error())
		}
	}
	outputs, err := s.repo.VideoReplicationOutputs(userID, projectID)
	if err != nil {
		return nil, err
	}
	for _, output := range outputs {
		if (output.Status == "queued" || output.Status == "running") && output.TaskID != "" {
			if _, err := s.CancelTask(ctx, userID, output.TaskID); err != nil {
				failures = append(failures, err.Error())
			}
		}
	}
	view, err := s.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, err
	}
	if len(failures) > 0 {
		return nil, BadAuthRequest("部分任务取消失败：" + failures[0])
	}
	return view, nil
}

func videoReplicationCanvasSize(ratio, resolution string) (int, int) {
	short := 720
	if strings.HasPrefix(strings.TrimSpace(resolution), "1080") {
		short = 1080
	} else if strings.HasPrefix(strings.TrimSpace(resolution), "480") {
		short = 480
	}
	parts := strings.Split(strings.TrimSpace(ratio), ":")
	if len(parts) != 2 {
		return short * 16 / 9 &^ 1, short
	}
	var w, h float64
	if _, err := fmt.Sscanf(parts[0]+" "+parts[1], "%g %g", &w, &h); err != nil || w <= 0 || h <= 0 {
		return short * 16 / 9 &^ 1, short
	}
	if w >= h {
		return int(float64(short)*w/h) &^ 1, short
	}
	return short, int(float64(short)*h/w) &^ 1
}

// videoReplicationChosenRuns 返回每个分镜采用的画面版本（采用版本优先，否则最新成功版本）与每个分镜的配音版本。
func videoReplicationChosenRuns(runs []model.VideoReplicationShotRun) (map[string]model.VideoReplicationShotRun, map[string]model.VideoReplicationShotRun) {
	video := map[string]model.VideoReplicationShotRun{}
	voice := map[string]model.VideoReplicationShotRun{}
	for _, run := range runs {
		if run.Status != model.VideoReplicationRunSucceeded || run.OutputResourceID == "" {
			continue
		}
		target := video
		if run.Kind == "voice" {
			target = voice
		}
		if current, ok := target[run.ShotID]; !ok || run.Selected || (!current.Selected && run.Version > current.Version) {
			target[run.ShotID] = run
		}
	}
	return video, voice
}

type videoReplicationMedia = struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	StorageKey string `json:"storageKey"`
}

func videoReplicationMediaRef(id, kind string) *videoReplicationMedia {
	return &videoReplicationMedia{ID: id, Kind: kind, StorageKey: "resource:" + id}
}

// buildVideoReplicationTimeline 把画面版本按分镜顺序拼成时间线。
// 分段在参考视频自己的镜头切点处衔接，拼接处就是原片的硬切；配音按镜头起点叠加成一条连续音轨，
// 有配音时画面片段静音，避免每段模型音频在切点处断开。native 方案只有一个整片片段。
func buildVideoReplicationTimeline(storyboard VideoReplicationStoryboard, params VideoReplicationParameters, plan *VideoReplicationPlan, runs []model.VideoReplicationShotRun, durationOf func(resourceID string) int64) (renderProject, []string, []string) {
	video, voice := videoReplicationChosenRuns(runs)
	width, height := videoReplicationCanvasSize(params.Ratio, params.Resolution)
	timeline := renderProject{Version: 1, Width: width, Height: height, Fit: "crop", Tracks: []renderTrack{{ID: "video", Kind: "video"}, {ID: "subtitle", Kind: "subtitle"}}}
	dubbed := plan != nil && plan.AudioMode == videoReplicationAudioTTS
	if dubbed || (plan != nil && plan.AudioMode == videoReplicationAudioMute) {
		timeline.Tracks[0].Muted = true
	}
	if dubbed {
		timeline.Tracks = append(timeline.Tracks, renderTrack{ID: "voice", Kind: "audio"})
	}
	addVoice := func(shotID string, startMs, maxMs int64) []string {
		run, ok := voice[shotID]
		if !dubbed || !ok {
			return nil
		}
		length := durationOf(run.OutputResourceID)
		if length <= 0 || length > maxMs {
			length = maxMs
		}
		timeline.Clips = append(timeline.Clips, renderClip{ID: "voice-" + shotID, Kind: "audio", TrackID: "voice", StartMs: startMs, DurationMs: length, SourceDurationMs: length, Volume: 1, DirectMedia: videoReplicationMediaRef(run.OutputResourceID, "audio")})
		return []string{run.ID}
	}
	addSubtitles := func(shot VideoReplicationShot, startMs, durationMs int64) {
		if params.Subtitles && strings.TrimSpace(shot.Subtitle) != "" {
			timeline.Clips = append(timeline.Clips, renderClip{ID: "sub-" + shot.ID, Kind: "subtitle", TrackID: "subtitle", StartMs: startMs, DurationMs: durationMs, Text: shot.Subtitle})
		} else if storyboard.Subtitles {
			// 字幕内容取最终稿；时间按字数估算，不是字级语音对齐。
			text := firstNonEmpty(strings.TrimSpace(shot.Voiceover), strings.TrimSpace(shot.Subtitle))
			if storyboard.SubtitlesAdapted {
				text = firstNonEmpty(strings.TrimSpace(shot.Subtitle), strings.TrimSpace(shot.Voiceover))
			}
			for cueIndex, cue := range videoReplicationSubtitleCues(text, startMs, durationMs) {
				cue.ID = fmt.Sprintf("sub-%s-%d", shot.ID, cueIndex+1)
				timeline.Clips = append(timeline.Clips, cue)
			}
		}
	}
	runIDs, missing := []string{}, []string{}
	if plan != nil && plan.Strategy == VideoReplicationStrategyNative {
		run, ok := video[videoReplicationNativeShotID]
		if !ok {
			return timeline, runIDs, []string{"完整视频"}
		}
		total := durationOf(run.OutputResourceID)
		if total <= 0 {
			total = int64(plan.TargetSeconds) * 1000
		}
		timeline.Clips = append(timeline.Clips, renderClip{ID: "full", Kind: "video", TrackID: "video", StartMs: 0, DurationMs: total, SourceDurationMs: total, Volume: 1, DirectMedia: videoReplicationMediaRef(run.OutputResourceID, "video")})
		runIDs = append(runIDs, run.ID)
		// 原生整片按分镜时间轴把每句配音放到对应时间点。
		cursor := int64(0)
		for _, shot := range storyboard.Shots {
			if cursor >= total {
				break
			}
			length := int64(snapVideoReplicationSeconds(shot.DurationSec, nil)) * 1000
			if storyboard.VoiceAdapted || storyboard.SubtitlesAdapted {
				addSubtitles(shot, cursor, min(length, total-cursor))
			}
			runIDs = append(runIDs, addVoice(shot.ID, cursor, min(length, total-cursor))...)
			if storyboard.Subtitles && strings.TrimSpace(shot.Voiceover) != "" {
				for cueIndex, cue := range videoReplicationSubtitleCues(shot.Voiceover, cursor, min(length, total-cursor)) {
					cue.ID = fmt.Sprintf("sub-%s-%d", shot.ID, cueIndex+1)
					timeline.Clips = append(timeline.Clips, cue)
				}
			}
			cursor += length
		}
		timeline.DurationMs = total
		return timeline, runIDs, nil
	}
	cursor := int64(0)
	for index, shot := range storyboard.Shots {
		run, ok := video[shot.ID]
		if !ok {
			missing = append(missing, fmt.Sprint(index+1))
			continue
		}
		durationMs := int64(snapVideoReplicationSeconds(shot.DurationSec, params.ShotSeconds)) * 1000
		if actual := durationOf(run.OutputResourceID); actual > 0 {
			durationMs = actual
		}
		// 模型只能出整秒的视频（14.77 秒的原片段会生成 15 秒）：按原片段长度截掉多出的尾巴，
		// 让每段在成片里的起点与原视频一致，叠加原音轨时声音和画面不会越拼越错位。
		if span := shot.SourceEndMs - shot.SourceStartMs; span >= videoReplicationMinSegmentSeconds*500 && durationMs > span {
			durationMs = span
		}
		clip := renderClip{ID: "shot-" + shot.ID, Kind: "video", TrackID: "video", StartMs: cursor, DurationMs: durationMs, SourceDurationMs: durationMs, Volume: 1, DirectMedia: videoReplicationMediaRef(run.OutputResourceID, "video")}
		if params.Transition == "fade" {
			clip.FadeInMs, clip.FadeInColor, clip.FadeOutMs, clip.FadeOutColor = 200, "black", 200, "black"
		}
		timeline.Clips = append(timeline.Clips, clip)
		addSubtitles(shot, cursor, durationMs)
		runIDs = append(runIDs, run.ID)
		runIDs = append(runIDs, addVoice(shot.ID, cursor, durationMs)...)
		cursor += durationMs
	}
	timeline.DurationMs = cursor
	return timeline, runIDs, missing
}

// videoReplicationComposeReady 判断画面（与需要时的配音）是否全部成功、没有进行中的任务；返回缺失说明。
func videoReplicationComposeReady(storyboard VideoReplicationStoryboard, plan *VideoReplicationPlan, runs []model.VideoReplicationShotRun) string {
	video, voice := videoReplicationChosenRuns(runs)
	for _, run := range runs {
		if videoReplicationRunActive(run.Status) {
			return "仍有任务在生成"
		}
	}
	if plan.Strategy == VideoReplicationStrategyNative {
		if _, ok := video[videoReplicationNativeShotID]; !ok {
			return "完整视频还没有生成成功"
		}
	} else {
		for index, shot := range storyboard.Shots {
			if _, ok := video[shot.ID]; !ok {
				return fmt.Sprintf("片段 %d 还没有成功版本", index+1)
			}
		}
	}
	if plan.AudioMode == videoReplicationAudioTTS {
		for index, shot := range storyboard.Shots {
			if strings.TrimSpace(shot.Voiceover) == "" {
				continue
			}
			if _, ok := voice[shot.ID]; !ok {
				return fmt.Sprintf("片段 %d 的配音还没有成功", index+1)
			}
		}
	}
	return ""
}

// composeVideoReplicationOutput 组装时间线并提交渲染任务，同一提交标识只合成一次。
func (s *Service) composeVideoReplicationOutput(project *model.VideoReplicationProject, plan *VideoReplicationPlan, key string) error {
	if _, err := s.repo.VideoReplicationOutputByKey(project.ID, key); err == nil {
		return nil
	}
	storyboard := videoReplicationStoryboard(project)
	if storyboard == nil || len(storyboard.Shots) == 0 {
		return BadAuthRequest("还没有分镜")
	}
	runs, err := s.repo.VideoReplicationRuns(project.UserID, project.ID)
	if err != nil {
		return err
	}
	if reason := videoReplicationComposeReady(*storyboard, plan, runs); reason != "" {
		return BadAuthRequest(reason)
	}
	timeline, runIDs, missing := buildVideoReplicationTimeline(*storyboard, videoReplicationParameters(project), plan, runs, func(resourceID string) int64 {
		if resource, err := s.repo.ResourceForUser(project.UserID, resourceID); err == nil {
			return resource.DurationMs
		}
		return 0
	})
	if len(missing) > 0 {
		return BadAuthRequest("以下片段还没有成功生成的版本：" + strings.Join(missing, "、"))
	}
	// 替换音频 + 原片伴奏：各段只取分离出的新人声，背景音乐用整条原伴奏。
	if plan.Instrumental != "" && plan.AudioMode == videoReplicationAudioNative && plan.Strategy == VideoReplicationStrategySegmented {
		chosen, _ := videoReplicationChosenRuns(runs)
		vocals, pending := s.videoReplicationSegmentVocals(project.UserID, chosen)
		if pending {
			return errVideoReplicationStemsPending
		}
		runByClip := map[string]string{}
		for shotID, run := range chosen {
			runByClip["shot-"+shotID] = run.ID
		}
		applyVideoReplicationStems(&timeline, runByClip, vocals, plan.Instrumental)
	}
	// 保留原声（已确认授权）：片段静音，原参考视频音轨作为整条音频叠加。属于音频后处理，不是模型原生音频。
	if plan.AudioMode == videoReplicationAudioOriginal {
		if audioID := videoReplicationPrimaryAudio(project); audioID != "" {
			timeline.Tracks[0].Muted = true
			timeline.Tracks = append(timeline.Tracks, renderTrack{ID: "original-audio", Kind: "audio"})
			timeline.Clips = append(timeline.Clips, renderClip{ID: "original-audio", Kind: "audio", TrackID: "original-audio", StartMs: 0, DurationMs: timeline.DurationMs, SourceDurationMs: timeline.DurationMs, Volume: 1, DirectMedia: videoReplicationMediaRef(audioID, "audio")})
		}
	}
	task, err := s.CreateTimelineRenderTask(project.UserID, TimelineRenderCreateRequest{Timeline: timeline})
	if err != nil {
		return err
	}
	mode := VideoReplicationStrategySegmented
	if plan.Strategy == VideoReplicationStrategyNative {
		// 原生整片 + 配音后处理：画面仍是一次生成的完整视频，只叠加了配音音轨。
		mode = "native_dubbed"
	}
	runIDsJSON, _ := json.Marshal(runIDs)
	output := &model.VideoReplicationOutput{ID: newID(), ProjectID: project.ID, UserID: project.UserID, IdempotencyKey: key, Mode: mode, TaskID: task.ID, Status: "queued", RunIDsJSON: string(runIDsJSON), DurationMs: timeline.DurationMs}
	if err := s.repo.CreateVideoReplicationOutput(output); err != nil {
		// 并发的同键请求已经写入：本次渲染任务是重复的（不计费），取消它。
		if _, lookupErr := s.repo.VideoReplicationOutputByKey(project.ID, key); lookupErr == nil {
			_, _ = s.CancelTask(context.Background(), project.UserID, task.ID)
			return nil
		}
		return err
	}
	return nil
}

// videoReplicationAutoComposeKey 由参与合成的版本决定：重新生成某一段后版本变化，会自动再合成一次。
func videoReplicationAutoComposeKey(storyboard VideoReplicationStoryboard, plan *VideoReplicationPlan, runs []model.VideoReplicationShotRun) string {
	_, runIDs, _ := buildVideoReplicationTimeline(storyboard, VideoReplicationParameters{}, plan, runs, func(string) int64 { return 0 })
	sum := sha256.Sum256([]byte(strings.Join(runIDs, ",") + "|" + plan.Digest))
	return "auto-" + hex.EncodeToString(sum[:12])
}

// ComposeVideoReplication 手动重新合成（例如切换了某段的采用版本）；一般情况下片段完成后会自动合成。
func (s *Service) ComposeVideoReplication(userID, projectID string, input VideoReplicationComposeInput) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(input.IdempotencyKey)
	if !videoReplicationKeyPattern.MatchString(key) {
		return nil, BadAuthRequest("提交标识无效")
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if err := s.syncVideoReplication(context.Background(), project); err != nil {
		return nil, err
	}
	plan, err := requireConfirmedVideoReplicationPlan(project)
	if err != nil {
		return nil, err
	}
	if plan.Strategy == VideoReplicationStrategyNative && plan.AudioMode != videoReplicationAudioTTS {
		return nil, BadAuthRequest("原生直出由模型一次输出完整视频，不经过剪辑合成")
	}
	if err := s.composeVideoReplicationOutput(project, plan, key); err != nil {
		return nil, err
	}
	return s.VideoReplicationProject(userID, projectID)
}

// videoReplicationNaturalFaceRule 是替换人物 / 人脸时对面部的共同要求（原片编辑与逐段生成都用）：
// 只强调长相一致时，模型容易把整段的脸做成参考照片上那一个表情，显得僵硬。
const videoReplicationNaturalFaceRule = "微笑、说话时嘴部和面部肌肉自然联动，不要僵硬、呆板、面无表情或像贴上去的面具。"

// videoReplicationLifelikeRule 是画面里有人物时每段都带的真人质感要求（不论换的是商品、人物还是服装）：
// 原片编辑会重新渲染整帧，只写「人物与原片一致」时，模型常把人做成磨皮蜡像脸、表情定格、动作像木偶一样匀速僵硬。
// 分段生成时模型常让每段从静止姿势开始、结尾定格，拼起来每个接缝都会顿一下，所以也写明首尾要动着。
// 写在替换句和用户要求之后、「保持不变」之前，提示词超长截断时不会被截掉。
const videoReplicationLifelikeRule = "真人实拍：皮肤真实不磨皮，不像蜡像或 CG；眼神、眨眼和微表情随说话自然变化，情绪真实有感染力，像真人带货时真心推荐，不定格、不假笑；动作意图和节奏沿用原片且流畅自然，有重心和惯性，肢体协调联动，不僵硬、不机械、不卡顿、不像木偶；吃、喝、试用、涂抹沿用原片动作意图，替换商品时按新结构微调接触姿势；咀嚼吞咽时嘴唇、脸颊和喉咙跟着动，手和嘴与食物或商品接触真实，不穿模、不悬空；人物从第一帧起就照原片自然在动，不要从静止或摆好的姿势开始，结尾也不定格。"

// videoReplicationReplaceModeRule 把电商 Agent 商品视频选择的替换方式写成逐段画面的一致性要求。
func videoReplicationReplaceModeRule(params VideoReplicationParameters) string {
	parts := []string{}
	switch params.ReplaceMode {
	case "precise-product":
		parts = append(parts, "精准商品替换：商品的轮廓、比例、颜色、材质、Logo 与文字位置都必须严格按商品参考图还原，镜头靠近商品时细节清晰，不得出现原视频商品的任何残留。")
	case "precise-person":
		parts = append(parts, "精准人物替换：人物的五官、脸型、发型、肤色、身材比例都必须严格按人物参考图还原，正脸、侧脸与转身镜头保持同一人物，动作与表情跟随原视频。")
	case "combination":
		parts = append(parts, "组合替换：按每张参考图的用途同时替换对应元素，各元素之间的大小比例、遮挡关系和光线要协调统一。")
	}
	if hint := strings.TrimSpace(params.SubjectHint); hint != "" {
		parts = append(parts, "商品目标是原视频中的「"+hint+"」，其他已选择的类别同时替换，未选择为替换目标的物品保持不变。")
	}
	return strings.Join(parts, "")
}

// videoReplicationVoiceAudios 返回按顺序排列的参考音色资源，最多 limit 段。
func videoReplicationVoiceAudios(assets []model.VideoReplicationAsset, limit int) []string {
	voices := []model.VideoReplicationAsset{}
	for _, asset := range assets {
		if asset.Category == model.VideoReplicationAssetVoiceAudio {
			voices = append(voices, asset)
		}
	}
	sort.SliceStable(voices, func(i, j int) bool { return voices[i].SortOrder < voices[j].SortOrder })
	ids := []string{}
	for _, voice := range voices {
		if len(ids) >= limit {
			break
		}
		ids = append(ids, voice.ResourceID)
	}
	return ids
}

const (
	// videoReplicationMaxVoiceAudios：替换音频只用 1 段参考音色。多段录音的电平、底噪和语气不一致，模型会在段间混出杂音或忽男忽女。
	videoReplicationMaxVoiceAudios = 1
	// videoReplicationMaxVoiceMs：视频模型参考音频的时长上限（Seedance 2.0 多段合计 15 秒）。
	videoReplicationMaxVoiceMs = 15000
)

// videoReplicationTargetLines 返回这次提交要说的原视频台词：分段取本段口播，整片直出按时间顺序拼接全部口播。
func videoReplicationTargetLines(storyboard VideoReplicationStoryboard, target videoReplicationTarget) string {
	if target.shotID != videoReplicationNativeShotID {
		if target.index >= 0 && target.index < len(storyboard.Shots) {
			return strings.TrimSpace(storyboard.Shots[target.index].Voiceover)
		}
		return ""
	}
	lines := []string{}
	for _, shot := range storyboard.Shots {
		if line := strings.TrimSpace(shot.Voiceover); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, " ")
}

// videoReplicationSound 是生成声音时的附加信息。
type videoReplicationSound struct {
	// Edit：原片编辑模式，素材按 Seedance 的 @图片N / @音频N 写法引用。
	Edit bool
	// Music：视频理解对原片背景音乐的描述，用来让模型配出同风格的音乐。
	Music string
	// SpeechSegment：替换音频 / 口播视频，本段没有台词时要明确不开口，避免模型自己编台词、嘴型对不上。
	SpeechSegment bool
	// Narration：原片为画外旁白，不让参考人物或画面里原本沉默的人代说。
	Narration bool
	// Stems：成片会叠加原视频分离出的伴奏，本段只要人声，不要生成背景音乐。
	Stems bool
	// SourceVoice：参考音频是从原视频截出的说话人人声（不是用户上传的）。
	SourceVoice bool
	// VoiceHint：原视频说话人的音色描述；没有发送参考音频时用文字保持音色。
	VoiceHint string
	// Subtitled：成片另外叠加与台词一致的字幕轨，画面里不要画字。
	Subtitled bool
	// Delivery：说话的语气情绪（用户在音色绑定里写的语气指导 > 视频理解识别的原片说话人语气）。
	Delivery string
	// Selling：原片的营销节奏（如 痛点-卖点-证明-行动号召），让情绪随段落起伏。
	Selling string
	// Pace：按本段台词字数和时长算出的语速要求（videoReplicationPaceRule）。
	Pace              string
	ReferenceDelivery string
}

// videoReplicationPaceRule 按本段要说的字数和时长给出语速：不写时模型要么把一段话挤成一串赶着念，
// 要么一字一顿地拖满时长。无原片声音表达依据时才使用整段平均值。
func videoReplicationPaceRule(lines string, seconds int) string {
	count := 0
	for _, r := range lines {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			count++
		}
	}
	if count == 0 {
		return ""
	}
	if seconds <= 0 {
		return "语速：和真人日常聊天一样自然，不赶不拖，句与句之间自然换气。"
	}
	rate := float64(count) / float64(seconds)
	switch {
	case rate > 5.5:
		return fmt.Sprintf("语速：本段约 %d 个字在 %d 秒内说完，像原片一样轻快利落，但每个字都吐清楚，句间留短促换气，不连成一串、不抢拍。", count, seconds)
	case rate < 2.5:
		return fmt.Sprintf("语速：本段台词不多（约 %d 个字 / %d 秒），从容自然地说，说完留出表情和动作的停顿，不刻意拖长字音、不一字一顿。", count, seconds)
	default:
		return fmt.Sprintf("语速：和原片说话人一样的自然聊天语速（约每秒 %.0f 个字），在本段时长内正好说完，不赶、不拖，句与句之间自然换气。", rate)
	}
}

// videoReplicationMusicNote 返回视频理解对原片背景音乐的描述；没有音乐或识别不出时返回空。
func videoReplicationMusicNote(project *model.VideoReplicationProject) string {
	if project == nil || strings.TrimSpace(project.AnalysisJSON) == "" {
		return ""
	}
	var analysis VideoReplicationAnalysis
	if json.Unmarshal([]byte(project.AnalysisJSON), &analysis) != nil || analysis.Voice == nil {
		return ""
	}
	music := strings.TrimSpace(analysis.Voice.Music)
	for _, none := range []string{"无", "没有", "无背景音乐", "无法判断", "不确定", "unknown"} {
		if music == none {
			return ""
		}
	}
	return videoReplicationClipRunes(music, 50)
}

// videoReplicationDeliveryNote 返回说话的语气要求和营销节奏：用户在音色绑定里改过的语气指导优先，
// 否则用视频理解识别出的原片说话人语气（如「轻柔欢快、亲切种草」）。
// 不写进提示词时模型只会照着参考音频的读法平铺直叙地念台词，像机器人。
func videoReplicationDeliveryNote(project *model.VideoReplicationProject) (delivery, selling string) {
	if project == nil {
		return "", ""
	}
	tones := []string{}
	seen := map[string]bool{}
	add := func(value string) {
		if value = strings.TrimSpace(value); value != "" && !seen[value] {
			seen[value] = true
			tones = append(tones, value)
		}
	}
	for _, role := range videoReplicationVoiceCast(project).Roles {
		add(role.Instructions)
	}
	var analysis VideoReplicationAnalysis
	if strings.TrimSpace(project.AnalysisJSON) != "" && json.Unmarshal([]byte(project.AnalysisJSON), &analysis) == nil {
		if len(tones) == 0 {
			add(stringValue(analysis.Product["deliveryTone"]))
		}
		if len(tones) == 0 && analysis.Voice != nil {
			for _, speaker := range analysis.Voice.Speakers {
				add(speaker.Tone)
			}
		}
		selling = videoReplicationClipRunes(strings.TrimSpace(analysis.Marketing), 60)
	}
	return videoReplicationClipRunes(strings.Join(tones, "；"), 80), selling
}

// videoReplicationDeliveryRule 是有台词时的语气要求：参考音频只借音色，情绪按原片和商品调性来，像真人分享而不是念稿。
func videoReplicationDeliveryRule(sound videoReplicationSound) string {
	rule := "语气：像真人博主对着镜头真心分享，不是念稿"
	if sound.Narration {
		rule = "语气：像真人在画外自然讲述和分享，不是念稿"
	}
	if sound.ReferenceDelivery != "" {
		rule = "语气：像直播间主播现场带货，按原片实测表达节奏，有观众交流感，不是念稿"
		if sound.Narration {
			rule = "语气：保持画外旁白，沿用原片的直播讲解节奏，不新增出镜说话人"
		}
		rule += "；" + sound.ReferenceDelivery + "；仅复刻表达方式，不沿用旧商品优惠，不增加未确认的价格、秒杀、限量或专属福利"
	}
	if sound.Delivery != "" && sound.ReferenceDelivery == "" {
		rule += "，语气" + sound.Delivery
	}
	rule += "，符合新商品调性和本句表达意图；重音、停顿、换气、音高和语速快慢随内容自然变化，情绪真实有层次。品尝或试穿时表情、视线和手势响应实际感受，不强制每句微笑、惊讶或大笑，不用固定亢奋腔；台词外不加笑声、语气词或新卖点"
	if sound.Selling != "" {
		rule += "；情绪跟着原片的带货节奏走（" + sound.Selling + "）"
	}
	rule += "；不要平铺直叙、一字一顿、播音腔或机器人朗读。"
	if sound.Pace != "" {
		rule += sound.Pace
	}
	return rule
}

// videoReplicationWithVoicePrompt 在画面提示词后写明声音要求：参考音频是画面人物（模特 / 人脸参考图里的人）的声音，
// 台词使用已确认的最终稿，口型与新生成的人声同步；纯旁白保留画外声音，不新增出镜说话人。
// 画面部分过长时只截画面描述，声音与台词要求总是完整保留。
func videoReplicationWithVoicePrompt(prompt, lines string, voiceCount int, references []map[string]string, sound videoReplicationSound, budget videoReplicationPromptBudget) string {
	imageName, audioName := "参考图%d", "参考音频%d"
	if sound.Edit {
		imageName, audioName = "@图片%d", "@音频%d"
	}
	parts := []string{}
	lines = strings.TrimSpace(lines)
	speaker := "画面中说话的人物"
	noOtherVoice := "不出现其他人声或旁白"
	if sound.Narration {
		speaker, noOtherVoice = "原视频的画外旁白", "不新增出镜说话人，不让画面中原本沉默的人开口"
	}
	if voiceCount > 0 {
		people := []string{}
		for index, reference := range references {
			if reference["category"] == model.VideoReplicationAssetModelImage || reference["category"] == model.VideoReplicationAssetFaceImage {
				people = append(people, fmt.Sprintf(imageName, index+1))
			}
		}
		if len(people) > 0 && !sound.Narration {
			speaker = "画面中的人物（即" + strings.Join(people, "、") + "中的人）"
		}
		names := make([]string, 0, voiceCount)
		for index := 1; index <= voiceCount; index++ {
			names = append(names, fmt.Sprintf(audioName, index))
		}
		audio := strings.Join(names, "、")
		if sound.SourceVoice {
			parts = append(parts, "声音："+audio+" 是原视频说话人的声音，"+speaker+"用这个声音说话，音色和口音与它一致，全程同一个声音；只借用音色，不照搬"+audio+"里说的内容或背景声音，"+noOtherVoice+"。")
		} else {
			parts = append(parts, "声音："+audio+" 是"+speaker+"的声音，嗓音、音高和音色质感与它一致，全程同一个声音；"+audio+" 只提供音色，语气和情绪不照搬它的读法，按下面的语气要求说；不用原视频人声，"+noOtherVoice+"。")
		}
	} else if lines != "" && sound.Edit {
		parts = append(parts, "声音：沿用 @视频1 中原说话人的音色、口音与嗓音质感，由"+speaker+"说下面的新台词，不播放原片旧台词；"+noOtherVoice+"。")
	} else if lines != "" && sound.VoiceHint != "" {
		parts = append(parts, "声音："+speaker+"的音色与原视频说话人一致（"+sound.VoiceHint+"），全程同一个声音，"+noOtherVoice+"。")
	}
	if lines != "" {
		lineSpeaker := "人物"
		if sound.Narration {
			lineSpeaker = "画外旁白"
		}
		parts = append(parts, "台词："+lineSpeaker+"逐字说「"+lines+"」，不增减改字。")
		parts = append(parts, videoReplicationDeliveryRule(sound))
		if sound.Narration {
			parts = append(parts, "旁白保持画外声音，画面延续原片的商品、人物与动作，不为台词新增人物、嘴型或对镜口播。")
		} else {
			parts = append(parts, "口型：嘴型与人声逐字同步，开口、停顿、收尾与声音同时发生；说话人正脸或侧脸可见，嘴部不被商品或手遮挡；不说话时嘴自然合上，面部表情随语气自然变化。口语化地说，吐字清楚但不播音腔，不吞字、不含糊。")
		}
		if sound.Subtitled {
			parts = append(parts, "字幕：不生成烧录字幕或悬浮文案，字幕由后期按台词逐字叠加；商品、包装和参数表载体上的真实文字按已提供资料保留。")
		}
	} else if sound.SpeechSegment {
		parts = append(parts, "本段没有台词：人物不开口说话，不生成人声，表情照常自然变化（可以微笑、眨眼）。")
	}
	music := "背景音乐：沿用原视频背景音乐的风格、节奏和速度"
	if sound.Music != "" {
		music += "（原片：" + sound.Music + "）"
	}
	music += "，各段一致，音量低于人声；补充要求没说换音乐就不换，原视频没有音乐就不加。"
	if sound.Stems {
		music = "背景音乐：不要生成任何背景音乐、配乐和音效，只生成最终台词的干净人声（成片会另外叠加原视频的背景音乐）。"
	}
	parts = append(parts, music)
	parts = append(parts, "音质：干净清晰，无电流声、滋滋声、底噪、爆音、回声或机器人声，音量随情绪自然起伏、不忽大忽小。")
	suffix := strings.Join(parts, "\n")
	return budget.without(suffix).clip(prompt) + "\n" + suffix
}

func videoReplicationClipRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
