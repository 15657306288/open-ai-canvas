package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

// 商品视频复刻的声音：Agent 从参考视频识别说话人，按后台登记的音色库为每个说话人推荐音色，
// 用户可改音色、把说话人绑定到某张模特图并试听。生成时每个有口播的镜头提交一个 TTS 任务，
// 合成时按镜头起点把配音铺在整条时间线上，片段之间的声音是连续的一条轨。
// TTS 使用后台指定的语音合成模型，走普通 canvas_audio 任务（按模型计费、失败退款）。

type VideoReplicationVoiceRole struct {
	SpeakerID          string `json:"speakerId"`
	Label              string `json:"label"`
	Gender             string `json:"gender,omitempty"`
	Age                string `json:"age,omitempty"`
	Tone               string `json:"tone,omitempty"`
	OnCamera           bool   `json:"onCamera"`
	VoiceKey           string `json:"voiceKey,omitempty"`
	VoiceName          string `json:"voiceName,omitempty"`
	Reason             string `json:"reason,omitempty"`
	ModelAssetID       string `json:"modelAssetId,omitempty"`
	Instructions       string `json:"instructions,omitempty"`
	InstructionsSource string `json:"instructionsSource,omitempty"`
}

type VideoReplicationVoiceCast struct {
	Roles []VideoReplicationVoiceRole `json:"roles"`
}

type VideoReplicationVoicePreviewInput struct {
	SpeakerID string `json:"speakerId"`
	VoiceKey  string `json:"voiceKey"`
	Text      string `json:"text,omitempty"`
}

func videoReplicationVoiceCast(project *model.VideoReplicationProject) VideoReplicationVoiceCast {
	var cast VideoReplicationVoiceCast
	_ = json.Unmarshal([]byte(project.VoiceCastJSON), &cast)
	if cast.Roles == nil {
		cast.Roles = []VideoReplicationVoiceRole{}
	}
	return cast
}

func (cast VideoReplicationVoiceCast) role(speakerID string) (VideoReplicationVoiceRole, bool) {
	for _, role := range cast.Roles {
		if role.SpeakerID == speakerID {
			return role, true
		}
	}
	return VideoReplicationVoiceRole{}, false
}

// videoReplicationSpeakerRoles 汇总说话人：画面理解给出的说话人 + 分镜里出现的说话人；
// 分镜有口播但没有识别到说话人时，补一个「旁白」。
func videoReplicationSpeakerRoles(analysis *VideoReplicationAnalysis, storyboard *VideoReplicationStoryboard) []VideoReplicationVoiceRole {
	roles := []VideoReplicationVoiceRole{}
	seen := map[string]bool{}
	if analysis != nil && analysis.Voice != nil {
		for _, speaker := range analysis.Voice.Speakers {
			id := strings.TrimSpace(speaker.ID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			roles = append(roles, VideoReplicationVoiceRole{SpeakerID: id, Label: firstNonEmpty(speaker.Label, id), Gender: speaker.Gender, Age: speaker.Age, Tone: speaker.Tone, OnCamera: speaker.OnCamera})
		}
	}
	if storyboard != nil {
		for _, shot := range storyboard.Shots {
			if strings.TrimSpace(shot.Voiceover) == "" {
				continue
			}
			id := firstNonEmpty(strings.TrimSpace(shot.Speaker), "S1")
			if seen[id] {
				continue
			}
			seen[id] = true
			roles = append(roles, VideoReplicationVoiceRole{SpeakerID: id, Label: map[bool]string{true: "旁白", false: id}[id == "S1" && len(roles) == 0]})
		}
	}
	return roles
}

// recommendVideoReplicationVoices 让策划模型从后台音色库里为每个说话人选音色，并在用户上传了模特图时建议绑定。
// 没有配置 TTS 时只返回说话人列表，不编造音色。
func (s *Service) recommendVideoReplicationVoices(ctx context.Context, cfg ecommerceProviderConfig, project *model.VideoReplicationProject, assets []model.VideoReplicationAsset, analysis *VideoReplicationAnalysis, storyboard *VideoReplicationStoryboard) VideoReplicationVoiceCast {
	cast := VideoReplicationVoiceCast{Roles: videoReplicationSpeakerRoles(analysis, storyboard)}
	setting, err := s.videoReplicationSetting()
	if err != nil || !setting.ttsReady() || len(cast.Roles) == 0 {
		return cast
	}
	people := []map[string]any{}
	for _, view := range videoReplicationAssetViews(assets) {
		if view.Category == model.VideoReplicationAssetModelImage || view.Category == model.VideoReplicationAssetFaceImage {
			people = append(people, map[string]any{"assetId": view.ID, "label": view.Label, "profile": view.Profile})
		}
	}
	lines := map[string][]string{}
	if storyboard != nil {
		for _, shot := range storyboard.Shots {
			if text := strings.TrimSpace(shot.Voiceover); text != "" {
				id := firstNonEmpty(strings.TrimSpace(shot.Speaker), "S1")
				lines[id] = append(lines[id], text)
			}
		}
	}
	brief, _ := json.Marshal(map[string]any{"speakers": cast.Roles, "lines": lines, "voices": setting.TTS.Voices, "people": people, "modelProfile": analysis.Model, "product": analysis.Product, "userBrief": videoReplicationReadablePrompt(project.Prompt, assets), "targets": videoReplicationTypeBrief(project.VideoType)})
	system := "你是广告配音导演。只能从给定 voices 中选择音色，输出严格 JSON。"
	prompt := fmt.Sprintf(`上下文（JSON）：%s
为每个 speaker 选择最合适的 voice（考虑性别、年龄、语气、商品调性与用户要求），并写一句不超过 40 字的配音语气指导 instructions。
如果 people 非空，为对镜出镜的 speaker 选择最匹配的人物 assetId（没有合适的留空）。
输出 JSON：{"roles":[{"speakerId":"S1","voiceKey":"voices 中的 key","reason":"推荐理由（20 字内）","modelAssetId":"","instructions":""}]}`, string(brief))
	var result struct {
		Roles []VideoReplicationVoiceRole `json:"roles"`
	}
	if err := s.videoReplicationChatJSON(ctx, cfg, "voice", project.ID, system, prompt, nil, nil, &result); err != nil {
		return cast
	}
	validPeople := map[string]bool{}
	for _, person := range people {
		validPeople[stringValue(person["assetId"])] = true
	}
	for index := range cast.Roles {
		role := &cast.Roles[index]
		for _, picked := range result.Roles {
			if picked.SpeakerID != role.SpeakerID {
				continue
			}
			if voice, ok := setting.voice(strings.TrimSpace(picked.VoiceKey)); ok {
				role.VoiceKey, role.VoiceName = voice.Key, voice.Name
			}
			if validPeople[picked.ModelAssetID] {
				role.ModelAssetID = picked.ModelAssetID
			}
			role.Reason, role.Instructions = clipText(picked.Reason, 60), clipText(picked.Instructions, 120)
			role.InstructionsSource = "agent"
		}
		if role.VoiceKey == "" {
			role.VoiceKey, role.VoiceName = setting.TTS.Voices[0].Key, setting.TTS.Voices[0].Name
		}
	}
	return cast
}

// UpdateVideoReplicationVoiceCast 保存用户调整后的音色与模特绑定；音色必须来自后台音色库，模特图必须属于本项目。
func (s *Service) UpdateVideoReplicationVoiceCast(userID, projectID string, input VideoReplicationVoiceCast) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	setting, err := s.videoReplicationSetting()
	if err != nil {
		return nil, err
	}
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return nil, err
	}
	people := map[string]bool{}
	for _, asset := range assets {
		if asset.Category == model.VideoReplicationAssetModelImage || asset.Category == model.VideoReplicationAssetFaceImage {
			people[asset.ID] = true
		}
	}
	if len(input.Roles) > 12 {
		return nil, BadAuthRequest("说话人最多 12 个")
	}
	current := videoReplicationVoiceCast(project)
	next := VideoReplicationVoiceCast{Roles: []VideoReplicationVoiceRole{}}
	for _, role := range input.Roles {
		role.SpeakerID = strings.TrimSpace(role.SpeakerID)
		if role.SpeakerID == "" || len(role.SpeakerID) > 24 {
			return nil, BadAuthRequest("说话人无效")
		}
		if role.VoiceKey != "" {
			voice, ok := setting.voice(role.VoiceKey)
			if !ok {
				return nil, BadAuthRequest("所选音色未在后台开放")
			}
			role.VoiceName = voice.Name
		}
		if role.ModelAssetID != "" && !people[role.ModelAssetID] {
			return nil, BadAuthRequest("绑定的人物图片不存在")
		}
		if previous, ok := current.role(role.SpeakerID); ok {
			// 识别信息以分析结果为准，用户只改音色、绑定、语气与显示名。
			role.Gender, role.Age, role.Tone, role.OnCamera = previous.Gender, previous.Age, previous.Tone, previous.OnCamera
			if role.VoiceKey != previous.VoiceKey {
				role.Reason = ""
			} else {
				role.Reason = previous.Reason
			}
		}
		role.InstructionsSource = "user"
		if previous, ok := current.role(role.SpeakerID); ok && previous.InstructionsSource == "agent" && strings.TrimSpace(previous.Instructions) == strings.TrimSpace(role.Instructions) {
			role.InstructionsSource = "agent"
		}
		role.Label = clipText(firstNonEmpty(strings.TrimSpace(role.Label), role.SpeakerID), 30)
		role.Instructions = clipText(strings.TrimSpace(role.Instructions), 120)
		next.Roles = append(next.Roles, role)
	}
	raw, _ := json.Marshal(next)
	if err := s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"voice_cast_json": string(raw)}); err != nil {
		return nil, err
	}
	return s.VideoReplicationProject(userID, projectID)
}

// createVideoReplicationTTSTask 用后台 TTS 模型创建一个普通 canvas_audio 任务（计费、退款、重启恢复由任务系统负责）。
func (s *Service) createVideoReplicationTTSTask(userID string, setting VideoReplicationSetting, voiceKey, instructions, text, requestID string) (*model.Task, error) {
	if !setting.ttsReady() {
		return nil, BadAuthRequest("后台还没有配置配音模型（TTS）")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, BadAuthRequest("配音文本为空")
	}
	ref := setting.TTS.Model
	config := map[string]any{"mode": "audio", "audioVoice": voiceKey, "audioFormat": firstNonEmpty(setting.TTS.Format, "mp3")}
	if instructions != "" {
		config["audioInstructions"] = instructions
	}
	request := CreateTaskRequest{Type: "canvas_audio", Operation: "audio", Prompt: text, RequestID: requestID}
	if ref.LogicalModelID != "" {
		request.LogicalModelID = ref.LogicalModelID
		provider, err := s.resolveVideoReplicationProvider(userID, ref, "audio")
		if err != nil {
			return nil, err
		}
		config["model"] = provider.Model
		request.Model = provider.Model
	} else {
		config["channelId"], config["model"] = ref.ChannelID, ref.ModelKey
		request.Model = ref.ModelKey
	}
	request.Input = map[string]any{"mode": "audio", "prompt": text, "config": config, "metadata": map[string]any{"source": "video-replication"}}
	return s.CreateTask(userID, request)
}

// PreviewVideoReplicationVoice 试听：用指定音色合成一句台词，返回任务，前台轮询任务拿到音频。
func (s *Service) PreviewVideoReplicationVoice(userID, projectID string, input VideoReplicationVoicePreviewInput) (*model.Task, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	setting, err := s.videoReplicationSetting()
	if err != nil {
		return nil, err
	}
	voice, ok := setting.voice(strings.TrimSpace(input.VoiceKey))
	if !ok {
		return nil, BadAuthRequest("所选音色未在后台开放")
	}
	text := strings.TrimSpace(input.Text)
	role, _ := videoReplicationVoiceCast(project).role(input.SpeakerID)
	if text == "" {
		if storyboard := videoReplicationStoryboard(project); storyboard != nil {
			for _, shot := range storyboard.Shots {
				if strings.TrimSpace(shot.Voiceover) != "" && firstNonEmpty(shot.Speaker, "S1") == firstNonEmpty(input.SpeakerID, "S1") {
					text = shot.Voiceover
					break
				}
			}
		}
	}
	if text == "" {
		text = "大家好，今天给你们分享一个我最近一直在用的好东西。"
	}
	if utf8.RuneCountInString(text) > 120 {
		text = string([]rune(text)[:120])
	}
	return s.createVideoReplicationTTSTask(userID, setting, voice.Key, role.Instructions, text, "")
}

// submitVideoReplicationVoiceRuns 为每个有口播的镜头提交一个配音任务（kind=voice），已有进行中或成功版本的跳过。
func (s *Service) submitVideoReplicationVoiceRuns(project *model.VideoReplicationProject, plan *VideoReplicationPlan, key string) (int, string, error) {
	storyboard := videoReplicationStoryboard(project)
	if storyboard == nil {
		return 0, "", nil
	}
	setting, err := s.videoReplicationSetting()
	if err != nil {
		return 0, "", err
	}
	cast := videoReplicationVoiceCast(project)
	runs, err := s.repo.VideoReplicationRuns(project.UserID, project.ID)
	if err != nil {
		return 0, "", err
	}
	done := map[string]bool{}
	for _, run := range runs {
		if run.Kind == "voice" && (run.Status == model.VideoReplicationRunSucceeded || videoReplicationRunActive(run.Status)) {
			done[run.ShotID] = true
		}
	}
	submitted := 0
	for _, shot := range storyboard.Shots {
		text := strings.TrimSpace(shot.Voiceover)
		if text == "" || done[shot.ID] {
			continue
		}
		role, ok := cast.role(firstNonEmpty(shot.Speaker, "S1"))
		if !ok || role.VoiceKey == "" {
			if len(cast.Roles) == 0 || cast.Roles[0].VoiceKey == "" {
				return submitted, "配音缺少音色，请在声音配置里为说话人选择音色", nil
			}
			role = cast.Roles[0]
		}
		runKey := key + ":voice:" + shot.ID
		if _, err := s.repo.VideoReplicationRunByKey(project.ID, runKey); err == nil {
			continue
		}
		run := &model.VideoReplicationShotRun{ID: newID(), ProjectID: project.ID, UserID: project.UserID, ShotID: shot.ID, Kind: "voice", IdempotencyKey: runKey, Status: model.VideoReplicationRunSubmitting, Prompt: text}
		created, err := s.repo.ReserveVideoReplicationRun(run)
		if err != nil || !created {
			continue
		}
		task, err := s.createVideoReplicationTTSTask(project.UserID, setting, role.VoiceKey, role.Instructions, text, run.ID)
		if err != nil {
			_ = s.repo.UpdateVideoReplicationRun(run.ID, map[string]any{"status": model.VideoReplicationRunFailed, "error": "配音提交失败（未扣费）：" + clipText(err.Error(), 300)})
			return submitted, "配音：" + err.Error(), nil
		}
		submitted++
		_ = s.repo.UpdateVideoReplicationRun(run.ID, map[string]any{"task_id": task.ID, "status": videoReplicationRunStatus(task.Status)})
	}
	return submitted, "", nil
}
