package videoparser

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// egressProxy 是只监听 127.0.0.1 的 HTTP / CONNECT 代理，给 yt-dlp 子进程使用。
// 每个目标主机都按 outbound 的出站策略解析，只拨号到校验通过的公网 IP（防 SSRF 与 DNS 重绑定），
// 端口只允许 80 / 443。这样 yt-dlp 内部的每一次请求与重定向都受同一套地址校验。
type egressProxy struct {
	listener net.Listener
	server   *http.Server
	url      string
}

func (s *Service) egressURL() (string, error) {
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	if s.egress != nil {
		return s.egress.url, nil
	}
	proxy, err := startEgressProxy(s.opts.ResolveHost, s.opts.egressPortAllowed)
	if err != nil {
		return "", err
	}
	s.egress = proxy
	return proxy.url, nil
}

// Close 停止本地出口代理（测试与进程退出时使用）。
func (s *Service) Close() {
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	if s.egress != nil {
		_ = s.egress.server.Close()
		s.egress = nil
	}
}

// defaultEgressPort 只放行 80 / 443；测试可替换为放行本地测试服务器的端口。
func defaultEgressPort(port string) bool { return port == "443" || port == "80" }

func startEgressProxy(resolve func(ctx context.Context, host string) ([]string, error), allowPort func(string) bool) (*egressProxy, error) {
	if allowPort == nil {
		allowPort = defaultEgressPort
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	dial := func(ctx context.Context, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if !allowPort(port) {
			return nil, errors.New("egress port not allowed")
		}
		ips, err := resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = errors.New("no allowed address")
		}
		return nil, lastErr
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           func(ctx context.Context, _, address string) (net.Conn, error) { return dial(ctx, address) },
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			serveConnect(w, r, dial)
			return
		}
		if r.URL.Scheme != "http" || r.URL.Host == "" {
			http.Error(w, "proxy only", http.StatusBadRequest)
			return
		}
		outReq := r.Clone(r.Context())
		outReq.RequestURI = ""
		for _, name := range []string{"Proxy-Authorization", "Proxy-Connection", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade"} {
			outReq.Header.Del(name)
		}
		resp, err := transport.RoundTrip(outReq)
		if err != nil {
			http.Error(w, "egress blocked or unreachable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for name, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return &egressProxy{listener: listener, server: server, url: "http://" + listener.Addr().String()}, nil
}

func serveConnect(w http.ResponseWriter, r *http.Request, dial func(context.Context, string) (net.Conn, error)) {
	address := r.Host
	if !strings.Contains(address, ":") {
		address += ":443"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	upstream, err := dial(ctx, address)
	cancel()
	if err != nil {
		http.Error(w, "egress blocked or unreachable", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	// 解析请求都很短；整条隧道最长 5 分钟，避免异常连接长期占用。
	deadline := time.Now().Add(5 * time.Minute)
	_ = client.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = client.Close(); _ = upstream.Close() }) }
	go func() {
		if buffered != nil && buffered.Reader.Buffered() > 0 {
			_, _ = io.CopyN(upstream, buffered, int64(buffered.Reader.Buffered()))
		}
		_, _ = io.Copy(upstream, client)
		closeBoth()
	}()
	_, _ = io.Copy(client, upstream)
	closeBoth()
}
