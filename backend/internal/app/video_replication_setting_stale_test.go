package app

import (
	"encoding/json"
	"testing"

	"yingce/backend/internal/model"
)

func TestVideoReplicationSettingDropsUnavailableModels(t *testing.T) {
	svc, db := newChannelModelTestService(t)
	admin := &model.User{ID: "admin", Role: model.UserRoleAdmin}
	channels := []model.ModelChannel{
		{ID: "on", Scope: model.ChannelScopeSystem, Enabled: true},
		{ID: "off", Scope: model.ChannelScopeSystem, Enabled: false},
	}
	models := []model.ChannelModel{
		{ID: "vision", ChannelID: "on", ModelKey: "gemini", Capability: "text", Enabled: true},
		{ID: "planning", ChannelID: "on", ModelKey: "planning", Capability: "text", Enabled: true},
		{ID: "asr", ChannelID: "on", ModelKey: "transcribe", Capability: "audio", Enabled: false},
		{ID: "tts", ChannelID: "off", ModelKey: "voice", Capability: "audio", Enabled: true},
		{ID: "video-deleted", ChannelID: "on", ModelKey: "seedance", Capability: "video", Enabled: true},
		{ID: "video-live", ChannelID: "on", ModelKey: "seedance-live", Capability: "video", Enabled: true},
	}
	for _, rows := range []any{&channels, &models} {
		if err := db.Create(rows).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 模拟旧版本留下的设置和管理员删除模型前打开的页面。
	input := VideoReplicationSetting{
		Vision:   &EcommerceAgentModelRef{ChannelModelID: "vision"},
		Planning: &EcommerceAgentModelRef{ChannelModelID: "planning"},
		ASR:      &VideoReplicationASRSetting{Mode: "model", Model: EcommerceAgentModelRef{ChannelModelID: "asr"}, Language: "zh"},
		TTS:      &VideoReplicationTTSSetting{Model: EcommerceAgentModelRef{ChannelModelID: "tts"}},
		VideoModels: []VideoReplicationVideoModel{
			{EcommerceAgentModelRef: EcommerceAgentModelRef{ChannelModelID: "video-deleted"}, Enabled: true},
			{EcommerceAgentModelRef: EcommerceAgentModelRef{ChannelModelID: "video-live"}, Enabled: false},
		},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.repo.SaveSystemSetting(&model.SystemSetting{Key: videoReplicationSettingKey, ValueJSON: string(raw)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteAdminChannelModels(admin, "on", []string{"vision", "video-deleted"}); err != nil {
		t.Fatal(err)
	}

	assertPruned := func(setting VideoReplicationSetting) {
		t.Helper()
		if setting.Vision != nil || setting.TTS != nil || setting.ASR == nil || setting.ASR.Mode != "env" || setting.ASR.Language != "zh" {
			t.Fatalf("stale analysis or audio references: %+v", setting)
		}
		if setting.Planning == nil || setting.Planning.ModelKey != "planning" {
			t.Fatalf("valid planning model not preserved: %+v", setting.Planning)
		}
		if !setting.RestrictVideoModels || len(setting.VideoModels) != 1 || setting.VideoModels[0].ChannelModelID != "video-live" || setting.VideoModels[0].Enabled {
			t.Fatalf("video list or its restriction changed: %+v", setting)
		}
	}
	status, err := svc.AdminVideoReplicationStatus(admin)
	if err != nil {
		t.Fatal(err)
	}
	assertPruned(status.Setting)
	if err := svc.videoReplicationModelAllowed(VideoReplicationParameters{ChannelID: "on", Model: "seedance-live"}); err == nil {
		t.Fatal("a closed video model must not be allowed when all models in the list are closed")
	}

	input.MaxReferenceSeconds = 90
	status, err = svc.UpdateVideoReplicationSetting(admin, input)
	if err != nil {
		t.Fatal(err)
	}
	assertPruned(status.Setting)
	row, err := svc.repo.SystemSetting(videoReplicationSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	var saved VideoReplicationSetting
	if err := json.Unmarshal([]byte(row.ValueJSON), &saved); err != nil {
		t.Fatal(err)
	}
	assertPruned(saved)
	if saved.MaxReferenceSeconds != 90 {
		t.Fatalf("unrelated change was not saved: %+v", saved)
	}

	if err := svc.DeleteAdminChannelModel(admin, "on", "video-live"); err != nil {
		t.Fatal(err)
	}
	if err := svc.repo.SaveSystemSetting(&model.SystemSetting{Key: featureAvailabilitySettingKey, ValueJSON: `{"videoWorkbenchEnabled":true}`}); err != nil {
		t.Fatal(err)
	}
	config, err := svc.VideoReplicationConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !config.RestrictVideoModels || len(config.VideoModels) != 0 || config.TTSConfigured {
		t.Fatalf("empty restricted public list must stay closed: %+v", config)
	}
}
