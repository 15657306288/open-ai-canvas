package videoparser

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// yt-dlp 的出口代理必须拒绝内网 / 回环地址与非 80/443 端口（默认出站策略，不放开私网）。
func TestEgressProxyBlocksPrivateTargetsAndOddPorts(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "false")
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "")
	svc := New(Options{}, nil)
	t.Cleanup(svc.Close)
	proxyURL, err := svc.egressURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(proxyURL)
	if host, _, _ := net.SplitHostPort(parsed.Host); host != "127.0.0.1" {
		t.Fatalf("egress must listen on loopback only: %s", parsed.Host)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(parsed)}}
	for _, target := range []string{"https://127.0.0.1/", "https://10.0.0.8/", "https://169.254.169.254/latest/meta-data/", "http://192.168.1.1/", "https://localhost/", "https://example.com:8443/"} {
		resp, err := client.Get(target)
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code != http.StatusBadGateway && code != http.StatusBadRequest {
				t.Fatalf("%s reached through egress proxy: %d", target, code)
			}
		}
	}
	// 直接调用拨号策略：回环地址解析失败即视为拦截。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := svc.opts.ResolveHost(ctx, "127.0.0.1"); err == nil {
		t.Fatal("loopback must be rejected by outbound policy")
	}
}

func TestPageClientRejectsRedirectOutsidePlatformDomains(t *testing.T) {
	m := newMockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "b23.tv":
			http.Redirect(w, r, "https://metadata.internal.example/", http.StatusFound)
		case "v.kuaishou.com":
			http.Redirect(w, r, "https://v.kuaishou.com:8080/x", http.StatusFound)
		default:
			t.Errorf("unexpected request to %s", r.Host)
		}
	})
	svc := New(m.options(), nil)
	for _, text := range []string{"https://b23.tv/AbCdEf9", "https://v.kuaishou.com/2Bd8Xx"} {
		link, err := svc.Identify(text)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.expand(context.Background(), link); AsError(err).Code != ErrBlocked {
			t.Fatalf("%s => %v, want blocked", text, err)
		}
	}
}
