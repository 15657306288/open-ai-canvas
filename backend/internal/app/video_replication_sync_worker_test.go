package app

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

// 用户关掉页面后，后台仍按任务真实状态推进生成中的项目；超出时间窗口和已结束的项目不碰。
func TestSyncActiveVideoReplicationsAdvancesWithoutPolling(t *testing.T) {
	svc, db := newVideoReplicationTestService(t)
	storyboard := `{"shots":[{"id":"shot_a","prompt":"a","durationSec":5}]}`
	projects := []model.VideoReplicationProject{
		{ID: "recent", UserID: "user-1", Status: model.VideoReplicationStatusGenerating, StoryboardJSON: storyboard},
		{ID: "stale", UserID: "user-1", Status: model.VideoReplicationStatusGenerating, StoryboardJSON: storyboard, CreatedAt: time.Now().Add(-8 * 24 * time.Hour)},
		{ID: "done", UserID: "user-1", Status: model.VideoReplicationStatusCompleted, StoryboardJSON: storyboard},
	}
	for i := range projects {
		if err := db.Create(&projects[i]).Error; err != nil {
			t.Fatal(err)
		}
		task := model.Task{ID: "task-" + projects[i].ID, UserID: "user-1", Type: "canvas_video", Status: model.TaskStatusSucceeded, ResultJSON: `{"resourceId":"video-` + projects[i].ID + `"}`}
		if err := db.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
		run := model.VideoReplicationShotRun{ID: "run-" + projects[i].ID, ProjectID: projects[i].ID, UserID: "user-1", ShotID: "shot_a", Version: 1, IdempotencyKey: "k:" + projects[i].ID, TaskID: task.ID, Status: model.VideoReplicationRunRunning}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
	}
	active, err := svc.repo.ActiveVideoReplicationProjects(time.Now().Add(-videoReplicationSyncWindow), 10)
	if err != nil || len(active) != 1 || active[0].ID != "recent" {
		t.Fatalf("active = %+v err=%v", active, err)
	}
	svc.syncActiveVideoReplications(context.Background())
	for id, want := range map[string]string{"recent": model.VideoReplicationRunSucceeded, "stale": model.VideoReplicationRunRunning, "done": model.VideoReplicationRunRunning} {
		runs, err := svc.repo.VideoReplicationRuns("user-1", id)
		if err != nil || len(runs) != 1 || runs[0].Status != want {
			t.Fatalf("%s runs = %+v err=%v", id, runs, err)
		}
	}
	if runs, _ := svc.repo.VideoReplicationRuns("user-1", "recent"); runs[0].OutputResourceID != "video-recent" || !runs[0].Selected {
		t.Fatalf("recent run not synced: %+v", runs[0])
	}
}

// 暂停原因 / 失败原因写进 stage（varchar(200)）前要截断，否则 PostgreSQL 拒绝写入，项目永远同步失败。
func TestSyncVideoReplicationClipsLongStage(t *testing.T) {
	svc, db := newVideoReplicationTestService(t)
	reason := strings.Repeat("upstream error ", 40)
	project := &model.VideoReplicationProject{ID: "long", UserID: "user-1", Status: model.VideoReplicationStatusGenerating, Paused: true, PauseReason: reason, StoryboardJSON: `{"shots":[{"id":"shot_a","prompt":"a","durationSec":5}]}`}
	if err := db.Create(project).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.syncVideoReplication(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	refreshed, _ := svc.repo.VideoReplicationProject("user-1", "long")
	if n := utf8.RuneCountInString(refreshed.Stage); n == 0 || n > 200 {
		t.Fatalf("stage length = %d", n)
	}
}
