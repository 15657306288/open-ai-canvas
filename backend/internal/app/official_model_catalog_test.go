package app

import (
	"encoding/json"
	"testing"

	"infinite-canvas/backend/internal/model"
)

func TestOfficialModelCatalogLoadsExpectedFamilies(t *testing.T) {
	catalog, err := loadOfficialModelCatalog()
	if err != nil {
		t.Fatalf("loadOfficialModelCatalog() error = %v", err)
	}
	if catalog.Version == "" || len(catalog.Models) < 10 {
		t.Fatalf("catalog = %#v, want a versioned multi-provider catalog", catalog)
	}
	want := map[string]string{
		"gpt-4o":                "openai-response",
		"gpt-image-1":           "openai-image",
		"gemini-2.5-flash":      "gemini-generate-content",
		"deepseek-chat":         "deepseek-chat",
		"deepseek-vl2":          "deepseek-chat",
		"claude-fable-5.1":      "claude-api",
		"gemini-3.8-flash":      "chat-completion",
		"gemini-3.8-flash-high": "chat-completion",
		"gemini-pro-agent":      "chat-completion",
		"gpt-5.6-sol":           "chat-completion",
		"gpt-6-astra":           "chat-completion",
	}
	for _, entry := range catalog.Models {
		if protocol, ok := want[entry.ModelKey]; ok && entry.Protocol != protocol {
			t.Fatalf("catalog model %q protocol = %q, want %q", entry.ModelKey, entry.Protocol, protocol)
		}
		if (entry.ModelKey == "gpt-4o" || entry.ModelKey == "gemini-3.8-flash" || entry.ModelKey == "gpt-5.6-sol") && (!entry.Multimodal || entry.MaxImages == 0) {
			t.Fatalf("catalog model %q lost multimodal reference limits: %#v", entry.ModelKey, entry)
		}
	}
}

func TestEnsureOfficialModelCatalogIsAdditiveAndIdempotent(t *testing.T) {
	svc, db := newChannelModelTestService(t)
	channel := model.ModelChannel{
		ID:         "channel-openai",
		Scope:      model.ChannelScopeSystem,
		Enabled:    true,
		Name:       "OpenAI 官方渠道",
		BaseURL:    "https://api.openai.com/v1",
		APIKey:     "secret",
		APIFormat:  "openai",
		ModelsJSON: `[]`,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	existing := model.ChannelModel{
		ID:                    "MODEL-existing",
		ChannelID:             channel.ID,
		ModelKey:              "gpt-4o",
		ProviderModelKey:      "custom-gpt-4o",
		DisplayName:           "我的 GPT-4o",
		Capability:            "text",
		Protocol:              model.ChannelInterfaceType("custom-openai-protocol"),
		BillingMode:           "per_token",
		UnitPriceMicrocredits: 123,
		PriceConfigured:       true,
		Enabled:               true,
		PriceVersion:          7,
		CapabilityConfigJSON:  `{"keep":true}`,
		CapabilityVersion:     9,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.EnsureOfficialModelCatalog(); err != nil {
		t.Fatalf("first sync error = %v", err)
	}
	items, err := svc.repo.ChannelModels(channel.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 7 {
		t.Fatalf("synced model count = %d, want 7 OpenAI entries", len(items))
	}
	var preserved *model.ChannelModel
	for index := range items {
		item := &items[index]
		if item.ModelKey == "gpt-4o" {
			preserved = item
			continue
		}
		if item.Enabled || item.PriceConfigured {
			t.Fatalf("new catalog model %q should be disabled and unpriced: %#v", item.ModelKey, item)
		}
		var config map[string]any
		if err := json.Unmarshal([]byte(item.CapabilityConfigJSON), &config); err != nil || len(config) == 0 {
			t.Fatalf("new catalog model %q has invalid capability config %q", item.ModelKey, item.CapabilityConfigJSON)
		}
	}
	if preserved == nil {
		t.Fatal("existing GPT-4o model disappeared")
	}
	if preserved.ProviderModelKey != existing.ProviderModelKey || preserved.DisplayName != existing.DisplayName || preserved.Protocol != existing.Protocol || preserved.BillingMode != existing.BillingMode || !preserved.Enabled || !preserved.PriceConfigured || preserved.UnitPriceMicrocredits != existing.UnitPriceMicrocredits || preserved.CapabilityConfigJSON != existing.CapabilityConfigJSON || preserved.CapabilityVersion != existing.CapabilityVersion {
		t.Fatalf("existing model was overwritten: got %#v, want preserved %#v", preserved, existing)
	}

	if err := svc.EnsureOfficialModelCatalog(); err != nil {
		t.Fatalf("second sync error = %v", err)
	}
	itemsAgain, err := svc.repo.ChannelModels(channel.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(itemsAgain) != len(items) {
		t.Fatalf("second sync model count = %d, want %d", len(itemsAgain), len(items))
	}
}

func TestModelListEntriesAreRegisteredAndKnownMultimodalModelsAreEnriched(t *testing.T) {
	svc, db := newChannelModelTestService(t)
	channel := model.ModelChannel{
		ID:         "channel-platform-list",
		Scope:      model.ChannelScopeSystem,
		Enabled:    true,
		Name:       "平台服务",
		APIFormat:  "custom",
		ModelsJSON: `["claude-fable-5.1","gemini-3.8-flash","gpt-5.6-sol","gpt-6-astra","gemini-3.8-flash-high","gemini-pro-agent","mj-8.2"]`,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.EnsureSystemChannelModels(); err != nil {
		t.Fatalf("EnsureSystemChannelModels() error = %v", err)
	}
	if err := svc.EnsureOfficialModelCatalog(); err != nil {
		t.Fatalf("EnsureOfficialModelCatalog() error = %v", err)
	}

	items, err := svc.repo.ChannelModels(channel.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 7 {
		t.Fatalf("registered model count = %d, want 7", len(items))
	}

	known := map[string]bool{
		"claude-fable-5.1":      true,
		"gemini-3.8-flash":      true,
		"gemini-3.8-flash-high": true,
		"gemini-pro-agent":      true,
		"gpt-5.6-sol":           true,
		"gpt-6-astra":           true,
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		seen[item.ModelKey] = true
		if item.ModelKey == "mj-8.2" {
			if item.Protocol != "" || item.Capability != "" || item.CapabilityConfigJSON != "" {
				t.Fatalf("mj-8.2 must stay unconfigured until its upstream protocol is confirmed: %#v", item)
			}
			continue
		}
		if !known[item.ModelKey] {
			continue
		}
		if item.Protocol == "" || item.Capability != "text" || item.CapabilityConfigJSON == "" {
			t.Fatalf("known model %q was not enriched: %#v", item.ModelKey, item)
		}
		config, decodeErr := DecodeModelCapabilityConfig(item.CapabilityConfigJSON)
		if decodeErr != nil || config == nil || config.Text == nil || config.Text.References.MaxImages == 0 {
			t.Fatalf("known model %q missing multimodal text capability: %#v", item.ModelKey, item)
		}
	}
	for key := range known {
		if !seen[key] {
			t.Fatalf("known model %q was not registered", key)
		}
	}
}

func TestEnsureOfficialModelCatalogRepairsLegacyMultimodalCapabilityWithoutChangingPrice(t *testing.T) {
	svc, db := newChannelModelTestService(t)
	channel := model.ModelChannel{
		ID:         "channel-legacy-multimodal",
		Scope:      model.ChannelScopeSystem,
		Enabled:    true,
		Name:       "平台服务",
		ModelsJSON: `["gemini-pro-agent"]`,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	legacy := DefaultModelCapabilityConfigForModel("chat-completion", "gemini-pro-agent")
	legacy.Text.References.MaxImages = 0
	legacy.Text.References.MaxImageBytes = 0
	legacy.Text.References.MaxVideos = 0
	legacy.Text.References.MaxVideoBytes = 0
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	existing := model.ChannelModel{
		ID:                    "MODEL-legacy-gemini-agent",
		ChannelID:             channel.ID,
		ModelKey:              "gemini-pro-agent",
		ProviderModelKey:      "gemini-pro-agent",
		DisplayName:           "Gemini Pro Agent · 自用中转",
		Capability:            "text",
		Protocol:              model.ChannelInterfaceChatCompletion,
		BillingMode:           "fixed_request",
		UnitPriceMicrocredits: 321000,
		PriceConfigured:       true,
		Enabled:               true,
		PriceVersion:          7,
		CapabilityConfigJSON:  string(raw),
		CapabilityVersion:     4,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.EnsureOfficialModelCatalog(); err != nil {
		t.Fatalf("EnsureOfficialModelCatalog() error = %v", err)
	}
	var repaired model.ChannelModel
	if err := db.First(&repaired, "id = ?", existing.ID).Error; err != nil {
		t.Fatal(err)
	}
	config, err := DecodeModelCapabilityConfig(repaired.CapabilityConfigJSON)
	if err != nil || config == nil || config.Text == nil {
		t.Fatalf("repaired capability config = %#v, err = %v", config, err)
	}
	if config.Text.References.MaxImages == 0 || config.Text.References.MaxVideos == 0 {
		t.Fatalf("legacy multimodal limits were not repaired: %#v", config.Text.References)
	}
	if repaired.DisplayName != existing.DisplayName || repaired.UnitPriceMicrocredits != existing.UnitPriceMicrocredits || !repaired.PriceConfigured || !repaired.Enabled || repaired.PriceVersion != existing.PriceVersion {
		t.Fatalf("price or enabled state changed: got %#v, want %#v", repaired, existing)
	}
}
