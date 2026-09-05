package model

import (
	"time"

	"gorm.io/gorm"
)

// AgentConfirmationStatus 外部 Agent 生成确认请求的状态。
type AgentConfirmationStatus string

const (
	AgentConfirmationPending  AgentConfirmationStatus = "pending"
	AgentConfirmationApproved AgentConfirmationStatus = "approved"
	AgentConfirmationRejected AgentConfirmationStatus = "rejected"
	AgentConfirmationExpired  AgentConfirmationStatus = "expired"
)

// AgentConfirmation 外部智能体/客户端经网关调用画布生成类工具（图片/视频/音频/文本）前，
// 必须创建一条用户确认请求：用户批准后才真正执行生成并扣费，拒绝/超时则退款。
// 网关在 reserve 冻结后挂起轮询本记录状态；用户在网站/画布确认。
type AgentConfirmation struct {
	ID                 string                  `json:"id" gorm:"primaryKey;size:36"`
	UserID             string                  `json:"userId" gorm:"size:36;index:idx_agent_confirmations_user_status,priority:1"`
	OrderID            string                  `json:"orderId" gorm:"size:36;index;not null"`
	Tool               string                  `json:"tool" gorm:"size:120;not null"`
	ModelKey           string                  `json:"modelKey" gorm:"size:120"`
	AmountMicrocredits int64                   `json:"amountMicrocredits" gorm:"not null"`
	PromptSummary      string                  `json:"promptSummary" gorm:"size:500"`
	Status             AgentConfirmationStatus `json:"status" gorm:"size:16;not null;index:idx_agent_confirmations_user_status,priority:2;index:idx_agent_confirmations_status"`
	IdempotencyKey     string                  `json:"idempotencyKey" gorm:"size:160;uniqueIndex:idx_agent_confirmations_idem,where:deleted_at IS NULL"`
	ExpiresAt          time.Time               `json:"expiresAt"`
	CreatedAt          time.Time               `json:"createdAt"`
	UpdatedAt          time.Time               `json:"updatedAt"`
	DeletedAt          gorm.DeletedAt          `json:"-" gorm:"index"`
}
