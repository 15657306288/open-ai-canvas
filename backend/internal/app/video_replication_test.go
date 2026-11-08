package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

func newVideoReplicationTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+newID()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.VideoReplicationProject{}, &model.VideoReplicationAsset{}, &model.VideoReplicationShotRun{}, &model.VideoReplicationOutput{}); err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repository.New(db)}, db
}

func TestParseVideoReplicationProbeReadsRealStreams(t *testing.T) {
	raw := []byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":1080,"height":1920,"avg_frame_rate":"30000/1001"},{"codec_type":"audio","codec_name":"aac"}],"format":{"duration":"10.010000"}}`)
	probe, err := parseVideoReplicationProbe(raw)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Width != 1080 || probe.Height != 1920 || probe.DurationMs != 10010 || probe.FPS != 29.97 || !probe.HasAudio || probe.VideoCodec != "h264" {
		t.Fatalf("unexpected probe: %+v", probe)
	}
	rotated, err := parseVideoReplicationProbe([]byte(`{"streams":[{"codec_type":"video","width":1920,"height":1080,"r_frame_rate":"25/1","side_data_list":[{"rotation":-90}]}],"format":{"duration":"5"}}`))
	if err != nil || rotated.Width != 1080 || rotated.Height != 1920 || rotated.FPS != 25 || rotated.HasAudio {
		t.Fatalf("rotation not applied: %+v %v", rotated, err)
	}
	if _, err := parseVideoReplicationProbe([]byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"5"}}`)); err == nil {
		t.Fatal("audio-only file must be rejected")
	}
}

func TestValidateVideoReplicationDurationBounds(t *testing.T) {
	for _, ms := range []int64{4000, 3960, 10000, 15000, 60000, 120000, 120050} {
		if err := validateVideoReplicationDuration(ms); err != nil {
			t.Fatalf("%d ms should pass: %v", ms, err)
		}
	}
	for _, ms := range []int64{3900, 1000, 120100, 180000} {
		if err := validateVideoReplicationDuration(ms); err == nil {
			t.Fatalf("%d ms should be rejected", ms)
		}
	}
}

func TestVideoReplicationSpansMergeShortAndCap(t *testing.T) {
	spans := videoReplicationSpans([]int64{300, 2000, 2400, 5000}, 8000)
	// 0-300 过短并入下一镜头；2000-2400 过短并入前一镜头。
	want := []videoReplicationSpan{{0, 2400}, {2400, 5000}, {5000, 8000}}
	if len(spans) != len(want) {
		t.Fatalf("spans = %+v", spans)
	}
	for i := range want {
		if spans[i] != want[i] {
			t.Fatalf("spans = %+v", spans)
		}
	}
	cuts := []int64{}
	for at := int64(1000); at < 40000; at += 1000 {
		cuts = append(cuts, at)
	}
	capped := videoReplicationSpans(cuts, 40000)
	if len(capped) != videoReplicationMaxShots || capped[0].StartMs != 0 || capped[len(capped)-1].EndMs != 40000 {
		t.Fatalf("capped = %+v", capped)
	}
	if single := videoReplicationSpans(nil, 6000); len(single) != 1 || single[0].EndMs != 6000 {
		t.Fatalf("single = %+v", single)
	}
}

func videoReplicationFixtureAssets() []model.VideoReplicationAsset {
	return []model.VideoReplicationAsset{
		{ID: "video-1", Category: model.VideoReplicationAssetReferenceVideo, ResourceID: "res-v1", SortOrder: 0, IsPrimary: true},
		{ID: "product-1", Category: model.VideoReplicationAssetProductImage, ResourceID: "res-p1", SortOrder: 0, IsPrimary: true},
		{ID: "product-2", Category: model.VideoReplicationAssetProductImage, ResourceID: "res-p2", SortOrder: 1},
		{ID: "model-1", Category: model.VideoReplicationAssetModelImage, ResourceID: "res-m1", SortOrder: 0, IsPrimary: true},
		{ID: "model-2", Category: model.VideoReplicationAssetModelImage, ResourceID: "res-m2", SortOrder: 1},
		{ID: "face-1", Category: model.VideoReplicationAssetFaceImage, ResourceID: "res-f1", SortOrder: 0, IsPrimary: true},
	}
}

func TestVideoReplicationMentionsBindAssetIDs(t *testing.T) {
	assets := videoReplicationFixtureAssets()
	prompt := "参考 @[node:video-1] 的节奏，用 @[node:product-2] 替换商品，@[node:model-1] 替换人物，再提 @[node:product-2]"
	mentions, err := videoReplicationMentions(prompt, assets)
	if err != nil {
		t.Fatal(err)
	}
	if len(mentions) != 3 || mentions[0].AssetID != "video-1" || mentions[1].Label != "商品图片02" || mentions[2].Category != model.VideoReplicationAssetModelImage {
		t.Fatalf("mentions = %+v", mentions)
	}
	readable := videoReplicationReadablePrompt(prompt, assets)
	if !strings.Contains(readable, "@参考视频01") || !strings.Contains(readable, "@商品图片02") || strings.Contains(readable, "@[node:") {
		t.Fatalf("readable = %s", readable)
	}
	if _, err := videoReplicationMentions("用 @[node:other-user-asset] 替换", assets); err == nil {
		t.Fatal("mention of an asset outside the project must be rejected")
	}
}

func TestVideoReplicationReferencesFollowTargetsAndModelCapability(t *testing.T) {
	assets := videoReplicationFixtureAssets()
	mentions := []VideoReplicationMention{{AssetID: "product-2", Category: model.VideoReplicationAssetProductImage}}
	refs := videoReplicationReferences("product", VideoReplicationParameters{ReferenceMode: "reference", MaxReferenceImages: 2}, assets, mentions)
	if len(refs) != 2 || refs[0]["assetId"] != "product-2" || refs[1]["assetId"] != "product-1" || refs[0]["role"] != "reference" {
		t.Fatalf("product refs = %+v", refs)
	}
	// 商品 + 模特同时替换：轮流取图，先保证每个目标至少一张。
	refs = videoReplicationReferences("product,model", VideoReplicationParameters{ReferenceMode: "reference", MaxReferenceImages: 3}, assets, nil)
	if len(refs) != 3 || refs[0]["assetId"] != "product-1" || refs[1]["assetId"] != "model-1" || refs[2]["assetId"] != "product-2" {
		t.Fatalf("product+model refs = %+v", refs)
	}
	refs = videoReplicationReferences("face", VideoReplicationParameters{ReferenceMode: "first_frame", MaxReferenceImages: 5}, assets, nil)
	if len(refs) != 1 || refs[0]["assetId"] != "face-1" || refs[0]["role"] != "first_frame" {
		t.Fatalf("face refs = %+v", refs)
	}
	if refs := videoReplicationReferences("", VideoReplicationParameters{ReferenceMode: "reference", MaxReferenceImages: 3}, assets, nil); len(refs) != 0 {
		t.Fatalf("no targets means no reference images: %+v", refs)
	}
}

func TestVideoReplicationRequirementOnlyNeedsReferenceVideo(t *testing.T) {
	videoOnly := []model.VideoReplicationAsset{{Category: model.VideoReplicationAssetReferenceVideo}}
	if missing := videoReplicationRequirement("", VideoReplicationParameters{}, videoOnly); missing != "" {
		t.Fatalf("only the reference video is required: %q", missing)
	}
	if videoReplicationRequirement("", VideoReplicationParameters{}, nil) == "" {
		t.Fatal("reference video is required")
	}
	if missing := videoReplicationRequirement("apparel", VideoReplicationParameters{}, videoOnly); !strings.Contains(missing, "服饰图片") {
		t.Fatalf("apparel target needs apparel images: %q", missing)
	}
	assets := videoReplicationFixtureAssets()
	if missing := videoReplicationRequirement("product,model,face", VideoReplicationParameters{}, assets); !strings.Contains(missing, "授权") {
		t.Fatalf("face target requires consent, got %q", missing)
	}
	if missing := videoReplicationRequirement("product,model,face", VideoReplicationParameters{FaceConsent: true}, assets); missing != "" {
		t.Fatalf("all targets satisfied: %q", missing)
	}
	if got := strings.Join(videoReplicationTargets("face, product,unknown,model"), ","); got != "product,model,face" {
		t.Fatalf("targets = %s", got)
	}
	if validVideoReplicationType("product,bogus") || !validVideoReplicationType("") {
		t.Fatal("target validation")
	}
}

func TestVideoReplicationDubbedTimelineIsContinuous(t *testing.T) {
	storyboard := VideoReplicationStoryboard{Shots: []VideoReplicationShot{{ID: "a", DurationSec: 5, Prompt: "a", Voiceover: "第一句"}, {ID: "b", DurationSec: 5, Prompt: "b"}, {ID: "c", DurationSec: 5, Prompt: "c", Voiceover: "第三句"}}}
	runs := []model.VideoReplicationShotRun{
		{ID: "va", ShotID: "a", Kind: "video", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "v-a"},
		{ID: "vb", ShotID: "b", Kind: "video", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "v-b"},
		{ID: "vc", ShotID: "c", Kind: "video", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "v-c"},
		{ID: "sa", ShotID: "a", Kind: "voice", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "a-a"},
	}
	plan := &VideoReplicationPlan{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioTTS}
	durations := map[string]int64{"a-a": 3200, "a-c": 9000}
	if reason := videoReplicationComposeReady(storyboard, plan, runs); !strings.Contains(reason, "片段 3 的配音") {
		t.Fatalf("missing voice must block compose: %q", reason)
	}
	runs = append(runs, model.VideoReplicationShotRun{ID: "sc", ShotID: "c", Kind: "voice", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "a-c"})
	if reason := videoReplicationComposeReady(storyboard, plan, runs); reason != "" {
		t.Fatalf("ready: %q", reason)
	}
	timeline, runIDs, missing := buildVideoReplicationTimeline(storyboard, VideoReplicationParameters{ShotSeconds: []int{5}}, plan, runs, func(id string) int64 { return durations[id] })
	if len(missing) != 0 || !timeline.Tracks[0].Muted || timeline.DurationMs != 15000 {
		t.Fatalf("timeline=%+v missing=%v", timeline, missing)
	}
	voice := []renderClip{}
	for _, clip := range timeline.Clips {
		if clip.Kind == "audio" {
			voice = append(voice, clip)
		}
	}
	// 配音按镜头起点铺开；超过镜头长度的配音截到镜头内，避免压到下一段。
	if len(voice) != 2 || voice[0].StartMs != 0 || voice[0].DurationMs != 3200 || voice[1].StartMs != 10000 || voice[1].DurationMs != 5000 {
		t.Fatalf("voice clips = %+v", voice)
	}
	if strings.Join(runIDs, ",") != "va,sa,vb,vc,sc" {
		t.Fatalf("runIDs = %v", runIDs)
	}
	key := videoReplicationAutoComposeKey(storyboard, plan, runs)
	runs = append(runs, model.VideoReplicationShotRun{ID: "vb2", ShotID: "b", Kind: "video", Version: 2, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "v-b2"})
	if videoReplicationAutoComposeKey(storyboard, plan, runs) == key {
		t.Fatal("regenerating one segment must trigger a new automatic compose")
	}
	runs = append(runs, model.VideoReplicationShotRun{ID: "vb3", ShotID: "b", Kind: "video", Version: 3, Status: model.VideoReplicationRunRunning})
	if videoReplicationComposeReady(storyboard, plan, runs) == "" {
		t.Fatal("active runs block compose")
	}
}

func TestVideoReplicationNativeDubbedTimeline(t *testing.T) {
	storyboard := VideoReplicationStoryboard{Shots: []VideoReplicationShot{{ID: "a", DurationSec: 4, Prompt: "a", Voiceover: "开场"}, {ID: "b", DurationSec: 6, Prompt: "b", Voiceover: "结尾"}}}
	runs := []model.VideoReplicationShotRun{
		{ID: "full", ShotID: videoReplicationNativeShotID, Kind: "video", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "v-full"},
		{ID: "sa", ShotID: "a", Kind: "voice", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "a-a"},
		{ID: "sb", ShotID: "b", Kind: "voice", Version: 1, Status: model.VideoReplicationRunSucceeded, OutputResourceID: "a-b"},
	}
	plan := &VideoReplicationPlan{Strategy: VideoReplicationStrategyNative, AudioMode: videoReplicationAudioTTS, TargetSeconds: 10}
	timeline, _, missing := buildVideoReplicationTimeline(storyboard, VideoReplicationParameters{}, plan, runs, func(id string) int64 { return map[string]int64{"v-full": 10000, "a-a": 2000, "a-b": 2500}[id] })
	videoClips := 0
	for _, clip := range timeline.Clips {
		if clip.Kind == "video" {
			videoClips++
		}
	}
	if len(missing) != 0 || videoClips != 1 || timeline.DurationMs != 10000 || timeline.Clips[len(timeline.Clips)-1].StartMs != 4000 {
		t.Fatalf("native dubbed timeline = %+v", timeline)
	}
}

func TestNormalizeVideoReplicationStoryboardSnapsDurations(t *testing.T) {
	storyboard, err := normalizeVideoReplicationStoryboard(VideoReplicationStoryboard{Shots: []VideoReplicationShot{{ID: "a", DurationSec: 3, Prompt: "x"}, {ID: "a", DurationSec: 7, Prompt: "y"}, {ID: "bad id!", DurationSec: 12, Prompt: "z"}}}, []int{5, 10})
	if err != nil {
		t.Fatal(err)
	}
	shots := storyboard.Shots
	if shots[0].DurationSec != 5 || shots[1].DurationSec != 5 || shots[2].DurationSec != 10 {
		t.Fatalf("durations = %d %d %d", shots[0].DurationSec, shots[1].DurationSec, shots[2].DurationSec)
	}
	if shots[0].ID != "a" || shots[1].ID == "a" || shots[2].ID == "bad id!" || shots[2].Index != 2 {
		t.Fatalf("ids = %q %q %q", shots[0].ID, shots[1].ID, shots[2].ID)
	}
	if _, err := normalizeVideoReplicationStoryboard(VideoReplicationStoryboard{Shots: []VideoReplicationShot{{ID: "a", Prompt: " "}}}, nil); err == nil {
		t.Fatal("shot without prompt must be rejected")
	}
}

func TestVideoReplicationCanvasSize(t *testing.T) {
	for _, item := range []struct {
		ratio, resolution string
		w, h              int
	}{{"9:16", "720p", 720, 1280}, {"16:9", "1080p", 1920, 1080}, {"1:1", "720", 720, 720}, {"", "", 1280, 720}} {
		if w, h := videoReplicationCanvasSize(item.ratio, item.resolution); w != item.w || h != item.h {
			t.Fatalf("%s %s => %dx%d", item.ratio, item.resolution, w, h)
		}
	}
}

func TestReserveVideoReplicationRunIsIdempotentAndSingleActive(t *testing.T) {
	svc, _ := newVideoReplicationTestService(t)
	project := &model.VideoReplicationProject{ID: newID(), UserID: "user-1", Status: model.VideoReplicationStatusAnalyzed}
	if err := svc.repo.CreateVideoReplicationProject(project); err != nil {
		t.Fatal(err)
	}
	first := &model.VideoReplicationShotRun{ID: newID(), ProjectID: project.ID, UserID: "user-1", ShotID: "shot_a", IdempotencyKey: "key-0001:shot_a", Status: model.VideoReplicationRunSubmitting}
	created, err := svc.repo.ReserveVideoReplicationRun(first)
	if err != nil || !created || first.Version != 1 {
		t.Fatalf("first reserve created=%v version=%d err=%v", created, first.Version, err)
	}
	// 同一镜头已有进行中版本：不同提交键也不能再建，避免重复扣费。
	second := &model.VideoReplicationShotRun{ID: newID(), ProjectID: project.ID, UserID: "user-1", ShotID: "shot_a", IdempotencyKey: "key-0002:shot_a", Status: model.VideoReplicationRunSubmitting}
	if created, err := svc.repo.ReserveVideoReplicationRun(second); err != nil || created {
		t.Fatalf("second reserve must be refused, created=%v err=%v", created, err)
	}
	// 同一提交键重复插入被唯一索引挡住。
	_ = svc.repo.UpdateVideoReplicationRun(first.ID, map[string]any{"status": model.VideoReplicationRunFailed})
	duplicate := &model.VideoReplicationShotRun{ID: newID(), ProjectID: project.ID, UserID: "user-1", ShotID: "shot_a", IdempotencyKey: "key-0001:shot_a", Status: model.VideoReplicationRunSubmitting}
	if created, err := svc.repo.ReserveVideoReplicationRun(duplicate); err == nil && created {
		t.Fatal("duplicate idempotency key must not create a second run")
	}
	// 失败后重新生成得到新版本号，其他镜头不受影响。
	retry := &model.VideoReplicationShotRun{ID: newID(), ProjectID: project.ID, UserID: "user-1", ShotID: "shot_a", IdempotencyKey: "key-0003:shot_a", Status: model.VideoReplicationRunSubmitting}
	if created, err := svc.repo.ReserveVideoReplicationRun(retry); err != nil || !created || retry.Version != 2 {
		t.Fatalf("retry created=%v version=%d err=%v", created, retry.Version, err)
	}
}

func TestSyncVideoReplicationMapsTaskStates(t *testing.T) {
	svc, db := newVideoReplicationTestService(t)
	storyboard := `{"shots":[{"id":"shot_a","index":0,"durationSec":5,"title":"a","prompt":"a"},{"id":"shot_b","index":1,"durationSec":5,"title":"b","prompt":"b"},{"id":"shot_c","index":2,"durationSec":5,"title":"c","prompt":"c"}]}`
	project := &model.VideoReplicationProject{ID: newID(), UserID: "user-1", Status: model.VideoReplicationStatusGenerating, StoryboardJSON: storyboard}
	if err := svc.repo.CreateVideoReplicationProject(project); err != nil {
		t.Fatal(err)
	}
	tasks := []model.Task{
		{ID: "task-ok", UserID: "user-1", Type: "canvas_video", Status: model.TaskStatusSucceeded, ResultJSON: `{"resourceId":"video-res-1"}`},
		{ID: "task-fail", UserID: "user-1", Type: "canvas_video", Status: model.TaskStatusFailed, Error: "上游失败，已退款"},
		{ID: "task-empty", UserID: "user-1", Type: "canvas_video", Status: model.TaskStatusSucceeded, ResultJSON: `{}`},
		{ID: "task-other-user", UserID: "user-2", Type: "canvas_video", Status: model.TaskStatusSucceeded, ResultJSON: `{"resourceId":"stolen"}`},
	}
	for i := range tasks {
		if err := db.Create(&tasks[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-10 * time.Minute)
	runs := []model.VideoReplicationShotRun{
		{ID: "run-ok", ProjectID: project.ID, UserID: "user-1", ShotID: "shot_a", Version: 1, IdempotencyKey: "k:a", TaskID: "task-ok", Status: model.VideoReplicationRunRunning},
		{ID: "run-fail", ProjectID: project.ID, UserID: "user-1", ShotID: "shot_b", Version: 1, IdempotencyKey: "k:b", TaskID: "task-fail", Status: model.VideoReplicationRunQueued},
		{ID: "run-empty", ProjectID: project.ID, UserID: "user-1", ShotID: "shot_c", Version: 1, IdempotencyKey: "k:c", TaskID: "task-empty", Status: model.VideoReplicationRunRunning},
		{ID: "run-lost", ProjectID: project.ID, UserID: "user-1", ShotID: "shot_c", Version: 2, IdempotencyKey: "k2:c", Status: model.VideoReplicationRunSubmitting, CreatedAt: old},
		{ID: "run-cross", ProjectID: project.ID, UserID: "user-1", ShotID: "shot_b", Version: 2, IdempotencyKey: "k2:b", TaskID: "task-other-user", Status: model.VideoReplicationRunRunning},
	}
	for i := range runs {
		if err := db.Create(&runs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.syncVideoReplication(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	got, err := svc.repo.VideoReplicationRuns("user-1", project.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]model.VideoReplicationShotRun{}
	for _, run := range got {
		byID[run.ID] = run
	}
	if run := byID["run-ok"]; run.Status != model.VideoReplicationRunSucceeded || run.OutputResourceID != "video-res-1" || !run.Selected {
		t.Fatalf("run-ok = %+v", run)
	}
	if run := byID["run-fail"]; run.Status != model.VideoReplicationRunFailed || !strings.Contains(run.Error, "退款") {
		t.Fatalf("run-fail = %+v", run)
	}
	if run := byID["run-empty"]; run.Status != model.VideoReplicationRunFailed || !strings.Contains(run.Error, "没有返回视频文件") {
		t.Fatalf("succeeded task without video must not count as success: %+v", run)
	}
	if run := byID["run-lost"]; run.Status != model.VideoReplicationRunFailed || !strings.Contains(run.Error, "未扣费") {
		t.Fatalf("run-lost = %+v", run)
	}
	if run := byID["run-cross"]; run.Status != model.VideoReplicationRunFailed || run.OutputResourceID != "" {
		t.Fatalf("another user's task must never be read: %+v", run)
	}
	refreshed, _ := svc.repo.VideoReplicationProject("user-1", project.ID)
	if refreshed.Status != model.VideoReplicationStatusAnalyzed {
		t.Fatalf("project status = %s", refreshed.Status)
	}
}

func TestSyncVideoReplicationRestoresFailedRunFromRecoveredTask(t *testing.T) {
	svc, db := newVideoReplicationTestService(t)
	project := &model.VideoReplicationProject{ID: newID(), UserID: "user-1", Status: model.VideoReplicationStatusGenerating, Paused: true, StoryboardJSON: `{"shots":[{"id":"shot_a","prompt":"a","durationSec":5}]}`}
	if err := svc.repo.CreateVideoReplicationProject(project); err != nil {
		t.Fatal(err)
	}
	task := &model.Task{ID: "recovered-task", UserID: "user-1", Type: "canvas_video", Status: model.TaskStatusFailed, Error: "保存超时"}
	if err := db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	run := &model.VideoReplicationShotRun{ID: "restored-run", ProjectID: project.ID, UserID: project.UserID, ShotID: "shot_a", Version: 1, IdempotencyKey: "recovered", TaskID: task.ID, Status: model.VideoReplicationRunFailed, Error: "保存超时"}
	if err := db.Create(run).Error; err != nil {
		t.Fatal(err)
	}
	for _, state := range []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning, model.TaskStatusSucceeded} {
		if err := db.Model(task).Updates(map[string]any{"status": state, "error": "", "result_json": `{"resourceId":"saved-video"}`}).Error; err != nil {
			t.Fatal(err)
		}
		if err := svc.syncVideoReplication(context.Background(), project); err != nil {
			t.Fatal(err)
		}
		runs, err := svc.repo.VideoReplicationRuns(project.UserID, project.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 1 || runs[0].Status != videoReplicationRunStatus(state) || runs[0].Error != "" {
			t.Fatalf("state=%s runs=%+v", state, runs)
		}
		if state == model.TaskStatusSucceeded && (runs[0].OutputResourceID != "saved-video" || !runs[0].Selected) {
			t.Fatal("recovered video not selected")
		}
	}
	var count int64
	db.Model(&model.Task{}).Count(&count)
	if count != 1 {
		t.Fatal("recovery submitted another generation")
	}
}

func TestSyncVideoReplicationRecoversTaskByRequestID(t *testing.T) {
	svc, db := newVideoReplicationTestService(t)
	project := &model.VideoReplicationProject{ID: newID(), UserID: "user-1", Status: model.VideoReplicationStatusGenerating, StoryboardJSON: `{"shots":[{"id":"shot_a","prompt":"a","durationSec":5}]}`}
	if err := svc.repo.CreateVideoReplicationProject(project); err != nil {
		t.Fatal(err)
	}
	// 进程在建任务后、写回 TaskID 前中断：任务以镜头版本 ID 作为 RequestID。
	if err := db.Create(&model.Task{ID: "task-r", UserID: "user-1", RequestID: "run-r", Type: "canvas_video", Status: model.TaskStatusRunning}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.VideoReplicationShotRun{ID: "run-r", ProjectID: project.ID, UserID: "user-1", ShotID: "shot_a", Version: 1, IdempotencyKey: "k:a", Status: model.VideoReplicationRunSubmitting, CreatedAt: time.Now().Add(-5 * time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.syncVideoReplication(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	runs, _ := svc.repo.VideoReplicationRuns("user-1", project.ID)
	if len(runs) != 1 || runs[0].TaskID != "task-r" || runs[0].Status != model.VideoReplicationRunRunning {
		t.Fatalf("runs = %+v", runs)
	}
	refreshed, _ := svc.repo.VideoReplicationProject("user-1", project.ID)
	if refreshed.Status != model.VideoReplicationStatusGenerating {
		t.Fatalf("project status = %s", refreshed.Status)
	}
}

func TestVideoReplicationAnalysisLeaseExpiry(t *testing.T) {
	svc, _ := newVideoReplicationTestService(t)
	project := &model.VideoReplicationProject{ID: newID(), UserID: "user-1", Status: model.VideoReplicationStatusDraft}
	if err := svc.repo.CreateVideoReplicationProject(project); err != nil {
		t.Fatal(err)
	}
	lease := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	if claimed, err := svc.repo.ClaimVideoReplicationAnalysis("user-1", project.ID, lease, map[string]any{"parameters_json": `{"maxVideoTasks":1}`}); err != nil || !claimed {
		t.Fatalf("claim = %v %v", claimed, err)
	}
	if claimed, _ := svc.repo.ClaimVideoReplicationAnalysis("user-1", project.ID, lease.Add(time.Minute), map[string]any{"parameters_json": `{"maxVideoTasks":2}`}); claimed {
		t.Fatal("second concurrent analysis must not claim")
	}
	if stored, err := svc.repo.VideoReplicationProject("user-1", project.ID); err != nil || videoReplicationParameters(stored).MaxVideoTasks != 1 {
		t.Fatalf("first submission must keep its confirmed task cap: %v, %v", stored, err)
	}
	if ok, err := svc.repo.UpdateVideoReplicationAnalysis("user-1", project.ID, lease, map[string]any{"stage": "x"}); err != nil || !ok {
		t.Fatalf("lease owner update = %v %v", ok, err)
	}
	if ok, _ := svc.repo.UpdateVideoReplicationAnalysis("user-1", project.ID, lease.Add(time.Hour), map[string]any{"stage": "stale"}); ok {
		t.Fatal("stale analysis must not write")
	}
	if claimed, _ := svc.repo.ClaimVideoReplicationAnalysis("user-2", project.ID, lease, nil); claimed {
		t.Fatal("other user must not claim")
	}
}

func videoReplicationPlanProject(t *testing.T, params VideoReplicationParameters, shots int, voiceType string) *model.VideoReplicationProject {
	t.Helper()
	board := VideoReplicationStoryboard{}
	for index := 0; index < shots; index++ {
		board.Shots = append(board.Shots, VideoReplicationShot{ID: fmt.Sprintf("shot_%d", index), DurationSec: 5, Prompt: "画面"})
	}
	boardJSON, _ := json.Marshal(board)
	paramsJSON, _ := json.Marshal(params)
	analysisJSON, _ := json.Marshal(VideoReplicationAnalysis{Voice: &VideoReplicationVoice{Type: voiceType}})
	project := &model.VideoReplicationProject{ID: "p1", UserID: "u1", VideoType: VideoReplicationTypeModel, StoryboardJSON: string(boardJSON), ParametersJSON: string(paramsJSON), AnalysisJSON: string(analysisJSON), StoryboardRevision: 3}
	return project
}

func TestVideoReplicationPlanNativeRequiresModelDuration(t *testing.T) {
	// 模型单次最长 30 秒：原生直出 60 秒必须被拒绝，不能缩短冒充。
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", MaxReferenceImages: 3, TargetSeconds: 60, ShotSeconds: []int{5, 10, 15, 20, 25, 30}}
	project := videoReplicationPlanProject(t, params, 12, "narration")
	if _, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "native", AudioMode: "mute"}); err == nil || !strings.Contains(err.Error(), "不能原生直出 60 秒") {
		t.Fatalf("native 60s must be refused, got %v", err)
	}
	params.TargetSeconds = 30
	project = videoReplicationPlanProject(t, params, 6, "narration")
	plan, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "native", AudioMode: "mute", EstimatedCredits: 40, BudgetCredits: 50})
	if err != nil {
		t.Fatal(err)
	}
	if plan.TaskCount != 1 || plan.TargetSeconds != 30 || len(plan.Segments) != 1 || plan.Segments[0].ShotID != videoReplicationNativeShotID || plan.UsesFFmpegCompose {
		t.Fatalf("native plan = %+v", plan)
	}
	if _, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "native", AudioMode: "keep_original", VoiceAuthorized: true}); err == nil {
		t.Fatal("native mode must not remux original audio")
	}
}

func TestVideoReplicationPlanSegmentedCountsRealTasks(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "first_frame", ShotSeconds: []int{5, 10}}
	project := videoReplicationPlanProject(t, params, 12, "music")
	plan, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "segmented", AudioMode: "mute", EstimatedCredits: 120, BudgetCredits: 150})
	if err != nil {
		t.Fatal(err)
	}
	if plan.TaskCount != 12 || plan.TargetSeconds != 60 || !plan.UsesFFmpegCompose || plan.Continuity != "tail_frame" || plan.SegmentMaxSeconds != 10 {
		t.Fatalf("segmented plan = %+v", plan)
	}
	if got := videoReplicationTaskEstimate(plan, 5); got != 10 {
		t.Fatalf("per task estimate = %v", got)
	}
	if _, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "segmented", AudioMode: "mute", EstimatedCredits: 120, BudgetCredits: 100}); err == nil {
		t.Fatal("budget lower than estimate must be refused")
	}
	if _, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "segmented", AudioMode: "keep_original"}); err == nil {
		t.Fatal("keeping original voice requires authorization")
	}
}

func TestVideoReplicationPlanNoSpliceOnlyAllowsNative(t *testing.T) {
	// 用户禁止剪辑拼接时，60 秒参考视频 + 单次最长 15 秒的模型：智能分段被拒绝，原生直出也做不到，不能偷偷切换策略。
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", TargetSeconds: 60, ShotSeconds: []int{5, 10, 15}}
	project := videoReplicationPlanProject(t, params, 6, "music")
	if _, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "segmented", AudioMode: "mute", NoSplice: true}); err == nil || !strings.Contains(err.Error(), "禁止剪辑拼接") {
		t.Fatalf("segmented plan must be refused when splicing is forbidden, got %v", err)
	}
	if _, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "native", AudioMode: "mute", NoSplice: true}); err == nil || !strings.Contains(err.Error(), "不能原生直出") {
		t.Fatalf("native 60s on a 15s model must be refused, got %v", err)
	}
	params.TargetSeconds = 15
	project = videoReplicationPlanProject(t, params, 1, "music")
	plan, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "native", AudioMode: "mute", NoSplice: true})
	if err != nil || !plan.NoSplice || plan.TaskCount != 1 || plan.UsesFFmpegCompose {
		t.Fatalf("native 15s plan = %+v err=%v", plan, err)
	}
}

func TestVideoReplicationAssignVoiceLinesKeepsWholeSentences(t *testing.T) {
	segments := []videoReplicationAutoSegment{{StartMs: 0, EndMs: 10000, Seconds: 10}, {StartMs: 10000, EndMs: 20000, Seconds: 10}, {StartMs: 20000, EndMs: 24000, Seconds: 4}}
	voice := &VideoReplicationVoice{CharsPerSec: 5, Segments: []timelineTranscriptionSegment{
		{StartMs: 500, EndMs: 4000, Text: "第一句。"},
		// 跨过 10 秒切点、中点在第一段：整句归第一段，不拆、不在第二段重复。
		{StartMs: 7000, EndMs: 11000, Text: "跨段的一句。"},
		{StartMs: 12000, EndMs: 19000, Text: "第三句。"},
		{StartMs: 30000, EndMs: 31000, Text: "超出末尾。"},
	}}
	videoReplicationAssignVoiceLines(segments, voice)
	if segments[0].Lines != "第一句。跨段的一句。" || segments[1].Lines != "第三句。" || segments[2].Lines != "超出末尾。" {
		t.Fatalf("lines = %q / %q / %q", segments[0].Lines, segments[1].Lines, segments[2].Lines)
	}
	if segments[0].MaxChars != 50 || segments[2].MaxChars != 20 {
		t.Fatalf("max chars = %d / %d", segments[0].MaxChars, segments[2].MaxChars)
	}
	videoReplicationAssignVoiceLines(segments, nil)
}

func TestVideoReplicationReplaceAudioNeedsTranscript(t *testing.T) {
	if videoReplicationNeedsTranscript(VideoReplicationParameters{Auto: true, AudioMode: "keep"}) {
		t.Fatal("one-click keep-original should skip ASR")
	}
	if !videoReplicationNeedsTranscript(VideoReplicationParameters{Auto: true, AudioMode: "replace"}) {
		t.Fatal("one-click replace audio must run ASR so original lines reach the prompt")
	}
	if !videoReplicationNeedsTranscript(VideoReplicationParameters{Auto: true, ConfirmFirst: true}) || !videoReplicationNeedsTranscript(VideoReplicationParameters{}) {
		t.Fatal("confirm-first and manual flows need ASR")
	}
}

func TestVideoReplicationVoiceLineSourceFallsBackToVisionDictation(t *testing.T) {
	shots := []VideoReplicationSourceShot{{StartMs: 0, EndMs: 4000, Voiceover: "姐妹们看这个。"}, {StartMs: 4000, EndMs: 9000}, {StartMs: 9000, EndMs: 14000, Voiceover: "链接在下方。"}}
	analysis := &VideoReplicationAnalysis{Videos: []VideoReplicationVideoAnalysis{{HasAudio: true, Shots: shots}}, Voice: &VideoReplicationVoice{HasAudio: true, ASRError: "未配置语音识别"}}
	segments := []videoReplicationAutoSegment{{StartMs: 0, EndMs: 9000, Seconds: 9}, {StartMs: 9000, EndMs: 14000, Seconds: 5}}
	videoReplicationAssignVoiceLines(segments, videoReplicationVoiceLineSource(analysis))
	if segments[0].Lines != "姐妹们看这个。" || segments[1].Lines != "链接在下方。" {
		t.Fatalf("fallback lines = %q / %q", segments[0].Lines, segments[1].Lines)
	}
	if analysis.Voice.Segments != nil {
		t.Fatal("fallback must not mutate saved ASR result")
	}

	// ASR 成功但没有人声：以识别为准，不采用模型听写。
	analysis.Voice = &VideoReplicationVoice{HasAudio: true, ASRAvailable: true, Type: "music"}
	if source := videoReplicationVoiceLineSource(analysis); len(source.Segments) != 0 {
		t.Fatalf("ASR result should win, got %+v", source.Segments)
	}
	// ASR 成功有台词：用识别结果。
	analysis.Voice = &VideoReplicationVoice{HasAudio: true, ASRAvailable: true, Segments: []timelineTranscriptionSegment{{StartMs: 100, EndMs: 2000, Text: "识别台词"}}}
	if source := videoReplicationVoiceLineSource(analysis); len(source.Segments) != 1 || source.Segments[0].Text != "识别台词" {
		t.Fatalf("ASR segments not used: %+v", source.Segments)
	}
	// 一键模式没跑 ASR（Voice 只有视频理解写入的概况）也能退回听写。
	analysis.Voice = &VideoReplicationVoice{Type: "on_camera"}
	if source := videoReplicationVoiceLineSource(analysis); len(source.Segments) != 2 {
		t.Fatalf("vision fallback without ASR = %+v", source)
	}
	if videoReplicationVoiceLineSource(nil) != nil {
		t.Fatal("nil analysis")
	}
}

func TestVideoReplicationPlanDigestTracksInputs(t *testing.T) {
	assets := []model.VideoReplicationAsset{{ID: "a1", Category: model.VideoReplicationAssetReferenceVideo, ResourceID: "r1", IsPrimary: true}, {ID: "a2", Category: model.VideoReplicationAssetProductImage, ResourceID: "r2"}}
	params := VideoReplicationParameters{SegmentSeconds: 10, Ratio: "9:16"}
	project := videoReplicationPlanProject(t, params, 2, "music")
	base := videoReplicationPlanDigest(project, assets)
	media := videoReplicationMediaDigest(assets)
	if videoReplicationMediaDigest([]model.VideoReplicationAsset{assets[1], assets[0]}) != media {
		t.Fatal("media digest must not depend on listing order")
	}
	project.Prompt = "换成红色款"
	if videoReplicationPlanDigest(project, assets) == base {
		t.Fatal("changing the brief must invalidate the saved plan")
	}
	if videoReplicationMediaDigest(assets) != media {
		t.Fatal("changing the brief must keep the media digest so video understanding is reused")
	}
	assets[0].IsPrimary = false
	if videoReplicationMediaDigest(assets) == media {
		t.Fatal("changing the primary reference must invalidate the media digest")
	}
}

func TestVideoReplicationPlanWarnsWhenVoiceoverTooLong(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", ShotSeconds: []int{5}}
	project := videoReplicationPlanProject(t, params, 2, "narration")
	board := videoReplicationStoryboard(project)
	board.Shots[0].Voiceover = strings.Repeat("字", 40)
	board.Shots[1].Voiceover = "短句"
	raw, _ := json.Marshal(board)
	project.StoryboardJSON = string(raw)
	project.VideoType = VideoReplicationTypeProduct
	plan, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "segmented", AudioMode: "tts_voiceover"})
	if err != nil {
		t.Fatal(err)
	}
	warned := 0
	for _, note := range plan.Notes {
		if strings.Contains(note, "可能被截断") {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("notes = %v", plan.Notes)
	}
}

func TestVideoReplicationPlanLipSyncNeedsAcknowledgement(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", ShotSeconds: []int{5, 10, 15}}
	project := videoReplicationPlanProject(t, params, 4, "on_camera")
	if _, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "segmented", AudioMode: "mute"}); err == nil || !strings.Contains(err.Error(), "口型") {
		t.Fatalf("on-camera speech + model replacement must require lip-sync acknowledgement, got %v", err)
	}
	plan, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "segmented", AudioMode: "mute", LipSyncAcknowledged: true})
	if err != nil || !plan.LipSyncRequired || plan.LipSyncSupported {
		t.Fatalf("plan = %+v err=%v", plan, err)
	}
}

func TestVideoReplicationConfirmedPlanGoesStaleOnChange(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", ShotSeconds: []int{5}}
	project := videoReplicationPlanProject(t, params, 2, "music")
	if _, err := requireConfirmedVideoReplicationPlan(project); err == nil {
		t.Fatal("generation without a confirmed plan must be refused")
	}
	plan, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "segmented", AudioMode: "mute"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plan)
	now := time.Now()
	project.PlanJSON, project.PlanConfirmedAt = string(raw), &now
	if _, err := requireConfirmedVideoReplicationPlan(project); err != nil {
		t.Fatalf("fresh plan rejected: %v", err)
	}
	project.StoryboardRevision++
	if _, err := requireConfirmedVideoReplicationPlan(project); err == nil {
		t.Fatal("storyboard change must require re-confirmation")
	}
	project.StoryboardRevision--
	project.Prompt = "改了描述"
	if _, err := requireConfirmedVideoReplicationPlan(project); err == nil {
		t.Fatal("prompt change must require re-confirmation")
	}
}

func TestVideoReplicationNativePromptCoversWholeTimeline(t *testing.T) {
	board := VideoReplicationStoryboard{Shots: []VideoReplicationShot{{ID: "a", DurationSec: 10, Prompt: "开场展示商品"}, {ID: "b", DurationSec: 10, Prompt: "模特上手"}, {ID: "c", DurationSec: 10, Prompt: "结尾定格"}}}
	prompt := videoReplicationNativePrompt(VideoReplicationTypeProduct, VideoReplicationParameters{}, board, 30, "", []map[string]string{{"label": "商品图片01"}})
	for _, want := range []string{"完整的 30 秒", "[0-10秒] 开场展示商品", "[10-20秒] 模特上手", "[20-30秒] 结尾定格", "参考图1=商品图片01"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("native prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestVideoReplicationKeyUsedMatchesPrefixOnly(t *testing.T) {
	svc, db := newVideoReplicationTestService(t)
	if err := db.Create(&model.VideoReplicationShotRun{ID: "r1", ProjectID: "p", UserID: "u", ShotID: "shot_a", Version: 1, IdempotencyKey: "key_0001:shot_a", Status: model.VideoReplicationRunFailed}).Error; err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]bool{"key_0001": true, "key_000": false, "keyX0001": false, "key_0002": false} {
		if got, err := svc.repo.VideoReplicationKeyUsed("p", key); err != nil || got != want {
			t.Fatalf("%s used=%v err=%v", key, got, err)
		}
	}
	if used, _ := svc.repo.VideoReplicationKeyUsed("other", "key_0001"); used {
		t.Fatal("keys are scoped to the project")
	}
}

func TestVideoReplicationSettingRestrictsModelsAndParams(t *testing.T) {
	svc, db, channel, channelModel, profile := newVideoTokenQuoteFixture(t)
	spec, err := CapabilitySpecFromModelCapabilityConfig(profile, "video")
	if err != nil {
		t.Fatal(err)
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&model.LogicalModel{ID: "lm-15", Code: "video", Capability: "video", Enabled: true, ActiveRevisionID: "rev"},
		&model.LogicalModelRevision{ID: "rev", LogicalModelID: "lm-15", Version: 1, CapabilitySpecJSON: string(specJSON), DefaultOptionsJSON: "{}"},
		&model.LogicalModelRoute{ID: "route", LogicalModelRevisionID: "rev", ChannelModelID: channelModel.ID, Enabled: true, Weight: 1},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 未配置时不限定模型。
	if err := svc.videoReplicationModelAllowed(VideoReplicationParameters{LogicalModelID: "any"}); err != nil {
		t.Fatalf("unrestricted: %v", err)
	}
	setting := VideoReplicationSetting{VideoModels: []VideoReplicationVideoModel{
		{EcommerceAgentModelRef: EcommerceAgentModelRef{LogicalModelID: "lm-15"}, Enabled: true, Ratios: []string{"9:16", "16:9"}, Resolutions: []string{"720p"}, GenerateAudio: true},
		{EcommerceAgentModelRef: EcommerceAgentModelRef{ChannelModelID: channelModel.ID}, Enabled: true, AllowAudioToggle: true},
		{EcommerceAgentModelRef: EcommerceAgentModelRef{LogicalModelID: "lm-off"}, Enabled: false},
	}}
	raw, _ := json.Marshal(setting)
	if err := svc.repo.SaveSystemSetting(&model.SystemSetting{Key: videoReplicationSettingKey, ValueJSON: string(raw)}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		params VideoReplicationParameters
		ok     bool
	}{
		{VideoReplicationParameters{LogicalModelID: "lm-15", Ratio: "9:16", Resolution: "720p", GenerateAudio: true}, true},
		{VideoReplicationParameters{LogicalModelID: "lm-15", Ratio: "1:1", Resolution: "720p", GenerateAudio: true}, false},
		{VideoReplicationParameters{LogicalModelID: "lm-15", Ratio: "9:16", Resolution: "1080p", GenerateAudio: true}, false},
		{VideoReplicationParameters{LogicalModelID: "lm-15", Ratio: "9:16", Resolution: "720p", GenerateAudio: false}, false},
		{VideoReplicationParameters{ChannelID: channel.ID, Model: channelModel.ModelKey, Ratio: "1:1", GenerateAudio: false}, true},
		{VideoReplicationParameters{LogicalModelID: "lm-off"}, false},
		{VideoReplicationParameters{LogicalModelID: "other"}, false},
	}
	for index, item := range cases {
		if err := svc.videoReplicationModelAllowed(item.params); (err == nil) != item.ok {
			t.Fatalf("case %d: err=%v want ok=%v", index, err, item.ok)
		}
	}
	if err := svc.DeleteSystemChannel(&model.User{ID: "admin", Role: model.UserRoleAdmin}, channel.ID); err != nil {
		t.Fatal(err)
	}
	effective, err := svc.videoReplicationSetting()
	if err != nil || !effective.RestrictVideoModels || len(effective.VideoModels) != 0 {
		t.Fatalf("deleting the last supplier must remove direct and logical entries: %+v, %v", effective, err)
	}
}

func TestVideoReplicationNativeFollowsReferenceDuration(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", ShotSeconds: []int{5, 10, 15}}
	project := videoReplicationPlanProject(t, params, 3, "music")
	analysis := VideoReplicationAnalysis{Voice: &VideoReplicationVoice{Type: "music"}, Videos: []VideoReplicationVideoAnalysis{{Primary: true, DurationMs: 9400}}}
	raw, _ := json.Marshal(analysis)
	project.AnalysisJSON = string(raw)
	plan, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "native", AudioMode: "mute"})
	if err != nil || plan.TargetSeconds != 10 || plan.TaskCount != 1 {
		t.Fatalf("9.4s reference on a 15s model should natively produce 10s: plan=%+v err=%v", plan, err)
	}
	analysis.Videos[0].DurationMs = 42000
	raw, _ = json.Marshal(analysis)
	project.AnalysisJSON = string(raw)
	if _, err := buildVideoReplicationPlan(project, VideoReplicationPlanInput{Strategy: "native", AudioMode: "mute"}); err == nil {
		t.Fatal("42s reference must not be natively produced by a 15s model")
	}
}

func TestVideoReplicationAutoSegmentsFollowReferenceCuts(t *testing.T) {
	video := VideoReplicationVideoAnalysis{DurationMs: 60000}
	for _, end := range []int64{4000, 9000, 13000, 21000, 27000, 33000, 41000, 50000, 60000} {
		start := int64(0)
		if len(video.Shots) > 0 {
			start = video.Shots[len(video.Shots)-1].EndMs
		}
		video.Shots = append(video.Shots, VideoReplicationSourceShot{StartMs: start, EndMs: end})
	}
	segments := videoReplicationAutoSegments(video, 15, nil)
	total := int64(0)
	for index, segment := range segments {
		if segment.EndMs-segment.StartMs > 15000 {
			t.Fatalf("segment %d too long: %+v", index, segment)
		}
		if segment.Seconds < videoReplicationMinSegmentSeconds {
			t.Fatalf("segment %d shorter than 4s: %+v", index, segment)
		}
		if index > 0 && segment.StartMs != segments[index-1].EndMs {
			t.Fatalf("segments not contiguous: %+v", segments)
		}
		total += segment.EndMs - segment.StartMs
	}
	if total != 60000 {
		t.Fatalf("segments cover %dms, want 60000", total)
	}
	if len(segments) != 4 {
		t.Fatalf("reference cuts must not increase the four quoted tasks: %+v", segments)
	}
	video.DurationMs = 27000
	segments = videoReplicationAutoSegments(video, 15, nil)
	if len(segments) != 2 || segments[0].EndMs != 13000 {
		t.Fatalf("prefer a reference cut when it fits the quoted task count: %+v", segments)
	}
}

func TestVideoReplicationAutoSegmentsTrimShortOverflow(t *testing.T) {
	shots := []VideoReplicationSourceShot{{StartMs: 0, EndMs: 6000}, {StartMs: 6000, EndMs: 12000}, {StartMs: 12000, EndMs: 15600}}
	segments := videoReplicationAutoSegments(VideoReplicationVideoAnalysis{DurationMs: 15600, Shots: shots}, 15, []int{4, 5, 6, 8, 10, 12, 15})
	if len(segments) != 1 || segments[0].StartMs != 0 || segments[0].EndMs != 15000 || segments[0].Seconds != 15 || len(segments[0].Refs) != 3 {
		t.Fatalf("15.6s video with 15s limit should trim to one 15s segment: %+v", segments)
	}
	segments = videoReplicationAutoSegments(VideoReplicationVideoAnalysis{DurationMs: 16200, Shots: shots}, 15, nil)
	if len(segments) != 2 {
		t.Fatalf("16.2s video exceeds the 1s trim allowance and should split: %+v", segments)
	}
}

func TestVideoReplicationAutoSegmentsSplitLongShotsAndMergeTail(t *testing.T) {
	video := VideoReplicationVideoAnalysis{DurationMs: 32000, Shots: []VideoReplicationSourceShot{{StartMs: 0, EndMs: 32000}}}
	segments := videoReplicationAutoSegments(video, 30, nil)
	if len(segments) != 2 || segments[0].Seconds != 28 || segments[1].Seconds != 4 {
		t.Fatalf("unexpected segments: %+v", segments)
	}
	if got := videoReplicationAutoSegmentSeconds(3, []int{2, 3, 5, 10}); got != 5 {
		t.Fatalf("segment seconds snapped to %d, want 5", got)
	}
	if got := snapVideoReplicationSeconds(3, nil); got != videoReplicationMinSegmentSeconds {
		t.Fatalf("3s shot should become 4s, got %d", got)
	}
}
