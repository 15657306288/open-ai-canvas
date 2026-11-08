package app

import "testing"

func TestVideoReplicationVoiceCapacityDoesNotIncreaseTaskCount(t *testing.T) {
	params := VideoReplicationParameters{SegmentSeconds: 30, MaxReferenceVideoSeconds: 30, MaxReferenceVideos: 10, ReferenceMode: "reference", MaxReferenceAudios: 10, GenerateAudio: true, AudioMode: "replace"}
	for _, tc := range []struct {
		ms      int64
		voices  int
		blocked bool
	}{
		{27000, 1, false},
		{27000, 2, true},
		{28524, 1, true},
		{30000, 1, true},
		{30000, 0, false},
	} {
		segments := videoReplicationAutoSegments(VideoReplicationVideoAnalysis{DurationMs: tc.ms}, params.SegmentSeconds, nil)
		if len(segments) != 1 {
			t.Fatalf("%d ms split into %d tasks", tc.ms, len(segments))
		}
		budget := videoReplicationVoiceBudgetMs(params, segments[0].Seconds)
		if got := videoReplicationVoiceDoesNotFit(budget, tc.voices); got != tc.blocked {
			t.Fatalf("%d ms with %d voices: budget %d, blocked %v", tc.ms, tc.voices, budget, got)
		}
	}
	if got := videoReplicationVoiceBudgetMs(VideoReplicationParameters{}, 25); got != -1 {
		t.Fatalf("unknown limit should not cap voice: %d", got)
	}
}

func TestVideoReplicationFitVoicesWhenNoRoom(t *testing.T) {
	s := &Service{}
	if voices, reason, err := s.videoReplicationFitVoices("u", []string{"voice"}, 700, false); err != nil || voices != nil || reason == "" {
		t.Fatalf("uploaded voice without room must stop with a reason: %v %q %v", voices, reason, err)
	}
	if voices, reason, err := s.videoReplicationFitVoices("u", []string{"voice"}, 700, true); err != nil || voices != nil || reason != "" {
		t.Fatalf("source voice without room should be dropped silently: %v %q %v", voices, reason, err)
	}
	if voices, _, _ := s.videoReplicationFitVoices("u", []string{"voice"}, -1, false); len(voices) != 1 || voices[0] != "voice" {
		t.Fatalf("no limit should keep voice: %v", voices)
	}
}

func TestVideoReplicationUploadedVoiceSplitsAtOriginalSpeed(t *testing.T) {
	params := VideoReplicationParameters{SegmentSeconds: 30, MaxReferenceVideoSeconds: 30, MaxReferenceVideos: 1, ReferenceMode: "reference", AudioMode: "replace"}
	limit := videoReplicationVoiceSegmentSeconds(params, 1)
	segments := videoReplicationAutoSegments(VideoReplicationVideoAnalysis{DurationMs: 28800}, limit, nil)
	if limit != 25 || len(segments) != 2 || segments[0].StartMs != 0 || segments[1].EndMs != 28800 {
		t.Fatalf("28.8s original should remain complete across two tasks: %+v", segments)
	}
	for _, segment := range segments {
		if videoReplicationVoiceDoesNotFit(videoReplicationVoiceBudgetMs(params, segment.Seconds), 1) {
			t.Fatalf("no room for uploaded voice: %+v", segment)
		}
	}
	params.AudioMode = "keep"
	if got := videoReplicationVoiceSegmentSeconds(params, 1); got != 30 {
		t.Fatalf("keep original sound = %d", got)
	}
	params.AudioMode = "replace"
	params.ShotSeconds = []int{30}
	if got := videoReplicationVoiceSegmentSeconds(params, 1); got != 0 {
		t.Fatalf("unsupported short durations must stop: %d", got)
	}
}
