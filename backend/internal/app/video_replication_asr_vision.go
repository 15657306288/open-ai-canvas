package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 语音识别「视频理解模型听写」方式（ASR mode=vision）：平台没有接入 /audio/transcriptions 转写模型、服务器也没有 whisper 时，
// 直接用已配置的视频理解渠道（OpenAI 兼容 chat + input_audio）听写参考视频音轨，输出带时间戳的整句台词。
// 只支持「内嵌画面 + 音轨」类型的视频理解渠道（apimart-gemini-video-text、openai-gemini-multimodal），其他渠道的音频输入格式不同，明确报错而不是静默失败。

const videoReplicationASRModeVision = "vision"

func (setting VideoReplicationSetting) asrVisionMode() bool {
	return setting.ASR != nil && setting.ASR.Mode == videoReplicationASRModeVision
}

// videoReplicationVisionTranscriber 返回用视频理解模型听写的转写函数；渠道不可用时返回原因。
func (s *Service) videoReplicationVisionTranscriber(userID, language string) (videoReplicationTranscriber, string) {
	cfg, err := s.videoReplicationTextConfig(userID, true)
	if err != nil {
		return nil, "视频理解模型不可用，无法听写台词：" + err.Error()
	}
	if !videoReplicationUsesInlineMedia(cfg) {
		return nil, "当前视频理解渠道不支持音频听写（需要 OpenAI 兼容 Gemini 多模态视频理解渠道），请改用语音识别模型或 whisper 服务"
	}
	cfg.TextTimeout = 4 * time.Minute
	return func(ctx context.Context, wavPath string) ([]timelineTranscriptionSegment, string, error) {
		audio, durationMs, err := videoReplicationASRAudio(ctx, wavPath)
		if err != nil {
			return nil, "", err
		}
		system := "你是专业的中文语音听写员。只听写音轨里人说出的话，逐字记录，不改写、不总结、不翻译；背景音乐的歌词和无意义的语气声不算。输出严格 JSON。"
		hint := ""
		if language = strings.TrimSpace(language); language != "" {
			hint = "音频语言：" + language + "。"
		}
		prompt := fmt.Sprintf(`%s音轨总长 %.1f 秒。请按时间顺序把每一整句话听写出来，给出该句开始和结束的秒数（保留 1 位小数，不超过总长）。
输出 JSON：{"language":"zh/en 等语言代码","segments":[{"start":0.5,"end":3.2,"text":"这一句的原话"}]}
没有人声时 segments 为空数组。`, hint, float64(durationMs)/1000)
		var result struct {
			Language string `json:"language"`
			Segments []struct {
				Start float64 `json:"start"`
				End   float64 `json:"end"`
				Text  string  `json:"text"`
			} `json:"segments"`
		}
		media := []videoReplicationInlineMedia{{DurationMs: durationMs, Audio: audio}}
		call := func() error {
			return s.videoReplicationInlineChatJSON(ctx, cfg, "asr", system, prompt, media, &result)
		}
		err = call()
		if videoReplicationRetryable(err) && ctx.Err() == nil {
			err = call()
		}
		if err != nil {
			return nil, "", err
		}
		segments := make([]timelineTranscriptionSegment, 0, len(result.Segments))
		for _, item := range result.Segments {
			segments = append(segments, timelineTranscriptionSegment{StartMs: int64(math.Round(item.Start * 1000)), EndMs: int64(math.Round(item.End * 1000)), Text: item.Text})
		}
		return normalizeVideoReplicationDictation(segments, durationMs), strings.TrimSpace(result.Language), nil
	}, ""
}

// normalizeVideoReplicationDictation 清理模型听写结果：去掉空句，时间限制在音轨范围内，结束不早于开始，按开始时间排序。
func normalizeVideoReplicationDictation(segments []timelineTranscriptionSegment, durationMs int64) []timelineTranscriptionSegment {
	out := make([]timelineTranscriptionSegment, 0, len(segments))
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		start, end := max(0, segment.StartMs), max(0, segment.EndMs)
		if durationMs > 0 {
			start, end = min(start, durationMs), min(end, durationMs)
		}
		if end <= start {
			end = start + 1000
			if durationMs > 0 && end > durationMs {
				end = durationMs
				start = max(0, min(start, end-1))
			}
		}
		out = append(out, timelineTranscriptionSegment{StartMs: start, EndMs: end, Text: text})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMs < out[j].StartMs })
	return out
}

// videoReplicationASRAudio 把转写用的 16 kHz wav 压成 mp3（体积约为 wav 的 1/5），返回 base64 与时长。
func videoReplicationASRAudio(ctx context.Context, wavPath string) (string, int64, error) {
	info, err := os.Stat(wavPath)
	if err != nil {
		return "", 0, err
	}
	// 16 kHz 单声道 16 位 wav：去掉 44 字节文件头后每秒 32000 字节。
	durationMs := max(0, info.Size()-44) * 1000 / 32000
	audio, err := encodeVideoReplicationAudio(ctx, wavPath)
	return audio, durationMs, err
}

func encodeVideoReplicationAudio(ctx context.Context, path string) (string, error) {
	ffmpegBin, err := renderFfmpegBinary()
	if err != nil {
		return "", err
	}
	output := filepath.Join(filepath.Dir(path), "inline-audio.mp3")
	defer os.Remove(output)
	if raw, err := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", path, "-vn", "-ac", "1", "-ar", "16000", "-b:a", "48k", output).CombinedOutput(); err != nil {
		return "", fmt.Errorf("压缩音轨失败：%s", clipText(string(raw), 300))
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}
