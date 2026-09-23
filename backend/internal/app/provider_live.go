package app

// 生成任务「当前使用账号」实时登记表：任务面板用它展示正在生成的是哪个
// 账号，并提供打开豆包 / Dola 网页会话的直链。只做内存登记，任务结束自动
// 失效（读时发现任务已终态即删除），不落库、不参与恢复。

import (
	"strings"
	"time"

	"infinite-canvas/backend/internal/doubao"
	"infinite-canvas/backend/internal/model"
)

// providerLiveEntry 单个任务的账号实时信息；updatedAt 用于过期兜底清理。
type providerLiveEntry struct {
	Info      doubao.LiveInfo `json:"info"`
	UpdatedAt time.Time       `json:"-"`
}

const providerLiveTTL = 2 * time.Hour

// setProviderLiveAccount 登记/更新任务当前使用的账号信息（换号重试时以最后一次为准）。
func (s *Service) setProviderLiveAccount(taskID string, info doubao.LiveInfo) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	s.providerLiveAccounts.Store(taskID, providerLiveEntry{Info: info, UpdatedAt: time.Now()})
}

// ProviderLiveAccount 查询任务当前使用的账号信息。任务不存在或非本人返回
// NotFound；任务已到终态（成功/失败/取消）或登记已过期时清理并返回空信息。
func (s *Service) ProviderLiveAccount(userID, taskID string) (*doubao.LiveInfo, error) {
	task, err := s.Task(userID, taskID)
	if err != nil {
		return nil, err
	}
	raw, ok := s.providerLiveAccounts.Load(task.ID)
	if !ok {
		return &doubao.LiveInfo{}, nil
	}
	entry, _ := raw.(providerLiveEntry)
	terminated := task.Status == model.TaskStatusSucceeded || task.Status == model.TaskStatusFailed || task.Status == model.TaskStatusCancelled
	if entry.UpdatedAt.Add(providerLiveTTL).Before(time.Now()) || terminated {
		s.providerLiveAccounts.Delete(task.ID)
		return &doubao.LiveInfo{}, nil
	}
	info := entry.Info
	return &info, nil
}
