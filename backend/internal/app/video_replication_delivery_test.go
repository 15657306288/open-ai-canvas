package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func livestreamAnalysis() VideoReplicationAnalysis {
	return VideoReplicationAnalysis{
		Summary: VideoReplicationSummary{Delivery: &VideoReplicationSpeechDelivery{Mode: "livestream", Pace: "短句快速连说，展示时放慢", Pauses: "卖点后短促换气", Emphasis: "新商品名称重读", Interaction: "看镜头，举起商品", Evidence: "0–10秒音轨中连续快速讲解，展示时停顿"}},
		Product: map[string]any{"deliveryTone": "温柔缓慢地分享"},
		Voice:   &VideoReplicationVoice{Type: "on_camera", HasAudio: true, CharsPerSec: 6.5},
		Videos: []VideoReplicationVideoAnalysis{{HasAudio: true, Shots: []VideoReplicationSourceShot{
			{StartMs: 0, EndMs: 5000, SpeechDelivery: "快速介绍后举起商品停顿"},
			{StartMs: 5000, EndMs: 10000, SpeechDelivery: "第二镜头放慢展示细节"},
		}}},
	}
}

func TestVideoReplicationDeliveryReadsActualVisionResult(t *testing.T) {
	f := newVideoReplicationCostFixture(t)
	f.reply.Store(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		data, _ := json.Marshal(body)
		if !strings.Contains(string(data), "speechDelivery") || !strings.Contains(string(data), "静态直播间画面") {
			t.Error("vision request lacks sound evidence analysis")
		}
		videoReplicationVoiceBudgetReply(w, map[string]any{
			"summary": livestreamAnalysis().Summary,
			"shots":   []any{map[string]string{"ref": "V1-S1", "voiceover": "看看这件新品。", "speechDelivery": "快速介绍后举起商品停顿"}},
		})
	})
	cfg := f.cfg("vision-test")
	cfg.TextProvider.InterfaceType = "apimart-gemini-video-text"
	analysis := &VideoReplicationAnalysis{Voice: &VideoReplicationVoice{HasAudio: true}, Videos: []VideoReplicationVideoAnalysis{{Primary: true, HasAudio: true, Shots: []VideoReplicationSourceShot{{Index: 0, EndMs: 5000}}}}}
	if err := f.svc.understandVideoReplicationShots(context.Background(), cfg, "project", analysis, nil, nil, []string{"https://media.example/reference.mp4"}); err != nil {
		t.Fatal(err)
	}
	if analysis.Summary.Delivery == nil || analysis.Summary.Delivery.Mode != "livestream" || analysis.Videos[0].Shots[0].SpeechDelivery != "快速介绍后举起商品停顿" {
		t.Fatalf("observed speech was lost: %+v", analysis)
	}
}

func TestVideoReplicationDeliverySelectsClipAndRespectsEvidence(t *testing.T) {
	for _, kind := range []string{"live", "agent-tone", "review", "image-only", "no-evidence", "user-slow", "music"} {
		t.Run(kind, func(t *testing.T) {
			analysis := livestreamAnalysis()
			project := &model.VideoReplicationProject{}
			switch kind {
			case "agent-tone":
				project.VoiceCastJSON = `{"roles":[{"instructions":"温柔缓慢","instructionsSource":"agent"}]}`
			case "review":
				analysis.Summary.Delivery.Mode = "review"
			case "image-only":
				analysis.Videos[0].HasAudio, analysis.Voice.HasAudio = false, false
			case "no-evidence":
				analysis.Summary.Delivery.Evidence = ""
			case "user-slow":
				project.VoiceCastJSON = `{"roles":[{"instructions":"慢速舒缓"}]}`
			case "music":
				analysis.Voice.Type = "music"
			}
			raw, _ := json.Marshal(analysis)
			project.AnalysisJSON = string(raw)
			delivery, pace := videoReplicationReferenceDelivery(project, 0, 5000)
			if kind != "live" && kind != "agent-tone" {
				if delivery != "" || pace != "" {
					t.Fatalf("unsupported forced live speech: %s %s", delivery, pace)
				}
				return
			}
			if !strings.Contains(delivery, "快速介绍后举起商品停顿") || strings.Contains(delivery, "第二镜头") || !strings.Contains(pace, "6.5") || strings.Contains(pace, "自然聊天") {
				t.Fatalf("clip delivery lost: %s %s", delivery, pace)
			}
		})
	}
}

func TestVideoReplicationDeliveryReachesFinalGeneration(t *testing.T) {
	params := VideoReplicationParameters{ReplaceMode: "combination", ChannelID: "system", Model: "video", ReferenceMode: "reference", MaxReferenceImages: 10, ShotSeconds: []int{10}, GenerateAudio: true, AudioMode: "generate", PromptMaxChars: 8000}
	svc, project, db := videoReplicationAcceptanceProject(t, params, 1)
	board := videoReplicationStoryboard(project)
	board.Shots[0].Voiceover = "看看这件新品的细节。"
	board.Shots[0].SourceStartMs, board.Shots[0].SourceEndMs = 0, 5000
	board.Shots[0].Prompt = strings.Repeat("人物展示新品。", 1000)
	boardRaw, _ := json.Marshal(board)
	analysisRaw, _ := json.Marshal(livestreamAnalysis())
	if err := svc.repo.UpdateVideoReplicationProject(project.UserID, project.ID, map[string]any{"video_type": "model,product", "storyboard_json": string(boardRaw), "analysis_json": string(analysisRaw)}); err != nil {
		t.Fatal(err)
	}
	for _, category := range []string{model.VideoReplicationAssetModelImage, model.VideoReplicationAssetProductImage} {
		if err := db.Create(&model.VideoReplicationAsset{ID: newID(), ProjectID: project.ID, UserID: project.UserID, Category: category, ResourceID: "res-" + category, IsPrimary: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioNative, SpeakLines: true, LipSyncAcknowledged: true}); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.GenerateVideoReplication(project.UserID, project.ID, VideoReplicationGenerateInput{IdempotencyKey: "live-delivery"}, "")
	runs := videoReplicationRunsByShot(t, svc, project)["shot_0"]
	if len(runs) != 1 {
		t.Fatalf("no generation request: %+v", runs)
	}
	for _, want := range []string{"直播间主播现场带货", "短句快速连说", "快速介绍后举起商品停顿", "6.5 字/秒", "不增加未确认的价格", "看看这件新品的细节。"} {
		if !strings.Contains(runs[0].Prompt, want) {
			t.Fatalf("final prompt lost %q: %s", want, runs[0].Prompt)
		}
	}
	if strings.Contains(runs[0].Prompt, "温柔缓慢地分享") || strings.Contains(runs[0].Prompt, "自然聊天语速") || strings.Contains(runs[0].Prompt, "第二镜头") {
		t.Fatal("generic product tone flattened live delivery")
	}
}
