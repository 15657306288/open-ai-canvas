package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"yingce/backend/internal/model"
)

func TestVideoComparisonOwnershipAndCleanup(t *testing.T) {
	s, db := newVideoReplicationTestService(t)
	if err := db.AutoMigrate(&model.Resource{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SystemSetting{Key: featureAvailabilitySettingKey, ValueJSON: `{"videoWorkbenchEnabled":true}`}).Error; err != nil {
		t.Fatal(err)
	}
	s.dataDir = t.TempDir()
	project := model.VideoReplicationProject{ID: "comparison-project", UserID: "owner", Title: "Test", ParametersJSON: "{}"}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"reference", "generated"} {
		if err := db.Create(&model.Resource{ID: id, UserID: "owner", Provider: "local", ObjectKey: id + ".mp4", MimeType: "video/mp4", Status: model.ResourceStatusReady}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.VideoReplicationAsset{ID: "reference-asset", ProjectID: project.ID, UserID: "owner", Category: model.VideoReplicationAssetReferenceVideo, ResourceID: "reference"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.VideoReplicationOutput{ID: "comparison-output", ProjectID: project.ID, UserID: "owner", Status: "succeeded", ResourceID: "generated"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ user, output string }{{"", "comparison-output"}, {"intruder", "comparison-output"}, {"owner", "other-project-output"}} {
		if _, err := s.PrepareVideoReplicationComparison(context.Background(), tc.user, project.ID, tc.output); err == nil {
			t.Fatalf("invalid ownership accepted: %+v", tc)
		}
	}
	// Even a project-owned output cannot reference another user's resource.
	if err := db.Model(&model.Resource{}).Where("id = ?", "generated").Update("user_id", "intruder").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareVideoReplicationComparison(context.Background(), "owner", project.ID, "comparison-output"); err == nil {
		t.Fatal("foreign resource accepted")
	}
	if err := db.Model(&model.Resource{}).Where("id = ?", "generated").Update("user_id", "owner").Error; err != nil {
		t.Fatal(err)
	}
	bin := requireVideoReplicationFFmpeg(t)
	if err := os.MkdirAll(filepath.Join(s.dataDir, "resources"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"reference", "generated"} {
		makeReplicationClip(t, bin, filepath.Join(s.dataDir, "resources", id+".mp4"), 1, 1, true)
	}
	download, err := s.PrepareVideoReplicationComparison(context.Background(), "owner", project.ID, "comparison-output")
	if err != nil {
		t.Fatal(err)
	}
	if probe, err := probeVideoReplicationFile(context.Background(), download.Path); err != nil || !probe.HasAudio || probe.Width != 808 {
		t.Fatalf("comparison: %+v %v", probe, err)
	}
	download.Close()
	if _, err := os.Stat(download.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary export not removed: %v", err)
	}
	var count int64
	if err := db.Model(&model.VideoReplicationOutput{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("export mutated output versions: %d %v", count, err)
	}
}
