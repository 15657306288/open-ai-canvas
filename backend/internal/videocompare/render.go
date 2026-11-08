// Package videocompare renders a local, side-by-side comparison without changing either source.
package videocompare

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strings"
)

type Media struct {
	Width, Height int
	DurationMs    int64
	HasAudio      bool
}

type Options struct {
	Reference, Generated Media
	StartMs, EndMs       int64
}

// Args keeps the generated duration, freezes a shorter reference's last frame,
// and uses one soundtrack: generated audio first, reference audio otherwise.
func Args(reference, generated, output string, o Options) ([]string, error) {
	if o.Generated.Width <= 0 || o.Generated.Height <= 0 || o.Reference.DurationMs <= 0 || o.Generated.DurationMs <= 0 || o.Generated.DurationMs > 120100 {
		return nil, fmt.Errorf("对比视频仅支持 120 秒以内的有效成片")
	}
	end := o.Reference.DurationMs
	if o.EndMs > 0 {
		end = min(end, o.EndMs)
	}
	if o.StartMs < 0 || o.StartMs >= end {
		return nil, fmt.Errorf("原视频裁剪区间无效")
	}
	scale := math.Min(960/float64(o.Generated.Width), 720/float64(o.Generated.Height))
	w := max(2, int(float64(o.Generated.Width)*scale)/2*2)
	h := max(2, int(float64(o.Generated.Height)*scale)/2*2)
	seconds := fmt.Sprintf("%.3f", float64(o.Generated.DurationMs)/1000)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-threads", "2",
		"-ss", fmt.Sprintf("%.3f", float64(o.StartMs)/1000), "-t", fmt.Sprintf("%.3f", float64(end-o.StartMs)/1000), "-i", reference,
		"-threads", "2", "-i", generated, "-filter_complex_threads", "1"}
	fit := fmt.Sprintf("setpts=PTS-STARTPTS,fps=30,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,format=yuv420p", w, h, w, h)
	filters := []string{fmt.Sprintf("[0:v:0]%s,tpad=stop_mode=clone:stop_duration=%s[left]", fit, seconds),
		fmt.Sprintf("[1:v:0]%s[right]", fit), "[left][right]hstack=inputs=2:shortest=1[v]"}
	audio := -1
	if o.Generated.HasAudio {
		audio = 1
	} else if o.Reference.HasAudio {
		audio = 0
	}
	if audio >= 0 {
		filters = append(filters, fmt.Sprintf("[%d:a:0]asetpts=PTS-STARTPTS,aresample=48000,apad[a]", audio))
	}
	args = append(args, "-filter_complex", strings.Join(filters, ";"), "-map", "[v]")
	if audio >= 0 {
		args = append(args, "-map", "[a]", "-c:a", "aac", "-b:a", "160k")
	} else {
		args = append(args, "-an")
	}
	return append(args, "-t", seconds, "-c:v", "libx264", "-threads", "2", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p", "-movflags", "+faststart", output), nil
}

func Render(ctx context.Context, binary, reference, generated, output string, options Options) error {
	args, err := Args(reference, generated, output, options)
	if err != nil {
		return err
	}
	if data, err := exec.CommandContext(ctx, binary, args...).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("对比视频合成超时或已取消，请稍后重试: %w", ctx.Err())
		}
		// The caller logs diagnostics; media paths and FFmpeg internals are not user copy.
		return fmt.Errorf("对比视频合成失败: %w (%s)", err, strings.TrimSpace(string(data)))
	}
	return nil
}
