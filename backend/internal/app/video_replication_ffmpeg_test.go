package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

// 这些用例真实调用 ffmpeg/ffprobe（与生产后端镜像同样通过 PATH 查找），本机没有安装时跳过。

func requireVideoReplicationFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil || !mediaProbeAvailable() {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	return ffmpegBin
}

// makeReplicationClip 用 lavfi 生成 segments 段不同画面（每段 segmentSeconds 秒）的竖屏视频，可选正弦波音轨。
func makeReplicationClip(t *testing.T, ffmpegBin, path string, segments int, segmentSeconds float64, audio bool) {
	t.Helper()
	sources := []string{"color=c=red", "color=c=blue", "testsrc2", "color=c=green", "color=c=white", "smptebars"}
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	inputs := ""
	for index := 0; index < segments; index++ {
		source := sources[index%len(sources)]
		separator := "="
		if strings.Contains(source, "=") {
			separator = ":"
		}
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("%s%ss=360x640:r=30:d=%.2f", source, separator, segmentSeconds))
		inputs += fmt.Sprintf("[%d:v]", index)
	}
	total := float64(segments) * segmentSeconds
	if audio {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("sine=f=440:d=%.2f", total))
	}
	args = append(args, "-filter_complex", fmt.Sprintf("%sconcat=n=%d:v=1:a=0[v]", inputs, segments), "-map", "[v]")
	if audio {
		args = append(args, "-map", fmt.Sprintf("%d:a", segments), "-c:a", "aac")
	}
	args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", path)
	if out, err := exec.Command(ffmpegBin, args...).CombinedOutput(); err != nil {
		t.Fatalf("generate clip: %v %s", err, out)
	}
}

func TestVideoReplicationFFmpegPipelineOnRealVideos(t *testing.T) {
	ffmpegBin := requireVideoReplicationFFmpeg(t)
	dir := t.TempDir()
	ctx := context.Background()

	// 场景 A：10 秒、4 个镜头、含音轨。
	tenSeconds := filepath.Join(dir, "reference-10s.mp4")
	makeReplicationClip(t, ffmpegBin, tenSeconds, 4, 2.5, true)
	probe, err := probeVideoReplicationFile(ctx, tenSeconds)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Width != 360 || probe.Height != 640 || probe.DurationMs < 9900 || probe.DurationMs > 10100 || probe.FPS != 30 || !probe.HasAudio {
		t.Fatalf("10s probe = %+v", probe)
	}
	if err := validateVideoReplicationDuration(probe.DurationMs); err != nil {
		t.Fatalf("10s video rejected: %v", err)
	}
	cuts, err := detectVideoReplicationCuts(ctx, ffmpegBin, tenSeconds)
	if err != nil {
		t.Fatal(err)
	}
	spans := videoReplicationSpans(cuts, probe.DurationMs)
	if len(spans) != 4 {
		t.Fatalf("expected 4 shots from 3 hard cuts, cuts=%v spans=%+v", cuts, spans)
	}
	for index, span := range spans {
		frame := filepath.Join(dir, fmt.Sprintf("frame-%d.jpg", index))
		if err := extractVideoReplicationFrame(ctx, ffmpegBin, tenSeconds, (span.StartMs+span.EndMs)/2, frame); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(frame); err != nil || info.Size() < 500 {
			t.Fatalf("keyframe %d missing: %v", index, err)
		}
	}
	audio := filepath.Join(dir, "audio.m4a")
	if err := extractVideoReplicationAudio(ctx, ffmpegBin, tenSeconds, audio); err != nil {
		t.Fatal(err)
	}
	if duration := probeMediaDurationMs(audio); duration < 9000 {
		t.Fatalf("extracted audio duration = %d", duration)
	}
	t.Logf("10s reference: %dx%d %.2ffps audio=%v cuts=%v shots=%d", probe.Width, probe.Height, probe.FPS, probe.HasAudio, cuts, len(spans))

	// 场景 B：15 秒、6 个镜头，处在上限内。
	fifteen := filepath.Join(dir, "reference-15s.mp4")
	makeReplicationClip(t, ffmpegBin, fifteen, 6, 2.5, false)
	probe15, err := probeVideoReplicationFile(ctx, fifteen)
	if err != nil || validateVideoReplicationDuration(probe15.DurationMs) != nil || probe15.HasAudio {
		t.Fatalf("15s probe = %+v err=%v", probe15, err)
	}
	cuts15, _ := detectVideoReplicationCuts(ctx, ffmpegBin, fifteen)
	if spans := videoReplicationSpans(cuts15, probe15.DurationMs); len(spans) != 6 {
		t.Fatalf("15s shots = %+v (cuts %v)", spans, cuts15)
	}

	// 时长越界：3 秒与 125 秒都被真实时长校验拒绝。
	for _, item := range []struct {
		segments int
		seconds  float64
	}{{1, 3}, {5, 25}} {
		path := filepath.Join(dir, fmt.Sprintf("bad-%d.mp4", item.segments))
		makeReplicationClip(t, ffmpegBin, path, item.segments, item.seconds, false)
		bad, err := probeVideoReplicationFile(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if validateVideoReplicationDuration(bad.DurationMs) == nil {
			t.Fatalf("%dms video should be rejected", bad.DurationMs)
		}
	}
}

func TestVideoReplicationComposeRendersRealTimeline(t *testing.T) {
	ffmpegBin := requireVideoReplicationFFmpeg(t)
	dir := t.TempDir()
	ctx := context.Background()
	// 模拟三个镜头的生成结果（各 4 秒；第二个无音轨，验证静音回退）。
	paths := map[string]string{}
	for index, audio := range []bool{true, false, true} {
		path := filepath.Join(dir, fmt.Sprintf("shot-%d.mp4", index))
		makeReplicationClip(t, ffmpegBin, path, 1, 4, audio)
		paths[fmt.Sprintf("res-%d", index)] = path
	}
	storyboard := VideoReplicationStoryboard{Shots: []VideoReplicationShot{{ID: "s1", DurationSec: 4, Prompt: "a", Subtitle: "开场"}, {ID: "s2", DurationSec: 4, Prompt: "b"}, {ID: "s3", DurationSec: 4, Prompt: "c", Subtitle: "结尾"}}}
	runs := []model.VideoReplicationShotRun{
		{ID: "r1", ShotID: "s1", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "res-0", Selected: true},
		{ID: "r2-old", ShotID: "s2", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "res-missing"},
		{ID: "r2", ShotID: "s2", Version: 2, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "res-1", Selected: true},
		{ID: "r3", ShotID: "s3", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "res-2"},
	}
	params := VideoReplicationParameters{Ratio: "9:16", Resolution: "720p", Transition: "fade", Subtitles: true}
	timeline, runIDs, missing := buildVideoReplicationTimeline(storyboard, params, &VideoReplicationPlan{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioNative}, runs, func(resourceID string) int64 { return probeMediaDurationMs(paths[resourceID]) })
	if len(missing) != 0 || strings.Join(runIDs, ",") != "r1,r2,r3" || timeline.Width != 720 || timeline.Height != 1280 {
		t.Fatalf("timeline runIDs=%v missing=%v size=%dx%d", runIDs, missing, timeline.Width, timeline.Height)
	}
	plan := buildRenderPlan(timeline)
	if !plan.HasMedia || len(plan.Segments) != 3 || !strings.Contains(plan.SubtitleSRT, "开场") || !strings.Contains(plan.SubtitleSRT, "结尾") {
		t.Fatalf("plan = %+v", plan)
	}
	for index := range plan.Segments {
		resourceID, _ := mediaResourceID(plan.Segments[index].Clip)
		path := paths[resourceID]
		hasAudio, err := probeHasAudioStream(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		plan.Segments[index].Source = &renderSource{ResourceID: resourceID, Clip: plan.Segments[index].Clip, Path: path, Ext: ".mp4", HasAudio: hasAudio, Width: 360, Height: 640}
	}
	plan.PropagateID = "test-render"
	output := filepath.Join(dir, "final.mp4")
	cmd := exec.CommandContext(ctx, ffmpegBin, buildRenderFFmpegArgs(plan, output)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("render failed: %v %s", err, out)
	}
	final, err := probeVideoReplicationFile(ctx, output)
	if err != nil {
		t.Fatal(err)
	}
	if final.Width != 720 || final.Height != 1280 || final.DurationMs < 11800 || final.DurationMs > 12300 || !final.HasAudio {
		t.Fatalf("final = %+v", final)
	}
	raw, _ := json.Marshal(final)
	t.Logf("composed final video: %s", raw)
	if _, missing := func() ([]string, []string) {
		_, ids, missing := buildVideoReplicationTimeline(storyboard, params, &VideoReplicationPlan{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioNative}, runs[:1], func(string) int64 { return 0 })
		return ids, missing
	}(); strings.Join(missing, ",") != "2,3" {
		t.Fatalf("missing shots = %v", missing)
	}
}

func TestVideoReplicationFFmpegLongReferences(t *testing.T) {
	ffmpegBin := requireVideoReplicationFFmpeg(t)
	dir := t.TempDir()
	ctx := context.Background()
	for _, item := range []struct {
		segments int
		seconds  float64
		shots    int
	}{{12, 5, 12}, {24, 5, 24}} {
		path := filepath.Join(dir, fmt.Sprintf("long-%d.mp4", item.segments))
		makeReplicationClip(t, ffmpegBin, path, item.segments, item.seconds, true)
		probe, err := probeVideoReplicationFile(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateVideoReplicationDuration(probe.DurationMs); err != nil {
			t.Fatalf("%dms rejected: %v", probe.DurationMs, err)
		}
		cuts, err := detectVideoReplicationCuts(ctx, ffmpegBin, path)
		if err != nil {
			t.Fatal(err)
		}
		spans := videoReplicationSpans(cuts, probe.DurationMs)
		if len(spans) != item.shots {
			t.Fatalf("%.0fs reference: %d shots (cuts %v)", float64(probe.DurationMs)/1000, len(spans), cuts)
		}
		t.Logf("%.0fs reference: %d cuts -> %d shots", float64(probe.DurationMs)/1000, len(cuts), len(spans))
	}
}

func TestInspectVideoReplicationFileDetectsProblems(t *testing.T) {
	ffmpegBin := requireVideoReplicationFFmpeg(t)
	dir := t.TempDir()
	ctx := context.Background()
	good := filepath.Join(dir, "good.mp4")
	makeReplicationClip(t, ffmpegBin, good, 2, 3, true)
	quality := inspectVideoReplicationFile(ctx, good, 6000, true)
	if !quality.Passed || quality.DurationMs < 5900 || !quality.HasAudio {
		t.Fatalf("good video failed: %+v", quality)
	}
	// 模型返回时长不足（目标 30 秒只给 6 秒）必须判定失败，不能当成功。
	if short := inspectVideoReplicationFile(ctx, good, 30000, true); short.Passed {
		t.Fatalf("short video passed: %+v", short)
	}
	black := filepath.Join(dir, "black.mp4")
	if out, err := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=black:s=360x640:r=30:d=6", "-c:v", "libx264", "-pix_fmt", "yuv420p", black).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	blackQuality := inspectVideoReplicationFile(ctx, black, 6000, true)
	if blackQuality.Passed || blackQuality.BlackMs < 5000 {
		t.Fatalf("black/silent video passed: %+v", blackQuality)
	}
	broken := filepath.Join(dir, "broken.mp4")
	_ = os.WriteFile(broken, []byte("not a video"), 0o600)
	if bad := inspectVideoReplicationFile(ctx, broken, 6000, false); bad.Passed {
		t.Fatalf("broken file passed: %+v", bad)
	}
	raw, _ := json.Marshal(blackQuality.Checks)
	t.Logf("black video checks: %s", raw)
}
