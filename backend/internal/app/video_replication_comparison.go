package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
	"yingce/backend/internal/model"
	"yingce/backend/internal/videocompare"
)

// Each API process admits one export. This is independent of paid generation and
// does not change the project, outputs, resources, tasks, or billing records.
var videoComparisonSlot = make(chan struct{}, 1)

type VideoComparisonDownload struct {
	Path    string
	Name    string
	cleanup func()
}

func (d *VideoComparisonDownload) Close() { d.cleanup() }

func (s *Service) PrepareVideoReplicationComparison(ctx context.Context, userID, projectID, outputID string) (*VideoComparisonDownload, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, Forbidden("请先登录")
	}
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return nil, err
	}
	outputs, err := s.repo.VideoReplicationOutputs(userID, projectID)
	if err != nil {
		return nil, err
	}
	var referenceID, generatedID string
	for _, asset := range assets {
		if asset.Category == model.VideoReplicationAssetReferenceVideo && (referenceID == "" || asset.IsPrimary) {
			referenceID = asset.ResourceID
		}
	}
	for _, output := range outputs {
		if output.ID == outputID && output.Status == "succeeded" {
			generatedID = output.ResourceID
			break
		}
	}
	if generatedID == "" {
		return nil, videoReplicationNotFound(gorm.ErrRecordNotFound)
	}
	if referenceID == "" {
		return nil, BadAuthRequest("缺少原视频，无法下载对比视频")
	}
	for _, id := range []string{referenceID, generatedID} {
		resource, err := s.Resource(userID, id)
		if err != nil {
			return nil, videoReplicationNotFound(err)
		}
		if !strings.HasPrefix(resource.MimeType, "video/") {
			return nil, BadAuthRequest("对比素材必须是视频")
		}
	}
	select {
	case videoComparisonSlot <- struct{}{}:
	default:
		return nil, NewAppError(http.StatusTooManyRequests, "对比视频正在合成，请稍后重试")
	}
	defer func() { <-videoComparisonSlot }()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	bin, err := renderFfmpegBinary()
	if err != nil {
		return nil, NewAppError(http.StatusServiceUnavailable, "暂时无法合成对比视频，请稍后重试")
	}
	dir, err := os.MkdirTemp("", "yingce-comparison-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "comparison.mp4")
	params := videoReplicationParameters(project)
	reference, generated := filepath.Join(dir, "reference"), filepath.Join(dir, "generated")
	err = func() error {
		if err := s.copyVideoComparisonResource(ctx, userID, referenceID, reference, videoReplicationMaxVideoBytes); err != nil {
			return err
		}
		if err := s.copyVideoComparisonResource(ctx, userID, generatedID, generated, 256<<20); err != nil {
			return err
		}
		left, err := probeVideoReplicationFile(ctx, reference)
		if err != nil {
			return err
		}
		right, err := probeVideoReplicationFile(ctx, generated)
		if err != nil {
			return err
		}
		return videocompare.Render(ctx, bin, reference, generated, path, videocompare.Options{
			Reference: videocompare.Media{Width: left.Width, Height: left.Height, DurationMs: left.DurationMs, HasAudio: left.HasAudio},
			Generated: videocompare.Media{Width: right.Width, Height: right.Height, DurationMs: right.DurationMs, HasAudio: right.HasAudio},
			StartMs:   params.TrimStartMs, EndMs: params.TrimEndMs,
		})
	}()
	if err != nil {
		cleanup()
		slog.Warn("video comparison export failed", "project", projectID, "error", err)
		return nil, NewAppError(http.StatusUnprocessableEntity, "对比视频合成失败，请确认原视频和成片可以播放后重试")
	}
	return &VideoComparisonDownload{Path: path, Name: fmt.Sprintf("%s-对比.mp4", project.Title), cleanup: cleanup}, nil
}

func (s *Service) copyVideoComparisonResource(ctx context.Context, userID, id, path string, limit int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resource, body, err := s.OpenResource(userID, id)
	if err != nil {
		return err
	}
	defer body.Close()
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	if resource.Size > limit {
		return BadAuthRequest("视频文件过大，无法合成对比视频")
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(body, limit+1))
	closeErr := file.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > limit {
		return BadAuthRequest("视频文件过大，无法合成对比视频")
	}
	return nil
}
