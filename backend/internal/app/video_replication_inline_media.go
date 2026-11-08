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
	"strconv"
	"strings"
)

// 视频理解的「内嵌画面 + 音轨」输入：只接受公网视频链接的渠道（部分 OpenAI 兼容接口）由模型服务回源下载视频，
// 跨境时经常超时（签名链接抓取失败、内嵌视频 data URL 被拒绝）。
// 改为在服务端按时间均匀抽帧（与 Gemini 理解视频时约 1 帧/秒的采样一致），连同完整音轨一起内嵌在请求里发送，
// 不再需要模型服务回源下载。部分接口单条消息最多 16 张图片，所以画面按时间分到多条消息里。

const (
	videoReplicationInlineImagesPerMessage = 16
	// videoReplicationInlineMaxFrames 是所有参考视频合计的最多帧数；120 秒的单个视频约 1 帧/秒。
	videoReplicationInlineMaxFrames = 120
	videoReplicationInlineFrameEdge = 512
)

type videoReplicationInlineFrame struct {
	AtSec   float64
	DataURL string
}

// videoReplicationInlineMedia 是一个参考视频的内嵌输入：按时间顺序的画面与完整音轨（mp3 base64，没有音轨时为空）。
type videoReplicationInlineMedia struct {
	DurationMs int64
	Frames     []videoReplicationInlineFrame
	Audio      string
	AudioLabel string
}

// prepareVideoReplicationInlineMedia 从参考视频抽取最多 maxFrames 帧（不超过 1 帧/秒）与 16 kHz 单声道音轨。
func prepareVideoReplicationInlineMedia(ctx context.Context, ffmpegBin, source string, probe videoReplicationProbe, maxFrames int) (videoReplicationInlineMedia, error) {
	media := videoReplicationInlineMedia{DurationMs: probe.DurationMs}
	if probe.DurationMs <= 0 || maxFrames <= 0 {
		return media, fmt.Errorf("参考视频时长无效，无法抽取画面")
	}
	seconds := float64(probe.DurationMs) / 1000
	fps := math.Min(1, float64(maxFrames)/seconds)
	dir, err := os.MkdirTemp("", "yingce-video-inline-")
	if err != nil {
		return media, err
	}
	defer os.RemoveAll(dir)
	scale := fmt.Sprintf("fps=%s,scale=%d:%d:force_original_aspect_ratio=decrease", strconv.FormatFloat(fps, 'f', 4, 64), videoReplicationInlineFrameEdge, videoReplicationInlineFrameEdge)
	if output, err := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", source, "-vf", scale, "-q:v", "6", filepath.Join(dir, "frame-%04d.jpg")).CombinedOutput(); err != nil {
		return media, fmt.Errorf("抽取参考视频画面失败：%s", clipText(string(output), 300))
	}
	names, _ := filepath.Glob(filepath.Join(dir, "frame-*.jpg"))
	sort.Strings(names)
	if len(names) > maxFrames {
		names = names[:maxFrames]
	}
	for index, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return media, err
		}
		media.Frames = append(media.Frames, videoReplicationInlineFrame{AtSec: math.Round(float64(index)/fps*10) / 10, DataURL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)})
	}
	if len(media.Frames) == 0 {
		return media, fmt.Errorf("没有从参考视频抽取到画面")
	}
	if probe.HasAudio {
		audioPath := filepath.Join(dir, "audio.mp3")
		if output, err := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", source, "-vn", "-ac", "1", "-ar", "16000", "-b:a", "48k", audioPath).CombinedOutput(); err != nil {
			return media, fmt.Errorf("提取参考视频音轨失败：%s", clipText(string(output), 300))
		}
		data, err := os.ReadFile(audioPath)
		if err != nil {
			return media, err
		}
		media.Audio = base64.StdEncoding.EncodeToString(data)
	}
	return media, nil
}

// videoReplicationInlineMessages 把每个参考视频的画面按时间分成多条消息（每条不超过 16 张，每张前标注秒数），
// 再附上各视频的完整音轨和分析要求。
func videoReplicationInlineMessages(system, prompt string, media []videoReplicationInlineMedia) []map[string]any {
	messages := []map[string]any{{"role": "system", "content": system}}
	for videoIndex, item := range media {
		for start := 0; start < len(item.Frames); start += videoReplicationInlineImagesPerMessage {
			end := min(start+videoReplicationInlineImagesPerMessage, len(item.Frames))
			parts := []map[string]any{{"type": "text", "text": fmt.Sprintf("参考视频 V%d（总长 %.1f 秒）按时间顺序的画面，第 %d–%d 张：", videoIndex+1, float64(item.DurationMs)/1000, start+1, end)}}
			for _, frame := range item.Frames[start:end] {
				parts = append(parts, map[string]any{"type": "text", "text": fmt.Sprintf("%.1f 秒", frame.AtSec)}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": frame.DataURL}})
			}
			messages = append(messages, map[string]any{"role": "user", "content": parts})
		}
	}
	last := []map[string]any{}
	for videoIndex, item := range media {
		if item.Audio == "" {
			last = append(last, map[string]any{"type": "text", "text": fmt.Sprintf("参考视频 V%d 没有音轨。", videoIndex+1)})
			continue
		}
		label := firstNonEmpty(item.AudioLabel, fmt.Sprintf("参考视频 V%d 的完整音轨：", videoIndex+1))
		last = append(last, map[string]any{"type": "text", "text": label}, map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": item.Audio, "format": "mp3"}})
	}
	last = append(last, map[string]any{"type": "text", "text": prompt})
	return append(messages, map[string]any{"role": "user", "content": last})
}

// videoReplicationInlineChatJSON 用「内嵌画面 + 音轨」调用 OpenAI 兼容的视频理解渠道；与其他分析调用一样计量并记成本。
func (s *Service) videoReplicationInlineChatJSON(ctx context.Context, cfg ecommerceProviderConfig, stage, system, prompt string, media []videoReplicationInlineMedia, out any) error {
	if cfg.TextProvider == nil {
		return fmt.Errorf("视频理解渠道未配置")
	}
	provider := *cfg.TextProvider
	// 该渠道以 Bearer 认证（与 /v1/chat/completions 其他调用一致）。
	provider.APIFormat = ""
	ctx, meter := s.meterVideoReplicationCall(ctx, stage, "text", &provider, 1)
	callCtx, cancel := context.WithTimeout(ctx, cfg.TextTimeout)
	defer cancel()
	body := map[string]any{"model": provider.Model, "messages": videoReplicationInlineMessages(system, prompt, media)}
	_ = ensureChatCompletionStreamUsage(body)
	result, err := requestTextProvider(withProtocolRegistry(callCtx, s.protocolRegistry()), provider, "/chat/completions", body, "chat-completion", true, nil)
	s.settleVideoReplicationMeter(meter)
	if err != nil {
		return err
	}
	return ecommerceParseJSON(result.Text, out)
}

// videoReplicationInlineMediaProtocols 是走 OpenAI 兼容 /chat/completions、不能回源下载也不收 data URL 视频的视频理解协议：
// apimart-gemini-video-text 协议，以及通用的 OpenAI 兼容 Gemini 多模态协议（sub2api、new-api 等不开放 /v1beta 的中转站）。
var videoReplicationInlineMediaProtocols = map[string]bool{
	"apimart-gemini-video-text": true,
	"openai-gemini-multimodal":  true,
}

// videoReplicationUsesInlineMedia 判断视频理解渠道是否只能接收公网视频链接，需要改用内嵌画面与音轨。
func videoReplicationUsesInlineMedia(cfg ecommerceProviderConfig) bool {
	return cfg.TextProvider != nil && videoReplicationInlineMediaProtocols[cfg.TextProvider.InterfaceType]
}

// videoReplicationRetryable 判断是否是值得自动重试一次的偶发上游错误（抓取超时、繁忙、网关超时）。
func videoReplicationRetryable(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, marker := range []string{"cannot fetch content", "did not respond", "rate limit", "timeout", "timed out", "502", "503", "504", "连接提前关闭"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// videoReplicationFriendlyError 把常见的英文上游错误换成用户看得懂的中文说明。
func videoReplicationFriendlyError(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "cannot fetch content") || strings.Contains(lower, "did not respond"):
		return "模型服务无法下载参考视频（访问超时），请稍后重试"
	case strings.Contains(lower, "too many images"):
		return "参考视频画面数量超过模型单次上限，请缩短参考视频或减少参考视频数量后重试"
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "429"):
		return "模型服务繁忙，请稍后重试"
	}
	return message
}
