package handler

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// 服务令牌：Go 与 Node 共用同一个环境变量名。未配置或不匹配一律 fail-closed。
const (
	internalTokenHeader    = "X-Internal-Token"
	internalTokenEnv       = "CANVAS_INTERNAL_SERVICE_TOKEN"
	internalMaxBodyBytes   = 1 << 16 // 64 KiB，内部计费请求体很小
	internalDefaultScene   = "mcp"
	internalMaxToolLen     = 120
	internalMaxSceneLen    = 80
	internalMaxIdemLen     = 160
	internalMaxErrorLen    = 500
)

// InternalTokenAuth 保护 /api/internal：恒定时间比较；服务端未配置 token 时该组整体不可用。
func InternalTokenAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		want := strings.TrimSpace(os.Getenv(internalTokenEnv))
		got := strings.TrimSpace(c.GetHeader(internalTokenHeader))
		if want == "" || got == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
			writeFailure(c, http.StatusUnauthorized, http.StatusUnauthorized, "unauthorized")
			c.Abort()
			return
		}
		c.Next()
	}
}

// RegisterInternalRoutes 注册 MCP 网关使用的内部计费接口（账户查询 / 冻结 / 结算 / 退款）。
// 统一响应 envelope：{code,data,msg}，复用 ok / failService。
func RegisterInternalRoutes(api *gin.RouterGroup, svc *service.Service) {
	g := api.Group("/internal")
	g.Use(InternalTokenAuth())

	// GET /api/internal/accounts/:userId
	g.GET("/accounts/:userId", func(c *gin.Context) {
		userID := strings.TrimSpace(c.Param("userId"))
		acc, err := svc.InternalCreditAccount(userID)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{
			"userId":                userID,
			"availableMicrocredits": acc.AvailableMicrocredits,
			"reservedMicrocredits":  acc.ReservedMicrocredits,
			"version":               acc.Version,
		})
	})

	// POST /api/internal/accounts/:userId/reservations
	g.POST("/accounts/:userId/reservations", func(c *gin.Context) {
		userID := strings.TrimSpace(c.Param("userId"))
		var req struct {
			AmountMicrocredits int64  `json:"amountMicrocredits"`
			Tool               string `json:"tool"`
			Scene              string `json:"scene"`
			IdempotencyKey     string `json:"idempotencyKey"`
		}
		if !decodeInternalJSON(c, &req) {
			return
		}
		req.Tool = strings.TrimSpace(req.Tool)
		req.Scene = strings.TrimSpace(req.Scene)
		req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
		if req.Scene == "" {
			req.Scene = internalDefaultScene
		}
		if req.AmountMicrocredits <= 0 {
			fail(c, http.StatusBadRequest, errors.New("amountMicrocredits 必须是正整数"))
			return
		}
		if req.Tool == "" || len(req.Tool) > internalMaxToolLen {
			fail(c, http.StatusBadRequest, errors.New("tool 非法或过长"))
			return
		}
		if len(req.Scene) > internalMaxSceneLen || req.IdempotencyKey == "" || len(req.IdempotencyKey) > internalMaxIdemLen {
			fail(c, http.StatusBadRequest, errors.New("scene/idempotencyKey 非法或过长"))
			return
		}
		order, err := svc.ReserveInternalBilling(userID, req.AmountMicrocredits, req.Tool, req.Scene, req.IdempotencyKey)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, internalOrderView(order))
	})

	// POST /api/internal/accounts/:userId/reservations/:orderId/settle
	g.POST("/accounts/:userId/reservations/:orderId/settle", func(c *gin.Context) {
		terminal := decodeTerminal(c)
		if terminal == nil {
			return
		}
		order, err := svc.SettleInternalBilling(strings.TrimSpace(c.Param("userId")), strings.TrimSpace(c.Param("orderId")), terminal.IdempotencyKey)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, internalOrderView(order))
	})

	// POST /api/internal/accounts/:userId/reservations/:orderId/refund
	g.POST("/accounts/:userId/reservations/:orderId/refund", func(c *gin.Context) {
		terminal := decodeTerminal(c)
		if terminal == nil {
			return
		}
		errText := strings.TrimSpace(terminal.Error)
		if len(errText) > internalMaxErrorLen {
			errText = errText[:internalMaxErrorLen]
		}
		order, err := svc.RefundInternalBilling(strings.TrimSpace(c.Param("userId")), strings.TrimSpace(c.Param("orderId")), terminal.IdempotencyKey, errText)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, internalOrderView(order))
	})
}

type internalTerminalRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	Error          string `json:"error"`
}

// decodeTerminal 解析 settle/refund 请求体并校验幂等键；失败时已写响应，返回 nil。
func decodeTerminal(c *gin.Context) *internalTerminalRequest {
	var req internalTerminalRequest
	if !decodeInternalJSON(c, &req) {
		return nil
	}
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.IdempotencyKey == "" || len(req.IdempotencyKey) > internalMaxIdemLen {
		fail(c, http.StatusBadRequest, errors.New("idempotencyKey 非法或为空"))
		return nil
	}
	return &req
}

// internalOrderView 是内部计费订单的最小对外视图，只暴露契约要求的字段。
func internalOrderView(order *model.BillingOrder) gin.H {
	return gin.H{
		"orderId":            order.ID,
		"status":             string(order.Status),
		"amountMicrocredits": order.AmountMicrocredits,
		"idempotencyKey":     order.IdempotencyKey,
	}
}

// decodeInternalJSON 严格解码：限制体积、拒绝未知字段、拒绝多段 JSON；金额用 int64，天然拒绝浮点。
func decodeInternalJSON(c *gin.Context, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, internalMaxBodyBytes+1))
	if err != nil {
		fail(c, http.StatusBadRequest, errors.New("请求体读取失败"))
		return false
	}
	if len(raw) == 0 {
		fail(c, http.StatusBadRequest, errors.New("请求体不能为空"))
		return false
	}
	if len(raw) > internalMaxBodyBytes {
		fail(c, http.StatusBadRequest, errors.New("请求体过大"))
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		fail(c, http.StatusBadRequest, errors.New("请求 JSON 非法"))
		return false
	}
	if dec.More() {
		fail(c, http.StatusBadRequest, errors.New("请求体包含多余内容"))
		return false
	}
	return true
}
