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

func validateVideoReplicationVisionProtocol(cfg ecommerceProviderConfig) error {
	if cfg.TextProvider == nil || (cfg.TextProvider.InterfaceType != "gemini-multimodal-text" && !videoReplicationUsesInlineMedia(cfg)) {
		return fmt.Errorf("完整视频理解需要选择 Gemini 多模态文本识别、OpenAI 兼容 Gemini 多模态视频理解协议，并确认上游支持视频输入；当前配置仅可用于图文问答")
	}
	return nil
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
		return "", fmt.Errorf("完整视频预处理失败：%s", clipText(string(output), 300))
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
