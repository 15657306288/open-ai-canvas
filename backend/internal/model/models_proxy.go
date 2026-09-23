package model

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// 网络代理协议常量。
const (
	ProxyProtocolHTTP   = "http"
	ProxyProtocolHTTPS  = "https"
	ProxyProtocolSOCKS5 = "socks5"
)

// NetworkProxy 出网代理配置。绑定到账号后，该账号的服务端直连 HTTP 请求
// （豆包网页接口、DeepSeek 网页中继等）走此代理；CDP 浏览器链路不受影响。
type NetworkProxy struct {
	ID       string `gorm:"size:64;primaryKey" json:"id"`
	Name     string `gorm:"size:128;not null;default:''" json:"name"`
	Protocol string `gorm:"size:16;not null;default:'http'" json:"protocol"`
	Host     string `gorm:"size:255;not null;default:''" json:"host"`
	Port     int    `gorm:"not null;default:0" json:"port"`
	Username string `gorm:"size:128;not null;default:''" json:"username"`
	// Password is accepted on create/update but never serialized by list/read APIs.
	Password  string    `gorm:"size:128;not null;default:''" json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (NetworkProxy) TableName() string { return "network_proxies" }

// NormalizedProxyProtocol 归一化代理协议名；不支持时返回空串。
func NormalizedProxyProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ProxyProtocolHTTP:
		return ProxyProtocolHTTP
	case ProxyProtocolHTTPS:
		return ProxyProtocolHTTPS
	case ProxyProtocolSOCKS5, "socks":
		return ProxyProtocolSOCKS5
	}
	return ""
}

// ProxyURL 拼出可交给 http.Transport 的代理地址；配置不完整时返回空串。
func (p NetworkProxy) ProxyURL() string {
	host := strings.TrimSpace(p.Host)
	scheme := NormalizedProxyProtocol(p.Protocol)
	if host == "" || scheme == "" || p.Port <= 0 || p.Port > 65535 {
		return ""
	}
	builder := url.URL{Scheme: scheme, Host: fmt.Sprintf("%s:%d", host, p.Port)}
	if username := strings.TrimSpace(p.Username); username != "" {
		builder.User = url.UserPassword(username, p.Password)
	}
	return builder.String()
}
