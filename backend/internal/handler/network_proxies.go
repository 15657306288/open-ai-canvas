package handler

// 网络代理 REST API：代理列表 CRUD + 测 IP + 账号绑定。

import (
	"net/http"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterNetworkProxyRoutes 注册 /network-proxies 路由（需登录）。
func RegisterNetworkProxyRoutes(api *gin.RouterGroup, svc *service.Service) {
	group := api.Group("/network-proxies")
	group.Use(func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "未登录", "reason": "unauthorized"})
			return
		}
		if err := svc.RequireAdmin(user); err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "msg": "仅管理员可管理账号池代理", "reason": "forbidden"})
			return
		}
		c.Set("currentUser", user)
		c.Next()
	})

	group.GET("", func(c *gin.Context) {
		proxies, err := svc.NetworkProxyList()
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, gin.H{"proxies": proxies})
	})

	group.POST("", func(c *gin.Context) {
		var req service.NetworkProxyUpsertRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		proxy, err := svc.NetworkProxyCreate(req)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, gin.H{"proxy": proxy})
	})

	group.PATCH("/:id", func(c *gin.Context) {
		var req service.NetworkProxyUpsertRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		proxy, err := svc.NetworkProxyUpdate(c.Param("id"), req)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, gin.H{"proxy": proxy})
	})

	// 连通性测试：通过代理访问 IP 回显服务。
	group.POST("/:id/test", func(c *gin.Context) {
		result, err := svc.NetworkProxyTest(c.Param("id"))
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, result)
	})

	group.DELETE("/:id", func(c *gin.Context) {
		if err := svc.NetworkProxyDelete(c.Param("id")); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, gin.H{"ok": true})
	})

	// 批量绑定：POST /network-proxies/assign {poolType, ids, proxyId}
	group.POST("/assign", func(c *gin.Context) {
		var req service.NetworkProxyAssignRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		affected, err := svc.NetworkProxyAssign(req)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		ok(c, gin.H{"affected": affected})
	})
}
