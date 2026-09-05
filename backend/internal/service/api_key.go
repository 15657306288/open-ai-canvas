package service

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

// ApiKey 网站自助签发的外部 API Key（供外部智能体/网关经 MCP 调用画布）。
// 明文 ak_xxx 只在签发时返回一次，数据库仅存 sha256 哈希；网关 remote 校验后拿到真实 userId。

// 网站签发的前缀与长度（与网关 KeyStore 兼容：ak_ + 32 位十六进制 = 256 位熵）。
const (
	apiKeyPrefix   = "ak_"
	apiKeyIDPrefix = "k_"
	apiKeyMaxName  = 80
)

// CreateApiKey 为用户签发一个 API Key，返回明文（仅此一次）与记录。
func (s *Service) CreateApiKey(userID string, name string) (string, *model.ApiKey, error) {
	userID = strings.TrimSpace(userID)
	name = strings.TrimSpace(name)
	if userID == "" {
		return "", nil, BadAuthRequest("缺少用户")
	}
	if name == "" {
		name = "默认 API Key"
	}
	if len(name) > apiKeyMaxName {
		return "", nil, BadAuthRequest("API Key 名称过长")
	}

	plain := apiKeyPrefix + randomHex(16) // 16 字节 → 32 hex
	hash := hashKeyHex(plain)

	item := &model.ApiKey{
		ID:      apiKeyIDPrefix + randomHex(4),
		UserID:  userID,
		Name:    name,
		KeyHash: hash,
		Prefix:  plain[:12], // ak_ + 前 8 hex，便于展示辨识
		Status:  model.ApiKeyActive,
	}
	if err := s.repo.SaveApiKey(item); err != nil {
		return "", nil, err
	}
	return plain, item, nil
}

// ListApiKeys 列出用户全部 API Key（不含明文）。
func (s *Service) ListApiKeys(userID string) ([]model.ApiKey, error) {
	return s.repo.ListApiKeys(strings.TrimSpace(userID))
}

// DeleteApiKey 删除（软删）用户 API Key；不存在返回 404。
func (s *Service) DeleteApiKey(userID string, id string) error {
	ok, err := s.repo.DeleteApiKey(strings.TrimSpace(id), strings.TrimSpace(userID))
	if err != nil {
		return err
	}
	if !ok {
		return NotFound("API Key 不存在")
	}
	return nil
}

// VerifyApiKeyResult 网关 remote 校验 API Key 后返回的调用主体信息。
type VerifyApiKeyResult struct {
	KeyID            string `json:"keyId"`
	UserID           string `json:"userId"`
	DisplayName      string `json:"displayName"`
	Enabled          bool   `json:"enabled"`
	BalanceMicrocredits *int64 `json:"balanceMicrocredits,omitempty"`
}

// VerifyApiKey 校验网关传入的明文 API Key（internal 接口使用）：哈希比对后返回绑定的网站用户。
// 不存在/已停用/用户不存在一律返回错误，由网关 fail-closed。
func (s *Service) VerifyApiKey(plainKey string) (*VerifyApiKeyResult, error) {
	plainKey = strings.TrimSpace(plainKey)
	if !strings.HasPrefix(plainKey, apiKeyPrefix) {
		return nil, Unauthorized("API Key 格式非法")
	}
	item, err := s.repo.ApiKeyByHash(hashKeyHex(plainKey))
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, Unauthorized("API Key 无效")
		}
		return nil, err
	}
	if item.Status != model.ApiKeyActive {
		return nil, Unauthorized("API Key 已停用")
	}
	users, err := s.repo.UsersByIDs([]string{item.UserID})
	if err != nil {
		return nil, err
	}
	u, ok := users[item.UserID]
	if !ok {
		return nil, Unauthorized("API Key 所属用户不可用")
	}
	// 尽力更新最近使用时间，失败不阻断校验。
	_ = s.repo.TouchApiKeyLastUsed(item.ID)

	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.Username
	}
	if displayName == "" {
		displayName = item.UserID
	}
	result := &VerifyApiKeyResult{
		KeyID:       item.ID,
		UserID:      item.UserID,
		DisplayName: displayName,
		Enabled:     true,
	}
	// 附带账户可用积分（供网关展示/预检）。
	if acc, err := s.InternalCreditAccount(item.UserID); err == nil {
		bal := acc.AvailableMicrocredits
		result.BalanceMicrocredits = &bal
	}
	return result, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(b)
}

// hashKeyHex 计算明文 API Key 的 sha256 摘要（与 provider 包的 sha256Hex 同名冲突，故用别名）。
func hashKeyHex(s string) string {
	return sha256Hex([]byte(s))
}
