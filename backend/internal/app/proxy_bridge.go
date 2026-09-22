package app

// 网络代理：代理列表 CRUD + 账号绑定 + 连通性测试。
// 绑定只影响服务端直连 HTTP 链路（豆包网页接口、DeepSeek 网页中继等）；
// 千问 / Dola 的 CDP 浏览器链路不经过这里。

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/outbound"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// networkProxyTestEndpoints 测 IP 用的回显服务，按序尝试、先成功先用。
// api.ipify.org 在国内网络（直连与部分国内代理出口）不可达，必须放国内站点在前。
var networkProxyTestEndpoints = []string{
	"https://myip.ipip.net",
	"http://cip.cc",
	"https://api.ipify.org/?format=json",
}

// networkProxyIPv4Pattern 从回显文本中提取 IPv4（三种站点的响应里都只会出现访问方 IP）。
var networkProxyIPv4Pattern = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

type NetworkProxyUpsertRequest struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type NetworkProxyAssignRequest struct {
	// PoolType 当前支持豆包账号池；保留字段便于后续扩展其它全局池。
	PoolType string   `json:"poolType"`
	IDs      []string `json:"ids"`
	// ProxyID 目标代理；空串表示解除绑定（直连）。
	ProxyID string `json:"proxyId"`
}

func (s *Service) networkProxyDB() (*gorm.DB, error) {
	if s.repo == nil {
		return nil, errors.New("数据库不可用")
	}
	return s.repo.DB(), nil
}

func normalizeNetworkProxyInput(req NetworkProxyUpsertRequest) (*model.NetworkProxy, error) {
	name := strings.TrimSpace(req.Name)
	protocol := model.NormalizedProxyProtocol(req.Protocol)
	host := strings.TrimSpace(req.Host)
	username := strings.TrimSpace(req.Username)
	password := strings.TrimSpace(req.Password)
	if name == "" {
		return nil, errors.New("请填写代理名称")
	}
	if len(name) > 128 {
		return nil, errors.New("代理名称过长")
	}
	if protocol == "" {
		return nil, errors.New("协议仅支持 http / https / socks5")
	}
	if host == "" {
		return nil, errors.New("请填写主机地址")
	}
	if req.Port <= 0 || req.Port > 65535 {
		return nil, errors.New("端口需在 1-65535 之间")
	}
	proxy := &model.NetworkProxy{Name: name, Protocol: protocol, Host: host, Port: req.Port, Username: username, Password: password}
	if proxy.ProxyURL() == "" {
		return nil, errors.New("代理地址无效")
	}
	return proxy, nil
}

// NetworkProxyList 代理列表（按创建顺序）。
func (s *Service) NetworkProxyList() ([]model.NetworkProxy, error) {
	db, err := s.networkProxyDB()
	if err != nil {
		return nil, err
	}
	var proxies []model.NetworkProxy
	if err := db.Order("created_at ASC").Find(&proxies).Error; err != nil {
		return nil, err
	}
	return proxies, nil
}

// NetworkProxyCreate 新增代理。
func (s *Service) NetworkProxyCreate(req NetworkProxyUpsertRequest) (*model.NetworkProxy, error) {
	db, err := s.networkProxyDB()
	if err != nil {
		return nil, err
	}
	proxy, err := normalizeNetworkProxyInput(req)
	if err != nil {
		return nil, err
	}
	proxy.ID = uuid.NewString()
	if err := db.Create(proxy).Error; err != nil {
		return nil, err
	}
	return proxy, nil
}

// NetworkProxyUpdate 修改代理。
func (s *Service) NetworkProxyUpdate(id string, req NetworkProxyUpsertRequest) (*model.NetworkProxy, error) {
	db, err := s.networkProxyDB()
	if err != nil {
		return nil, err
	}
	var proxy model.NetworkProxy
	if err := db.Where("id = ?", id).First(&proxy).Error; err != nil {
		return nil, errors.New("代理不存在或已删除")
	}
	input, err := normalizeNetworkProxyInput(req)
	if err != nil {
		return nil, err
	}
	proxy.Name = input.Name
	proxy.Protocol = input.Protocol
	proxy.Host = input.Host
	proxy.Port = input.Port
	proxy.Username = input.Username
	proxy.Password = input.Password
	if err := db.Save(&proxy).Error; err != nil {
		return nil, err
	}
	return &proxy, nil
}

// NetworkProxyDelete 删除代理并解除所有账号绑定（解绑后账号回到直连）。
func (s *Service) NetworkProxyDelete(id string) error {
	db, err := s.networkProxyDB()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).Delete(&model.NetworkProxy{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.DoubaoAccount{}).Where("proxy_id = ?", id).Update("proxy_id", "").Error; err != nil {
			return err
		}
		return nil
	})
}

// NetworkProxyTest 通过代理访问 IP 回显服务，验证代理可用性。
func (s *Service) NetworkProxyTest(id string) (map[string]any, error) {
	db, err := s.networkProxyDB()
	if err != nil {
		return nil, err
	}
	var proxy model.NetworkProxy
	if err := db.Where("id = ?", id).First(&proxy).Error; err != nil {
		return nil, errors.New("代理不存在或已删除")
	}
	raw := proxy.ProxyURL()
	if raw == "" {
		return nil, errors.New("代理配置不完整")
	}
	ctx := outbound.WithProxyURL(context.Background(), raw)
	client := outbound.HTTPClientFromContext(ctx, 8*time.Second)
	var lastMessage string
	for _, endpoint := range networkProxyTestEndpoints {
		startedAt := time.Now()
		resp, err := client.Get(endpoint)
		if err != nil {
			lastMessage = endpoint + "：" + err.Error()
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		if readErr != nil {
			lastMessage = endpoint + "：响应读取失败：" + readErr.Error()
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastMessage = endpoint + "：HTTP " + resp.Status
			continue
		}
		ip := ""
		if match := networkProxyIPv4Pattern.Find(data); len(match) > 0 {
			ip = strings.TrimSpace(string(match))
		}
		if ip == "" {
			lastMessage = endpoint + "：响应中未找到 IP"
			continue
		}
		return map[string]any{"ok": true, "ip": ip, "latencyMs": time.Since(startedAt).Milliseconds()}, nil
	}
	if lastMessage == "" {
		lastMessage = "未配置可用的 IP 回显服务"
	}
	return map[string]any{"ok": false, "message": "连接失败：" + lastMessage}, nil
}

// NetworkProxyAssign 批量绑定 / 解绑账号代理。
func (s *Service) NetworkProxyAssign(req NetworkProxyAssignRequest) (int, error) {
	db, err := s.networkProxyDB()
	if err != nil {
		return 0, err
	}
	proxyID := strings.TrimSpace(req.ProxyID)
	if proxyID != "" {
		var count int64
		if err := db.Model(&model.NetworkProxy{}).Where("id = ?", proxyID).Count(&count).Error; err != nil {
			return 0, err
		}
		if count == 0 {
			return 0, errors.New("代理不存在或已删除")
		}
	}
	poolType := strings.TrimSpace(req.PoolType)
	if poolType != "doubao" {
		return 0, errors.New("账号池类型仅支持 doubao")
	}
	affected := 0
	for _, id := range req.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		var result *gorm.DB
		result = db.Model(&model.DoubaoAccount{}).Where("id = ?", id).Update("proxy_id", proxyID)
		if result.Error != nil {
			return affected, result.Error
		}
		affected += int(result.RowsAffected)
	}
	return affected, nil
}
