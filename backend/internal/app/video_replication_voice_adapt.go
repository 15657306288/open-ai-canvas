package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

// 口播自适应：参考视频有口播 / 字幕 / 文案时，Agent 按用户的商品与补充说明改写台词，
// 新台词用原视频说话人的音色说出（用户上传了参考音色时用用户的），口型与台词逐字同步，字幕与台词一致。

// videoReplicationAdaptVoice 判断这次是否要按用户商品改写口播：
//   - 静音、模型不出声时不改（改了也说不出来）；
//   - 保留音乐和节奏不等于保留原商品台词；分离服务只影响伴奏来源，不能阻止改写；
//   - 换了商品 / 服饰，或用户写了补充说明时改；只换人物且没写要求时，原台词本来就对，保持不变。
func videoReplicationAdaptVoice(videoType string, params VideoReplicationParameters, prompt string) bool {
	if params.AudioMode == "mute" || !params.GenerateAudio {
		return false
	}
	// 精准人物保留现有声音策略，视觉备注不能因本次商品改写修复而触发重新配音。
	if params.ReplaceMode == "precise-person" && !params.RewriteVoice && (params.KeepOriginalAudio || params.AudioMode == "keep") && videoReplicationSeparatorURL() == "" {
		return false
	}
	return params.RewriteVoice || videoReplicationHasTarget(videoType, VideoReplicationTypeProduct) || videoReplicationHasTarget(videoType, VideoReplicationTypeApparel) || strings.TrimSpace(prompt) != ""
}

func videoReplicationAdaptSubtitles(videoType string, params VideoReplicationParameters, analysis *VideoReplicationAnalysis) bool {
	return videoReplicationHasSubtitles(analysis) && (params.RewriteVoice || videoReplicationHasTarget(videoType, VideoReplicationTypeProduct) || videoReplicationHasTarget(videoType, VideoReplicationTypeApparel))
}

// 改写稿缺失或本段容不下时必须停在规划阶段，不能把原商品文案混入新商品视频。
func videoReplicationAdaptedLine(original, rewritten string, maxChars, segment int) (string, error) {
	if strings.TrimSpace(original) == "" {
		return "", nil
	}
	line := videoReplicationCleanVoiceover(rewritten)
	if line == "" {
		return "", fmt.Errorf("第 %d 段商品文案未完成改写，请重新分析；未提交视频生成", segment)
	}
	if utf8.RuneCountInString(line) > maxChars {
		return "", fmt.Errorf("第 %d 段改写文案超过本段可容纳的 %d 字，请缩短后重新分析；未提交视频生成", segment, maxChars)
	}
	return line, nil
}

const videoReplicationVoiceRepairAttempts = 2

type videoReplicationVoiceRepairLine struct {
	Index       int    `json:"index"`
	Original    string `json:"original"`
	Draft       string `json:"draft"`
	MaxChars    int    `json:"maxChars"`
	TargetChars int    `json:"targetChars"`
}

// 只重写未通过校验的文案，批量限次修复；不改分段、不截断句子，也不退回原商品台词。
func (s *Service) repairVideoReplicationAdaptedLines(ctx context.Context, cfg ecommerceProviderConfig, projectID string, contextJSON []byte, segments []videoReplicationAutoSegment, shots []VideoReplicationShot) error {
	for attempt := 0; ; attempt++ {
		pending := []videoReplicationVoiceRepairLine{}
		var invalid error
		for index, segment := range segments {
			original, draft := segment.Lines, shots[index].Voiceover
			if strings.TrimSpace(original) == "" {
				original, draft = segment.Subtitle, shots[index].Subtitle
			}
			if _, err := videoReplicationAdaptedLine(original, draft, segment.MaxChars, index+1); err != nil {
				invalid = err
				pending = append(pending, videoReplicationVoiceRepairLine{
					Index: index + 1, Original: original, Draft: videoReplicationCleanVoiceover(draft),
					MaxChars: segment.MaxChars, TargetChars: max(1, segment.MaxChars*9/10),
				})
			}
		}
		if len(pending) == 0 {
			return nil
		}
		if attempt == videoReplicationVoiceRepairAttempts {
			return fmt.Errorf("商品文案自动调整 %d 次后仍不可用：%w", attempt, invalid)
		}
		payload, err := json.Marshal(map[string]any{"context": json.RawMessage(contextJSON), "lines": pending})
		if err != nil {
			return err
		}
		system := "你是商品视频文案编辑。只修复 lines 中缺失或超长的商品文案，不改画面、片段数、时间和其他段落。context.product、productName 与 userBrief 提供新商品事实和要求；original 只供参考句式和节奏，不沿用旧商品的品牌、名称、参数或卖点。draft 不为空时精简其重复表达，保留有依据的新商品信息和自然口语；为空时按新商品事实改写 original。商品名称按 productName 照写，不推断或编造卖点。每条写完整句子，目标约 targetChars 字，绝对不能超过 maxChars 个字符（含标点和空格），长度优先于接近原句。保留说话的情绪与营销意图，不添加动作提示、价格或功效承诺。只返回严格 JSON：{\"lines\":[{\"index\":1,\"text\":\"修复后的文案\"}]}。所有请求的 index 必须各出现一次，不返回其他 index。"
		var result struct {
			Lines []struct {
				Index int    `json:"index"`
				Text  string `json:"text"`
			} `json:"lines"`
		}
		system += videoReplicationDeliveryPlanningRule
		if err := s.videoReplicationChatJSON(ctx, cfg, "voice-repair", projectID, system, string(payload), nil, nil, &result); err != nil {
			return fmt.Errorf("商品文案自动调整失败，请重新分析；未提交视频生成：%w", err)
		}
		requested := make(map[int]bool, len(pending))
		for _, line := range pending {
			requested[line.Index] = true
		}
		if len(result.Lines) != len(pending) {
			return fmt.Errorf("商品文案自动调整返回的段落不完整，请重新分析；未提交视频生成")
		}
		for _, line := range result.Lines {
			if !requested[line.Index] {
				return fmt.Errorf("商品文案自动调整返回了重复或错误的段落，请重新分析；未提交视频生成")
			}
			delete(requested, line.Index)
		}
		for _, line := range result.Lines {
			if strings.TrimSpace(segments[line.Index-1].Lines) != "" {
				shots[line.Index-1].Voiceover = line.Text
			} else {
				shots[line.Index-1].Subtitle = line.Text
			}
		}
	}
}

// 纯旁白保持画外声音；不能因换商品、上传人物素材或提供新台词而凭空新增说话人。
func videoReplicationNarrationOnly(analysis *VideoReplicationAnalysis) bool {
	return analysis != nil && analysis.Voice != nil && analysis.Voice.Type == "narration" && !videoReplicationOnCameraSpeech(analysis)
}

// 与台词一样按镜头时间中点归属，避免跨分段重复；纯字幕视频也有可供改写的原文。
func videoReplicationAssignSubtitleLines(segments []videoReplicationAutoSegment, shots []VideoReplicationSourceShot) {
	for index := range segments {
		if segments[index].MaxChars <= 0 {
			segments[index].MaxChars = max(4, int(float64(segments[index].Seconds)*videoReplicationDefaultCharsPerSecond))
		}
	}
	for _, shot := range shots {
		text := strings.TrimSpace(shot.Subtitle)
		if text == "" || videoReplicationNoneText(text) || shot.EndMs <= shot.StartMs {
			continue
		}
		middle := (shot.StartMs + shot.EndMs) / 2
		for index := range segments {
			if middle >= segments[index].StartMs && middle < segments[index].EndMs {
				if segments[index].Subtitle != "" {
					segments[index].Subtitle += "；"
				}
				segments[index].Subtitle += text
				break
			}
		}
	}
}

// videoReplicationHasSubtitles：参考视频画面里有字幕（视频理解的概括或逐镜头字幕原文）。
func videoReplicationHasSubtitles(analysis *VideoReplicationAnalysis) bool {
	if analysis == nil {
		return false
	}
	summary := strings.TrimSpace(analysis.Summary.Subtitles)
	if summary != "" && !videoReplicationNoneText(summary) {
		return true
	}
	for _, video := range analysis.Videos {
		for _, shot := range video.Shots {
			if text := strings.TrimSpace(shot.Subtitle); text != "" && !videoReplicationNoneText(text) {
				return true
			}
		}
	}
	return false
}

func videoReplicationNoneText(value string) bool {
	value = strings.Trim(strings.TrimSpace(value), "。.")
	for _, none := range []string{"无", "没有", "无字幕", "没有字幕", "无法判断", "不确定", "unknown", "none"} {
		if strings.EqualFold(value, none) {
			return true
		}
	}
	return strings.HasPrefix(value, "无字幕") || strings.HasPrefix(value, "没有字幕")
}

// videoReplicationSpeakerHint 是原视频说话人的音色描述：模型收不了参考音频时，用文字让新人声尽量接近原音色。
func videoReplicationSpeakerHint(analysis *VideoReplicationAnalysis) string {
	if analysis == nil || analysis.Voice == nil {
		return ""
	}
	for _, speaker := range analysis.Voice.Speakers {
		parts := []string{}
		for _, value := range []string{speaker.Gender, speaker.Age, speaker.Tone} {
			if value = strings.TrimSpace(value); value != "" && !videoReplicationNoneText(value) {
				parts = append(parts, value)
			}
		}
		if len(parts) > 0 {
			return videoReplicationClipRunes(strings.Join(parts, "，"), 40)
		}
	}
	return ""
}

// videoReplicationSubtitleCues 把一段口播拆成按语速铺开的短字幕：按标点断句，每条不超过 16 个字，
// 时长按字数比例分配到这一段的说话时间里，字幕与人声同步出现、同步结束。
func videoReplicationSubtitleCues(text string, startMs, durationMs int64) []renderClip {
	text = strings.TrimSpace(text)
	if text == "" || durationMs <= 0 {
		return nil
	}
	pieces := []string{}
	var current []rune
	flush := func() {
		if piece := strings.Trim(string(current), " ，,、；;"); piece != "" {
			pieces = append(pieces, piece)
		}
		current = current[:0]
	}
	for _, r := range text {
		switch r {
		case '。', '！', '？', '!', '?', '，', ',', '；', ';', '…', '\n':
			if r == '！' || r == '？' || r == '!' || r == '?' {
				current = append(current, r)
			}
			flush()
			continue
		}
		current = append(current, r)
		if len(current) >= 16 {
			flush()
		}
	}
	flush()
	if len(pieces) == 0 {
		return nil
	}
	total := 0
	for _, piece := range pieces {
		total += utf8.RuneCountInString(piece)
	}
	// 说话时间按约 4.5 字/秒估算，不超过本段时长，开头留 150ms，避免字幕先于声音出现。
	speech := min(durationMs-150, int64(float64(total)/videoReplicationDefaultCharsPerSecond*1000)+400)
	if speech < 600 {
		speech = max(durationMs-150, 1)
	}
	cues := make([]renderClip, 0, len(pieces))
	cursor := startMs + 150
	for index, piece := range pieces {
		length := speech * int64(utf8.RuneCountInString(piece)) / int64(max(total, 1))
		if index == len(pieces)-1 {
			length = startMs + 150 + speech - cursor
		}
		if length <= 0 {
			continue
		}
		cues = append(cues, renderClip{Kind: "subtitle", TrackID: "subtitle", StartMs: cursor, DurationMs: length, Text: piece})
		cursor += length
	}
	return cues
}

// videoReplicationSourceVoiceClip 从原视频截一段说话片段（≤15 秒）作为参考音色：
// 先用分离服务去掉背景音乐，再从第一句台词开始截取，没有可用台词时间时从头截取。
// 分离服务不可用时截取原音轨，仍给模型真实声音参考；背景音乐由生成提示词排除。
func (s *Service) videoReplicationSourceVoiceClip(ctx context.Context, userID string, project *model.VideoReplicationProject, analysis *VideoReplicationAnalysis) (string, error) {
	source := videoReplicationPrimaryAudio(project)
	if source == "" {
		return "", nil
	}
	vocals := source
	if videoReplicationSeparatorURL() != "" {
		separated, err := s.separateVideoReplicationStem(ctx, userID, source, videoReplicationStemVocals)
		if err != nil {
			slog.Warn("video replication source voice separation failed; using source audio", "projectId", project.ID, "error", err)
		} else {
			vocals = separated
		}
	}
	startMs, endMs := int64(0), int64(videoReplicationMaxVoiceMs)
	if voice := videoReplicationVoiceLineSource(analysis); voice != nil {
		first := true
		for _, line := range voice.Segments {
			if strings.TrimSpace(line.Text) == "" || line.EndMs <= line.StartMs {
				continue
			}
			if first {
				startMs, endMs, first = max(0, line.StartMs-100), line.EndMs, false
				continue
			}
			if line.EndMs-startMs > videoReplicationMaxVoiceMs-200 {
				break
			}
			endMs = line.EndMs
		}
	}
	if endMs-startMs > videoReplicationMaxVoiceMs-200 {
		endMs = startMs + videoReplicationMaxVoiceMs - 200
	}
	if endMs-startMs < 2000 {
		endMs = startMs + 2000
	}
	identity := fmt.Sprintf("video-replication-source-voice:v2:%s:%d-%d", vocals, startMs, endMs)
	if resource, err := s.resourceForUploadKey(userID, normalizedResourceUploadKey([]string{identity})); err == nil && resource != nil && resource.Status == model.ResourceStatusReady {
		return resource.ID, nil
	}
	ffmpegBin, err := renderFfmpegBinary()
	if err != nil {
		return "", err
	}
	clipID := ""
	err = s.withVideoReplicationResourceFile(userID, vocals, videoReplicationMaxVideoBytes, func(_ *model.Resource, path string) error {
		output := filepath.Join(filepath.Dir(path), "source-voice.mp3")
		// 只做截取、单声道与响度统一；不做降噪，保留原音色。
		cmd := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-ss", fmt.Sprintf("%.3f", float64(startMs)/1000), "-t", fmt.Sprintf("%.3f", float64(endMs-startMs)/1000), "-i", path, "-vn", "-ac", "1", "-ar", "44100", "-af", "loudnorm=I=-16:TP=-1.5:LRA=11", "-c:a", "libmp3lame", "-b:a", "128k", "-y", output)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("截取原视频人声失败：%s", clipText(strings.TrimSpace(string(out)), 300))
		}
		saved, err := s.saveVideoReplicationFile(userID, output, "原视频音色.mp3", "audio", 0, 0, endMs-startMs, identity)
		if err != nil {
			return err
		}
		clipID = saved.ID
		return nil
	})
	return clipID, err
}

// videoReplicationCleanVoiceover 清理改写后的口播：去掉括号里的动作提示、引号、表情符号和多余空白，
// 只留下要说出口的文字，避免配音读出「（笑）」或字幕出现乱码符号。
func videoReplicationCleanVoiceover(value string) string {
	value = strings.TrimSpace(value)
	for _, pair := range [][2]rune{{'（', '）'}, {'(', ')'}, {'【', '】'}, {'[', ']'}} {
		for {
			start := strings.IndexRune(value, pair[0])
			if start < 0 {
				break
			}
			end := strings.IndexRune(value[start:], pair[1])
			if end < 0 {
				value = value[:start]
				break
			}
			value = value[:start] + value[start+end+utf8.RuneLen(pair[1]):]
		}
	}
	var b strings.Builder
	space := false
	for _, r := range value {
		switch {
		case r == '“' || r == '”' || r == '"' || r == '「' || r == '」' || r == '《' || r == '》' || r == '*' || r == '#' || r == '~' || r == '～':
			continue
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '　':
			space = true
			continue
		case r >= 0x1F000 || (r >= 0x2600 && r <= 0x27BF) || r == '️' || r == '‍' || r == utf8.RuneError:
			continue
		}
		if space && b.Len() > 0 {
			b.WriteRune(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// videoReplicationStoryboardHasLines：分镜里至少有一段要说的台词。
func videoReplicationStoryboardHasLines(storyboard VideoReplicationStoryboard) bool {
	for _, shot := range storyboard.Shots {
		if strings.TrimSpace(shot.Voiceover) != "" {
			return true
		}
	}
	return false
}

// videoReplicationMinVoiceMs：上游参考音频单个至少 2 秒。
const videoReplicationMinVoiceMs = 2000

const videoReplicationVoiceCapacityMessage = "参考视频和上传的音色超过模型的合计时长上限。请裁短参考视频、减少音色或切换模型。未提交视频生成"

func videoReplicationVoiceDoesNotFit(budgetMs int64, voiceCount int) bool {
	return budgetMs >= 0 && budgetMs < int64(voiceCount)*videoReplicationMinVoiceMs
}

// videoReplicationFitVoices 把参考音色截到本段剩下的参考时长内（budgetMs < 0 表示不限，原样返回）。
// 剩下不足 2 秒时：从原片截的说话人人声直接不带（提示词改用文字描述原音色）；用户上传的音色不能悄悄丢掉，返回原因让本段停下。
func (s *Service) videoReplicationFitVoices(userID string, voices []string, budgetMs int64, sourceVoice bool) ([]string, string, error) {
	if budgetMs < 0 || len(voices) == 0 {
		return voices, "", nil
	}
	if videoReplicationVoiceDoesNotFit(budgetMs, len(voices)) {
		if sourceVoice {
			return nil, "", nil
		}
		return nil, videoReplicationVoiceCapacityMessage, nil
	}
	perVoiceMs := budgetMs / int64(len(voices))
	fitted := make([]string, 0, len(voices))
	for _, voice := range voices {
		id, err := s.videoReplicationTrimAudio(userID, voice, perVoiceMs)
		if err != nil {
			return nil, "", err
		}
		fitted = append(fitted, id)
	}
	return fitted, "", nil
}

// videoReplicationTrimAudio 返回最长 maxMs 的音频资源：本身够短直接用原资源，否则截取开头 maxMs 存为新资源（同一截法复用）。
func (s *Service) videoReplicationTrimAudio(userID, resourceID string, maxMs int64) (string, error) {
	identity := fmt.Sprintf("video-replication-voice-fit:v1:%s:%d", resourceID, maxMs)
	if resource, err := s.resourceForUploadKey(userID, normalizedResourceUploadKey([]string{identity})); err == nil && resource != nil && resource.Status == model.ResourceStatusReady {
		return resource.ID, nil
	}
	ffmpegBin, err := renderFfmpegBinary()
	if err != nil {
		return "", err
	}
	result := resourceID
	err = s.withVideoReplicationResourceFile(userID, resourceID, videoReplicationMaxVideoBytes, func(resource *model.Resource, path string) error {
		if resource != nil && resource.DurationMs > 0 && resource.DurationMs <= maxMs {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		output := filepath.Join(filepath.Dir(path), "voice-fit.mp3")
		cmd := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", path, "-t", fmt.Sprintf("%.3f", float64(maxMs)/1000), "-vn", "-ac", "1", "-ar", "44100", "-c:a", "libmp3lame", "-b:a", "128k", "-y", output)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s", clipText(strings.TrimSpace(string(out)), 300))
		}
		saved, err := s.saveVideoReplicationFile(userID, output, "参考音色.mp3", "audio", 0, 0, maxMs, identity)
		if err != nil {
			return err
		}
		result = saved.ID
		return nil
	})
	return result, err
}
