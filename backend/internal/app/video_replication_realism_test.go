package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

func TestVideoReplicationEditClipCarriesRealSourceAudioOnlyWhenNeeded(t *testing.T) {
	ffmpegBin := requireVideoReplicationFFmpeg(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "source.mp4")
	makeReplicationClip(t, ffmpegBin, input, 1, 2, true)
	for _, withAudio := range []bool{false, true} {
		output := filepath.Join(dir, "clip.mp4")
		if err := cutVideoReplicationEditClip(context.Background(), ffmpegBin, input, 0, 1000, 0, "22", output, withAudio); err != nil {
			t.Fatal(err)
		}
		probe, err := probeVideoReplicationFile(context.Background(), output)
		if err != nil || probe.HasAudio != withAudio {
			t.Fatalf("audio=%v: %+v, %v", withAudio, probe, err)
		}
	}
	makeReplicationClip(t, ffmpegBin, input, 1, 2, false)
	if err := cutVideoReplicationEditClip(context.Background(), ffmpegBin, input, 0, 1000, 0, "22", filepath.Join(dir, "silent.mp4"), true); err == nil {
		t.Fatal("missing source audio must not masquerade as a usable timbre reference")
	}
}

func TestVideoReplicationSubmissionPreservesProductMaterialRolesWithLongScene(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", MaxReferenceImages: 4, ShotSeconds: []int{10}, PromptMaxChars: 1000, ProductName: "海盐曲奇饼干"}
	svc, project, db := videoReplicationAcceptanceProject(t, params, 1)
	board := videoReplicationStoryboard(project)
	board.Shots[0].Prompt = strings.Repeat("原视频的机位与商品展示。", 500)
	raw, _ := json.Marshal(board)
	if err := svc.repo.UpdateVideoReplicationProject(project.UserID, project.ID, map[string]any{"storyboard_json": string(raw)}); err != nil {
		t.Fatal(err)
	}
	for _, category := range []string{model.VideoReplicationAssetProductDetailImage, model.VideoReplicationAssetPackagingImage} {
		if err := db.Create(&model.VideoReplicationAsset{ID: newID(), ProjectID: project.ID, UserID: project.UserID, Category: category, ResourceID: "res-" + category, Name: category + ".png"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioMute}); err != nil {
		t.Fatal(err)
	}
	// 本地无模型：在实际任务预留处取得请求，不调用上游或产生付费生成。
	_, _ = svc.GenerateVideoReplication(project.UserID, project.ID, VideoReplicationGenerateInput{IdempotencyKey: "realism-long-scene-1"}, "")
	runs := videoReplicationRunsByShot(t, svc, project)["shot_0"]
	if len(runs) != 1 {
		t.Fatalf("expected one attempted task, got %d", len(runs))
	}
	prompt := runs[0].Prompt
	for _, want := range []string{"海盐曲奇饼干", "原片无人时不新增人物", "@图片1 提供新商品本体", "@图片2 只提供同一新商品的细节和拆解结构", "@图片3 只替换原片已出现的对应包装"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("final request lost %q: %s", want, prompt)
		}
	}
	if len([]rune(prompt)) > params.PromptMaxChars {
		t.Fatalf("final prompt exceeds configured limit: %d", len([]rune(prompt)))
	}
	if !strings.Contains(runs[0].Error, "未扣费") {
		t.Fatalf("expected refusal before upstream generation: %+v", runs[0])
	}
}

func TestVideoReplicationPhysicalRulesStayWithinRequestedTargets(t *testing.T) {
	params := VideoReplicationParameters{PromptMaxChars: 6000}
	background := videoReplicationEditPrompt("background", params, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", []map[string]string{{"category": model.VideoReplicationAssetBackgroundImage}})
	if !strings.Contains(background, videoReplicationBackgroundPhysics) || strings.Contains(background, "人物和商品不动") {
		t.Fatalf("background edit must preserve motion and integrate the scene: %s", background)
	}
	apparel := videoReplicationEditPrompt("apparel", params, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", []map[string]string{{"category": model.VideoReplicationAssetApparelImage}})
	if !strings.Contains(apparel, videoReplicationFabricPhysics) {
		t.Fatalf("apparel edit lacks fabric dynamics: %s", apparel)
	}
	product := videoReplicationVisualContract("product", params, nil)
	if strings.Contains(product, videoReplicationBackgroundPhysics) || strings.Contains(product, videoReplicationFabricPhysics) {
		t.Fatal("product replacement must not request a new background or outfit")
	}
	if got := videoReplicationVisualContract("model", VideoReplicationParameters{ReplaceMode: "precise-person"}, nil); !strings.Contains(got, "不蜡像脸") {
		t.Fatalf("precise person needs the same lifelike face requirements: %s", got)
	}
	combination := videoReplicationEditPrompt("model,background", params, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", []map[string]string{{"category": model.VideoReplicationAssetModelImage}, {"category": model.VideoReplicationAssetBackgroundImage}})
	if strings.Contains(combination, "场景背景仍是 @视频1 的") || !strings.Contains(combination, "场景背景按背景参考图替换") {
		t.Fatalf("person reference must not override an explicit background replacement: %s", combination)
	}
}

func TestVideoReplicationEveryReplacementSubmitsNaturalSpeechAndFace(t *testing.T) {
	for _, tc := range []struct{ mode, targets string }{
		{"model", "model"}, {"face", "face"}, {"precise-person", "model"},
		{"product", "product"}, {"precise-product", "product"},
		{"clothing", "apparel"}, {"background", "background"},
		{"combination", "product,model,background"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			params := VideoReplicationParameters{ReplaceMode: tc.mode, ChannelID: "system", Model: "video", ReferenceMode: "reference", MaxReferenceImages: 10, ShotSeconds: []int{10}, GenerateAudio: true, AudioMode: "generate", PromptMaxChars: 8000, FaceConsent: true}
			svc, project, db := videoReplicationAcceptanceProject(t, params, 1)
			project.VideoType = tc.targets
			board := videoReplicationStoryboard(project)
			board.Shots[0].Prompt = strings.Repeat("人物保持原片机位自然展示新品。", 1000)
			board.Shots[0].Voiceover = "看看这款新品的细节。"
			raw, _ := json.Marshal(board)
			if err := svc.repo.UpdateVideoReplicationProject(project.UserID, project.ID, map[string]any{"video_type": tc.targets, "storyboard_json": string(raw)}); err != nil {
				t.Fatal(err)
			}
			for _, category := range []string{model.VideoReplicationAssetModelImage, model.VideoReplicationAssetFaceImage, model.VideoReplicationAssetApparelImage, model.VideoReplicationAssetBackgroundImage} {
				if err := db.Create(&model.VideoReplicationAsset{ID: newID(), ProjectID: project.ID, UserID: project.UserID, Category: category, ResourceID: "res-" + category, IsPrimary: true}).Error; err != nil {
					t.Fatal(err)
				}
			}
			analysis, _ := json.Marshal(VideoReplicationAnalysis{Product: map[string]any{"deliveryTone": "温和自然地讲解新品的质感"}, Voice: &VideoReplicationVoice{Speakers: []VideoReplicationSpeaker{{Tone: "原片夸张叫卖"}}}})
			if err := svc.repo.UpdateVideoReplicationProject(project.UserID, project.ID, map[string]any{"analysis_json": string(analysis)}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioNative, SpeakLines: true, LipSyncAcknowledged: true}); err != nil {
				t.Fatal(err)
			}
			_, _ = svc.GenerateVideoReplication(project.UserID, project.ID, VideoReplicationGenerateInput{IdempotencyKey: "natural-all-modes"}, "")
			runs := videoReplicationRunsByShot(t, svc, project)["shot_0"]
			if len(runs) != 1 {
				t.Fatalf("no final request: %+v", runs)
			}
			for _, required := range []string{"不蜡像脸", "动作有重心", "台词：人物逐字说", "口型：嘴型与人声逐字同步", "温和自然地讲解新品的质感", "不要平铺直叙"} {
				if !strings.Contains(runs[0].Prompt, required) {
					t.Fatalf("final request lost %q: %s", required, runs[0].Prompt)
				}
			}
			if strings.Contains(runs[0].Prompt, "原片夸张叫卖") {
				t.Fatal("old product delivery displaced new product tone")
			}
			if tc.targets == "apparel" || strings.Contains(tc.targets, "product") {
				for _, required := range []string{"产品参数表 / 介绍卡", "不沿用旧数值", "缺失字段省略"} {
					if !strings.Contains(runs[0].Prompt, required) {
						t.Fatalf("missing product card rule %q", required)
					}
				}
			}
		})
	}
}

func TestVideoReplicationAdaptedTalkingUsesNewLipSyncEvenWhenKeepingTimbre(t *testing.T) {
	params := VideoReplicationParameters{AudioMode: "keep", Talking: true, PromptMaxChars: 6000}
	board := VideoReplicationStoryboard{VoiceAdapted: true}
	prompt := videoReplicationEditPrompt("product", params, VideoReplicationShot{}, board, "", []map[string]string{{"category": model.VideoReplicationAssetProductImage}})
	if !strings.Contains(prompt, "嘴型跟随本段新生成的人声逐字同步") || strings.Contains(prompt, "口型与 @视频1 一致") {
		t.Fatalf("rewritten dialogue must not retain original mouth movements: %s", prompt)
	}
}

func TestVideoReplicationAdaptedNativePlanRequiresAnActuallySentVoice(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", MaxReferenceImages: 2, MaxReferenceVideos: 1, GenerateAudio: true, ShotSeconds: []int{10}, PromptMaxChars: 5000}
	svc, project, _ := videoReplicationAcceptanceProject(t, params, 1)
	board := videoReplicationStoryboard(project)
	board.VoiceAdapted = true
	board.Shots[0].Voiceover = "这颗苹果看起来不错。"
	raw, _ := json.Marshal(board)
	if err := svc.repo.UpdateVideoReplicationProject(project.UserID, project.ID, map[string]any{"storyboard_json": string(raw)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategyNative, AudioMode: videoReplicationAudioNative}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.GenerateVideoReplication(project.UserID, project.ID, VideoReplicationGenerateInput{IdempotencyKey: "realism-native-voice-1"}, "")
	if err == nil || !strings.Contains(err.Error(), "实际音色输入") {
		t.Fatalf("model capability alone must not count as a sent reference: %v", err)
	}
	if runs := videoReplicationRunsByShot(t, svc, project); len(runs) != 0 {
		t.Fatalf("invalid voice plan must stop before task reservation: %+v", runs)
	}
}

func TestVideoReplicationNativeAdaptedSubtitlesStopBeforeConfirmationAndSubmission(t *testing.T) {
	for _, test := range []struct {
		name     string
		audio    string
		spoken   bool
		explicit bool
	}{
		{name: "silent source subtitles", audio: videoReplicationAudioMute},
		{name: "rewritten spoken subtitles", audio: videoReplicationAudioNative, spoken: true},
		{name: "requested subtitles for rewritten speech", audio: videoReplicationAudioNative, spoken: true, explicit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", MaxReferenceImages: 2, GenerateAudio: true, ShotSeconds: []int{10}, Subtitles: test.explicit}
			svc, project, db := videoReplicationAcceptanceProject(t, params, 1)
			board := videoReplicationStoryboard(project)
			board.Subtitles = !test.explicit
			board.SubtitlesAdapted = !test.spoken
			board.VoiceAdapted = test.spoken
			board.Shots[0].Subtitle = "这颗苹果看起来不错。"
			if test.spoken {
				board.Shots[0].Voiceover = board.Shots[0].Subtitle
			}
			raw, _ := json.Marshal(board)
			project.StoryboardJSON = string(raw)
			if err := svc.repo.UpdateVideoReplicationProject(project.UserID, project.ID, map[string]any{"storyboard_json": project.StoryboardJSON}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategyNative, AudioMode: test.audio, NoSplice: true}); err == nil || !strings.Contains(err.Error(), "不能叠加改写后的商品字幕") {
				t.Fatalf("unsupported direct output must be rejected at confirmation: %v", err)
			}
			stored, err := svc.repo.VideoReplicationProject(project.UserID, project.ID)
			if err != nil || stored.PlanJSON != "" || stored.PlanConfirmedAt != nil {
				t.Fatalf("rejected confirmation must not save an alternate plan: %+v, %v", stored, err)
			}
			// 已保存的旧方案也必须在付费任务预留前拦住，不能依赖确认入口的新校验。
			now := time.Now()
			legacy := VideoReplicationPlan{Strategy: VideoReplicationStrategyNative, AudioMode: test.audio, TargetSeconds: 10, TaskCount: 1, NoSplice: true, StoryboardRevision: project.StoryboardRevision, Digest: videoReplicationDigest(project), ConfirmedAt: now}
			planJSON, _ := json.Marshal(legacy)
			if err := svc.repo.UpdateVideoReplicationProject(project.UserID, project.ID, map[string]any{"plan_json": string(planJSON), "plan_confirmed_at": now}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.GenerateVideoReplication(project.UserID, project.ID, VideoReplicationGenerateInput{IdempotencyKey: "native-subtitle-refusal"}, ""); err == nil || !strings.Contains(err.Error(), "不能叠加改写后的商品字幕") {
				t.Fatalf("saved unsupported plan must be rejected before submission: %v", err)
			}
			if runs := videoReplicationRunsByShot(t, svc, project); len(runs) != 0 {
				t.Fatalf("unsupported subtitles must not reserve video runs: %+v", runs)
			}
			for _, table := range []any{&model.Task{}, &model.BillingOrder{}, &model.ApiCallLog{}} {
				var count int64
				if err := db.Model(table).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("refusal must not create tasks, billing or upstream calls: %T count=%d error=%v", table, count, err)
				}
			}
			stored, err = svc.repo.VideoReplicationProject(project.UserID, project.ID)
			if err != nil || stored.PlanJSON != string(planJSON) {
				t.Fatalf("refusal must preserve the confirmed strategy, task count and no-splice choice: %+v, %v", stored, err)
			}
		})
	}
}

func TestVideoReplicationNativeDubbedTimelineRetainsAdaptedSubtitles(t *testing.T) {
	board := VideoReplicationStoryboard{Subtitles: true, SubtitlesAdapted: true, VoiceAdapted: true, Shots: []VideoReplicationShot{{ID: "apple", DurationSec: 5, Subtitle: "新鲜苹果", Voiceover: "新鲜苹果"}}}
	runs := []model.VideoReplicationShotRun{
		{ID: "run", ShotID: videoReplicationNativeShotID, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "video"},
		{ID: "voice-run", ShotID: "apple", Kind: "voice", Status: model.VideoReplicationRunSucceeded, OutputResourceID: "voice"},
	}
	plan := &VideoReplicationPlan{Strategy: VideoReplicationStrategyNative, AudioMode: videoReplicationAudioTTS, TargetSeconds: 5}
	if err := videoReplicationValidateNativeSubtitles(plan, board, VideoReplicationParameters{}); err != nil {
		t.Fatalf("TTS postprocessing can render adapted subtitles: %v", err)
	}
	timeline, _, missing := buildVideoReplicationTimeline(board, VideoReplicationParameters{}, plan, runs, func(string) int64 { return 5000 })
	if len(missing) != 0 || !timeline.Tracks[0].Muted {
		t.Fatalf("native dubbed timeline unavailable: %+v, %v", timeline, missing)
	}
	for _, clip := range timeline.Clips {
		if clip.Kind == "subtitle" && clip.Text == "新鲜苹果" && clip.StartMs+clip.DurationMs <= 5000 {
			return
		}
	}
	t.Fatalf("adapted subtitles were lost in native composition: %+v", timeline.Clips)
}
