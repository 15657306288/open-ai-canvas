package netproxy

// 代理配置的跨域读取工具：豆包池 / 网页中继池在取号时解析账号绑定的代理地址。
// 独立成包是为了避免 doubao / webrelay 互相依赖。

import (
	"strings"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

// LookupURL 返回账号绑定的代理地址；未绑定、库不可用或配置已删除时返回空串（直连）。
func LookupURL(db *gorm.DB, proxyID string) string {
	proxyID = strings.TrimSpace(proxyID)
	if db == nil || proxyID == "" {
		return ""
	}
	var proxy model.NetworkProxy
	if err := db.Where("id = ?", proxyID).First(&proxy).Error; err != nil {
		return ""
	}
	return proxy.ProxyURL()
}
