package app

import (
	"strings"
	"testing"
)

func TestVideoReplicationModelFamily(t *testing.T) {
	cases := map[string]VideoReplicationParameters{
		VideoReplicationKnowledgeSeedance: {Model: "doubao-seedance-2-0-mini-260128"},
		VideoReplicationKnowledgeKling:    {Model: "kling-v4", ModelLabel: "可灵 4.0"},
		"":                                {Model: "minimax-video-768p"},
	}
	for want, params := range cases {
		if got := videoReplicationModelFamily(params); got != want {
			t.Fatalf("%s: got %q want %q", params.Model, got, want)
		}
	}
	if videoReplicationModelFamily(VideoReplicationParameters{Model: "x", ModelLabel: "Seedance 2.0"}) != VideoReplicationKnowledgeSeedance {
		t.Fatal("logical model label should match seedance")
	}
}

func TestVideoReplicationKnowledgeDefaultsAndToggle(t *testing.T) {
	var setting VideoReplicationSetting
	enabled, content := setting.knowledge(VideoReplicationKnowledgeSeedance)
	if !enabled || !strings.Contains(content, "Seedance 2.5") {
		t.Fatal("seedance knowledge should default to enabled built-in content")
	}
	if enabled, content := setting.knowledge(VideoReplicationKnowledgeKling); !enabled || !strings.Contains(content, "可灵 4.0") {
		t.Fatal("kling knowledge should default to enabled built-in content")
	}
	saved, err := normalizeVideoReplicationKnowledge([]VideoReplicationKnowledgeBase{{ID: "seedance", Enabled: false, Content: content}, {ID: "kling", Enabled: true, Content: "自定义"}})
	if err != nil {
		t.Fatal(err)
	}
	if saved[0].Content != "" {
		t.Fatal("content equal to default should not be stored")
	}
	setting.Knowledge = saved
	if enabled, _ := setting.knowledge(VideoReplicationKnowledgeSeedance); enabled {
		t.Fatal("disabled knowledge should stay disabled")
	}
	if _, content := setting.knowledge(VideoReplicationKnowledgeKling); content != "自定义" {
		t.Fatal("custom content should be used")
	}
	if _, err := normalizeVideoReplicationKnowledge([]VideoReplicationKnowledgeBase{{ID: "other"}}); err == nil {
		t.Fatal("unknown knowledge id should be rejected")
	}
}

func TestVideoReplicationAdaptVoice(t *testing.T) {
	params := VideoReplicationParameters{GenerateAudio: true, AudioMode: "generate"}
	if !videoReplicationAdaptVoice(VideoReplicationTypeProduct, params, "") {
		t.Fatal("product replacement should adapt voiceover")
	}
	if videoReplicationAdaptVoice(VideoReplicationTypeModel, params, "") {
		t.Fatal("model-only replacement without notes keeps the original lines")
	}
	if !videoReplicationAdaptVoice(VideoReplicationTypeModel, params, "突出防水") {
		t.Fatal("user notes should adapt voiceover")
	}
	params.AudioMode = "mute"
	if videoReplicationAdaptVoice(VideoReplicationTypeProduct, params, "") {
		t.Fatal("muted output never adapts voiceover")
	}
}

func TestVideoReplicationSubtitleCuesFollowVoiceover(t *testing.T) {
	cues := videoReplicationSubtitleCues("姐妹们，这款面霜真的好用！一抹就化开。", 1000, 5000)
	if len(cues) != 3 {
		t.Fatalf("want 3 cues, got %d: %+v", len(cues), cues)
	}
	joined := ""
	end := int64(0)
	for _, cue := range cues {
		joined += cue.Text
		if cue.StartMs < end || cue.DurationMs <= 0 {
			t.Fatalf("cues overlap or empty: %+v", cues)
		}
		end = cue.StartMs + cue.DurationMs
	}
	if joined != "姐妹们这款面霜真的好用！一抹就化开" || end > 6000 {
		t.Fatalf("unexpected cues %q end %d", joined, end)
	}
}

func TestVideoReplicationCleanVoiceover(t *testing.T) {
	if got := videoReplicationCleanVoiceover("（笑）“这款保温杯”真的绝了😍  一整天都热乎"); got != "这款保温杯真的绝了 一整天都热乎" {
		t.Fatalf("got %q", got)
	}
}
