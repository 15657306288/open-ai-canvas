package videoparser

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
)

// ytdlpProvider 调用自部署的 yt-dlp（Unlicense，PyPI 基础包，不装 GPL 的可选依赖）只做元数据解析：
// --dump-single-json --skip-download。媒体由 Go 侧带 SSRF 防护的客户端下载，yt-dlp 的全部出网请求
// 经本地出口代理逐个校验目标地址；不读取任何配置文件或 Cookie，子进程只拿到最小环境变量。
type ytdlpProvider struct{ svc *Service }

func (p *ytdlpProvider) Name() string       { return "yt-dlp" }
func (p *ytdlpProvider) Kind() ProviderKind { return ProviderSelfHosted }

func (p *ytdlpProvider) Available() error {
	if p.svc.opts.YTDLPPath == "" {
		return errors.New("服务器未安装 yt-dlp")
	}
	return nil
}

func (p *ytdlpProvider) Resolve(ctx context.Context, req Request) (*Result, error) {
	proxy := p.svc.proxyFor(req.Platform)
	if proxy == "" {
		egress, err := p.svc.egressURL()
		if err != nil {
			return nil, newError(ErrUnavailable, "解析出口未就绪，请稍后重试", err)
		}
		proxy = egress
	}
	args := []string{
		"--ignore-config", "--no-cookies", "--no-cookies-from-browser", "--no-cache-dir",
		"--dump-single-json", "--skip-download", "--no-playlist", "--no-warnings", "--no-progress",
		"--socket-timeout", "15", "--retries", "1", "--extractor-retries", "1",
		"--proxy", proxy, "--", req.URL.String(),
	}
	home := os.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CACHE_HOME=" + home, "LANG=C.UTF-8", "PYTHONIOENCODING=utf-8", "PYTHONDONTWRITEBYTECODE=1"}
	stdout, stderr, err := p.svc.opts.Runner.Run(ctx, p.svc.opts.YTDLPPath, args, env, 16<<20)
	if err != nil {
		if ctx.Err() != nil {
			return nil, newError(ErrTimeout, req.Platform.Info().Label+"解析超时，请稍后重试", ctx.Err())
		}
		return nil, classifyYTDLPError(string(stderr), err)
	}
	return parseYTDLPOutput(stdout)
}

// classifyYTDLPError 把 yt-dlp 的英文报错归到统一错误码；原始信息只写日志。
func classifyYTDLPError(stderr string, cause error) error {
	lower := strings.ToLower(stderr)
	wrapped := errors.Join(cause, errors.New(clip(stderr, 400)))
	has := func(words ...string) bool {
		for _, word := range words {
			if strings.Contains(lower, word) {
				return true
			}
		}
		return false
	}
	switch {
	case has("drm"):
		return newError(ErrDRM, "该视频受 DRM 版权保护，不能解析", wrapped)
	case has("cookies", "sign in", "log in", "login", "logged-in", "authentication", "members-only", "premium", "private video", "confirm you’re not a bot", "confirm you're not a bot"):
		return newError(ErrNeedsLogin, "平台要求登录或验证后才能读取这条视频，请下载后本地上传", wrapped)
	case has("timed out", "timeout", "temporarily unavailable", "http error 5", "connection reset", "connection refused", "too many requests", "http error 429"):
		return newError(ErrUpstream, "平台暂时无法访问，请稍后重试", wrapped)
	case has("your country", "geo restricted", "geo-restricted", "not available in your region"):
		return &Error{Code: ErrUnavailable, Message: "这条视频在服务器所在地区不可观看", Err: wrapped}
	case has("http error 404", "not found", "does not exist", "has been removed", "is not available", "unavailable", "deleted"):
		return newError(ErrNotFound, "作品不存在、已删除或不公开", wrapped)
	case has("unsupported url"):
		return newError(ErrUnsupported, "暂不支持这种链接格式", wrapped)
	case has("no video formats found", "no formats found", "requested format is not available"):
		return &Error{Code: ErrUpstream, Message: "没有找到可用的视频格式，可能是图文作品或平台改版", Err: wrapped}
	case has("unable to download"):
		return newError(ErrUpstream, "平台暂时无法访问，请稍后重试", wrapped)
	}
	return newError(ErrUpstream, "解析失败，请稍后重试", wrapped)
}

type ytdlpFormat struct {
	FormatID       string            `json:"format_id"`
	FormatNote     string            `json:"format_note"`
	URL            string            `json:"url"`
	Protocol       string            `json:"protocol"`
	Ext            string            `json:"ext"`
	VCodec         *string           `json:"vcodec"`
	ACodec         *string           `json:"acodec"`
	Width          *float64          `json:"width"`
	Height         *float64          `json:"height"`
	TBR            *float64          `json:"tbr"`
	Filesize       *float64          `json:"filesize"`
	FilesizeApprox *float64          `json:"filesize_approx"`
	HTTPHeaders    map[string]string `json:"http_headers"`
	HasDRM         any               `json:"has_drm"`
}

type ytdlpInfo struct {
	ytdlpFormat
	Type         string        `json:"_type"`
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Uploader     string        `json:"uploader"`
	Channel      string        `json:"channel"`
	Thumbnail    string        `json:"thumbnail"`
	WebpageURL   string        `json:"webpage_url"`
	Duration     *float64      `json:"duration"`
	IsLive       *bool         `json:"is_live"`
	LiveStatus   string        `json:"live_status"`
	Availability string        `json:"availability"`
	Formats      []ytdlpFormat `json:"formats"`
	Entries      []ytdlpInfo   `json:"entries"`
}

func parseYTDLPOutput(stdout []byte) (*Result, error) {
	var info ytdlpInfo
	if err := json.Unmarshal(stdout, &info); err != nil {
		return nil, newError(ErrUpstream, "解析器返回了无效数据", err)
	}
	if info.Type == "playlist" || info.Type == "multi_video" {
		if len(info.Entries) == 0 {
			return nil, newError(ErrNotVideo, "链接是合集，没有单个视频", nil)
		}
		info = info.Entries[0]
	}
	if (info.IsLive != nil && *info.IsLive) || info.LiveStatus == "is_live" || info.LiveStatus == "is_upcoming" {
		return nil, newError(ErrNotVideo, "直播不能作为参考视频，请使用录好的视频", nil)
	}
	switch info.Availability {
	case "premium_only", "subscriber_only", "needs_auth":
		return nil, newError(ErrNeedsLogin, "这条视频需要登录、会员或订阅才能观看，不支持解析", nil)
	}
	formats := info.Formats
	if len(formats) == 0 && info.URL != "" {
		formats = []ytdlpFormat{info.ytdlpFormat}
	}
	result := &Result{Meta: Metadata{ID: info.ID, Title: info.Title, Author: firstNonEmpty(info.Uploader, info.Channel), CoverURL: info.Thumbnail, SourceURL: info.WebpageURL}}
	if info.Duration != nil && *info.Duration > 0 && !math.IsInf(*info.Duration, 0) {
		result.Meta.DurationMs = int64(math.Round(*info.Duration * 1000))
	}
	drm := 0
	for _, format := range formats {
		if drmFlag(format.HasDRM) {
			drm++
			continue
		}
		if asset, ok := ytdlpMediaAsset(format); ok {
			// 平台标注带水印的格式只在没有其他格式时使用（由选流逻辑处理）。
			asset.Watermarked = strings.Contains(strings.ToLower(format.FormatNote), "watermark")
			result.Media = append(result.Media, asset)
		}
	}
	if len(result.Media) == 0 {
		if drm > 0 {
			return nil, newError(ErrDRM, "该视频受 DRM 版权保护，不能解析", nil)
		}
		return nil, &Error{Code: ErrUpstream, Message: "没有找到可直接下载的视频格式"}
	}
	return result, nil
}

func drmFlag(value any) bool {
	switch flag := value.(type) {
	case bool:
		return flag
	case string:
		return flag != "" && flag != "false"
	}
	return false
}

// ytdlpMediaAsset 只接受单文件 http(s) 格式；HLS / 分片 DASH / 故事板等需要额外协议的格式不下载。
func ytdlpMediaAsset(format ytdlpFormat) (MediaAsset, bool) {
	protocol := strings.ToLower(format.Protocol)
	if format.URL == "" || (protocol != "" && protocol != "https" && protocol != "http") || format.Ext == "mhtml" {
		return MediaAsset{}, false
	}
	vcodec, acodec := codecValue(format.VCodec), codecValue(format.ACodec)
	asset := MediaAsset{URL: format.URL, Ext: strings.ToLower(format.Ext), VideoCodec: vcodec, AudioCodec: acodec, Headers: safeHeaders(format.HTTPHeaders)}
	if format.Width != nil {
		asset.Width = int(*format.Width)
	}
	if format.Height != nil {
		asset.Height = int(*format.Height)
	}
	if format.TBR != nil {
		asset.BitrateK = *format.TBR
	}
	if format.Filesize != nil {
		asset.SizeBytes = int64(*format.Filesize)
	} else if format.FilesizeApprox != nil {
		asset.SizeBytes = int64(*format.FilesizeApprox)
	}
	switch {
	case vcodec == "none" && acodec == "none":
		return MediaAsset{}, false
	case vcodec == "none":
		asset.Kind = MediaAudio
	case acodec == "none":
		asset.Kind = MediaVideo
	default:
		// 编码未知（null）时按合流处理：微博等平台的 MP4 直链通常不标注编码。
		asset.Kind = MediaMuxed
	}
	if asset.Kind == MediaAudio && asset.Ext == "" {
		asset.Ext = "m4a"
	}
	return asset, true
}

func codecValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*value))
}

// safeHeaders 只保留下载媒体需要的请求头；Cookie、Authorization 等一律丢弃。
func safeHeaders(headers map[string]string) map[string]string {
	out := map[string]string{}
	for name, value := range headers {
		switch strings.ToLower(name) {
		case "user-agent", "referer", "origin", "accept", "accept-language":
			if value = strings.TrimSpace(value); value != "" && !strings.ContainsAny(value, "\r\n") {
				out[name] = value
			}
		}
	}
	return out
}
