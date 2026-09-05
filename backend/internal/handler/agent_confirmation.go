package handler

import (
	"strings"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterAgentConfirmationRoutes 用户侧外部生成确认 API：
//   - GET  /api/agent-confirmations            当前用户待确认的生成请求
//   - POST /api/agent-confirmations/:id/approve 批准生成（网关据此执行并结算）
//   - POST /api/agent-confirmations/:id/reject  拒绝生成（网关据此退款）
//
// 外部智能体/客户端经网关调用生成类工具时，网关 reserve 冻结后挂起，等待用户在此确认。
func RegisterAgentConfirmationRoutes(r *gin.RouterGroup, svc *service.Service) {
	g := r.Group("/agent-confirmations")

	// 当前用户待确认列表（网关挂起期间用户在网站/画布可见并操作）。
	g.GET("", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		items, err := svc.PendingAgentConfirmations(user.ID, 20)
		if err != nil {
			failService(c, err)
			return
		}
		views := make([]gin.H, 0, len(items))
		for index := range items {
			views = append(views, confirmationView(&items[index]))
		}
		ok(c, gin.H{"items": views})
	})

	// 批准：仅本人、仅 pending；成功后网关轮询到 approved 才会真正执行生成。
	g.POST("/:id/approve", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		conf, err := svc.ApproveAgentConfirmation(user.ID, strings.TrimSpace(c.Param("id")))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, confirmationView(conf))
	})

	// 拒绝：仅本人、仅 pending；成功后网关退款。
	g.POST("/:id/reject", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		conf, err := svc.RejectAgentConfirmation(user.ID, strings.TrimSpace(c.Param("id")))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, confirmationView(conf))
	})
}

func confirmationView(conf *model.AgentConfirmation) gin.H {
	return gin.H{
		"id":                 conf.ID,
		"tool":               conf.Tool,
		"modelKey":           conf.ModelKey,
		"amountMicrocredits": conf.AmountMicrocredits,
		"promptSummary":      conf.PromptSummary,
		"status":             string(conf.Status),
		"createdAt":          conf.CreatedAt,
		"expiresAt":          conf.ExpiresAt,
	}
}
