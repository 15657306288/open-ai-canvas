package repository

import (
	"time"

	"infinite-canvas/backend/internal/model"
)

// SaveApiKey 保存网站签发的 API Key（创建时使用）。
func (r *Repository) SaveApiKey(item *model.ApiKey) error {
	return r.db.Create(item).Error
}

// ApiKeyByHash 按 sha256 哈希查 API Key（网关 remote 校验时使用）。
func (r *Repository) ApiKeyByHash(hash string) (*model.ApiKey, error) {
	var item model.ApiKey
	if err := r.db.First(&item, "key_hash = ?", hash).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// ApiKeyByID 按 ID 查 API Key（限当前用户）。
func (r *Repository) ApiKeyByID(id string, userID string) (*model.ApiKey, error) {
	var item model.ApiKey
	if err := r.db.First(&item, "id = ? AND user_id = ?", id, userID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// ListApiKeys 列出指定用户的 API Key（不含明文哈希），按创建时间倒序。
func (r *Repository) ListApiKeys(userID string) ([]model.ApiKey, error) {
	var items []model.ApiKey
	if err := r.db.Where("user_id = ?", userID).Order("created_at desc").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// DeleteApiKey 软删除 API Key（仅限当前用户，返回是否真正删除）。
func (r *Repository) DeleteApiKey(id string, userID string) (bool, error) {
	res := r.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.ApiKey{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// TouchApiKeyLastUsed 更新 API Key 最近使用时间（网关校验通过后调用，不阻断主流程）。
func (r *Repository) TouchApiKeyLastUsed(id string) error {
	return r.db.Model(&model.ApiKey{}).Where("id = ?", id).Update("last_used_at", time.Now()).Error
}
