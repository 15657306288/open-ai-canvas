package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"infinite-canvas/backend/internal/model"
)

func TestChannelFromRequestStoresConnectionWithoutDefaultProtocol(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	channel, err := channelFromRequest(ChannelRequest{
		Name:             "混合模型渠道",
		BaseURL:          server.URL + "/v1",
		APIKey:           "access-key",
		SecretKey:        "secret-key",
		ConcurrencyLimit: intPtr(6),
		Models:           []string{"seedance-2.0"},
	}, model.ModelChannel{})
	if err != nil {
		t.Fatalf("channelFromRequest() error = %v", err)
	}
	if channel.APIFormat != "openai" {
		t.Fatalf("APIFormat = %q, want openai", channel.APIFormat)
	}
	if channel.ConcurrencyLimit != 6 {
		t.Fatalf("ConcurrencyLimit = %d, want 6", channel.ConcurrencyLimit)
	}
	if channel.APIKey != "access-key" || channel.SecretKey != "secret-key" {
		t.Fatal("channel credentials were not stored")
	}
}

func TestMergeChannelRequestSupportsEnabledOnlyPatch(t *testing.T) {
	enabled := false
	req := mergeChannelRequest(ChannelRequest{Enabled: &enabled}, model.ModelChannel{
		Name:        "Video",
		BaseURL:     "https://example.com/v1",
		APIFormat:   "openai",
		ModelsJSON:  `["custom-video"]`,
		HeadersJSON: `[{"name":"User-Agent","value":"Stored Agent"}]`,
	})
	if req.Name != "Video" || req.BaseURL != "https://example.com/v1" || len(req.Models) != 1 || len(req.Headers) != 1 {
		t.Fatalf("mergeChannelRequest() = %#v", req)
	}
}

func TestChannelFromRequestStoresAndClearsHeaders(t *testing.T) {
	request := ChannelRequest{Name: "Headers", BaseURL: "https://example.com/v1", Headers: []OutboundHeader{{Name: "User-Agent", Value: "Custom Agent"}}}
	channel, err := channelFromRequest(request, model.ModelChannel{})
	if err != nil {
		t.Fatal(err)
	}
	if channel.HeadersJSON != `[{"name":"User-Agent","value":"Custom Agent"}]` {
		t.Fatalf("HeadersJSON = %q", channel.HeadersJSON)
	}

	request.Headers = []OutboundHeader{}
	channel, err = channelFromRequest(request, channel)
	if err != nil {
		t.Fatal(err)
	}
	if channel.HeadersJSON != `[]` {
		t.Fatalf("cleared HeadersJSON = %q", channel.HeadersJSON)
	}
}

func TestPublicChannelOnlyReturnsSystemHeadersToAdmin(t *testing.T) {
	channel := model.ModelChannel{ID: "system-1", Scope: model.ChannelScopeSystem, BaseURL: "https://example.com/v1", HeadersJSON: `[{"name":"X-Gateway-Tenant","value":"tenant-a"}]`}
	adminView := publicChannel(channel, true, nil)
	if len(adminView.Headers) != 1 || adminView.Headers[0].Name != "X-Gateway-Tenant" {
		t.Fatalf("admin headers = %#v", adminView.Headers)
	}
	userView := publicChannel(channel, false, nil)
	if len(userView.Headers) != 0 {
		t.Fatalf("user headers = %#v", userView.Headers)
	}
}

func TestChannelFromRequestRejectsInvalidConcurrencyLimit(t *testing.T) {
	for _, limit := range []int{0, 1000} {
		_, err := channelFromRequest(ChannelRequest{Name: "Bad", BaseURL: "https://example.com/v1", ConcurrencyLimit: &limit}, model.ModelChannel{})
		if err == nil {
			t.Fatalf("channelFromRequest() concurrencyLimit = %d, error = nil", limit)
		}
	}
}

func TestRuntimeConcurrencyUsesEnvironmentFallback(t *testing.T) {
	t.Setenv("CANVAS_CHANNEL_CONCURRENCY", "7")
	t.Setenv("CANVAS_WORKER_CONCURRENCY", "9")
	setting := defaultRuntimePolicy().Task
	if setting.ChannelConcurrency != 7 || setting.WorkerConcurrency != 9 {
		t.Fatalf("runtimeConcurrencyFromEnvironment() = %#v", setting)
	}

	useGlobal := true
	channel, err := channelFromRequest(ChannelRequest{Name: "Global", BaseURL: "https://example.com/v1", UseGlobalConcurrency: &useGlobal}, model.ModelChannel{ConcurrencyLimit: 4})
	if err != nil || channel.ConcurrencyLimit != 0 {
		t.Fatalf("global concurrency channel = %#v, error = %v", channel, err)
	}
}

func intPtr(value int) *int { return &value }
