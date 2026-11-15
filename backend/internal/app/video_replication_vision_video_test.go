package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVideoReplicationFullVideoProtocolRequests(t *testing.T) {
	for _, wire := range []string{"gemini-multimodal-text", "apimart-gemini-video-text"} {
		t.Run(wire, func(t *testing.T) {
			svc, _ := mockEcommerceAgentService(t)
			t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				encoded, _ := json.Marshal(body)
				if wire == "gemini-multimodal-text" {
					if r.URL.Path != "/v1beta/models/gemini-test:generateContent" || r.Header.Get("x-goog-api-key") != "fixture-key" || !strings.Contains(string(encoded), `"mimeType":"video/mp4"`) || !strings.Contains(string(encoded), `"data":"dmlkZW8="`) || strings.Contains(string(encoded), `"image_url"`) {
						t.Errorf("native video request: %s %s", r.URL.Path, encoded)
					}
					_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"ok\":true}"}]}}]}`))
				} else {
					if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" || !strings.Contains(string(encoded), `"video_url":{"url":"https://media.example/full.mp4"}`) {
						t.Errorf("compatible video request: %s %s", r.URL.Path, encoded)
					}
					_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`))
				}
			}))
			defer upstream.Close()
			cfg := ecommerceProviderConfig{TextTimeout: time.Second * 5, TextProvider: &providerConfig{InterfaceType: wire, BaseURL: upstream.URL, Model: "gemini-test", APIKey: "fixture-key"}}
			video := "data:video/mp4;base64,dmlkZW8="
			if wire == "apimart-gemini-video-text" {
				video = "https://media.example/full.mp4"
			}
			var result map[string]any
			if err := svc.ecommerceMultimodalJSON(context.Background(), cfg, "test", "system", "watch", nil, []string{video}, &result); err != nil || result["ok"] != true || calls != 1 {
				t.Fatalf("result=%v calls=%d error=%v", result, calls, err)
			}
		})
	}
}

func TestVideoReplicationOpenAIGeminiMultimodalUsesInlineMedia(t *testing.T) {
	cfg := ecommerceProviderConfig{TextProvider: &providerConfig{InterfaceType: "openai-gemini-multimodal"}}
	if err := validateVideoReplicationVisionProtocol(cfg); err != nil || !videoReplicationUsesInlineMedia(cfg) {
		t.Fatalf("validate=%v inline=%v", err, videoReplicationUsesInlineMedia(cfg))
	}
}

// 视频理解渠道不再按 Gemini 协议名放行：只按模型声明的多模态能力判断是否可用，
// 并按能力选择素材形态（完整视频 / 内嵌画面+音轨 / 抽帧画面）。
func TestVideoReplicationVisionFollowsModelCapability(t *testing.T) {
	profile := func(images, videos int) *ModelCapabilityConfig {
		return &ModelCapabilityConfig{Text: &TextCapabilityConfig{References: TextReferenceConfig{MaxImages: images, MaxVideos: videos}}}
	}
	cases := []struct {
		name     string
		wire     string
		profile  *ModelCapabilityConfig
		wantMode string
		wantErr  bool
	}{
		{"智能路由组多模态", "magpie-chat", profile(8, 0), "frames", false},
		{"通用chat多模态", "chat-completion", profile(3, 0), "frames", false},
		{"声明支持视频输入", "chat-completion", profile(8, 1), "video", false},
		{"原生Gemini", "gemini-multimodal-text", profile(0, 0), "video", false},
		{"内联Gemini兼容", "openai-gemini-multimodal", profile(8, 0), "inline", false},
		{"旧渠道未声明能力", "chat-completion", nil, "frames", false},
		{"纯文本模型", "chat-completion", profile(0, 0), "", true},
		{"未配置模型", "chat-completion", nil, "", true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			cfg := ecommerceProviderConfig{}
			if item.name != "未配置模型" {
				cfg.TextProvider = &providerConfig{InterfaceType: item.wire, CapabilityConfig: item.profile}
			}
			err := validateVideoReplicationVisionProtocol(cfg)
			if (err != nil) != item.wantErr {
				t.Fatalf("validate error=%v wantErr=%v", err, item.wantErr)
			}
			if err == nil {
				if got := videoReplicationVisionInputMode(cfg); got != item.wantMode {
					t.Fatalf("mode=%s want=%s", got, item.wantMode)
				}
			}
		})
	}
}

func TestVideoReplicationVisionFrameBudgetRespectsImageLimit(t *testing.T) {
	cfg := ecommerceProviderConfig{TextProvider: &providerConfig{InterfaceType: "magpie-chat", CapabilityConfig: &ModelCapabilityConfig{
		Text: &TextCapabilityConfig{References: TextReferenceConfig{MaxImages: 8}},
	}}}
	perVideo, total := videoReplicationVisionFrameBudget(cfg, 2)
	if perVideo != 4 || total != 8 {
		t.Fatalf("perVideo=%d total=%d want 4/8", perVideo, total)
	}
}

func TestVideoReplicationFullVideoPreservesTimelineAndAudio(t *testing.T) {
	bin := requireVideoReplicationFFmpeg(t)
	for _, audio := range []bool{true, false} {
		dir := t.TempDir()
		source := filepath.Join(dir, "input.mp4")
		makeReplicationClip(t, bin, source, 2, 2, audio)
		probe, err := probeVideoReplicationFile(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		data, err := prepareVideoReplicationVisionVideo(context.Background(), bin, source, probe, 3<<20)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(data, "data:video/mp4;base64,"))
		if err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(dir, "normalized.mp4")
		if err := os.WriteFile(output, raw, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := probeVideoReplicationFile(context.Background(), output)
		if err != nil || got.DurationMs < 3900 || got.HasAudio != audio || len(raw) > 3<<20 {
			t.Fatalf("probe=%+v size=%d error=%v", got, len(raw), err)
		}
		cuts, err := detectVideoReplicationCuts(context.Background(), bin, output)
		if err != nil || len(cuts) == 0 || cuts[0] < 1800 || cuts[0] > 2200 {
			t.Fatalf("temporal cut lost: %v %v", cuts, err)
		}
	}
}
