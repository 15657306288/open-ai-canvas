package repository

import (
	"time"

	"infinite-canvas/backend/internal/model"
)

// SaveAgentConfirmation 保存外部生成确认记录（创建时使用）。
func (r *Repository) SaveAgentConfirmation(item *model.AgentConfirmation) error {
	return r.db.Create(item).Error
}

// AgentConfirmationByID 按 ID 查询确认记录（含软删除过滤）。
func (r *Repository) AgentConfirmationByID(id string) (*model.AgentConfirmation, error) {
	var item model.AgentConfirmation
	if err := r.db.First(&item, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// AgentConfirmationByIdempotency 按幂等键查询确认记录（网关重试/重连场景）。
func (r *Repository) AgentConfirmationByIdempotency(userID string, idempotencyKey string) (*model.AgentConfirmation, error) {
	var item model.AgentConfirmation
	if err := r.db.First(&item, "user_id = ? AND idempotency_key = ?", userID, idempotencyKey).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// ListPendingAgentConfirmations 列出指定用户的 pending 确认（未过期），按创建时间升序。
func (r *Repository) ListPendingAgentConfirmations(userID string, limit int) ([]model.AgentConfirmation, error) {
	var items []model.AgentConfirmation
	now := time.Now()
	query := r.db.Where("user_id = ? AND status = ? AND expires_at > ?", userID, model.AgentConfirmationPending, now).Order("created_at asc")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// UpdateAgentConfirmationStatus 仅当记录仍为 from 状态时更新到 to（条件更新，防并发覆盖）。
// 返回是否真正发生了状态迁移。
func (r *Repository) UpdateAgentConfirmationStatus(id string, from model.AgentConfirmationStatus, to model.AgentConfirmationStatus) (bool, error) {
	res := r.db.Model(&model.AgentConfirmation{}).
		Where("id = ? AND status = ?", id, from).
		Update("status", to)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ExpireAgentConfirmations 将超过 expires_at 的 pending 确认批量置为 expired（惰性清理）。
func (r *Repository) ExpireAgentConfirmations() error {
	return r.db.Model(&model.AgentConfirmation{}).
		Where("status = ? AND expires_at <= ?", model.AgentConfirmationPending, time.Now()).
		Update("status", model.AgentConfirmationExpired).Error
}
