package app

import (
	"testing"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newAccountPoolCatalogTestService 造一个最小服务：目录读模型必须在「没有任何 system_channels 记录」
// 的情况下也能发布账号池可选渠道，所以这里只建目录与账号池相关的表。
func newAccountPoolCatalogTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&model.ModelChannel{},
		&model.ChannelModel{},
		&model.ChannelModelPriceTier{},
		&model.SystemSetting{},
		&model.DoubaoAccount{},
		&model.DoubaoPoolMeta{},
	); err != nil {
		t.Fatal(err)
	}
	return New(repository.New(db), t.TempDir())
}

func accountPoolCatalogChannel(t *testing.T, catalog *ModelCatalogResponse, channelID string) PublicChannelCatalog {
	t.Helper()
	for _, channel := range catalog.Channels {
		if channel.ID == channelID {
			return channel
		}
	}
	t.Fatalf("模型目录缺少渠道 %s：%#v", channelID, catalog.Channels)
	return PublicChannelCatalog{}
}

func accountPoolCatalogModelKeys(channel PublicChannelCatalog) []string {
	keys := make([]string, 0, len(channel.Models))
	for _, item := range channel.Models {
		keys = append(keys, item.ModelKey)
	}
	return keys
}

// 账号池渠道必须出现在公开目录里：可选、默认对用户暴露（DefaultEnabled=true）、带可用性快照，
// 且模型条目标注为账号池计费口径。
// 这是「用户能选到账号池」的唯一入口——渠道不落 system_channels，目录不合成就没有任何 UI 能选到它。
func TestModelCatalogPublishesAccountPoolOptionalChannels(t *testing.T) {
	svc := newAccountPoolCatalogTestService(t)
	catalog, err := svc.ModelCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Source != ModelCatalogSourceSystem {
		t.Fatalf("目录来源 = %q", catalog.Source)
	}

	doubaoChannel := accountPoolCatalogChannel(t, catalog, DoubaoPoolChannelID)
	if !doubaoChannel.Optional || !doubaoChannel.DefaultEnabled {
		t.Fatalf("账号池渠道必须是可选且默认暴露：%#v", doubaoChannel)
	}
	if doubaoChannel.Availability == nil {
		t.Fatal("账号池渠道必须带可用性快照")
	}
	// 空池（测试库没有任何账号）必须是 empty 而不是 error：状态读到了，只是没有可用账号。
	if doubaoChannel.Availability.State != "empty" || doubaoChannel.Availability.ReadyAccounts != 0 {
		t.Fatalf("空池可用性 = %#v", doubaoChannel.Availability)
	}
	wantKeys := []string{doubaoPoolImageModel, doubaoPoolLayerModel, "doubao-seedance-video", "doubao-seedance-video-fast"}
	gotKeys := accountPoolCatalogModelKeys(doubaoChannel)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("豆包渠道模型 = %v, want %v", gotKeys, wantKeys)
	}
	for index, key := range wantKeys {
		if gotKeys[index] != key {
			t.Fatalf("豆包渠道模型 = %v, want %v", gotKeys, wantKeys)
		}
	}
	for _, item := range doubaoChannel.Models {
		if string(item.Protocol) != DoubaoPoolInterfaceType {
			t.Fatalf("模型 %s 协议 = %q, want %q", item.ModelKey, item.Protocol, DoubaoPoolInterfaceType)
		}
		if !item.Available || item.PricingMode != "pool" || item.PriceLabel == "" {
			t.Fatalf("模型 %s 的账号池计费口径不完整：%#v", item.ModelKey, item)
		}
		if len(item.PriceTiers) != 0 {
			t.Fatalf("账号池模型不应发布价格档：%#v", item.PriceTiers)
		}
	}

	dolaChannel := accountPoolCatalogChannel(t, catalog, DolaPoolChannelID)
	if !dolaChannel.Optional || !dolaChannel.DefaultEnabled {
		t.Fatalf("Dola 渠道必须是可选且默认暴露：%#v", dolaChannel)
	}
	// Dola 站不出图，所以这里只登记视频键：模型键与渠道 ID 决定取号站点，配错就是必然失败项。
	for _, item := range dolaChannel.Models {
		if item.Capability != "video" {
			t.Fatalf("Dola 渠道不应登记 %s 能力模型：%#v", item.Capability, item)
		}
	}
	if len(dolaChannel.Models) != 3 {
		t.Fatalf("Dola 渠道模型 = %v", accountPoolCatalogModelKeys(dolaChannel))
	}
}

// 目录按请求意图过滤能力：图片选择器里不能混进视频模型，纯视频渠道在图片意图下整体隐藏。
func TestModelCatalogAccountPoolChannelFollowsIntent(t *testing.T) {
	svc := newAccountPoolCatalogTestService(t)
	catalog, err := svc.ModelCatalog(&ModelRequestIntent{Capability: "image"})
	if err != nil {
		t.Fatal(err)
	}
	doubaoChannel := accountPoolCatalogChannel(t, catalog, DoubaoPoolChannelID)
	for _, item := range doubaoChannel.Models {
		if item.Capability != "image" {
			t.Fatalf("图片意图下出现 %s 能力模型：%#v", item.Capability, item)
		}
	}
	for _, channel := range catalog.Channels {
		if channel.ID == DolaPoolChannelID {
			t.Fatalf("图片意图下不应发布纯视频渠道：%#v", channel)
		}
	}
}

// 账号池任务没有渠道模型行：任务能力校验必须走「无系统渠道」分支，否则真实请求会被
// 误报成「当前系统渠道模型未配置或已停用」，用户即使能选到模型也发不出去。
func TestValidateTaskCapabilityAcceptsAccountPoolChannel(t *testing.T) {
	svc := newAccountPoolCatalogTestService(t)
	input := map[string]any{
		"mode": "image",
		"config": providerConfig{
			ChannelID:     DoubaoPoolChannelID,
			InterfaceType: DoubaoPoolInterfaceType,
			BaseURL:       "/api/" + DoubaoPoolChannelID,
			APIKey:        "system",
			Model:         doubaoPoolLayerModel,
			Count:         "3",
		},
		"referenceImages": []providerMedia{{StorageKey: "resource:source", MimeType: "image/png", Bytes: 1024}},
	}
	if err := svc.ValidateTaskCapability(input); err != nil {
		t.Fatalf("账号池任务能力校验被拒绝：%v", err)
	}

	// 蒙版走池：池协议没有蒙版端点，必须在校验阶段就明确拒绝。
	withMask := map[string]any{
		"mode": "image",
		"config": providerConfig{
			ChannelID:     DoubaoPoolChannelID,
			InterfaceType: DoubaoPoolInterfaceType,
			Model:         doubaoPoolImageModel,
		},
		"mask": providerMedia{StorageKey: "resource:mask", MimeType: "image/png", Bytes: 1024},
	}
	if err := svc.ValidateTaskCapability(withMask); err == nil {
		t.Fatal("账号池不支持蒙版编辑，校验阶段必须拒绝")
	}
}

// 账号池按「不扣平台积分」计费：即使 credits 功能开启，也不能因为查不到渠道模型行而拒绝建单。
func TestTaskBillingOrderSkipsAccountPoolChannel(t *testing.T) {
	svc, _ := newFeatureAvailabilityTestService(t)
	actor := &model.User{ID: "admin-1", Role: model.UserRoleAdmin}
	if _, err := svc.UpdateFeatureAvailability(actor, FeatureAvailability{
		ShortDramaEnabled: true, TaskCenterEnabled: true, CreditsEnabled: true, CustomChannelsEnabled: true, FrontendModelsEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	order, err := svc.taskBillingOrder("user-1", &model.Task{ID: "task-1", Type: "canvas_image", Operation: "image"}, map[string]any{
		"config": map[string]any{"channelId": DoubaoPoolChannelID, "model": doubaoPoolImageModel},
	})
	if err != nil {
		t.Fatalf("账号池建单失败：%v", err)
	}
	if order != nil {
		t.Fatalf("账号池不应产生积分订单：%#v", order)
	}
}

// 池协议的能力默认值必须与执行层上限一致：图层拆分输出上限、参考图数量、蒙版与时长档。
func TestDoubaoPoolCapabilityDefaultsMatchPoolLimits(t *testing.T) {
	image := DefaultImageCapabilityConfig(DoubaoPoolInterfaceType, doubaoPoolImageModel)
	if image.MaxOutputs != doubaoPoolMaxLayers {
		t.Fatalf("图片输出上限 = %d, want %d", image.MaxOutputs, doubaoPoolMaxLayers)
	}
	if image.References.MaxImages != doubaoPoolMaxRefImages || image.References.MaskSupported {
		t.Fatalf("图片参考图能力 = %#v", image.References)
	}
	if image.TransparentBackground.Supported || image.Quality.Supported {
		t.Fatalf("账号池没有透明背景与质量档：%#v", image)
	}

	profile := DefaultModelCapabilityConfigForModel(DoubaoPoolInterfaceType, "doubao-seedance-video")
	if profile.Video == nil {
		t.Fatal("账号池视频能力配置缺失")
	}
	if profile.Video.Duration.Min != 4 || profile.Video.Duration.Max != 30 {
		t.Fatalf("视频时长档 = %#v", profile.Video.Duration)
	}
	if profile.Video.References.MaxImages != doubaoPoolMaxRefImages || profile.Video.References.MaxVideos != 0 {
		t.Fatalf("视频参考素材能力 = %#v", profile.Video.References)
	}
}
