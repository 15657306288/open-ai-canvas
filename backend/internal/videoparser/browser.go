package videoparser

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// 无头浏览器：用于抖音这类只允许页面脚本发起数据请求的平台。浏览器只打开平台的官方公开页面（例如抖音官方嵌入播放器），
// 读取页面自己请求到的接口响应；不注入脚本、不伪造签名、不读取或保存 Cookie，遇到验证码或登录要求直接失败。
//
// 安全边界：
//   - 使用 --remote-debugging-pipe，调试协议走进程管道，不监听任何端口；
//   - 全部流量经本地出口代理（egress.go），每个目标只连校验过的公网 IP，回环地址也强制走代理被拦截；
//   - 每次解析使用全新的临时用户目录，结束后删除；子进程只拿到最小环境变量；
//   - 屏蔽视频、图片、字体请求，浏览器里不下载媒体，媒体由服务端带限额的下载器获取。

const (
	// browserStartTimeout 覆盖容器里首次启动（浏览器文件尚未进入页缓存）的耗时；热启动通常在 1 秒内。
	browserStartTimeout = 45 * time.Second
	browserMaxBody      = 8 << 20
)

// browserBlockedURLs 是浏览器里不需要加载的资源，减少流量与内存。
var browserBlockedURLs = []string{"*douyinvod.com*", "*.mp4*", "*.m4s*", "*.m3u8*", "*.jpeg*", "*.jpg*", "*.png*", "*.webp*", "*.gif*", "*.avif*", "*.woff*", "*.ttf*", "*.otf*"}

// capturedResponse 是浏览器里一次命中规则的网络响应。
type capturedResponse struct {
	URL    string
	Status int
	Body   []byte
}

// pageCapturer 在无头浏览器里打开公开页面，把页面自己发出的、命中 match 的接口响应交给 onResponse；onResponse 返回 true 时结束。
type pageCapturer interface {
	Available() error
	Capture(ctx context.Context, pageURL string, match func(string) bool, onResponse func(capturedResponse) (bool, error)) error
}

type chromiumCapturer struct {
	svc   *Service
	slots chan struct{}
}

func newChromiumCapturer(svc *Service) *chromiumCapturer {
	return &chromiumCapturer{svc: svc, slots: make(chan struct{}, svc.opts.BrowserConcurrency)}
}

func (c *chromiumCapturer) Available() error {
	if c.svc.opts.ChromiumPath == "" {
		return errors.New("服务器未安装 Chromium")
	}
	if !browserPipeSupported {
		return errors.New("当前系统不支持无头浏览器")
	}
	return nil
}

func (c *chromiumCapturer) Capture(ctx context.Context, pageURL string, match func(string) bool, onResponse func(capturedResponse) (bool, error)) error {
	if err := c.Available(); err != nil {
		return newError(ErrUnavailable, "解析浏览器不可用", err)
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return newError(ErrBusy, "解析浏览器繁忙，请稍后重试", ctx.Err())
	}
	proxy, err := c.svc.egressURL()
	if err != nil {
		return newError(ErrUnavailable, "解析出口未就绪，请稍后重试", err)
	}
	profile, err := os.MkdirTemp("", "ecv-browser-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(profile)
	browser, err := startChromium(ctx, c.svc.opts.ChromiumPath, profile, proxy)
	if err != nil {
		return &Error{Code: ErrUnavailable, Message: "解析浏览器启动失败，请稍后重试", Retryable: true, Err: err}
	}
	defer browser.close()
	return browser.capture(ctx, pageURL, match, onResponse)
}

// WarmUp 把无头浏览器启动并关闭一次（不打开任何页面），让首次抖音解析不用承担浏览器冷启动的耗时。
func (s *Service) WarmUp(ctx context.Context) {
	if s.opts.ChromiumPath == "" || !browserPipeSupported || !s.platformEnabled(PlatformDouyin) || s.opts.Disabled[PlatformDouyin] {
		return
	}
	started := time.Now()
	proxy, err := s.egressURL()
	if err != nil {
		s.opts.Logger.Warn("video link parser browser warm-up skipped", "error", err)
		return
	}
	profile, err := os.MkdirTemp("", "ecv-browser-warm-")
	if err != nil {
		return
	}
	defer os.RemoveAll(profile)
	browser, err := startChromium(ctx, s.opts.ChromiumPath, profile, proxy)
	if err != nil {
		s.opts.Logger.Warn("video link parser browser warm-up failed", "elapsedMs", time.Since(started).Milliseconds(), "error", clip(err.Error(), 200))
		return
	}
	browser.close()
	s.opts.Logger.Info("video link parser browser warmed up", "elapsedMs", time.Since(started).Milliseconds())
}

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// chromium 是一次性的浏览器进程与它的调试管道（fd 3 读命令、fd 4 写响应，消息以 NUL 分隔）。
type chromium struct {
	cmd       *exec.Cmd
	in        *os.File
	out       *os.File
	stderr    *limitedBuffer
	nextID    atomic.Int64
	writeMu   sync.Mutex
	mu        sync.Mutex
	pending   map[int64]chan cdpMessage
	events    chan cdpMessage
	done      chan struct{}
	closeOnce sync.Once
}

func chromiumArgs(profile, proxy string) []string {
	return []string{
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--disable-sync", "--disable-background-networking", "--disable-component-update", "--disable-default-apps",
		"--disable-breakpad", "--metrics-recording-only", "--no-pings", "--mute-audio", "--hide-scrollbars", "--disable-quic",
		"--disable-features=Translate,MediaRouter,OptimizationHints,site-per-process,IsolateOrigins",
		"--renderer-process-limit=2", "--js-flags=--max-old-space-size=256",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp", "--webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--proxy-server=" + proxy, "--proxy-bypass-list=<-loopback>",
		"--lang=zh-CN", "--window-size=1280,800", "--user-data-dir=" + profile,
		"--remote-debugging-pipe", "about:blank",
	}
}

func startChromium(ctx context.Context, path, profile, proxy string) (*chromium, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, err
	}
	cmd := exec.Command(path, chromiumArgs(profile, proxy)...)
	cmd.ExtraFiles = []*os.File{inR, outW}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + profile, "TMPDIR=" + profile, "XDG_CONFIG_HOME=" + profile, "XDG_CACHE_HOME=" + profile, "LANG=C.UTF-8"}
	stderr := &limitedBuffer{limit: 16 << 10}
	cmd.Stdout, cmd.Stderr = nil, stderr
	cmd.WaitDelay = 2 * time.Second
	prepareBrowserCmd(cmd)
	if err := cmd.Start(); err != nil {
		for _, file := range []*os.File{inR, inW, outR, outW} {
			_ = file.Close()
		}
		return nil, err
	}
	_ = inR.Close()
	_ = outW.Close()
	b := &chromium{cmd: cmd, in: inW, out: outR, stderr: stderr, pending: map[int64]chan cdpMessage{}, events: make(chan cdpMessage, 2048), done: make(chan struct{})}
	go b.readLoop()
	startCtx, cancel := context.WithTimeout(ctx, browserStartTimeout)
	defer cancel()
	if err := b.call(startCtx, "", "Browser.getVersion", nil, nil); err != nil {
		b.close()
		return nil, fmt.Errorf("chromium did not answer: %w (%s)", err, clip(string(stderr.Bytes()), 200))
	}
	return b, nil
}

// readLoop 把命令结果交给等待者，只转发解析需要的网络事件，其余事件丢弃。
func (b *chromium) readLoop() {
	defer close(b.done)
	reader := bufio.NewReaderSize(b.out, 1<<20)
	for {
		data, err := reader.ReadBytes(0)
		if err != nil {
			return
		}
		var msg cdpMessage
		if json.Unmarshal(data[:len(data)-1], &msg) != nil {
			continue
		}
		if msg.ID != 0 {
			b.mu.Lock()
			ch := b.pending[msg.ID]
			delete(b.pending, msg.ID)
			b.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		switch msg.Method {
		case "Network.requestWillBeSent", "Network.responseReceived", "Network.loadingFinished", "Network.loadingFailed", "Target.targetCrashed", "Inspector.detached":
			select {
			case b.events <- msg:
			default:
			}
		}
	}
}

func (b *chromium) call(ctx context.Context, sessionID, method string, params any, out any) error {
	id := b.nextID.Add(1)
	ch := make(chan cdpMessage, 1)
	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()
	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	_, err = b.in.Write(append(data, 0))
	b.writeMu.Unlock()
	if err != nil {
		return err
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		if out != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, out)
		}
		return nil
	case <-b.done:
		return errors.New("browser exited")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// close 先请求浏览器正常退出，超时后结束整个进程组，避免残留渲染进程。
func (b *chromium) close() {
	b.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = b.call(ctx, "", "Browser.close", nil, nil)
		cancel()
		exited := make(chan struct{})
		go func() {
			_ = b.cmd.Wait()
			close(exited)
		}()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			killBrowserTree(b.cmd)
			<-exited
		}
		killBrowserTree(b.cmd)
		_ = b.in.Close()
		_ = b.out.Close()
	})
}

// capture 打开页面并监听网络事件；页面导航失败、浏览器崩溃或超时都返回错误。
func (b *chromium) capture(ctx context.Context, pageURL string, match func(string) bool, onResponse func(capturedResponse) (bool, error)) error {
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := b.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return browserError(ctx, err)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &attached); err != nil {
		return browserError(ctx, err)
	}
	sid := attached.SessionID
	if err := b.call(ctx, sid, "Network.enable", map[string]any{"maxResourceBufferSize": browserMaxBody, "maxTotalBufferSize": 4 * browserMaxBody}, nil); err != nil {
		return browserError(ctx, err)
	}
	_ = b.call(ctx, sid, "Network.setBlockedURLs", map[string]any{"urls": browserBlockedURLs}, nil)
	navigated := make(chan error, 1)
	go func() {
		var result struct {
			ErrorText string `json:"errorText"`
		}
		err := b.call(ctx, sid, "Page.navigate", map[string]any{"url": pageURL}, &result)
		if err == nil && result.ErrorText != "" {
			err = errors.New(result.ErrorText)
		}
		navigated <- err
	}()
	tracked := map[string]string{}
	statuses := map[string]int{}
	for {
		select {
		case <-ctx.Done():
			return newError(ErrTimeout, "页面加载超时，请稍后重试", ctx.Err())
		case <-b.done:
			return &Error{Code: ErrUpstream, Message: "解析浏览器意外退出，请稍后重试", Retryable: true, Err: errors.New(clip(string(b.stderr.Bytes()), 200))}
		case err := <-navigated:
			if err != nil {
				return classifyTransportError(err, "打开页面失败")
			}
			navigated = nil
		case msg := <-b.events:
			if msg.SessionID != sid {
				continue
			}
			switch msg.Method {
			case "Target.targetCrashed", "Inspector.detached":
				return &Error{Code: ErrUpstream, Message: "页面崩溃，请稍后重试", Retryable: true}
			case "Network.requestWillBeSent":
				var ev struct {
					RequestID string `json:"requestId"`
					Type      string `json:"type"`
					Request   struct {
						URL string `json:"url"`
					} `json:"request"`
				}
				if json.Unmarshal(msg.Params, &ev) == nil && (ev.Type == "XHR" || ev.Type == "Fetch") && match(ev.Request.URL) {
					tracked[ev.RequestID] = ev.Request.URL
				}
			case "Network.responseReceived":
				var ev struct {
					RequestID string `json:"requestId"`
					Response  struct {
						Status int `json:"status"`
					} `json:"response"`
				}
				if json.Unmarshal(msg.Params, &ev) == nil {
					if _, ok := tracked[ev.RequestID]; ok {
						statuses[ev.RequestID] = ev.Response.Status
					}
				}
			case "Network.loadingFailed":
				var ev struct {
					RequestID string `json:"requestId"`
				}
				if json.Unmarshal(msg.Params, &ev) == nil {
					delete(tracked, ev.RequestID)
				}
			case "Network.loadingFinished":
				var ev struct {
					RequestID string `json:"requestId"`
				}
				if json.Unmarshal(msg.Params, &ev) != nil {
					continue
				}
				requestURL, ok := tracked[ev.RequestID]
				if !ok {
					continue
				}
				delete(tracked, ev.RequestID)
				var body struct {
					Body          string `json:"body"`
					Base64Encoded bool   `json:"base64Encoded"`
				}
				if err := b.call(ctx, sid, "Network.getResponseBody", map[string]any{"requestId": ev.RequestID}, &body); err != nil {
					continue
				}
				raw := []byte(body.Body)
				if body.Base64Encoded {
					decoded, err := base64.StdEncoding.DecodeString(body.Body)
					if err != nil {
						continue
					}
					raw = decoded
				}
				if len(raw) > browserMaxBody {
					continue
				}
				done, err := onResponse(capturedResponse{URL: requestURL, Status: statuses[ev.RequestID], Body: raw})
				if err != nil || done {
					return err
				}
			}
		}
	}
}

func browserError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return newError(ErrTimeout, "页面加载超时，请稍后重试", err)
	}
	return &Error{Code: ErrUpstream, Message: "解析浏览器出错，请稍后重试", Retryable: true, Err: err}
}

// findChromium 查找部署者安装的 Chromium。
func findChromium() string {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}
