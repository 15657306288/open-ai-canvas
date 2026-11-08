package app

import (
	"context"
	"log/slog"
	"time"
)

const (
	videoReplicationSyncInterval = 15 * time.Second
	videoReplicationSyncBatch    = 200
	// 超过这个时间仍处于生成中的项目不再由后台推进，用户打开作品区时照常同步。
	videoReplicationSyncWindow   = 7 * 24 * time.Hour
	videoReplicationSyncLeaseTTL = 2 * time.Minute
)

// startVideoReplicationSyncWorker 定期推进生成中 / 合成中的商品视频复刻项目：
// 片段完成后自动拼接、质量检查都不再依赖用户停留在页面上轮询。
func (s *Service) startVideoReplicationSyncWorker() {
	s.runWorkerLoop(func(ctx context.Context) {
		ticker := time.NewTicker(videoReplicationSyncInterval)
		defer ticker.Stop()
		for {
			s.syncActiveVideoReplications(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (s *Service) syncActiveVideoReplications(ctx context.Context) {
	if s.IsDraining() {
		return
	}
	projects, err := s.repo.ActiveVideoReplicationProjects(time.Now().Add(-videoReplicationSyncWindow), videoReplicationSyncBatch)
	if err != nil {
		slog.Warn("video replication sync load failed", "error", err)
		return
	}
	for index := range projects {
		if ctx.Err() != nil || s.IsDraining() {
			return
		}
		project := &projects[index]
		// 多个后端实例同时运行：同一项目同一时刻只由一个实例推进。前端轮询仍可能并发同步，
		// 提交与合成本身按幂等键去重，这里的租约只减少重复读写。
		release := func() {}
		if s.coordinator != nil {
			lease, acquired, err := s.coordinator.Acquire(ctx, "video-replication-sync:"+project.ID, 1, videoReplicationSyncLeaseTTL)
			if err != nil || !acquired {
				continue
			}
			release = lease
		}
		if err := s.syncVideoReplication(ctx, project); err != nil {
			slog.Warn("video replication background sync failed", "projectId", project.ID, "error", err)
		}
		release()
	}
}
