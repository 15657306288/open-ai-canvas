package app

import (
	"strings"
	"testing"
)

// 账号池渠道（doubao-pool / dola-pool）是内置渠道：渠道行与模型行都不落库，目录读模型由
// accountPoolChannelCatalog 合成（见 doubao_pool_catalog_test.go），写路径则在这里分流：
// resolveSystemChannelModelSelection → resolveDoubaoPoolModelSelection。
//
// 本文件把写路径的两条强校验钉成可执行事实：
//  1. 客户端只能给「渠道 + 模型键」，协议、provider 模型名、价格档、凭据一律由服务端重建；
//     客户端塞进来的 baseUrl / apiKey / secretKey / headers / capabilityConfig 必须被丢弃，
//     否则内置渠道会变成「用用户自己的凭据和地址」的绕过通道。
//  2. 模型键必须与渠道配套、且与任务能力匹配。执行层不会为配错的选择报错：
//     runDoubaoPoolImageTask 只按模型键判断是否拆层（其余一律普通出图），
//     runDoubaoPoolVideoTask 会按站点把模型名再映射一次（accountPoolVideoVariant），
//     所以配错只会静默跑成另一个模型，必须在校验阶段拒绝。

// resolveAccountPoolSelection 走一遍账号池任务准入，返回重建后的 config。
func resolveAccountPoolSelection(t *testing.T, taskType string, config map[string]any) (map[string]any, error) {
	t.Helper()
	resolved, err := (&Service{}).resolveSystemChannelModelSelection(map[string]any{"config": config}, taskType, "")
	if err != nil {
		return nil, err
	}
	rebuilt, ok := resolved["config"].(map[string]any)
	if !ok {
		t.Fatalf("准入结果缺少 config：%#v", resolved)
	}
	return rebuilt, nil
}

func TestAccountPoolAdmissionRebuildsProviderSelection(t *testing.T) {
	config, err := resolveAccountPoolSelection(t, "canvas_image", map[string]any{
		"channelId":        DoubaoPoolChannelID,
		"model":            "models/" + doubaoPoolLayerModel,
		"interfaceType":    "chat-completion",
		"apiFormat":        "gemini",
		"baseUrl":          "https://attacker.example.com/v1",
		"apiKey":           "client-api-key",
		"secretKey":        "client-secret-key",
		"headers":          map[string]any{"X-Forwarded-Host": "attacker.example.com"},
		"priceTierId":      "client-tier",
		"providerModelKey": "attacker-model",
		"capabilityConfig": map[string]any{"version": 1},
		"ratio":            "16:9",
		"prompt":           "把这张图拆成 3 层",
	})
	if err != nil {
		t.Fatalf("账号池准入被拒绝：%v", err)
	}

	if config["channelId"] != DoubaoPoolChannelID || config["model"] != doubaoPoolLayerModel {
		t.Fatalf("渠道与模型键必须原样保留：%#v", config)
	}
	if config["interfaceType"] != DoubaoPoolInterfaceType || config["apiFormat"] != "openai" {
		t.Fatalf("请求协议必须由服务端重建：%#v", config)
	}
	if config["channelModelKey"] != doubaoPoolLayerModel || config["providerModelKey"] != doubaoPoolLayerModel {
		t.Fatalf("provider 模型名必须回填为模型键本身：%#v", config)
	}
	if config["priceTierId"] != "" {
		t.Fatalf("账号池没有价格档，必须清空：%#v", config["priceTierId"])
	}
	for _, key := range []string{"baseUrl", "apiKey", "secretKey", "headers", "capabilityConfig"} {
		if _, exists := config[key]; exists {
			t.Fatalf("客户端提供的 %s 必须被丢弃：%#v", key, config)
		}
	}
	// 准入只重建 provider 路由字段，创作参数必须原样保留。
	if config["ratio"] != "16:9" || strings.TrimSpace(stringValue(config["prompt"])) == "" {
		t.Fatalf("创作参数不应被准入丢弃：%#v", config)
	}
}

func TestAccountPoolAdmissionRejectsMismatchedSelections(t *testing.T) {
	cases := []struct {
		name      string
		taskType  string
		channelID string
		model     string
		wantError string
	}{
		{name: "未登记的模型键", taskType: "canvas_image", channelID: DoubaoPoolChannelID, model: "doubao-seedream-4-0-250828", wantError: "账号池渠道不支持模型"},
		{name: "视频模型进图片任务", taskType: "canvas_image", channelID: DoubaoPoolChannelID, model: "doubao-seedance-video", wantError: "所选模型与任务能力不匹配"},
		{name: "图片模型进视频任务", taskType: "canvas_video", channelID: DoubaoPoolChannelID, model: doubaoPoolImageModel, wantError: "所选模型与任务能力不匹配"},
		{name: "video_ 前缀任务按视频能力校验", taskType: "video_seedance", channelID: DoubaoPoolChannelID, model: doubaoPoolImageModel, wantError: "所选模型与任务能力不匹配"},
		{name: "Dola 站模型配豆包池", taskType: "canvas_video", channelID: DoubaoPoolChannelID, model: dolaPoolVideoModel25, wantError: "Dola 站模型必须选择 Dola 账号池渠道"},
		{name: "豆包模型配 Dola 池", taskType: "canvas_video", channelID: DolaPoolChannelID, model: "doubao-seedance-video", wantError: "Dola 账号池只支持 dola- 前缀的模型"},
		{name: "图片模型配 Dola 池", taskType: "canvas_image", channelID: DolaPoolChannelID, model: doubaoPoolImageModel, wantError: "Dola 账号池只支持 dola- 前缀的模型"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveAccountPoolSelection(t, tc.taskType, map[string]any{"channelId": tc.channelID, "model": tc.model})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("resolveDoubaoPoolModelSelection(%s, %s) err = %v，期望包含 %q", tc.channelID, tc.model, err, tc.wantError)
			}
		})
	}
}

// 配套的合法组合必须放行，且渠道 ID 原样保留：执行层按它锁定取号站点。
func TestAccountPoolAdmissionKeepsChannelForLegalSelection(t *testing.T) {
	cases := []struct {
		name      string
		taskType  string
		channelID string
		model     string
	}{
		{name: "豆包池文生图", taskType: "canvas_image", channelID: DoubaoPoolChannelID, model: doubaoPoolImageModel},
		{name: "豆包池图层拆分", taskType: "canvas_image", channelID: DoubaoPoolChannelID, model: doubaoPoolLayerModel},
		{name: "豆包池视频", taskType: "canvas_video", channelID: DoubaoPoolChannelID, model: "doubao-seedance-video"},
		{name: "Dola 池视频 2.5", taskType: "canvas_video", channelID: DolaPoolChannelID, model: dolaPoolVideoModel25},
		{name: "Dola 池快速档视频", taskType: "video_fast", channelID: DolaPoolChannelID, model: dolaPoolVideoModelFast},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := resolveAccountPoolSelection(t, tc.taskType, map[string]any{"channelId": tc.channelID, "model": tc.model})
			if err != nil {
				t.Fatalf("合法组合被拒绝：%v", err)
			}
			if config["channelId"] != tc.channelID || config["interfaceType"] != DoubaoPoolInterfaceType {
				t.Fatalf("准入结果不完整：%#v", config)
			}
		})
	}
}

// 报价路径与建单路径同一口径：账号池没有渠道模型行与价格档，报价直接返回空（前端用 priceLabel 展示），
// 不再让用户每次选中账号池模型都吃一次「当前模型暂时不可用」。准入校验仍然前置，非法选择依旧报错。
func TestAccountPoolQuoteSkipsCreditOrder(t *testing.T) {
	svc, _ := newChannelModelTestService(t)
	for _, tc := range []struct {
		name       string
		channelID  string
		modelKey   string
		capability string
		operation  string
	}{
		{name: "豆包池图片", channelID: DoubaoPoolChannelID, modelKey: doubaoPoolImageModel, capability: "image", operation: "text_to_image"},
		{name: "豆包池视频", channelID: DoubaoPoolChannelID, modelKey: "doubao-seedance-video", capability: "video", operation: "text_to_video"},
		{name: "Dola 池视频", channelID: DolaPoolChannelID, modelKey: dolaPoolVideoModel25, capability: "video", operation: "text_to_video"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quote, err := svc.QuoteChannelModel(ChannelModelQuoteRequest{
				ChannelID: tc.channelID,
				ModelKey:  tc.modelKey,
				Intent: ModelRequestIntent{
					Capability: tc.capability,
					Operation:  tc.operation,
					Inputs:     map[string]int{"image": 0, "video": 0, "audio": 0},
					Options:    map[string]any{"videoSeconds": 5},
				},
			})
			if err != nil {
				t.Fatalf("账号池报价不应失败：%v", err)
			}
			if quote != nil {
				t.Fatalf("账号池没有价格档，不应产生积分报价：%#v", quote)
			}
		})
	}
	for _, tc := range []struct {
		name       string
		channelID  string
		modelKey   string
		capability string
		wantError  string
	}{
		{name: "视频模型报图片价", channelID: DoubaoPoolChannelID, modelKey: "doubao-seedance-video", capability: "image", wantError: "所选模型与任务能力不匹配"},
		{name: "跨池模型键", channelID: DolaPoolChannelID, modelKey: "doubao-seedance-video", capability: "video", wantError: "Dola 账号池只支持 dola- 前缀的模型"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.QuoteChannelModel(ChannelModelQuoteRequest{
				ChannelID: tc.channelID,
				ModelKey:  tc.modelKey,
				Intent: ModelRequestIntent{
					Capability: tc.capability,
					Operation:  "text_to_video",
					Inputs:     map[string]int{"image": 0, "video": 0, "audio": 0},
				},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("报价准入应拒绝：err = %v，期望包含 %q", err, tc.wantError)
			}
		})
	}
}
