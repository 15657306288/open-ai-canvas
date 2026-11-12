package videoparser

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func stubResult(url string) func() (*Result, error) {
	return func() (*Result, error) {
		return &Result{Meta: Metadata{Title: "stub"}, Media: []MediaAsset{{Kind: MediaMuxed, URL: url, Ext: "mp4", Width: 720, Height: 1280}}}, nil
	}
}

func stubFailure(code ErrorCode) func() (*Result, error) {
	return func() (*Result, error) { return nil, newError(code, "stub failure "+string(code), nil) }
}

// bilibili 链接走默认注册表，但供应商换成可编排的 stub，验证调度中心本身的行为。
func stubService(t *testing.T, opts Options, providers ...*stubProvider) (*Service, *Link) {
	t.Helper()
	names := make([]string, 0, len(providers))
	list := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		names = append(names, provider.name)
		list = append(list, provider)
	}
	opts.Priority = map[string][]string{PlatformBilibili: names}
	svc := New(opts, nil, list...)
	link, err := svc.Identify("https://www.bilibili.com/video/BV1GJ411x7h7")
	if err != nil {
		t.Fatal(err)
	}
	return svc, link
}

func TestParseRetriesRetryableErrorOnce(t *testing.T) {
	flaky := &stubProvider{name: "flaky", results: []func() (*Result, error){stubFailure(ErrUpstream), stubResult("https://cdn.bilivideo.com/a.mp4")}}
	svc, link := stubService(t, Options{}, flaky)
	result, err := svc.Parse(context.Background(), link, nil)
	if err != nil || result.Provider != "flaky" || flaky.callCount() != 2 {
		t.Fatalf("retry => %+v %v calls=%d", result, err, flaky.callCount())
	}
	health := svc.Platforms(true)
	for _, status := range health {
		if status.Key == PlatformBilibili {
			if status.Health[0].Attempts != 2 || status.Health[0].Successes != 1 || status.Health[0].Failures != 1 || status.Usage.ParseSuccesses != 1 {
				t.Fatalf("health = %+v usage=%+v", status.Health, status.Usage)
			}
		}
	}
}

func TestParseFallsBackToNextProviderAndCountsCost(t *testing.T) {
	broken := &stubProvider{name: "broken", results: []func() (*Result, error){stubFailure(ErrNeedsLogin)}}
	paid := &stubProvider{name: "paid-api", results: []func() (*Result, error){stubResult("https://cdn.bilivideo.com/b.mp4")}}
	svc, link := stubService(t, Options{ProviderCostMicros: map[string]int64{"paid-api": 2500}}, broken, paid)
	result, err := svc.Parse(context.Background(), link, nil)
	if err != nil || result.Provider != "paid-api" || broken.callCount() != 1 {
		t.Fatalf("fallback => %+v %v broken=%d", result, err, broken.callCount())
	}
	for _, status := range svc.Platforms(true) {
		if status.Key == PlatformBilibili && status.Health[1].CostMicros != 2500 {
			t.Fatalf("cost not recorded: %+v", status.Health)
		}
	}
}

func TestParseStopsOnTerminalContentError(t *testing.T) {
	first := &stubProvider{name: "first", results: []func() (*Result, error){stubFailure(ErrNotVideo)}}
	second := &stubProvider{name: "second", results: []func() (*Result, error){stubResult("https://cdn.bilivideo.com/c.mp4")}}
	svc, link := stubService(t, Options{}, first, second)
	if _, err := svc.Parse(context.Background(), link, nil); AsError(err).Code != ErrNotVideo || second.callCount() != 0 {
		t.Fatalf("terminal error must stop fallback: %v second=%d", err, second.callCount())
	}
}

func TestParseKeepsMostUsefulErrorAcrossProviders(t *testing.T) {
	first := &stubProvider{name: "first", results: []func() (*Result, error){stubFailure(ErrNotFound)}}
	second := &stubProvider{name: "second", results: []func() (*Result, error){stubFailure(ErrUnsupported)}}
	svc, link := stubService(t, Options{}, first, second)
	if _, err := svc.Parse(context.Background(), link, nil); AsError(err).Code != ErrNotFound || AsError(err).Provider != "first" {
		t.Fatalf("expected not_found from first provider, got %v", err)
	}
}

func TestCircuitBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	down := &stubProvider{name: "down", results: []func() (*Result, error){stubFailure(ErrUnsupported)}}
	svc, link := stubService(t, Options{}, down)
	for i := 0; i < circuitFailureThreshold; i++ {
		svc.cache.Clear()
		if _, err := svc.Parse(context.Background(), link, nil); err == nil {
			t.Fatal("expected failure")
		}
	}
	calls := down.callCount()
	svc.cache.Clear()
	_, err := svc.Parse(context.Background(), link, nil)
	if parsed := AsError(err); parsed.Code != ErrUnavailable || !strings.Contains(parsed.Message, "熔断") || down.callCount() != calls {
		t.Fatalf("open circuit must skip provider: %v calls=%d/%d", err, down.callCount(), calls)
	}
	// 冷却结束后放行试探。
	svc.stats.now = func() time.Time { return time.Now().Add(circuitOpenDuration + time.Second) }
	svc.cache.Clear()
	_, _ = svc.Parse(context.Background(), link, nil)
	if down.callCount() != calls+1 {
		t.Fatalf("half-open probe not attempted: %d", down.callCount())
	}
}

func TestParseResultIsCachedPerWork(t *testing.T) {
	provider := &stubProvider{name: "once", results: []func() (*Result, error){stubResult("https://cdn.bilivideo.com/d.mp4")}}
	svc, link := stubService(t, Options{}, provider)
	first, err := svc.Parse(context.Background(), link, nil)
	if err != nil || first.Cached {
		t.Fatalf("first parse = %+v %v", first, err)
	}
	same, _ := svc.Identify("https://www.bilibili.com/video/BV1GJ411x7h7/?spm_id_from=333")
	second, err := svc.Parse(context.Background(), same, nil)
	if err != nil || !second.Cached || provider.callCount() != 1 {
		t.Fatalf("second parse = %+v %v calls=%d", second, err, provider.callCount())
	}
	second.Media[0].URL = "mutated"
	third, _ := svc.Parse(context.Background(), same, nil)
	if third.Media[0].URL == "mutated" {
		t.Fatal("cached result must be copied, not shared")
	}
}

func TestConcurrencyLimitReturnsBusy(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	slow := &stubProvider{name: "slow", results: []func() (*Result, error){func() (*Result, error) {
		started <- struct{}{}
		<-release
		return stubResult("https://cdn.bilivideo.com/e.mp4")()
	}}}
	svc, link := stubService(t, Options{Concurrency: 1, PlatformConcurrency: 1, QueueTimeout: 150 * time.Millisecond}, slow)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = svc.Parse(context.Background(), link, nil)
	}()
	<-started
	other, _ := svc.Identify("https://www.bilibili.com/video/BV13x41117TL")
	if _, err := svc.Parse(context.Background(), other, nil); AsError(err).Code != ErrBusy || !AsError(err).Retryable {
		t.Fatalf("expected busy, got %v", err)
	}
	close(release)
	wg.Wait()
}

func TestParseRejectsOverlongVideoFromMetadata(t *testing.T) {
	long := &stubProvider{name: "long", results: []func() (*Result, error){func() (*Result, error) {
		result, _ := stubResult("https://cdn.bilivideo.com/f.mp4")()
		result.Meta.DurationMs = 11 * 60 * 1000
		return result, nil
	}}}
	svc, link := stubService(t, Options{}, long)
	if _, err := svc.Parse(context.Background(), link, nil); AsError(err).Code != ErrTooLong {
		t.Fatalf("expected too_long, got %v", err)
	}
}

func TestDownloadEnforcesSizeLimitAndMediaType(t *testing.T) {
	m := newMockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big.mp4":
			_, _ = w.Write(fakeMP4(4 << 10))
		case "/page.mp4":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>login</html>"))
		case "/disguised.mp4":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("<!DOCTYPE html><html>login required</html>"))
		default:
			http.NotFound(w, r)
		}
	})
	opts := m.options()
	opts.MaxBytes = 1 << 10
	svc := New(opts, nil)
	result := func(path string) *Result {
		return &Result{Meta: Metadata{Platform: PlatformBilibili, PlatformLabel: "哔哩哔哩"}, Media: []MediaAsset{{Kind: MediaMuxed, URL: "https://upos.bilivideo.com" + path, Ext: "mp4"}}}
	}
	cases := map[string]ErrorCode{"/big.mp4": ErrTooLarge, "/page.mp4": ErrMedia, "/missing.mp4": ErrNotFound}
	for path, want := range cases {
		if _, err := svc.Download(context.Background(), result(path), t.TempDir(), nil); AsError(err).Code != want {
			t.Fatalf("%s => %v, want %s", path, err, want)
		}
	}
	opts.MaxBytes = 1 << 20
	svc = New(opts, nil)
	if _, err := svc.Download(context.Background(), result("/disguised.mp4"), t.TempDir(), nil); AsError(err).Code != ErrMedia {
		t.Fatalf("html body must be rejected even with binary content type: %v", err)
	}
	for _, raw := range []string{"ftp://upos.bilivideo.com/a.mp4", "https://upos.bilivideo.com:22/a.mp4", "https://user:pw@upos.bilivideo.com/a.mp4"} {
		blocked := &Result{Meta: Metadata{Platform: PlatformBilibili}, Media: []MediaAsset{{Kind: MediaMuxed, URL: raw}}}
		if _, err := svc.Download(context.Background(), blocked, t.TempDir(), nil); AsError(err).Code != ErrBlocked {
			t.Fatalf("%s must be blocked: %v", raw, err)
		}
	}
	// B 站 PCDN 等平台 CDN 使用 1024 以上的非标准端口，形状校验放行，地址仍由出站拨号器限制为公网。
	for _, raw := range []string{"https://xy1x2x3x4xy.mcdn.bilivideo.cn:4483/a.m4s", "https://upos-sz-mirrorcos.bilivideo.com:8082/a.m4s"} {
		u, _ := url.Parse(raw)
		if err := checkMediaShape(u); err != nil {
			t.Fatalf("%s should pass shape check: %v", raw, err)
		}
	}
	usage := svc.stats.platformUsage(PlatformBilibili)
	if usage.Downloads == 0 || usage.DownloadSuccesses != 0 {
		t.Fatalf("download usage = %+v", usage)
	}
}

func TestDownloadReportsProgressAndRemuxesToMP4(t *testing.T) {
	media := makeMedia(t, "muxed", 4)
	m := newMockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(media)))
		_, _ = w.Write(media)
	})
	svc := New(m.options(), nil)
	result := &Result{Meta: Metadata{Platform: PlatformKuaishou, PlatformLabel: "快手"}, Media: []MediaAsset{{Kind: MediaMuxed, URL: "https://txmov2.a.kwimgs.com/a.mp4", SizeBytes: int64(len(media))}}}
	var stages []Stage
	file, err := svc.Download(context.Background(), result, t.TempDir(), func(stage Stage) { stages = append(stages, stage) })
	if err != nil {
		t.Fatal(err)
	}
	if !file.Remuxed || len(stages) < 2 || stages[0].Name != "downloading" || stages[len(stages)-1].Name != "merging" {
		t.Fatalf("stages = %+v file=%+v", stages, file)
	}
	assertStreams(t, file.Path, true, true)
	usage := svc.stats.platformUsage(PlatformKuaishou)
	if usage.DownloadSuccesses != 1 || usage.DownloadedBytes != int64(len(media)) {
		t.Fatalf("usage = %+v", usage)
	}
}
