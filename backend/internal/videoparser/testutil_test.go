package videoparser

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// mockUpstream 是 CI 用的假上游：任何平台域名的 https 请求都被拨到本地 TLS 测试服务器，由 handler 按 Host + Path 返回固定数据。
// 它只替换网络出口，解析、域名白名单、重定向校验、下载与合并逻辑都走真实代码。
type mockUpstream struct {
	server *httptest.Server
	plain  *httptest.Server
	mu     sync.Mutex
	hits   map[string]int
}

// newMockUpstream 同时提供 https（TLS）与 http 两个本地服务器，平台分享文案里常见 http:// 短链。
func newMockUpstream(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *mockUpstream {
	t.Helper()
	m := &mockUpstream{hits: map[string]int{}}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.hits[r.Host+r.URL.Path]++
		m.mu.Unlock()
		handler(w, r)
	})
	m.server = httptest.NewTLSServer(wrapped)
	m.plain = httptest.NewServer(wrapped)
	t.Cleanup(m.server.Close)
	t.Cleanup(m.plain.Close)
	return m
}

func (m *mockUpstream) count(hostPath string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits[hostPath]
}

func (m *mockUpstream) client(timeout time.Duration, _ string) *http.Client {
	tlsAddr, plainAddr := m.server.Listener.Addr().String(), m.plain.Listener.Addr().String()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, plainAddr)
		},
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(address)
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, tlsAddr)
			if err != nil {
				return nil, err
			}
			// 仅测试：本地自签证书。
			client := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: host}) //nolint:gosec
			if err := client.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return client, nil
		},
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

// options 让单元测试永远不会启动真实的 yt-dlp：可执行路径是假的，执行器是 fakeRunner。
func (m *mockUpstream) options() Options {
	return Options{HTTPClient: m.client, QueueTimeout: 2 * time.Second, PageTimeout: 5 * time.Second, ProviderTimeout: 5 * time.Second, DownloadTimeout: 20 * time.Second,
		FFmpegPath: ffmpegPath(), YTDLPPath: "/fake/yt-dlp", Runner: &fakeRunner{}, NoBrowser: true}
}

func ffmpegPath() string {
	path, _ := exec.LookPath("ffmpeg")
	return path
}

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s not installed", name)
		}
	}
	return ffmpegPath()
}

// makeMedia 用 ffmpeg 生成真实的测试媒体：kind=muxed 带音轨的 MP4，video 只有画面，audio 只有 AAC 音频。
func makeMedia(t *testing.T, kind string, seconds int) []byte {
	t.Helper()
	ffmpeg := requireFFmpeg(t)
	out := filepath.Join(t.TempDir(), "media-"+kind)
	duration := []string{"-t", strconv.Itoa(seconds)}
	var args []string
	switch kind {
	case "video":
		args = append([]string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25"}, duration...)
		args = append(args, "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-f", "mp4", out)
	case "audio":
		args = append([]string{"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100"}, duration...)
		args = append(args, "-vn", "-c:a", "aac", "-f", "mp4", out)
	default:
		args = []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100"}
		args = append(args, duration...)
		args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", "-f", "mp4", out)
	}
	run := func(args []string) ([]byte, error) {
		return exec.Command(ffmpeg, append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)...).CombinedOutput()
	}
	if output, err := run(args); err != nil {
		// 没有 libx264 的 ffmpeg 构建改用 mpeg4 编码，测试只关心容器与流。
		for i, arg := range args {
			if arg == "libx264" {
				args[i] = "mpeg4"
			}
		}
		if retry, retryErr := run(args); retryErr != nil {
			t.Fatalf("ffmpeg generate %s: %v %s %s", kind, retryErr, output, retry)
		}
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fakeMP4 是 CI 没有 ffmpeg 时使用的最小 MP4 文件头，只用于验证下载、限额与类型识别。
func fakeMP4(size int) []byte {
	data := make([]byte, max(size, 64))
	copy(data, []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"))
	return data
}

// fakeRunner 替代 yt-dlp 子进程，按链接返回固定 JSON；同时记录参数与环境变量用于安全断言。
type fakeRunner struct {
	mu      sync.Mutex
	calls   int
	args    [][]string
	env     [][]string
	outputs map[string]string
	stderr  map[string]string
}

func (f *fakeRunner) Run(_ context.Context, _ string, args []string, env []string, _ int64) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.args = append(f.args, append([]string(nil), args...))
	f.env = append(f.env, append([]string(nil), env...))
	target := args[len(args)-1]
	if message, ok := f.stderr[target]; ok {
		return nil, []byte(message), errors.New("exit status 1")
	}
	if output, ok := f.outputs[target]; ok {
		return []byte(output), nil, nil
	}
	return nil, []byte("ERROR: Unsupported URL: " + target), errors.New("exit status 1")
}

// stubProvider 是可编排结果的供应商，用于验证重试、降级与熔断。
type stubProvider struct {
	name    string
	mu      sync.Mutex
	calls   int
	results []func() (*Result, error)
}

func (p *stubProvider) Name() string       { return p.name }
func (p *stubProvider) Kind() ProviderKind { return ProviderThirdParty }
func (p *stubProvider) Available() error   { return nil }

func (p *stubProvider) Resolve(context.Context, Request) (*Result, error) {
	p.mu.Lock()
	index := p.calls
	p.calls++
	p.mu.Unlock()
	if index >= len(p.results) {
		index = len(p.results) - 1
	}
	return p.results[index]()
}

func (p *stubProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}
