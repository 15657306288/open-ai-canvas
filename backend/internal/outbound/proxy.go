package outbound

// 账号级出网代理：代理地址随 ctx 传递，HTTP 客户端按地址缓存 Transport。
// 绑定代理的账号（豆包网页接口 / DeepSeek 网页中继）出网走指定代理，
// 未注入代理时保持原有行为（直连，环境变量 HTTP_PROXY 仍然生效）。

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// errProxyURLInvalid 代理地址不合法（协议或主机缺失）。
var errProxyURLInvalid = errors.New("代理地址无效，协议仅支持 http / https / socks5")

type proxyContextKey struct{}

// WithProxyURL 把代理地址写入 ctx（空串视为未设置）。
func WithProxyURL(ctx context.Context, rawURL string) context.Context {
	return context.WithValue(ctx, proxyContextKey{}, strings.TrimSpace(rawURL))
}

// ProxyURLFromContext 读取 ctx 中的代理地址；未注入返回空串。
func ProxyURLFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(proxyContextKey{}).(string)
	return strings.TrimSpace(value)
}

// ValidateProxyRawURL 校验手工拼接的代理地址（协议限 http/https/socks5）。
func ValidateProxyRawURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return errProxyURLInvalid
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
		return nil
	}
	return errProxyURLInvalid
}

var (
	proxyTransportMu sync.Mutex
	proxyTransports  = map[string]*http.Transport{}
)

// HTTPClientFromContext 返回遵循 ctx 代理的 HTTP 客户端。
// 同一代理地址共享一个 Transport（连接池复用）；未注入代理时退回默认行为。
func HTTPClientFromContext(ctx context.Context, timeout time.Duration) *http.Client {
	raw := ProxyURLFromContext(ctx)
	if raw == "" {
		return &http.Client{Timeout: timeout}
	}
	proxyTransportMu.Lock()
	transport := proxyTransports[raw]
	if transport == nil {
		if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
			transport = newProxyTransport(parsed)
			if transport != nil {
				proxyTransports[raw] = transport
			}
		}
	}
	proxyTransportMu.Unlock()
	if transport == nil {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// newProxyTransport 按代理协议构造 Transport：
//   - http   ：标准 HTTP 代理（http.Transport.Proxy 原生支持）
//   - https  ：HTTP-over-TLS 代理（Proxy 用 URL，DialContext 先 TLS 握手连代理）
//   - socks5 ：SOCKS5 代理（http.Transport.Proxy 不支持，必须自定义拨号器，
//     否则 CONNECT 报文发给 SOCKS5 服务器会被直接断开，表现为 EOF）
func newProxyTransport(parsed *url.URL) *http.Transport {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		transport.Proxy = http.ProxyURL(parsed)
	case "https":
		transport.Proxy = http.ProxyURL(parsed)
		proxyHost := parsed.Hostname()
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, parsed.Host)
			if err != nil {
				return nil, err
			}
			tlsConn := tls.Client(conn, &tls.Config{ServerName: proxyHost})
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return tlsConn, nil
		}
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			auth = &proxy.Auth{User: parsed.User.Username(), Password: password}
		}
		socksDialer, err := proxy.SOCKS5("tcp", parsed.Host, auth, dialer)
		if err != nil {
			return nil
		}
		contextDialer, ok := socksDialer.(proxy.ContextDialer)
		if !ok {
			return nil
		}
		transport.Proxy = nil
		transport.DialContext = contextDialer.DialContext
	default:
		return nil
	}
	return transport
}
