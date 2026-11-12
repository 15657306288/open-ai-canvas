package app

import (
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func TestVideoReplicationStructureCoversApparel(t *testing.T) {
	if !videoReplicationNeedsStructure("apparel", "") || !videoReplicationNeedsStructure("model,apparel", "") || videoReplicationNeedsStructure("face", "") || !videoReplicationNeedsStructure("face", "把梨换成香蕉") || videoReplicationNeedsStructure("face", "跑鞋复刻") {
		t.Fatal("structure alignment must run for product and apparel replacement only")
	}
	got := videoReplicationStructureAdaptation(VideoReplicationStructureSegment{Source: "双手插进外套侧袋", Match: "无", Action: "双手自然垂放后轻抚衣摆", Camera: "特写口袋 → 改为特写领口刺绣"})
	for _, want := range []string{"插进外套侧袋", "轻抚衣摆"} {
		if !strings.Contains(got, want) {
			t.Fatalf("adaptation %q missing %q", got, want)
		}
	}
	// 复刻不改原片镜头：camera 不进提示词。
	if strings.Contains(got, "镜头") {
		t.Fatalf("adaptation must not rewrite the camera: %q", got)
	}
	if got := videoReplicationStructureAdaptation(VideoReplicationStructureSegment{Action: "沿用原片动作", Camera: "改为特写袖口"}); got != "" {
		t.Fatalf("camera-only adaptation = %q", got)
	}
}

func TestVideoReplicationEditPromptApparelWearingAndStyling(t *testing.T) {
	references := []map[string]string{{"category": model.VideoReplicationAssetApparelImage}}
	storyboard := VideoReplicationStoryboard{ApparelStructure: "无口袋套头卫衣", Wearing: "下摆自然垂落", Styling: "黑色直筒裤配白色板鞋"}
	shot := VideoReplicationShot{Adaptation: "原片「双手插进口袋」→ 双手自然垂放"}
	prompt := videoReplicationEditPrompt("apparel", VideoReplicationParameters{AudioMode: "replace", Talking: true}, shot, storyboard, "", references)
	for _, want := range []string{"@图片1 中的服装", "无口袋套头卫衣", "下摆自然垂落", "黑色直筒裤配白色板鞋", "不穿透衣服", "双手自然垂放", "嘴型跟随本段新生成的人声"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "手部姿势与动作") {
		t.Fatal("a rewritten segment must not also demand the original hand actions")
	}
	plain := videoReplicationEditPrompt("apparel", VideoReplicationParameters{}, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", references)
	if !strings.Contains(plain, "插口袋") || !strings.Contains(plain, "人物走位与节奏") || strings.Contains(plain, "手部姿势与动作") {
		t.Fatalf("fallback apparel prompt:\n%s", plain)
	}
}

func TestVideoReplicationVoicePromptKeepsMusicAndLipSync(t *testing.T) {
	references := []map[string]string{{"category": model.VideoReplicationAssetModelImage}}
	prompt := videoReplicationWithVoicePrompt("画面", "这款外套很保暖", 1, references, videoReplicationSound{Edit: true, Music: "轻快电子乐", SpeechSegment: true}, videoReplicationPromptBudgetFor(VideoReplicationParameters{}))
	for _, want := range []string{"@音频1 是画面中的人物（即@图片1中的人）", "这款外套很保暖", "口型", "轻快电子乐", "补充要求没说换音乐就不换", "滋滋声"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	silent := videoReplicationWithVoicePrompt("画面", "", 1, references, videoReplicationSound{SpeechSegment: true}, videoReplicationPromptBudgetFor(VideoReplicationParameters{}))
	if !strings.Contains(silent, "人物不开口") || !strings.Contains(silent, "参考音频1") {
		t.Fatalf("segment without lines must keep the speaker silent:\n%s", silent)
	}
}

func TestRenderEdgeFadeFilter(t *testing.T) {
	if renderEdgeFadeFilter(0.04) != "" {
		t.Fatal("very short clips get no fade")
	}
	if got := renderEdgeFadeFilter(5); got != ",afade=t=in:st=0:d=0.015,afade=t=out:st=4.985:d=0.015" {
		t.Fatalf("fade = %q", got)
	}
}

func TestVideoReplicationStemsTimelineUsesNewVocalsAndOriginalMusic(t *testing.T) {
	timeline := renderProject{DurationMs: 10000, Tracks: []renderTrack{{ID: "video", Kind: "video"}}, Clips: []renderClip{
		{ID: "shot-a", Kind: "video", TrackID: "video", StartMs: 0, DurationMs: 6000, Volume: 1},
		{ID: "shot-b", Kind: "video", TrackID: "video", StartMs: 6000, DurationMs: 4000, Volume: 1},
	}}
	applyVideoReplicationStems(&timeline, map[string]string{"shot-a": "run-a", "shot-b": "run-b"}, map[string]string{"run-a": "voc-a"}, "music")
	plan := buildRenderPlan(timeline)
	if len(plan.Segments) != 2 || plan.Segments[0].Clip.Volume >= 0 || plan.Segments[1].Clip.Volume != 1 {
		t.Fatalf("separated segment must be muted, failed one keeps its own audio: %+v", plan.Segments)
	}
	if len(plan.AudioOverlays) != 2 || plan.AudioOverlays[0].Clip.ID != "stem-music" && plan.AudioOverlays[1].Clip.ID != "stem-music" {
		t.Fatalf("overlays = %+v", plan.AudioOverlays)
	}
	for _, overlay := range plan.AudioOverlays {
		if overlay.Clip.ID == "vocals-shot-a" && (overlay.Clip.StartMs != 0 || overlay.Clip.DurationMs != 6000) {
			t.Fatalf("vocals must sit on the segment: %+v", overlay.Clip)
		}
		if overlay.Clip.ID == "stem-music" && overlay.Clip.DurationMs != 10000 {
			t.Fatalf("music must cover the whole film: %+v", overlay.Clip)
		}
	}
}

func TestVideoReplicationVoicePromptStemsAsksForVoiceOnly(t *testing.T) {
	prompt := videoReplicationWithVoicePrompt("画面", "你好", 1, nil, videoReplicationSound{Music: "轻快电子乐", Stems: true}, videoReplicationPromptBudgetFor(VideoReplicationParameters{}))
	if !strings.Contains(prompt, "不要生成任何背景音乐") || strings.Contains(prompt, "轻快电子乐") {
		t.Fatalf("stems prompt:\n%s", prompt)
	}
}
