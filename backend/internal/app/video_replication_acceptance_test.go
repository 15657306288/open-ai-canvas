package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

// 这些测试走真实的服务层与 SQLite：本地没有配置任何模型，提交视频任务会在建任务时失败（「提交失败（未扣费）」），
// 测试只观察服务端尝试提交了哪些任务、有没有暂停或复用，不伪造任何模型输出。

func videoReplicationAcceptanceProject(t *testing.T, params VideoReplicationParameters, shots int) (*Service, *model.VideoReplicationProject, *gorm.DB) {
	t.Helper()
	svc, db := newVideoReplicationTestService(t)
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.ApiCallLog{}, &model.BillingOrder{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.repo.SaveSystemSetting(&model.SystemSetting{Key: featureAvailabilitySettingKey, ValueJSON: `{"videoWorkbenchEnabled":true}`}); err != nil {
		t.Fatal(err)
	}
	board := VideoReplicationStoryboard{}
	for index := 0; index < shots; index++ {
		board.Shots = append(board.Shots, VideoReplicationShot{ID: fmt.Sprintf("shot_%d", index), Index: index, DurationSec: 10, Title: "段", Prompt: "画面"})
	}
	boardJSON, _ := json.Marshal(board)
	paramsJSON, _ := json.Marshal(params)
	project := &model.VideoReplicationProject{ID: newID(), UserID: "user-1", Title: "验收", VideoType: VideoReplicationTypeProduct, Status: model.VideoReplicationStatusAnalyzed, StoryboardJSON: string(boardJSON), ParametersJSON: string(paramsJSON), StoryboardRevision: 1}
	if err := svc.repo.CreateVideoReplicationProject(project); err != nil {
		t.Fatal(err)
	}
	for _, asset := range []model.VideoReplicationAsset{
		{ID: newID(), ProjectID: project.ID, UserID: "user-1", Category: model.VideoReplicationAssetReferenceVideo, ResourceID: "res-video", Name: "ref.mp4", IsPrimary: true, DurationMs: int64(shots) * 10000},
		{ID: newID(), ProjectID: project.ID, UserID: "user-1", Category: model.VideoReplicationAssetProductImage, ResourceID: "res-product", Name: "p.png", IsPrimary: true},
	} {
		if err := db.Create(&asset).Error; err != nil {
			t.Fatal(err)
		}
	}
	return svc, project, db
}

func videoReplicationRunsByShot(t *testing.T, svc *Service, project *model.VideoReplicationProject) map[string][]model.VideoReplicationShotRun {
	t.Helper()
	runs, err := svc.repo.VideoReplicationRuns(project.UserID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]model.VideoReplicationShotRun{}
	for _, run := range runs {
		out[run.ShotID] = append(out[run.ShotID], run)
	}
	return out
}

func TestVideoReplicationAcceptanceRejectsUnconfirmedVideoTaskBeforeBilling(t *testing.T) {
	params := VideoReplicationParameters{Auto: true, MaxVideoTasks: 1, ChannelID: "system", Model: "video", ReferenceMode: "reference", MaxReferenceImages: 1, ShotSeconds: []int{10}}
	svc, project, db := videoReplicationAcceptanceProject(t, params, 2)
	_, err := svc.StartVideoReplication(project.UserID, project.ID, VideoReplicationStartInput{SegmentSeconds: 10, MaxVideoTasks: 1})
	if err == nil || !strings.Contains(err.Error(), "超过已确认的 1 次") {
		t.Fatalf("start should stop before analysis: %v", err)
	}
	if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioMute}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.GenerateVideoReplication(project.UserID, project.ID, VideoReplicationGenerateInput{IdempotencyKey: "one-task-cap"}, "")
	if err == nil || !strings.Contains(err.Error(), "超过已确认的 1 次") {
		t.Fatalf("saved plan must not bypass task cap: %v", err)
	}
	var orders int64
	if err := db.Model(&model.BillingOrder{}).Count(&orders).Error; err != nil || orders != 0 {
		t.Fatalf("billing orders = %d, err = %v", orders, err)
	}
	if runs := videoReplicationRunsByShot(t, svc, project); len(runs) != 0 {
		t.Fatalf("video runs created before confirmation: %v", runs)
	}
}

func TestVideoReplicationAcceptanceConfirmFirstReusesSavedAnalysis(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", ShotSeconds: []int{5, 10}, Auto: true, ConfirmFirst: true, SegmentSeconds: 10, KeepOriginalAudio: true}
	svc, project, db := videoReplicationAcceptanceProject(t, params, 3)
	assets, _ := svc.repo.VideoReplicationAssets(project.UserID, project.ID)
	saved := VideoReplicationAnalysis{Videos: []VideoReplicationVideoAnalysis{{AssetID: assets[0].ID, Primary: true, DurationMs: 30000}}, MediaDigest: videoReplicationMediaDigest(assets), PlanDigest: videoReplicationPlanDigest(project, assets)}
	raw, _ := json.Marshal(saved)
	if err := svc.repo.UpdateVideoReplicationProject(project.UserID, project.ID, map[string]any{"analysis_json": string(raw)}); err != nil {
		t.Fatal(err)
	}

	// 素材与要求都没变：直接返回已保存的方案，不进入分析（没有租约、状态不变、分析结果不变）。
	view, err := svc.StartVideoReplication(project.UserID, project.ID, VideoReplicationStartInput{SegmentSeconds: 10, KeepOriginalAudio: true, ConfirmFirst: true})
	if err != nil {
		t.Fatalf("cached start: %v", err)
	}
	if view.Status != model.VideoReplicationStatusAnalyzed || !view.AnalysisFresh || view.AnalysisLeaseUntil != nil {
		t.Fatalf("cache hit must not start analysis: status=%s fresh=%v lease=%v", view.Status, view.AnalysisFresh, view.AnalysisLeaseUntil)
	}
	// 缓存命中没有任何上游调用：不写调用日志，也不写成本账单，不会重复记费。
	var calls, orders int64
	_ = db.Model(&model.ApiCallLog{}).Count(&calls).Error
	_ = db.Model(&model.BillingOrder{}).Count(&orders).Error
	if calls != 0 || orders != 0 {
		t.Fatalf("cache hit must not record model cost: calls=%d orders=%d", calls, orders)
	}

	// 新增一张商品图：素材摘要变化，必须重新分析。本地没有识图模型，分析在开始前被拒绝，不会产生任何调用。
	if err := svc.repo.CreateVideoReplicationAsset(&model.VideoReplicationAsset{ID: newID(), ProjectID: project.ID, UserID: project.UserID, Category: model.VideoReplicationAssetProductImage, ResourceID: "res-product-2", Name: "p2.png"}); err != nil {
		t.Fatal(err)
	}
	stale, err := svc.VideoReplicationProject(project.UserID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.AnalysisFresh {
		t.Fatal("adding an asset must invalidate the saved analysis")
	}
	if _, err := svc.StartVideoReplication(project.UserID, project.ID, VideoReplicationStartInput{SegmentSeconds: 10, KeepOriginalAudio: true, ConfirmFirst: true}); err == nil {
		t.Fatal("changed assets must trigger a new analysis (refused here because no vision model is configured)")
	} else {
		t.Logf("assets changed: cache missed and analysis gating ran (refused before any model call): %v", err)
	}
	after, _ := svc.repo.VideoReplicationProject(project.UserID, project.ID)
	if after.Status == model.VideoReplicationStatusAnalyzing || after.AnalysisLeaseUntil != nil {
		t.Fatalf("refused analysis must not hold a lease: %s", after.Status)
	}
}

func TestVideoReplicationAcceptanceNoSpliceSubmitsOneTask(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", ShotSeconds: []int{5, 10, 15}, TargetSeconds: 60, Auto: true, ConfirmFirst: true}
	svc, project, _ := videoReplicationAcceptanceProject(t, params, 6)
	// 60 秒参考视频、模型单次最长 15 秒：禁止拼接时两种方式都必须被拒绝。
	if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioMute, NoSplice: true}); err == nil || !strings.Contains(err.Error(), "禁止剪辑拼接") {
		t.Fatalf("segmented with noSplice: %v", err)
	}
	if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategyNative, AudioMode: videoReplicationAudioMute, NoSplice: true}); err == nil || !strings.Contains(err.Error(), "不能原生直出 60 秒") {
		t.Fatalf("native 60s on a 15s model: %v", err)
	}
	if runs := videoReplicationRunsByShot(t, svc, project); len(runs) != 0 {
		t.Fatalf("refused plans must not submit anything: %v", runs)
	}

	// 模型支持 60 秒时：禁止拼接 + 原生直出只提交 1 个任务。
	params.ShotSeconds = []int{15, 30, 60}
	raw, _ := json.Marshal(params)
	_ = svc.repo.UpdateVideoReplicationProject(project.UserID, project.ID, map[string]any{"parameters_json": string(raw)})
	if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategyNative, AudioMode: videoReplicationAudioMute, NoSplice: true}); err != nil {
		t.Fatalf("native 60s plan: %v", err)
	}
	_, _ = svc.GenerateVideoReplication(project.UserID, project.ID, VideoReplicationGenerateInput{IdempotencyKey: "acceptance-native-1"}, "")
	runs := videoReplicationRunsByShot(t, svc, project)
	if len(runs) != 1 || len(runs[videoReplicationNativeShotID]) != 1 {
		t.Fatalf("native no-splice must submit exactly one task, got %v", runs)
	}
	run := runs[videoReplicationNativeShotID][0]
	if run.Status != model.VideoReplicationRunFailed || !strings.Contains(run.Error, "未扣费") {
		t.Fatalf("without a configured model the single task must fail before billing: %+v", run)
	}
}

func TestVideoReplicationAcceptanceRetryOnlyFailedShotAndBudget(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", ShotSeconds: []int{5, 10}, Auto: true, ConfirmFirst: true}
	svc, project, _ := videoReplicationAcceptanceProject(t, params, 3)
	if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioMute, EstimatedCredits: 30}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []model.VideoReplicationShotRun{
		{ID: newID(), ProjectID: project.ID, UserID: project.UserID, ShotID: "shot_0", Version: 1, IdempotencyKey: "k1:shot_0", Status: model.VideoReplicationRunSucceeded, OutputResourceID: "out-0", Selected: true},
		{ID: newID(), ProjectID: project.ID, UserID: project.UserID, ShotID: "shot_1", Version: 1, IdempotencyKey: "k1:shot_1", Status: model.VideoReplicationRunFailed, Error: "上游失败，已退款"},
		{ID: newID(), ProjectID: project.ID, UserID: project.UserID, ShotID: "shot_2", Version: 1, IdempotencyKey: "k1:shot_2", Status: model.VideoReplicationRunSucceeded, OutputResourceID: "out-2", Selected: true},
	} {
		if created, err := svc.repo.ReserveVideoReplicationRun(&run); err != nil || !created {
			t.Fatalf("seed run: %v %v", created, err)
		}
		_ = svc.repo.UpdateVideoReplicationRun(run.ID, map[string]any{"status": run.Status, "output_resource_id": run.OutputResourceID, "selected": run.Selected, "error": run.Error})
	}
	project, _ = svc.repo.VideoReplicationProject(project.UserID, project.ID)
	plan := videoReplicationPlan(project)
	storyboard := videoReplicationStoryboard(project)

	// 一键生成不自动重试：第 2 段失败立即报错，不提交新任务。
	_, failure := svc.retryVideoReplicationAuto(project, plan, storyboard, false)
	runs := videoReplicationRunsByShot(t, svc, project)
	if !strings.Contains(failure, "第 2 段生成失败") || len(runs["shot_0"]) != 1 || len(runs["shot_2"]) != 1 || len(runs["shot_1"]) != 1 {
		t.Fatalf("failed shot must fail immediately without retry: %q a=%d b=%d c=%d", failure, len(runs["shot_0"]), len(runs["shot_1"]), len(runs["shot_2"]))
	}
	// 其它段还在生成时同样立即报错。
	if _, failure := svc.retryVideoReplicationAuto(project, plan, storyboard, true); !strings.Contains(failure, "第 2 段生成失败") {
		t.Fatalf("failure must be reported while other shots are active: %q", failure)
	}
	// 用户手动重新生成同一提交标识两次：第二次是重放，不再新建任务。
	_, _ = svc.GenerateVideoReplication(project.UserID, project.ID, VideoReplicationGenerateInput{IdempotencyKey: "acceptance-regen-1", ShotIDs: []string{"shot_1"}, Regenerate: true}, "")
	_, _ = svc.GenerateVideoReplication(project.UserID, project.ID, VideoReplicationGenerateInput{IdempotencyKey: "acceptance-regen-1", ShotIDs: []string{"shot_1"}, Regenerate: true}, "")
	if got := videoReplicationRunsByShot(t, svc, project); len(got["shot_1"]) != 2 || len(got["shot_0"]) != 1 || len(got["shot_2"]) != 1 {
		t.Fatalf("replayed key must not submit again: a=%d b=%d c=%d", len(got["shot_0"]), len(got["shot_1"]), len(got["shot_2"]))
	}

	// 预算：确认预算 1 积分、每段预计 10 积分：提交前暂停，不建任务。
	if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioMute, EstimatedCredits: 30, BudgetCredits: 40}); err != nil {
		t.Fatal(err)
	}
	project, _ = svc.repo.VideoReplicationProject(project.UserID, project.ID)
	plan = videoReplicationPlan(project)
	plan.BudgetCredits = 1
	before := len(videoReplicationRunsByShot(t, svc, project)["shot_1"])
	_, failure, err := svc.submitVideoReplication(project, plan, "acceptance-budget", []string{"shot_1"}, true, "")
	if err != nil || !strings.Contains(failure, "预算") {
		t.Fatalf("budget must pause: failure=%q err=%v", failure, err)
	}
	paused, _ := svc.repo.VideoReplicationProject(project.UserID, project.ID)
	if !paused.Paused || len(videoReplicationRunsByShot(t, svc, project)["shot_1"]) != before {
		t.Fatalf("budget pause must not create a run: paused=%v", paused.Paused)
	}
}

// 中转站同时收到多段时自己的数据库被锁、拒了第 1 段：上游没建成任务，这段要自动重新提交，
// 不能把整个项目判失败；其它段还在生成也照样重新提交。
func TestVideoReplicationAcceptanceResubmitsShotRejectedAtSubmit(t *testing.T) {
	params := VideoReplicationParameters{LogicalModelID: "lm", Model: "m", ReferenceMode: "reference", ShotSeconds: []int{5, 10}, Auto: true, ConfirmFirst: true}
	svc, project, db := videoReplicationAcceptanceProject(t, params, 3)
	if _, err := svc.ConfirmVideoReplicationPlan(project.UserID, project.ID, VideoReplicationPlanInput{Strategy: VideoReplicationStrategySegmented, AudioMode: videoReplicationAudioMute, EstimatedCredits: 30}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Task{ID: "rejected-task", UserID: project.UserID, Type: "canvas_video", Status: model.TaskStatusFailed, Error: "模型服务暂时不可用（HTTP 500）；上游：database is locked (5) (SQLITE_BUSY)"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, run := range []model.VideoReplicationShotRun{
		{ID: newID(), ProjectID: project.ID, UserID: project.UserID, ShotID: "shot_0", Version: 1, IdempotencyKey: "k1:shot_0", Status: model.VideoReplicationRunFailed, TaskID: "rejected-task", Error: "模型服务暂时不可用（HTTP 500）；上游：database is locked (5) (SQLITE_BUSY)"},
		{ID: newID(), ProjectID: project.ID, UserID: project.UserID, ShotID: "shot_1", Version: 1, IdempotencyKey: "k1:shot_1", Status: model.VideoReplicationRunRunning},
		{ID: newID(), ProjectID: project.ID, UserID: project.UserID, ShotID: "shot_2", Version: 1, IdempotencyKey: "k1:shot_2", Status: model.VideoReplicationRunRunning},
	} {
		if created, err := svc.repo.ReserveVideoReplicationRun(&run); err != nil || !created {
			t.Fatalf("seed run: %v %v", created, err)
		}
		_ = svc.repo.UpdateVideoReplicationRun(run.ID, map[string]any{"status": run.Status, "error": run.Error, "task_id": run.TaskID})
	}
	project, _ = svc.repo.VideoReplicationProject(project.UserID, project.ID)
	_, failure := svc.retryVideoReplicationAuto(project, videoReplicationPlan(project), videoReplicationStoryboard(project), true)
	runs := videoReplicationRunsByShot(t, svc, project)
	if len(runs["shot_0"]) != 2 {
		t.Fatalf("rejected shot must be resubmitted even while others run: %d runs, failure %q", len(runs["shot_0"]), failure)
	}
	if strings.Contains(failure, "第 1 段生成失败") {
		t.Fatalf("a submit rejection must not fail the project: %q", failure)
	}
}

func TestVideoReplicationTransientSubmitError(t *testing.T) {
	for _, message := range []string{"模型服务暂时不可用（HTTP 500）；上游：database is locked (5) (SQLITE_BUSY)", "HTTP 429 Too Many Requests", "read: connection reset by peer"} {
		if !videoReplicationTransientSubmitError(message) {
			t.Fatalf("should resubmit: %s", message)
		}
	}
	for _, message := range []string{"请求内容未通过模型服务安全审核", "积分不足", "参数错误：duration must be -1"} {
		if videoReplicationTransientSubmitError(message) {
			t.Fatalf("should not resubmit: %s", message)
		}
	}
}
