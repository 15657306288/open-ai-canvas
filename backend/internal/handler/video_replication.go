package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"yingce/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterVideoReplicationRoutes 注册影棚视频「商品视频复刻」接口；归属与功能开关都在 service 校验。
func RegisterVideoReplicationRoutes(r *gin.RouterGroup, svc *service.Service) {
	group := r.Group("/video-replication/projects")
	group.POST("/:id/outputs/:outputId/comparison-download", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		download, err := svc.PrepareVideoReplicationComparison(c.Request.Context(), user.ID, c.Param("id"), c.Param("outputId"))
		if err != nil {
			failService(c, err)
			return
		}
		defer download.Close()
		c.Header("Cache-Control", "private, no-store")
		c.FileAttachment(download.Path, download.Name)
	})
	respond := func(c *gin.Context, view any, err error) {
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, view)
	}
	group.GET("", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		limit, err := parsePositiveQueryInt(c.Query("pageSize"), 30)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		projects, err := svc.VideoReplicationProjects(user.ID, limit)
		respond(c, gin.H{"projects": projects}, err)
	})
	group.POST("", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.VideoReplicationProjectInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.CreateVideoReplicationProject(user.ID, req)
		respond(c, view, err)
	})
	group.GET("/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		view, err := svc.VideoReplicationProject(user.ID, c.Param("id"))
		respond(c, view, err)
	})
	group.PATCH("/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.VideoReplicationProjectInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.UpdateVideoReplicationProject(user.ID, c.Param("id"), req)
		respond(c, view, err)
	})
	group.DELETE("/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		respond(c, gin.H{"id": c.Param("id")}, svc.DeleteVideoReplicationProject(user.ID, c.Param("id")))
	})
	group.POST("/:id/assets", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.VideoReplicationAssetInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.AddVideoReplicationAsset(user.ID, c.Param("id"), req)
		respond(c, view, err)
	})
	group.PUT("/:id/assets/order", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.VideoReplicationReorderInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.ReorderVideoReplicationAssets(user.ID, c.Param("id"), req)
		respond(c, view, err)
	})
	group.DELETE("/:id/assets/:assetId", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		view, err := svc.DeleteVideoReplicationAsset(user.ID, c.Param("id"), c.Param("assetId"))
		respond(c, view, err)
	})
	// 粘贴抖音 / 小红书 / 快手 / 视频号分享链接，解析并下载为参考视频。
	group.POST("/:id/link", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "uploads:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		var req service.VideoReplicationLinkInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.ImportVideoReplicationLink(c.Request.Context(), user.ID, c.Param("id"), req)
		respond(c, view, err)
	})
	// 带进度的链接导入（SSE）：识别平台 → 解析 → 下载 / 合并 → 校验 → 转存 → 登记素材库，最后推送 result 或 error 事件。
	group.POST("/:id/link/stream", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "uploads:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		var req service.VideoReplicationLinkInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Header("Cache-Control", "no-cache, no-store, no-transform")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)
		var mu sync.Mutex
		write := func(event service.VideoLinkEvent) {
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Event, data)
			c.Writer.Flush()
		}
		// stop 在持锁时关闭：心跳协程拿到锁后先确认请求未结束再写，处理函数返回后不会再写响应。
		stop := make(chan struct{})
		defer func() {
			mu.Lock()
			close(stop)
			mu.Unlock()
		}()
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-c.Request.Context().Done():
					return
				case <-ticker.C:
					mu.Lock()
					select {
					case <-stop:
						mu.Unlock()
						return
					default:
					}
					_, _ = fmt.Fprint(c.Writer, ": keepalive\n\n")
					c.Writer.Flush()
					mu.Unlock()
				}
			}
		}()
		result, err := svc.ImportVideoReplicationLinkWithProgress(c.Request.Context(), user.ID, c.Param("id"), req, write)
		if err != nil {
			write(service.VideoLinkEvent{Event: "error", Error: service.VideoLinkErrorView(err)})
			return
		}
		write(service.VideoLinkEvent{Event: "result", Result: result})
	})
	group.POST("/:id/analyze", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "tasks:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		view, err := svc.StartVideoReplicationAnalysis(user.ID, c.Param("id"))
		respond(c, view, err)
	})
	group.POST("/:id/start", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "tasks:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		var req service.VideoReplicationStartInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.StartVideoReplication(user.ID, c.Param("id"), req)
		respond(c, view, err)
	})
	group.PUT("/:id/storyboard", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req struct {
			Revision   int                                `json:"revision"`
			Storyboard service.VideoReplicationStoryboard `json:"storyboard"`
		}
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.UpdateVideoReplicationStoryboard(user.ID, c.Param("id"), req.Revision, req.Storyboard)
		respond(c, view, err)
	})
	group.POST("/:id/generate", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "tasks:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		var req service.VideoReplicationGenerateInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.GenerateVideoReplication(user.ID, c.Param("id"), req, TraceID(c))
		respond(c, view, err)
	})
	group.POST("/:id/plan", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.VideoReplicationPlanInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.ConfirmVideoReplicationPlan(user.ID, c.Param("id"), req)
		respond(c, view, err)
	})
	for path, paused := range map[string]bool{"/:id/pause": true, "/:id/resume": false} {
		paused := paused
		group.POST(path, func(c *gin.Context) {
			user, err := currentUser(c, svc)
			if err != nil {
				failService(c, err)
				return
			}
			view, err := svc.PauseVideoReplication(user.ID, c.Param("id"), paused)
			respond(c, view, err)
		})
	}
	group.PUT("/:id/voice-cast", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.VideoReplicationVoiceCast
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.UpdateVideoReplicationVoiceCast(user.ID, c.Param("id"), req)
		respond(c, view, err)
	})
	group.POST("/:id/voice-preview", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "tasks:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		var req service.VideoReplicationVoicePreviewInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		task, err := svc.PreviewVideoReplicationVoice(user.ID, c.Param("id"), req)
		respond(c, task, err)
	})
	group.POST("/:id/runs/:runId/select", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		view, err := svc.SelectVideoReplicationRun(user.ID, c.Param("id"), c.Param("runId"))
		respond(c, view, err)
	})
	group.POST("/:id/cancel", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		view, err := svc.CancelVideoReplication(c.Request.Context(), user.ID, c.Param("id"))
		respond(c, view, err)
	})
	group.POST("/:id/compose", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.VideoReplicationComposeInput
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		view, err := svc.ComposeVideoReplication(user.ID, c.Param("id"), req)
		respond(c, view, err)
	})
}

// RegisterVideoReplicationAdminRoutes 电商视频 Agent 后台：分析模型、开放的视频模型与参数、运行状态、项目监控。
func RegisterVideoReplicationAdminRoutes(r *gin.RouterGroup, svc *service.Service) {
	r.GET("/video-replication/config", func(c *gin.Context) {
		if _, err := currentUser(c, svc); err != nil {
			failService(c, err)
			return
		}
		config, err := svc.VideoReplicationConfig()
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, config)
	})
	r.GET("/admin/video-replication", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		status, err := svc.AdminVideoReplicationStatus(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, status)
	})
	r.PUT("/admin/video-replication", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.VideoReplicationSetting
		if err := decodeWorkbenchJSON(c, &req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		status, err := svc.UpdateVideoReplicationSetting(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, status)
	})
	// 参考视频链接解析：用户端看平台能力，后台看平台健康、熔断与调用成本。
	r.GET("/video-parser/platforms", func(c *gin.Context) {
		if _, err := currentUser(c, svc); err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"platforms": svc.VideoLinkPlatforms()})
	})
	r.GET("/admin/video-parser/health", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		platforms, err := svc.AdminVideoLinkParserHealth(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"platforms": platforms})
	})
	r.GET("/admin/video-replication/overview", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		days := 7
		if _, scanErr := fmt.Sscan(c.DefaultQuery("days", "7"), &days); scanErr != nil {
			days = 7
		}
		overview, err := svc.AdminVideoReplicationOverview(user, days)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, overview)
	})
	r.GET("/admin/video-replication/projects/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		view, err := svc.AdminVideoReplicationProject(user, c.Param("id"))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, view)
	})
	r.GET("/admin/video-replication/projects", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		limit, err := parsePositiveQueryInt(c.Query("pageSize"), 50)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		projects, err := svc.AdminVideoReplicationProjects(user, limit)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"projects": projects})
	})
}
