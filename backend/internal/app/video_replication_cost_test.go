package app

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/model"
	"yingce/backend/internal/platform"
)

// 这些测试验证分析阶段的「记录链路」：上游用本机 httptest 服务代替，返回固定的用量。
// 它们不代表任何真实供应商的价格或返回，真实成本需要在线上用真实渠道核对。

type videoReplicationCostFixture struct {
	svc      *Service
	db       *gorm.DB
	upstream *httptest.Server
	reply    atomic.Value // func(w http.ResponseWriter, r *http.Request)
	calls    atomic.Int32
}

func newVideoReplicationCostFixture(t *testing.T) *videoReplicationCostFixture {
	t.Helper()
	svc, db := mockEcommerceAgentService(t)
	svc.coordinator = platform.NewCoordinatorWithRedis(nil, "video-replication-cost-test")
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	if err := db.AutoMigrate(&model.ApiCallLog{}, &model.ModelPricing{}, &model.ChannelModelPriceTier{}, &model.Task{}, &model.UserDailyActivity{}, &model.Asset{}, &model.CanvasProject{}, &model.Result{}, &model.TaskLog{}, &model.TaskTextDelta{}); err != nil {
		t.Fatal(err)
	}
	fixture := &videoReplicationCostFixture{svc: svc, db: db}
	fixture.reply.Store(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"resp-1","choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	})
	fixture.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		fixture.reply.Load().(func(http.ResponseWriter, *http.Request))(w, r)
	}))
	t.Cleanup(fixture.upstream.Close)
	if err := db.Create(&model.ModelChannel{ID: "ch-1", Scope: model.ChannelScopeSystem, Enabled: true, Name: "测试渠道", BaseURL: fixture.upstream.URL}).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id, key, capability, mode string
		cost                      model.CreditCostPricing
	}{
		// 文本按 Token 计成本；语音识别按秒计成本（供应商的实际计费单位）。
		{"cm-text", "vision-test", "text", "token", model.CreditCostPricing{Configured: true, InputTokenPriceMicrocredits: 2_000_000, OutputTokenPriceMicrocredits: 8_000_000}},
		{"cm-asr", "asr-test", "audio", "per_second", model.CreditCostPricing{Configured: true, UnitPriceMicrocredits: 10_000}},
		{"cm-nocost", "nocost-test", "text", "token", model.CreditCostPricing{}},
	} {
		if err := db.Create(&model.ChannelModel{ID: item.id, ChannelID: "ch-1", ModelKey: item.key, Capability: item.capability, Enabled: true, PriceConfigured: true}).Error; err != nil {
			t.Fatal(err)
		}
		tier := model.ChannelModelPriceTier{ID: "tier-" + item.id, ChannelModelID: item.id, SelectorKey: "{}", SelectorJSON: "{}", Resolution: "*", BillingMode: item.mode, InputTokenPriceMicrocredits: 1, OutputTokenPriceMicrocredits: 1, UnitPriceMicrocredits: 1, PriceConfigured: true, Enabled: true, PriceVersion: 1, CostPricing: item.cost}
		if err := db.Create(&tier).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 渠道的上游单价（API 调用日志里的估算成本）：每百万输入 1 元、输出 2 元，每次识别 0.5 元。
	for _, pricing := range []model.ModelPricing{
		{ID: "mp-text", ChannelID: "ch-1", Model: "vision-test", Capability: "text", Currency: "CNY", InputPerMillionMicros: 1_000_000, OutputPerMillionMicros: 2_000_000},
		{ID: "mp-asr", ChannelID: "ch-1", Model: "asr-test", Capability: "audio", Currency: "CNY", PerRequestMicros: 500_000},
	} {
		if err := db.Create(&pricing).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.User{ID: "admin", Username: "admin", Role: model.UserRoleAdmin}).Error; err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *videoReplicationCostFixture) cfg(modelKey string) ecommerceProviderConfig {
	return ecommerceProviderConfig{TextTimeout: 5 * time.Second, TextModel: modelKey, TextProvider: &providerConfig{InterfaceType: "chat-completion", BaseURL: f.upstream.URL, APIKey: "fixture-key", Model: modelKey, ChannelID: "ch-1", ChannelModelKey: modelKey}}
}

func (f *videoReplicationCostFixture) logs(t *testing.T) []model.ApiCallLog {
	t.Helper()
	var logs []model.ApiCallLog
	if err := f.db.Order("created_at ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	return logs
}

func (f *videoReplicationCostFixture) orders(t *testing.T) []model.BillingOrder {
	t.Helper()
	var orders []model.BillingOrder
	if err := f.db.Order("created_at ASC").Find(&orders).Error; err != nil {
		t.Fatal(err)
	}
	return orders
}
