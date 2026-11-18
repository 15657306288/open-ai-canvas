package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Keep the complete timeline and existing audio within a single inline request.
// The original resource remains unchanged; only this analysis copy is compressed.
const videoReplicationVisionTotalBytes int64 = 12 << 20

// 视频理解不再按协议名或模型名（Gemini）硬性限制：只看所选文字模型声明的多模态能力。
// 超级智能路由组、其它 OpenAI 兼容中转里的多模态模型同样可以完成视频理解，
// 素材形态由 videoReplicationVisionInputMode 按能力自动选择。
func validateVideoReplicationVisionProtocol(cfg ecommerceProviderConfig) error {
	if cfg.TextProvider == nil {
		return fmt.Errorf("视频理解模型未配置，请在后台「分析与声音模型」选择一个支持图片或视频输入的文字模型")
	}
	// 原生 Gemini 与 Gemini 兼容内联协议本身就是视频理解渠道，历史上不依赖能力字段声明，直接放行。
	if cfg.TextProvider.InterfaceType == "gemini-multimodal-text" || videoReplicationUsesInlineMedia(cfg) {
		return nil
	}
	if profile := cfg.TextProvider.CapabilityConfig; profile != nil && profile.Text != nil {
		references := profile.Text.References
		if references.MaxImages < 1 && references.MaxVideos < 1 {
			return fmt.Errorf("视频理解模型未开启多模态输入，请在后台模型能力里开启「支持图片输入」或「支持视频识别」")
		}
	}
	return nil
}

// videoReplicationVisionInputMode 按渠道协议与模型能力决定把什么素材交给视频理解模型：
//   - inline：只收公网视频链接的 Gemini 兼容渠道，内嵌「抽帧画面 + 完整音轨」；
//   - video：原生 Gemini，或模型能力明确声明支持视频输入，直接传完整视频；
//   - frames：其它多模态文字模型（智能路由组、通用 OpenAI 兼容中转等），
//     只传按时间均匀抽取的画面，走所有多模态渠道都认识的 image_url 内容块。
func videoReplicationVisionInputMode(cfg ecommerceProviderConfig) string {
	if cfg.TextProvider == nil {
		return ""
	}
	if videoReplicationUsesInlineMedia(cfg) {
		return "inline"
	}
	if cfg.TextProvider.InterfaceType == "gemini-multimodal-text" {
		return "video"
	}
	if profile := cfg.TextProvider.CapabilityConfig; profile != nil && profile.Text != nil && profile.Text.References.MaxVideos > 0 {
		return "video"
	}
	return "frames"
}

// videoReplicationVisionFrameBudget 返回 frames 模式下每个参考视频可抽的帧数与总帧数。
// 总帧数受模型声明的单次图片上限约束；能力未声明时沿用界面预览关键帧上限。
func videoReplicationVisionFrameBudget(cfg ecommerceProviderConfig, videos int) (perVideo, total int) {
	total = videoReplicationVisionFrameLimit
	if cfg.TextProvider != nil {
		if profile := cfg.TextProvider.CapabilityConfig; profile != nil && profile.Text != nil {
			if limit := profile.Text.References.MaxImages; limit > 0 {
				total = min(total, limit)
			}
		}
	}
	if total <= 0 {
		return 0, 0
	}
	perVideo = max(2, total/max(1, videos))
	return perVideo, total
}

func prepareVideoReplicationVisionVideo(ctx context.Context, ffmpegBin, source string, probe videoReplicationProbe, budget int64) (string, error) {
	if budget <= 0 || probe.DurationMs <= 0 {
		return "", fmt.Errorf("视频理解的时长或大小预算无效")
	}
	dir, err := os.MkdirTemp("", "yingce-video-vision-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "full-video.mp4")
	// Reserve room for audio, MP4 overhead and bitrate variability. Never trim
	// or drop the audio to force a clip under the request limit.
	bits := budget * 8 * 1000 / probe.DurationMs
	videoBits := bits*8/10 - 64000
	if videoBits < 96000 {
		return "", fmt.Errorf("完整视频超过当前内联请求容量，请减少参考视频数量")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", source,
		"-map", "0:v:0", "-map", "0:a:0?", "-vf", "scale=640:640:force_original_aspect_ratio=decrease:force_divisible_by=2", "-r", "15",
		"-c:v", "libx264", "-preset", "veryfast", "-b:v", strconv.FormatInt(videoBits, 10), "-maxrate", strconv.FormatInt(videoBits, 10), "-bufsize", strconv.FormatInt(videoBits*2, 10),
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "64k", "-movflags", "+faststart", path}
	if output, err := exec.CommandContext(ctx, ffmpegBin, args...).CombinedOutput(); err != nil {
		return "", videoReplicationFFmpegFailure(ctx, "完整视频预处理失败", output, err)
	}
	normalized, err := probeVideoReplicationFile(ctx, path)
	if err != nil {
		return "", err
	}
	if normalized.DurationMs < probe.DurationMs-150 || normalized.DurationMs > probe.DurationMs+150 || normalized.HasAudio != probe.HasAudio {
		return "", fmt.Errorf("完整视频预处理校验失败：时长或音轨不一致")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > budget {
		return "", fmt.Errorf("完整视频超过当前内联请求容量，请减少参考视频数量")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(data), nil
}
