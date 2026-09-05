package handler

import (
	"strings"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterApiKeyRoutes 网站自助签发的外部 API Key 管理 API：
//   - POST   /api/api-keys           {name} → 签发 key（明文仅此一次返回）
//   - GET    /api/api-keys           列出当前用户 key（不含明文）
//   - DELETE /api/api-keys/:id       删除（停用）当前用户 key
//
// 这些 key 供外部智能体/网关经 MCP 调用画布：网关 remote 模式经
// /api/internal/api-keys/verify 校验拿到真实 userId 后按网站钱包计费。
func RegisterApiKeyRoutes(r *gin.RouterGroup, svc *service.Service) {
	g := r.Group("/api-keys")

	// 签发
	g.POST("", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, 400, err)
			return
		}
		plain, item, err := svc.CreateApiKey(user.ID, req.Name)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{
			"id":        item.ID,
			"name":      item.Name,
			"key":       plain, // 仅此一次
			"prefix":    item.Prefix,
			"status":    string(item.Status),
			"createdAt": item.CreatedAt,
		})
	})

	// 列表
	g.GET("", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		items, err := svc.ListApiKeys(user.ID)
		if err != nil {
			failService(c, err)
			return
		}
		views := make([]gin.H, 0, len(items))
		for i := range items {
			views = append(views, apiKeyView(&items[i]))
		}
		ok(c, gin.H{"items": views})
	})

	// 删除（软删）
	g.DELETE("/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		if err := svc.DeleteApiKey(user.ID, strings.TrimSpace(c.Param("id"))); err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"deleted": true})
	})
}

// RegisterInternalApiKeyRoutes 网关 remote 校验 API Key 的 internal 接口（X-Internal-Token 保护）。
func RegisterInternalApiKeyRoutes(api *gin.RouterGroup, svc *service.Service) {
	g := api.Group("/internal/api-keys")
	g.Use(InternalTokenAuth())

	// POST /api/internal/api-keys/verify  {key: "ak_..."} → {keyId,userId,displayName,enabled,...}
	// 网关 authenticateByKey 据此拿到真实 userId；不存在/停用一律 401 fail-closed。
	g.POST("/verify", func(c *gin.Context) {
		var req struct {
			Key string `json:"key"`
		}
		if !decodeInternalJSON(c, &req) {
			return
		}
		res, err := svc.VerifyApiKey(req.Key)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, res)
	})
}

func apiKeyView(k *model.ApiKey) gin.H {
	return gin.H{
		"id":         k.ID,
		"name":       k.Name,
		"prefix":     k.Prefix,
		"status":     string(k.Status),
		"lastUsedAt": k.LastUsedAt,
		"createdAt":  k.CreatedAt,
	}
}
