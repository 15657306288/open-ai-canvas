package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func TestVideoReplicationProductSpeechDoesNotDependOnSeparator(t *testing.T) {
	t.Setenv(videoReplicationSeparatorEnv, "")
	params := VideoReplicationParameters{GenerateAudio: true, AudioMode: "keep", KeepOriginalAudio: true}
	for _, target := range []string{"product", "apparel", "model,product"} {
		if !videoReplicationAdaptVoice(target, params, "") {
			t.Fatalf("%s must replace old product speech even when keeping music", target)
		}
	}
	if videoReplicationAdaptVoice("model", params, "") || videoReplicationAdaptVoice("face", params, "") {
		t.Fatal("person-only replacement must keep existing dialogue")
	}
	person := params
	person.ReplaceMode = "precise-person"
	if videoReplicationAdaptVoice("model", person, "眼神自然，手势流畅") {
		t.Fatal("precise-person visual notes must keep the existing original-audio behavior")
	}
	params.RewriteVoice = true
	params.AudioMode = "mute"
	if videoReplicationAdaptVoice("product", params, "") {
		t.Fatal("explicit mute must override a saved rewrite flag")
	}
}

func TestVideoReplicationRewriteRejectsMissingAndOverlongLines(t *testing.T) {
	for _, candidate := range []string{"", "（微笑）", strings.Repeat("苹果", 20)} {
		line, err := videoReplicationAdaptedLine("这根香蕉好吃", candidate, 20, 2)
		if err == nil || line != "" || !strings.Contains(err.Error(), "第 2 段") {
			t.Fatalf("invalid rewrite fell back to old product: %q, %v", line, err)
		}
	}
	line, err := videoReplicationAdaptedLine("这根香蕉好吃", "（微笑）看看这颗苹果。", 20, 1)
	if err != nil || line != "看看这颗苹果。" {
		t.Fatalf("valid replacement = %q, %v", line, err)
	}
	if line, err := videoReplicationAdaptedLine("", "模型擅自增加的口播", 20, 3); err != nil || line != "" {
		t.Fatalf("silent source must remain silent: %q, %v", line, err)
	}
}

func TestVideoReplicationNarrationKeepsSpeakerOffCamera(t *testing.T) {
	analysis := &VideoReplicationAnalysis{Voice: &VideoReplicationVoice{Type: "narration"}}
	if !videoReplicationNarrationOnly(analysis) || videoReplicationNarrationOnly(nil) {
		t.Fatal("narration classification")
	}
	references := []map[string]string{{"category": model.VideoReplicationAssetModelImage}}
	for _, sourceVoice := range []bool{false, true} {
		prompt := videoReplicationWithVoicePrompt("手拿苹果的产品特写", "看看这颗苹果。", 1, references, videoReplicationSound{Edit: true, Narration: true, SourceVoice: sourceVoice}, videoReplicationPromptBudgetFor(VideoReplicationParameters{PromptMaxChars: 5000}))
		for _, want := range []string{"画外旁白逐字说「看看这颗苹果。", "不新增出镜说话人", "@音频1"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("missing %q: %s", want, prompt)
			}
		}
		for _, forbidden := range []string{"口型：嘴型与人声", "人物逐字说", "不出现其他人声或旁白", "即@图片1中的人"} {
			if strings.Contains(prompt, forbidden) {
				t.Fatalf("narration forced an on-camera speaker (%s): %s", forbidden, prompt)
			}
		}
	}
}

func TestVideoReplicationSourceVideoVoiceAndFullDialogueSurvive(t *testing.T) {
	lines := strings.Repeat("这颗苹果看起来不错。", 50)
	prompt := videoReplicationWithVoicePrompt("画面", lines, 0, nil, videoReplicationSound{Edit: true}, videoReplicationPromptBudgetFor(VideoReplicationParameters{PromptMaxChars: 8000}))
	if !strings.Contains(prompt, "沿用 @视频1 中原说话人的音色") || !strings.Contains(prompt, lines) || !strings.Contains(prompt, "口型：嘴型与人声逐字同步") {
		t.Fatal("source voice or full final dialogue was lost")
	}
}

func TestVideoReplicationRewriteKeepsCompleteSubtitlesAfterNormalization(t *testing.T) {
	lines := strings.Repeat("看看苹果的颜色。", 30)
	board, err := normalizeVideoReplicationStoryboard(VideoReplicationStoryboard{
		VoiceAdapted: true,
		Subtitles:    true,
		Shots:        []VideoReplicationShot{{DurationSec: 30, Prompt: "苹果特写", Voiceover: lines, Subtitle: "旧香蕉字幕"}},
	}, []int{30})
	if err != nil || board.Shots[0].Voiceover != lines || board.Shots[0].Subtitle != lines {
		t.Fatalf("final dialogue/subtitles were clipped or diverged: %+v, %v", board, err)
	}
}

func TestVideoReplicationManualPlanCannotRestoreOldProductAudio(t *testing.T) {
	project := videoReplicationPlanProject(t, VideoReplicationParameters{LogicalModelID: "model", Model: "m", ReferenceMode: "reference", MaxReferenceVideos: 1, GenerateAudio: true, ShotSeconds: []int{5}}, 1, "narration")
	board := videoReplicationStoryboard(project)
	board.VoiceAdapted = true
	board.Shots[0].Voiceover = "看看这颗苹果。"
	raw, _ := json.Marshal(board)
	project.StoryboardJSON = string(raw)
	if _, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioOriginal, VoiceAuthorized: true}); err == nil {
		t.Fatal("manual confirmation must not overlay old banana speech")
	}
	plan, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioNative})
	if err != nil || !plan.SpeakLines {
		t.Fatalf("manual confirmation must speak rewritten narration: %+v, %v", plan, err)
	}
}

func TestVideoReplicationSilentProductSubtitlesAreAdaptedWithoutVoice(t *testing.T) {
	analysis := &VideoReplicationAnalysis{Videos: []VideoReplicationVideoAnalysis{{Shots: []VideoReplicationSourceShot{{StartMs: 0, EndMs: 5000, Subtitle: "看看这根香蕉。"}}}}}
	params := VideoReplicationParameters{AudioMode: "mute"}
	if !videoReplicationAdaptSubtitles("product", params, analysis) || videoReplicationAdaptSubtitles("model", params, analysis) {
		t.Fatal("muted product subtitles must be adapted without changing person-only replacement")
	}
	segments := []videoReplicationAutoSegment{{StartMs: 0, EndMs: 5000, Seconds: 5}, {StartMs: 5000, EndMs: 10000, Seconds: 5}}
	videoReplicationAssignSubtitleLines(segments, analysis.Videos[0].Shots)
	if segments[0].Subtitle != "看看这根香蕉。" || segments[1].Subtitle != "" || segments[0].MaxChars <= 0 {
		t.Fatalf("subtitle source must be assigned once with a readable budget: %+v", segments)
	}
	board, err := normalizeVideoReplicationStoryboard(VideoReplicationStoryboard{
		Subtitles: true, SubtitlesAdapted: true,
		Shots: []VideoReplicationShot{{ID: "apple", DurationSec: 5, Prompt: "苹果特写", Subtitle: "看看这颗苹果。"}},
	}, []int{5})
	if err != nil || board.Shots[0].Voiceover != "" || board.Shots[0].Subtitle != "看看这颗苹果。" || board.VoiceAdapted {
		t.Fatalf("subtitle-only replacement must not invent narration: %+v, %v", board, err)
	}
	runs := []model.VideoReplicationShotRun{{ID: "run", ShotID: "apple", Status: model.VideoReplicationRunSucceeded, OutputResourceID: "video"}}
	timeline, _, missing := buildVideoReplicationTimeline(board, params, &VideoReplicationPlan{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioMute}, runs, func(string) int64 { return 5000 })
	if len(missing) != 0 {
		t.Fatalf("timeline unexpectedly missing clips: %v", missing)
	}
	found := false
	for _, clip := range timeline.Clips {
		if clip.Kind == "subtitle" {
			found = true
			if !strings.Contains(clip.Text, "苹果") || strings.Contains(clip.Text, "香蕉") {
				t.Fatalf("old product text returned to the rendered video: %+v", clip)
			}
		}
	}
	if !found || !timeline.Tracks[0].Muted {
		t.Fatalf("muted output must retain rewritten subtitles: %+v", timeline)
	}
}

func TestVideoReplicationKnownSpeechWithoutTranscriptStopsBeforePlanning(t *testing.T) {
	for _, voiceType := range []string{"on_camera", "narration", "dialogue"} {
		analysis := &VideoReplicationAnalysis{
			Voice:  &VideoReplicationVoice{Type: voiceType, ASRAvailable: true},
			Videos: []VideoReplicationVideoAnalysis{{DurationMs: 5000, HasAudio: true, Shots: []VideoReplicationSourceShot{{StartMs: 0, EndMs: 5000}}}},
		}
		project := &model.VideoReplicationProject{VideoType: "product"}
		_, err := (&Service{}).planVideoReplicationAutoStoryboard(context.Background(), ecommerceProviderConfig{}, project, VideoReplicationParameters{GenerateAudio: true, AudioMode: "keep", SegmentSeconds: 5}, nil, analysis)
		if err == nil || !strings.Contains(err.Error(), "没有取得可用台词") {
			t.Fatalf("%s must not fall back to old product audio: %v", voiceType, err)
		}
	}
}
