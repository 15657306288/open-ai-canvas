package service

import (
	"errors"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

// 外部 Agent 生成确认：网关 reserve 冻结后创建，用户批准后网关才真正执行生成并结算。
// 确认记录的有效期（超过后网关轮询视为超时退款，这里也做惰性清理）。
const agentConfirmationTTL = 30 * time.Minute

// 生成类工具集合：外部调用这些工具前必须经用户确认（与网关确认门保持一致）。
var generationToolNames = map[string]bool{
	"canvas_generate_text":   true,
	"canvas_generate_image":  true,
	"canvas_generate_video":  true,
	"canvas_generate_audio":  true,
	"canvas_run_generation":  true,
}

// IsGenerationTool 判断 MCP 工具是否属于"生成前必须用户确认"的高影响工具。
func IsGenerationTool(tool string) bool {
	return generationToolNames[strings.TrimSpace(tool)]
}

// CreateAgentConfirmation 创建外部生成确认请求（幂等：同用户+幂等键返回已有记录）。
// 网关在 reserve 成功后调用；amountMicrocredits 为后端定价后的实际冻结金额。
func (s *Service) CreateAgentConfirmation(userID string, orderID string, tool string, modelKey string, amountMicrocredits int64, promptSummary string, idempotencyKey string) (*model.AgentConfirmation, error) {
	userID = strings.TrimSpace(userID)
	orderID = strings.TrimSpace(orderID)
	tool = strings.TrimSpace(tool)
	modelKey = strings.TrimSpace(modelKey)
	promptSummary = strings.TrimSpace(promptSummary)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if userID == "" || orderID == "" || tool == "" || idempotencyKey == "" {
		return nil, BadAuthRequest("确认请求参数不完整")
	}
	if amountMicrocredits <= 0 || len(tool) > internalMaxToolLength || len(modelKey) > internalMaxToolLength || len(idempotencyKey) > internalMaxIdempotencyKey {
		return nil, BadAuthRequest("确认请求参数非法")
	}
	if len(promptSummary) > 500 {
		promptSummary = truncateRunes(promptSummary, 500)
	}
	// 校验订单归属：确认必须挂在当前用户自己名下、且处于可执行（reserved）状态。
	order, err := s.repo.BillingOrder(orderID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, NotFound("计费订单不存在")
	}
	if err != nil {
		return nil, err
	}
	if order.UserID != userID {
		return nil, NotFound("计费订单不存在")
	}
	if order.Status != model.BillingStatusReserved {
		return nil, NewAppError(409, "计费订单状态不允许创建确认")
	}
	existing, err := s.repo.AgentConfirmationByIdempotency(userID, idempotencyKey)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	item := &model.AgentConfirmation{
		ID:                 newID(),
		UserID:             userID,
		OrderID:            orderID,
		Tool:               tool,
		ModelKey:           modelKey,
		AmountMicrocredits: amountMicrocredits,
		PromptSummary:      promptSummary,
		Status:             model.AgentConfirmationPending,
		IdempotencyKey:     idempotencyKey,
		ExpiresAt:          time.Now().Add(agentConfirmationTTL),
	}
	if err := s.repo.SaveAgentConfirmation(item); err != nil {
		return nil, err
	}
	return item, nil
}

// AgentConfirmationForUser 按 ID 查询确认记录，仅允许记录归属用户访问。
func (s *Service) AgentConfirmationForUser(userID string, id string) (*model.AgentConfirmation, error) {
	userID = strings.TrimSpace(userID)
	id = strings.TrimSpace(id)
	if userID == "" || id == "" {
		return nil, BadAuthRequest("参数不完整")
	}
	item, err := s.repo.AgentConfirmationByID(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, NotFound("确认请求不存在")
	}
	if err != nil {
		return nil, err
	}
	if item.UserID != userID {
		return nil, NotFound("确认请求不存在")
	}
	return item, nil
}

// ApproveAgentConfirmation 用户批准：仅 pending 可流转到 approved（条件更新防并发覆盖）。
func (s *Service) ApproveAgentConfirmation(userID string, id string) (*model.AgentConfirmation, error) {
	item, err := s.AgentConfirmationForUser(userID, id)
	if err != nil {
		return nil, err
	}
	if item.Status == model.AgentConfirmationApproved {
		return item, nil
	}
	if item.Status != model.AgentConfirmationPending {
		return nil, NewAppError(409, "确认请求已关闭，无法批准")
	}
	changed, err := s.repo.UpdateAgentConfirmationStatus(item.ID, model.AgentConfirmationPending, model.AgentConfirmationApproved)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, NewAppError(409, "确认请求状态已变化，请刷新后重试")
	}
	return s.AgentConfirmationForUser(userID, id)
}

// RejectAgentConfirmation 用户拒绝：仅 pending 可流转到 rejected。
func (s *Service) RejectAgentConfirmation(userID string, id string) (*model.AgentConfirmation, error) {
	item, err := s.AgentConfirmationForUser(userID, id)
	if err != nil {
		return nil, err
	}
	if item.Status == model.AgentConfirmationRejected {
		return item, nil
	}
	if item.Status != model.AgentConfirmationPending {
		return nil, NewAppError(409, "确认请求已关闭，无法拒绝")
	}
	changed, err := s.repo.UpdateAgentConfirmationStatus(item.ID, model.AgentConfirmationPending, model.AgentConfirmationRejected)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, NewAppError(409, "确认请求状态已变化，请刷新后重试")
	}
	return s.AgentConfirmationForUser(userID, id)
}

// PendingAgentConfirmations 返回用户当前待确认的生成请求（网关挂起期间用户在网站可见）。
func (s *Service) PendingAgentConfirmations(userID string, limit int) ([]model.AgentConfirmation, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, BadAuthRequest("参数不完整")
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	if err := s.repo.ExpireAgentConfirmations(); err != nil {
		return nil, err
	}
	return s.repo.ListPendingAgentConfirmations(userID, limit)
}

// InternalAgentConfirmation 供网关轮询：返回确认状态（网关据此决定执行/退款）。
func (s *Service) InternalAgentConfirmation(id string) (*model.AgentConfirmation, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, BadAuthRequest("参数不完整")
	}
	item, err := s.repo.AgentConfirmationByID(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, NotFound("确认请求不存在")
	}
	if err != nil {
		return nil, err
	}
	// 惰性过期：超过有效期仍 pending 的记录按 expired 返回。
	if item.Status == model.AgentConfirmationPending && time.Now().After(item.ExpiresAt) {
		if _, err := s.repo.UpdateAgentConfirmationStatus(item.ID, model.AgentConfirmationPending, model.AgentConfirmationExpired); err != nil {
			return nil, err
		}
		item.Status = model.AgentConfirmationExpired
	}
	return item, nil
}
