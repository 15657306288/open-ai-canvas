package handler

// 豆包账号池 REST API。

import (
	"net/http"
	"strings"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterDoubaoAccountRoutes 注册 /doubao-accounts 路由（需登录）。
func RegisterDoubaoAccountRoutes(api *gin.RouterGroup, svc *service.Service) {
	group := api.Group("/doubao-accounts")
	group.Use(func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "未登录", "reason": "unauthorized"})
			return
		}
		if err := svc.RequireAdmin(user); err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "msg": "仅管理员可管理全局豆包账号池", "reason": "forbidden"})
			return
		}
		c.Set("currentUser", user)
		c.Next()
	})

	group.GET("", func(c *gin.Context) {
		status, err := svc.DoubaoPoolStatus(c.Query("site"))
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		ok(c, status)
	})

	// 扫码登录：启动本机浏览器登录会话（同一站点同一时间仅一个）。
	group.POST("/qr/start", func(c *gin.Context) {
		var req struct {
			Site string `json:"site"`
		}
		_ = c.ShouldBindJSON(&req)
		view, started := svc.DoubaoQrStart(req.Site)
		ok(c, gin.H{"session": view, "started": started})
	})

	group.GET("/qr/status", func(c *gin.Context) {
		ok(c, gin.H{"session": svc.DoubaoQrStatus(c.Query("site"))})
	})

	group.POST("/qr/cancel", func(c *gin.Context) {
		var req struct {
			Site string `json:"site"`
		}
		_ = c.ShouldBindJSON(&req)
		if err := svc.DoubaoQrCancel(req.Site); err != nil {
			fail(c, http.StatusConflict, err)
			return
		}
		ok(c, gin.H{"canceled": true})
	})

	group.POST("", func(c *gin.Context) {
		var req service.DoubaoUpsertRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Cookie) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "msg": "Cookie/sessionid 不能为空", "reason": "invalid_request"})
			return
		}
		view, err := svc.DoubaoUpsertAccount(req)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, gin.H{"account": view})
	})

	group.POST("/bulk-import", func(c *gin.Context) {
		var req service.DoubaoBulkImportRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		result, err := svc.DoubaoBulkImport(req)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		status, err := svc.DoubaoPoolStatus(req.Site)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		ok(c, gin.H{"result": result, "status": status})
	})

	group.POST("/batch", func(c *gin.Context) {
		var req service.DoubaoBatchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		affected, err := svc.DoubaoBatchOp(req)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		status, err := svc.DoubaoPoolStatus(req.Site)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		ok(c, gin.H{"affected": affected, "status": status})
	})

	group.POST("/clear-cooldowns", func(c *gin.Context) {
		cleared, err := svc.DoubaoClearAllCooldowns()
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		ok(c, gin.H{"cleared": cleared})
	})

	group.POST("/pick", func(c *gin.Context) {
		var req struct {
			PreferID string `json:"preferId"`
		}
		_ = c.ShouldBindJSON(&req)
		cred, err := svc.DoubaoPickAccount(req.PreferID)
		if err != nil {
			fail(c, http.StatusConflict, err)
			return
		}
		ok(c, gin.H{"account": cred})
	})

	// 文生图（走账号池，账号类失败自动切换下一个账号）。豆包上游最长 5 分钟。
	group.POST("/generate/image", func(c *gin.Context) {
		var req service.DoubaoGenerateImageRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		result, err := svc.DoubaoGenerateImage(c.Request.Context(), req)
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		ok(c, result)
	})

	// AI 图层拆分（走账号池）：一次请求拆 N 层，每层各发一次图生图，单层失败只损失该层。
	group.POST("/generate/layers", func(c *gin.Context) {
		var req service.DoubaoGenerateLayersRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		result, err := svc.DoubaoGenerateLayers(c.Request.Context(), req)
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		ok(c, result)
	})

	// 文生视频（走账号池，同步等待出片，最长约 12 分钟）。
	group.POST("/generate/video", func(c *gin.Context) {
		var req service.DoubaoGenerateVideoRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		result, err := svc.DoubaoGenerateVideo(c.Request.Context(), req)
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		ok(c, result)
	})

	group.PATCH("/:id", func(c *gin.Context) {
		var req service.DoubaoUpdateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.DoubaoUpdateAccount(c.Param("id"), req)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, gin.H{"account": view})
	})

	group.DELETE("/:id", func(c *gin.Context) {
		if err := svc.DoubaoRemoveAccount(c.Param("id")); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, gin.H{"ok": true})
	})

	group.POST("/:id/mark-success", func(c *gin.Context) {
		if err := svc.DoubaoMarkSuccess(c.Param("id")); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, gin.H{"ok": true})
	})

	group.POST("/:id/mark-failed", func(c *gin.Context) {
		var req service.DoubaoMarkFailedRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		switched, next, err := svc.DoubaoMarkFailed(c.Param("id"), req)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, gin.H{"switched": switched, "next": next})
	})
}
