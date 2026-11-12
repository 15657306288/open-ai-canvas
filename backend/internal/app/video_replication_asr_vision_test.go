package app

import "testing"

func TestNormalizeVideoReplicationDictation(t *testing.T) {
	got := normalizeVideoReplicationDictation([]timelineTranscriptionSegment{
		{StartMs: 6000, EndMs: 9000, Text: " 第二句。"},
		{StartMs: 500, EndMs: 3000, Text: "第一句。"},
		{StartMs: 4000, EndMs: 4000, Text: "没给结束时间"},
		{StartMs: 9500, EndMs: 99000, Text: "超出总长"},
		{StartMs: 1000, EndMs: 2000, Text: "  "},
	}, 10000)
	if len(got) != 4 {
		t.Fatalf("segments = %+v", got)
	}
	if got[0].Text != "第一句。" || got[1].StartMs != 4000 || got[1].EndMs != 5000 || got[2].Text != "第二句。" || got[3].EndMs != 10000 {
		t.Fatalf("normalized = %+v", got)
	}
}

func TestVideoReplicationInlineMessagesAudioOnly(t *testing.T) {
	messages := videoReplicationInlineMessages("sys", "听写", []videoReplicationInlineMedia{{DurationMs: 5000, Audio: "bXAz"}})
	if len(messages) != 2 {
		t.Fatalf("audio-only request should be system + one user message, got %d", len(messages))
	}
	parts, _ := messages[1]["content"].([]map[string]any)
	if len(parts) != 3 || parts[1]["type"] != "input_audio" || parts[2]["text"] != "听写" {
		t.Fatalf("parts = %+v", parts)
	}
}

func TestVideoReplicationASRVisionSettingMode(t *testing.T) {
	setting := VideoReplicationSetting{ASR: &VideoReplicationASRSetting{Mode: videoReplicationASRModeVision}}
	if !setting.asrVisionMode() || setting.asrModelMode() {
		t.Fatal("vision mode flags")
	}
	if (VideoReplicationSetting{}).asrVisionMode() {
		t.Fatal("unset ASR is not vision mode")
	}
}
