package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"

	"yingce/backend/internal/model"
	"yingce/backend/internal/videoparser"
)

// 参考视频链接导入：videoparser 识别平台并解析 → 下载（必要时 FFmpeg 合并音视频）→ ffprobe 校验 →
// UploadResourceFile 转存到当前存储→ 登记素材库素材 → 替换项目的参考视频。
// 后续分析与生成只使用资源 ID（/api/resources/:id/file），不使用会过期的上游地址。

const videoLinkImportTimeout = 6 * time.Minute

// VideoLinkEvent 是导入过程推送给前端的进度事件（SSE）。
type VideoLinkEvent struct {
	Event         string                 `json:"event"`
	Stage         string                 `json:"stage,omitempty"`
	Platform      string                 `json:"platform,omitempty"`
	PlatformLabel string                 `json:"platformLabel,omitempty"`
	Progress      float64                `json:"progress,omitempty"`
	Message       string                 `json:"message,omitempty"`
	Title         string                 `json:"title,omitempty"`
	DurationMs    int64                  `json:"durationMs,omitempty"`
	Result        *VideoLinkImportResult `json:"result,omitempty"`
	Error         *VideoLinkImportError  `json:"error,omitempty"`
}

// VideoLinkSource 记录素材来源：平台、作品 ID、标题、作者与 ffprobe 读取的真实媒体参数。
type VideoLinkSource struct {
	Platform      string  `json:"platform"`
	PlatformLabel string  `json:"platformLabel"`
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Author        string  `json:"author,omitempty"`
	SourceURL     string  `json:"sourceUrl"`
	DurationMs    int64   `json:"durationMs"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	FPS           float64 `json:"fps"`
	HasAudio      bool    `json:"hasAudio"`
	Provider      string  `json:"provider"`
}

// VideoLinkImportResult：resourceId 是转存后的资源，assetId 是素材库素材，referenceAssetId 是项目里的参考视频素材。
type VideoLinkImportResult struct {
	Project          *VideoReplicationProjectView `json:"project"`
	Source           VideoLinkSource              `json:"source"`
	ResourceID       string                       `json:"resourceId"`
	AssetID          string                       `json:"assetId"`
	ReferenceAssetID string                       `json:"referenceAssetId"`
	Reused           bool                         `json:"reused"`
}

// VideoLinkImportError 是给前端的失败说明；code 来自解析服务的统一错误码。
type VideoLinkImportError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Platform  string `json:"platform,omitempty"`
	Status    int    `json:"status"`
}

func (s *Service) videoLinkParser() *videoparser.Service {
	s.videoLinkParserOnce.Do(func() {
		if s.videoLinkParserSvc != nil {
			return
		}
		opts := videoparser.OptionsFromEnv()
		if path, err := renderFfmpegBinary(); err == nil {
			opts.FFmpegPath = path
		}
		opts.MaxBytes = videoReplicationMaxVideoBytes
		opts.MaxDurationMs = videoReplicationMaxSourceMs
		s.videoLinkParserSvc = videoparser.NewDefault(opts)
	})
	return s.videoLinkParserSvc
}

// WarmVideoLinkParser 在进程启动后初始化链接解析服务并预热抖音解析用的无头浏览器；CANVAS_VIDEO_PARSER_WARMUP=0 时跳过。
func (s *Service) WarmVideoLinkParser() {
	if strings.TrimSpace(os.Getenv("CANVAS_VIDEO_PARSER_WARMUP")) == "0" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.videoLinkParser().WarmUp(ctx)
}

// VideoLinkPlatforms 是用户端的平台能力列表（不含运行统计）。
func (s *Service) VideoLinkPlatforms() []videoparser.PlatformStatus {
	return s.videoLinkParser().Platforms(false)
}

// AdminVideoLinkParserHealth 是后台的平台健康、供应商熔断与调用成本统计。
func (s *Service) AdminVideoLinkParserHealth(actor *model.User) ([]videoparser.PlatformStatus, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	return s.videoLinkParser().Platforms(true), nil
}

// ImportVideoReplicationLink 是同步接口（保留给旧前端）；新前端用带进度的 ImportVideoReplicationLinkWithProgress。
func (s *Service) ImportVideoReplicationLink(ctx context.Context, userID, projectID string, input VideoReplicationLinkInput) (*VideoReplicationProjectView, error) {
	result, err := s.ImportVideoReplicationLinkWithProgress(ctx, userID, projectID, input, nil)
	if err != nil {
		return nil, err
	}
	return result.Project, nil
}

// ImportVideoReplicationLinkWithProgress 解析分享链接，把视频转存为资源、登记到素材库，并替换项目的参考视频。
func (s *Service) ImportVideoReplicationLinkWithProgress(ctx context.Context, userID, projectID string, input VideoReplicationLinkInput, emit func(VideoLinkEvent)) (*VideoLinkImportResult, error) {
	send := func(event VideoLinkEvent) {
		if emit != nil {
			if event.Event == "" {
				event.Event = "stage"
			}
			emit(event)
		}
	}
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if project.Status == model.VideoReplicationStatusAnalyzing {
		return nil, BadAuthRequest("参考视频分析中，请等待分析完成后再调整素材")
	}
	parser := s.videoLinkParser()
	send(VideoLinkEvent{Stage: "detecting", Message: "正在识别平台"})
	link, err := parser.Identify(input.URL)
	if err != nil {
		return nil, videoLinkAppError(err)
	}
	info := link.Platform.Info()
	send(VideoLinkEvent{Stage: "detected", Platform: info.Key, PlatformLabel: info.Label, Message: "已识别：" + info.Label})
	ctx, cancel := context.WithTimeout(ctx, videoLinkImportTimeout)
	defer cancel()
	relay := func(stage videoparser.Stage) {
		send(VideoLinkEvent{Stage: stage.Name, Platform: stage.Platform, PlatformLabel: stage.PlatformLabel, Progress: stage.Progress, Message: stage.Message})
	}
	parsed, err := parser.Parse(ctx, link, relay)
	if err != nil {
		return nil, videoLinkAppError(err)
	}
	meta := parsed.Metadata()
	send(VideoLinkEvent{Stage: "parsed", Platform: meta.Platform, PlatformLabel: meta.PlatformLabel, Title: meta.Title, DurationMs: meta.DurationMs, Message: "已解析作品信息"})
	identity := videoLinkIdentity(meta)
	resource, probe, reused, err := s.videoLinkResource(ctx, userID, parser, parsed, identity, relay, send)
	if err != nil {
		return nil, videoLinkAppError(err)
	}
	source := VideoLinkSource{Platform: meta.Platform, PlatformLabel: meta.PlatformLabel, ID: meta.ID, Title: meta.Title, Author: meta.Author, SourceURL: meta.SourceURL,
		DurationMs: probe.DurationMs, Width: probe.Width, Height: probe.Height, FPS: probe.FPS, HasAudio: probe.HasAudio, Provider: parsed.Provider}
	send(VideoLinkEvent{Stage: "registering", Platform: meta.Platform, PlatformLabel: meta.PlatformLabel, Progress: 1, Message: "正在登记到素材库"})
	assetID, err := s.registerVideoLinkAsset(userID, resource, source, identity)
	if err != nil {
		return nil, err
	}
	view, err := s.replaceVideoReplicationReference(userID, projectID, resource, videoLinkFileName(meta))
	if err != nil {
		return nil, err
	}
	result := &VideoLinkImportResult{Project: view, Source: source, ResourceID: resource.ID, AssetID: assetID, Reused: reused}
	for _, asset := range view.Assets {
		if asset.Category == model.VideoReplicationAssetReferenceVideo && asset.ResourceID == resource.ID {
			result.ReferenceAssetID = asset.ID
			// 以服务器对转存文件的 ffprobe 结果为准（复用已有资源时 Resource 上没有帧率与音轨信息）。
			result.Source.DurationMs, result.Source.Width, result.Source.Height = asset.DurationMs, asset.Width, asset.Height
			result.Source.FPS, result.Source.HasAudio = asset.FPS, asset.HasAudio
		}
	}
	send(VideoLinkEvent{Stage: "done", Platform: meta.Platform, PlatformLabel: meta.PlatformLabel, Progress: 1, Title: meta.Title, DurationMs: result.Source.DurationMs, Message: "视频已转存并登记到素材库"})
	return result, nil
}

// videoLinkIdentity 是同一用户导入同一作品时的幂等键：重复粘贴同一链接直接复用已转存的资源。
func videoLinkIdentity(meta videoparser.Metadata) string {
	key := meta.ID
	if key == "" {
		sum := sha256.Sum256([]byte(meta.SourceURL))
		key = hex.EncodeToString(sum[:12])
	}
	return "videolink:v1:" + meta.Platform + ":" + key
}

// videoLinkResource 返回作品对应的资源：已转存过直接复用，否则下载、校验后转存。
func (s *Service) videoLinkResource(ctx context.Context, userID string, parser *videoparser.Service, parsed *videoparser.Result, identity string, relay func(videoparser.Stage), send func(VideoLinkEvent)) (*model.Resource, videoReplicationProbe, bool, error) {
	meta := parsed.Metadata()
	existing, err := s.resourceForUploadKey(userID, normalizedResourceUploadKey([]string{identity}))
	if err != nil {
		return nil, videoReplicationProbe{}, false, err
	}
	if existing != nil && existing.Status == model.ResourceStatusReady && existing.Kind == "video" {
		send(VideoLinkEvent{Stage: "reused", Platform: meta.Platform, PlatformLabel: meta.PlatformLabel, Progress: 1, Message: "这条视频已转存过，直接使用素材库里的文件"})
		return existing, videoReplicationProbe{Width: existing.Width, Height: existing.Height, DurationMs: existing.DurationMs}, true, nil
	}
	dir, err := os.MkdirTemp("", "ecv-video-link-")
	if err != nil {
		return nil, videoReplicationProbe{}, false, err
	}
	defer os.RemoveAll(dir)
	file, err := parser.Download(ctx, parsed, dir, relay)
	if err != nil {
		return nil, videoReplicationProbe{}, false, err
	}
	send(VideoLinkEvent{Stage: "probing", Platform: meta.Platform, PlatformLabel: meta.PlatformLabel, Progress: 1, Message: "正在用 FFmpeg 校验视频"})
	probe, err := probeVideoReplicationFile(ctx, file.Path)
	if err != nil {
		if errors.Is(err, errMediaProbeUnavailable) {
			return nil, probe, false, &AppError{Status: http.StatusServiceUnavailable, Code: http.StatusServiceUnavailable, Message: "服务器缺少 ffprobe，暂时无法校验参考视频", Retryable: true}
		}
		return nil, probe, false, BadAuthRequest(videoReplicationBrokenVideoMessage + "（" + videoLinkText(err.Error(), 120) + "）")
	}
	if probe.DurationMs < videoReplicationMinDurationMs-videoReplicationDurationSlackMs {
		return nil, probe, false, BadAuthRequest(fmt.Sprintf("视频时长 %.1f 秒，短于 4 秒，不能作为参考视频", float64(probe.DurationMs)/1000))
	}
	if probe.DurationMs > videoReplicationMaxSourceMs+videoReplicationDurationSlackMs {
		return nil, probe, false, BadAuthRequest(fmt.Sprintf("视频时长 %.1f 秒，超过 %d 分钟，请先在本地剪短", float64(probe.DurationMs)/1000, videoReplicationMaxSourceMs/60000))
	}
	send(VideoLinkEvent{Stage: "uploading", Platform: meta.Platform, PlatformLabel: meta.PlatformLabel, Progress: 1, Message: "正在转存到云存储"})
	handle, err := os.Open(file.Path)
	if err != nil {
		return nil, probe, false, err
	}
	defer handle.Close()
	resource, err := s.UploadResourceFile(userID, videoLinkFileName(meta), file.Size, "video", probe.Width, probe.Height, probe.DurationMs, handle, identity)
	if err != nil {
		return nil, probe, false, err
	}
	if resource.Kind != "video" || resource.Status != model.ResourceStatusReady {
		return nil, probe, false, BadAuthRequest(videoReplicationBrokenVideoMessage)
	}
	return resource, probe, false, nil
}

// replaceVideoReplicationReference 把项目的参考视频换成这个资源；已经是同一资源时不重复登记。
// 参考视频最多 1 个，只能先移除旧的再登记新的；新视频登记失败时把旧参考视频放回项目，不让项目丢掉参考视频。
func (s *Service) replaceVideoReplicationReference(userID, projectID string, resource *model.Resource, name string) (*VideoReplicationProjectView, error) {
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return nil, err
	}
	var removed []model.VideoReplicationAsset
	for _, asset := range assets {
		if asset.Category != model.VideoReplicationAssetReferenceVideo {
			continue
		}
		if asset.ResourceID == resource.ID {
			return s.VideoReplicationProject(userID, projectID)
		}
		if _, err := s.DeleteVideoReplicationAsset(userID, projectID, asset.ID); err != nil {
			return nil, err
		}
		removed = append(removed, asset)
	}
	view, err := s.AddVideoReplicationAsset(userID, projectID, VideoReplicationAssetInput{Category: model.VideoReplicationAssetReferenceVideo, ResourceID: resource.ID, Name: name})
	if err != nil {
		for _, asset := range removed {
			if _, restoreErr := s.AddVideoReplicationAsset(userID, projectID, VideoReplicationAssetInput{Category: asset.Category, ResourceID: asset.ResourceID, Name: asset.Name}); restoreErr != nil {
				slog.Warn("video link import: restore previous reference video failed", "projectId", projectID, "resourceId", asset.ResourceID, "error", restoreErr)
			}
		}
		return nil, err
	}
	return view, nil
}

// registerVideoLinkAsset 在素材库登记一条视频素材（与生成作品入库同一格式），ID 由用户与作品决定，重复导入不会产生重复素材。
func (s *Service) registerVideoLinkAsset(userID string, resource *model.Resource, source VideoLinkSource, identity string) (string, error) {
	sum := sha256.Sum256([]byte("videolink:" + userID + ":" + identity))
	assetID := "videolink_" + hex.EncodeToString(sum[:16])
	storageKey := "resource:" + resource.ID
	if existing, err := s.repo.AssetForUser(userID, assetID); err == nil {
		var payload struct {
			Data struct {
				StorageKey string `json:"storageKey"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(existing.PayloadJSON), &payload) == nil && payload.Data.StorageKey == storageKey {
			return assetID, nil
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	title := videoLinkText(source.Title, 80)
	if title == "" {
		title = source.PlatformLabel + "视频"
	}
	now := time.Now()
	url := resourceFileURL(resource.ID)
	data := map[string]any{"storageKey": storageKey, "url": url, "mimeType": resource.MimeType, "bytes": resource.Size, "width": source.Width, "height": source.Height, "durationMs": source.DurationMs}
	metadata := map[string]any{"source": "video-link", "platform": source.Platform, "platformLabel": source.PlatformLabel, "sourceId": source.ID, "sourceUrl": source.SourceURL, "author": source.Author, "fps": source.FPS, "hasAudio": source.HasAudio, "parser": source.Provider}
	payload, err := json.Marshal(map[string]any{"id": assetID, "kind": "video", "category": model.AssetCategoryMaterial, "status": model.AssetVersionStatusConfirmed, "source": "链接解析", "title": title, "coverUrl": url, "tags": []string{"链接解析", source.PlatformLabel}, "createdAt": now.UTC().Format(time.RFC3339Nano), "updatedAt": now.UTC().Format(time.RFC3339Nano), "data": data, "metadata": metadata})
	if err != nil {
		return "", err
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		return "", err
	}
	usage, err := s.repo.UserStructuredStorageUsage(userID)
	if err != nil {
		return "", err
	}
	if err := validateStructuredStorageQuotaWithPolicy(usage, "asset", false, int64(len(payload)), policy.Resource); err != nil {
		return "", err
	}
	asset := &model.Asset{ID: assetID, UserID: userID, Kind: "video", Category: model.AssetCategoryMaterial, Status: model.AssetVersionStatusConfirmed, Title: title, PayloadJSON: string(payload), CreatedAt: now, UpdatedAt: now}
	if err := s.repo.UpsertAsset(asset); err != nil {
		return "", err
	}
	return assetID, nil
}

// videoLinkFileName 用「平台-标题.mp4」命名资源，去掉文件名里不安全的字符。
func videoLinkFileName(meta videoparser.Metadata) string {
	title := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|#%`, r) {
			return -1
		}
		return r
	}, meta.Title)
	title = videoLinkText(title, 40)
	if title == "" {
		title = "视频-" + time.Now().Format("0102-1504")
	}
	return meta.PlatformLabel + "-" + title + ".mp4"
}

// videoLinkText 整理来自平台的文本后再入库：控制字符换成空格、合并空白，并按字符截断。
// 不能按字节截断（clipText）：中文会被切成半个字符，PostgreSQL 以 invalid byte sequence（22021）拒绝写入。
func videoLinkText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(value, ""))
	value = strings.Join(strings.Fields(value), " ")
	if runes := []rune(value); len(runes) > limit {
		value = strings.TrimSpace(string(runes[:limit]))
	}
	return value
}

// videoLinkAppError 把解析服务的错误转成对外的 AppError；只暴露中文说明，内部原因保留在 Cause 里。
func videoLinkAppError(err error) error {
	var parsed *videoparser.Error
	if !errors.As(err, &parsed) {
		return err
	}
	status := http.StatusBadGateway
	switch parsed.Code {
	case videoparser.ErrInvalidLink, videoparser.ErrUnsupported, videoparser.ErrNotVideo, videoparser.ErrTooLong, videoparser.ErrTooLarge, videoparser.ErrBlocked, videoparser.ErrMedia, videoparser.ErrNeedsLogin, videoparser.ErrDRM:
		status = http.StatusBadRequest
	case videoparser.ErrNotFound:
		status = http.StatusNotFound
	case videoparser.ErrUnavailable, videoparser.ErrBusy:
		status = http.StatusServiceUnavailable
	case videoparser.ErrTimeout:
		status = http.StatusGatewayTimeout
	}
	appErr := WrapAppError(status, parsed.Message, parsed)
	appErr.Retryable = parsed.Retryable
	appErr.Details = map[string]any{"parserCode": string(parsed.Code), "platform": parsed.Platform}
	return appErr
}

// VideoLinkErrorView 生成 SSE 里的错误事件；非 AppError 的内部错误不透出原文。
func VideoLinkErrorView(err error) *VideoLinkImportError {
	var appErr *AppError
	if errors.As(err, &appErr) {
		view := &VideoLinkImportError{Code: "error", Message: appErr.Message, Retryable: appErr.Retryable, Status: appErr.Status}
		if code, ok := appErr.Details["parserCode"].(string); ok && code != "" {
			view.Code = code
		}
		if platform, ok := appErr.Details["platform"].(string); ok {
			view.Platform = platform
		}
		return view
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &VideoLinkImportError{Code: string(videoparser.ErrTimeout), Message: "解析超时，请稍后重试", Retryable: true, Status: http.StatusGatewayTimeout}
	}
	return &VideoLinkImportError{Code: "internal_error", Message: "链接解析失败，请稍后重试", Retryable: true, Status: http.StatusInternalServerError}
}
