package model

import (
	"time"

	"gorm.io/gorm"
)

// ApiKeyStatus 网站签发的客户 API Key 状态。
type ApiKeyStatus string

const (
	ApiKeyActive   ApiKeyStatus = "active"
	ApiKeyDisabled ApiKeyStatus = "disabled"
)

// ApiKey 网站用户自助签发的 API Key（供外部智能体/网关经 MCP 调用画布）。
// 明文 ak_xxx 只在签发时返回一次，数据库只存 sha256 哈希与展示用前缀。
// 网关 remote 模式经 /api/internal/api-keys/verify 校验，拿到真实 userId 后计费。
type ApiKey struct {
	ID         string       `json:"id" gorm:"primaryKey;size:36"` // k_ 前缀
	UserID     string       `json:"userId" gorm:"index;size:36;not null"`
	Name       string       `json:"name" gorm:"size:80;not null"`
	KeyHash    string       `json:"-" gorm:"size:64;uniqueIndex:idx_api_keys_hash,where:deleted_at IS NULL;not null"`
	Prefix     string       `json:"prefix" gorm:"size:16"` // 明文前 8 位（ak_xxxx），便于展示辨识
	Status     ApiKeyStatus `json:"status" gorm:"size:16;not null;default:'active'"`
	LastUsedAt *time.Time   `json:"lastUsedAt"`
	CreatedAt  time.Time    `json:"createdAt"`
	UpdatedAt  time.Time    `json:"updatedAt"`
	DeletedAt  gorm.DeletedAt `json:"-" gorm:"index"`
}
