package app

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"

	"infinite-canvas/backend/internal/model"
)

//go:embed official_model_catalog.json
var officialModelCatalogJSON []byte

type officialModelCatalog struct {
	Version string                      `json:"version"`
	Models  []officialModelCatalogEntry `json:"models"`
}

type officialModelCatalogEntry struct {
	Provider         string                    `json:"provider"`
	ModelKey         string                    `json:"modelKey"`
	ProviderModelKey string                    `json:"providerModelKey,omitempty"`
	DisplayName      string                    `json:"displayName"`
	Description      string                    `json:"description,omitempty"`
	Protocol         string                    `json:"protocol"`
	Capability       string                    `json:"capability"`
	Multimodal       bool                      `json:"multimodal"`
	MaxImages        int                       `json:"maxImages,omitempty"`
	MaxImageBytes    int64                     `json:"maxImageBytes,omitempty"`
	MaxVideos        int                       `json:"maxVideos,omitempty"`
	MaxVideoBytes    int64                     `json:"maxVideoBytes,omitempty"`
	Match            officialModelCatalogMatch `json:"match"`
}

type officialModelCatalogMatch struct {
	Hosts      []string `json:"hosts,omitempty"`
	NameTokens []string `json:"nameTokens,omitempty"`
	APIFormats []string `json:"apiFormats,omitempty"`
}

// EnsureOfficialModelCatalog synchronizes the versioned official model directory
// into existing system channels. It is intentionally additive and idempotent:
// credentials, proxies, prices, enabled state, and custom routes are never
// changed by this process.
func (s *Service) EnsureOfficialModelCatalog() error {
	if s == nil || s.repo == nil {
		return nil
	}
	catalog, err := loadOfficialModelCatalog()
	if err != nil {
		return err
	}
	channels, err := s.repo.SystemChannels(true)
	if err != nil {
		return err
	}
	added, updated := 0, 0
	for index := range channels {
		channel := &channels[index]
		items, err := s.repo.ChannelModels(channel.ID, true)
		if err != nil {
			return err
		}
		byKey := make(map[string]*model.ChannelModel, len(items))
		for itemIndex := range items {
			byKey[catalogModelKey(items[itemIndex].ModelKey)] = &items[itemIndex]
		}
		for _, entry := range catalog.Models {
			key := catalogModelKey(entry.ModelKey)
			if key == "" || !officialCatalogEntryMatchesChannel(entry, channel, items) {
				continue
			}
			if item := byKey[key]; item != nil {
				changed, err := enrichOfficialChannelModel(item, entry)
				if err != nil {
					return fmt.Errorf("补全官方模型 %s：%w", entry.ModelKey, err)
				}
				if changed {
					if err := s.repo.SaveChannelModel(item); err != nil {
						return err
					}
					updated++
				}
				continue
			}
			modelID, err := s.repo.NextPrefixedID("MODEL")
			if err != nil {
				return err
			}
			item, err := officialChannelModel(modelID, channel.ID, entry)
			if err != nil {
				return fmt.Errorf("创建官方模型 %s：%w", entry.ModelKey, err)
			}
			if _, err := s.repo.CreateMissingChannelModels([]model.ChannelModel{item}); err != nil {
				return err
			}
			byKey[key] = &item
			items = append(items, item)
			added++
		}
	}
	if added > 0 || updated > 0 {
		log.Printf("official model catalog %s synchronized: added=%d updated=%d", catalog.Version, added, updated)
	}
	return nil
}

func loadOfficialModelCatalog() (officialModelCatalog, error) {
	var catalog officialModelCatalog
	if err := json.Unmarshal(officialModelCatalogJSON, &catalog); err != nil {
		return catalog, fmt.Errorf("解析官方模型目录失败：%w", err)
	}
	if strings.TrimSpace(catalog.Version) == "" || len(catalog.Models) == 0 {
		return catalog, fmt.Errorf("官方模型目录为空或缺少版本")
	}
	seen := make(map[string]bool, len(catalog.Models))
	for _, entry := range catalog.Models {
		key := catalogModelKey(entry.ModelKey)
		if key == "" || strings.TrimSpace(entry.Protocol) == "" || strings.TrimSpace(entry.Capability) == "" {
			return catalog, fmt.Errorf("官方模型目录存在无效条目：%q", entry.ModelKey)
		}
		if seen[entry.Provider+":"+key] {
			return catalog, fmt.Errorf("官方模型目录存在重复条目：%s", entry.ModelKey)
		}
		seen[entry.Provider+":"+key] = true
	}
	return catalog, nil
}

func officialChannelModel(id, channelID string, entry officialModelCatalogEntry) (model.ChannelModel, error) {
	providerKey := strings.TrimPrefix(strings.TrimSpace(entry.ProviderModelKey), "models/")
	if providerKey == "" {
		providerKey = strings.TrimPrefix(strings.TrimSpace(entry.ModelKey), "models/")
	}
	config, err := officialModelCapabilityConfig(entry)
	if err != nil {
		return model.ChannelModel{}, err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return model.ChannelModel{}, err
	}
	return model.ChannelModel{
		ID:                   id,
		ChannelID:            channelID,
		ModelKey:             strings.TrimPrefix(strings.TrimSpace(entry.ModelKey), "models/"),
		ProviderModelKey:     providerKey,
		DisplayName:          strings.TrimSpace(entry.DisplayName),
		Capability:           strings.TrimSpace(entry.Capability),
		Protocol:             model.ChannelInterfaceType(strings.TrimSpace(entry.Protocol)),
		BillingMode:          "fixed_request",
		Enabled:              false,
		PriceConfigured:      false,
		PriceVersion:         1,
		CapabilityConfigJSON: string(encoded),
		CapabilityVersion:    1,
	}, nil
}

func enrichOfficialChannelModel(item *model.ChannelModel, entry officialModelCatalogEntry) (bool, error) {
	if item == nil {
		return false, nil
	}
	changed := false
	if strings.TrimSpace(item.ProviderModelKey) == "" {
		item.ProviderModelKey = strings.TrimPrefix(strings.TrimSpace(entry.ProviderModelKey), "models/")
		if item.ProviderModelKey == "" {
			item.ProviderModelKey = strings.TrimPrefix(strings.TrimSpace(entry.ModelKey), "models/")
		}
		changed = true
	}
	if strings.TrimSpace(item.DisplayName) == "" {
		item.DisplayName = strings.TrimSpace(entry.DisplayName)
		changed = true
	}
	if strings.TrimSpace(item.Capability) == "" {
		item.Capability = strings.TrimSpace(entry.Capability)
		changed = true
	}
	if strings.TrimSpace(string(item.Protocol)) == "" {
		item.Protocol = model.ChannelInterfaceType(strings.TrimSpace(entry.Protocol))
		changed = true
	}
	if strings.TrimSpace(item.BillingMode) == "" {
		item.BillingMode = "fixed_request"
		changed = true
	}
	if strings.TrimSpace(item.CapabilityConfigJSON) == "" && item.Capability != "" {
		config, err := officialModelCapabilityConfig(entry)
		if err != nil {
			return false, err
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			return false, err
		}
		item.CapabilityConfigJSON = string(encoded)
		if item.CapabilityVersion == 0 {
			item.CapabilityVersion = 1
		}
		changed = true
	} else if strings.TrimSpace(item.CapabilityConfigJSON) != "" && item.Capability != "" {
		// 历史渠道模型可能已经保存了合法但过时的能力 JSON，例如 Gemini/GPT/
		// Claude/DeepSeek 的参考图上限被写成 0。沿用统一规范化逻辑只补缺失的
		// 多模态边界和默认字段，不触碰价格、启用状态或其它模型业务字段。
		config, decodeErr := DecodeModelCapabilityConfig(item.CapabilityConfigJSON)
		if decodeErr == nil && config != nil {
			normalized, normalizeErr := NormalizeModelCapabilityConfigForModel(item.Capability, string(item.Protocol), firstNonEmpty(item.ProviderModelKey, item.ModelKey), config)
			if normalizeErr == nil && normalized != nil {
				encoded, encodeErr := json.Marshal(normalized)
				if encodeErr != nil {
					return false, encodeErr
				}
				if string(encoded) != item.CapabilityConfigJSON {
					item.CapabilityConfigJSON = string(encoded)
					item.CapabilityVersion++
					changed = true
				}
			}
		}
	}
	return changed, nil
}

func officialModelCapabilityConfig(entry officialModelCatalogEntry) (*ModelCapabilityConfig, error) {
	capability := normalizeCapability(entry.Capability)
	switch capability {
	case "text":
		streaming := true
		return &ModelCapabilityConfig{Version: 1, Text: &TextCapabilityConfig{
			Streaming:  &streaming,
			References: TextReferenceConfig{PromptMaxChars: 32000, MaxImages: entry.MaxImages, MaxImageBytes: entry.MaxImageBytes, MaxVideos: entry.MaxVideos, MaxVideoBytes: entry.MaxVideoBytes},
		}}, nil
	case "image":
		config := DefaultModelCapabilityConfigForModel(entry.Protocol, entry.ModelKey)
		if config == nil || config.Image == nil {
			return nil, fmt.Errorf("无法生成图片能力配置")
		}
		config.Text = nil
		config.Video = nil
		if entry.MaxImages > 0 {
			config.Image.References.MaxImages = entry.MaxImages
		}
		if entry.MaxImageBytes > 0 {
			config.Image.References.MaxImageBytes = entry.MaxImageBytes
		}
		return config, nil
	default:
		return nil, fmt.Errorf("不支持的官方模型能力 %q", entry.Capability)
	}
}

func officialCatalogEntryMatchesChannel(entry officialModelCatalogEntry, channel *model.ModelChannel, existing []model.ChannelModel) bool {
	if channel == nil {
		return false
	}
	entryKey := catalogModelKey(entry.ModelKey)
	for _, item := range existing {
		if catalogModelKey(item.ModelKey) == entryKey || catalogModelKey(item.ProviderModelKey) == entryKey {
			return true
		}
	}
	baseURL := strings.TrimSpace(channel.BaseURL)
	host := ""
	if parsed, err := url.Parse(baseURL); err == nil {
		host = strings.ToLower(parsed.Hostname())
	}
	name := strings.ToLower(strings.TrimSpace(channel.Name) + " " + strings.TrimSpace(channel.PublicAlias))
	apiFormat := strings.ToLower(strings.TrimSpace(channel.APIFormat))
	if hasConflictingProviderHint(entry.Provider, host+" "+name) {
		return false
	}
	for _, token := range entry.Match.Hosts {
		if strings.Contains(host, strings.ToLower(strings.TrimSpace(token))) {
			return true
		}
	}
	for _, token := range entry.Match.NameTokens {
		if strings.Contains(name, strings.ToLower(strings.TrimSpace(token))) {
			return true
		}
	}
	for _, token := range entry.Match.APIFormats {
		if apiFormat == strings.ToLower(strings.TrimSpace(token)) && !hasKnownProviderHint(host+" "+name) {
			return true
		}
	}
	return false
}

func hasKnownProviderHint(value string) bool {
	value = strings.ToLower(value)
	for _, token := range []string{"openai", "gpt", "google", "gemini", "vertex", "deepseek", "anthropic", "claude"} {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

func hasConflictingProviderHint(provider, value string) bool {
	value = strings.ToLower(value)
	for _, token := range []string{"openai", "gpt", "google", "gemini", "vertex", "deepseek", "anthropic", "claude"} {
		if token == provider || (provider == "google" && (token == "gemini" || token == "vertex")) || (provider == "openai" && token == "gpt") || (provider == "anthropic" && token == "claude") {
			continue
		}
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

func catalogModelKey(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "models/"))
}
