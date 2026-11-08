// Package videoparser 是多平台参考视频链接解析服务：从分享文案里取出链接、识别平台、展开短链，
// 按平台适配器声明的供应商顺序解析出作品信息与媒体地址，并把媒体下载为本地文件（必要时用 FFmpeg 合并音视频）。
//
// 这里只解析用户有权使用的公开内容：不携带任何账号 Cookie，不绕过登录、付费墙或 DRM。
// 上游返回的媒体地址都是临时地址，调用方必须把下载后的文件转存为自己的资源，不能把上游地址当作长期输入。
package videoparser

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// 平台标识；新增平台时在这里加常量，并实现一个 PlatformAdapter 注册到 defaultAdapters。
const (
	PlatformDouyin      = "douyin"
	PlatformKuaishou    = "kuaishou"
	PlatformXiaohongshu = "xiaohongshu"
	PlatformChannels    = "channels"
	PlatformBilibili    = "bilibili"
	PlatformWeibo       = "weibo"
	PlatformXigua       = "xigua"
	PlatformTikTok      = "tiktok"
	PlatformYouTube     = "youtube"
	PlatformInstagram   = "instagram"
	PlatformX           = "x"
)

const (
	RegionDomestic = "domestic"
	RegionOverseas = "overseas"
)

// Support 是平台能力的如实标记，来自真实链接测试，不按“理论上支持”填写。
type Support string

const (
	SupportSupported   Support = "supported"   // 已支持：真实公开链接解析、下载、FFmpeg 读取均通过
	SupportPartial     Support = "partial"     // 部分支持：可用但有明确限制（清晰度、内容类型、成功率等）
	SupportNeedsAuth   Support = "needs_auth"  // 需要授权：只有登录态、平台授权或代理才能使用
	SupportUnavailable Support = "unavailable" // 暂不可用：当前环境下没有可用的合规实现
)

func (s Support) Label() string {
	switch s {
	case SupportSupported:
		return "已支持"
	case SupportPartial:
		return "部分支持"
	case SupportNeedsAuth:
		return "需要授权"
	default:
		return "暂不可用"
	}
}

// Usable 表示平台是否会实际发起解析；暂不可用的平台直接返回说明，不浪费上游请求。
func (s Support) Usable() bool {
	return s == SupportSupported || s == SupportPartial
}

// PlatformInfo 是平台的静态描述。Domains 同时作为链接与每一跳重定向的域名白名单。
type PlatformInfo struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	Region       string   `json:"region"`
	Domains      []string `json:"-"`
	ShortDomains []string `json:"-"`
}

// Capabilities 是 getCapabilities() 的返回：能力状态、依赖条件与真实测试说明。
type Capabilities struct {
	Support       Support  `json:"support"`
	SupportLabel  string   `json:"supportLabel"`
	Video         bool     `json:"video"`
	Metadata      bool     `json:"metadata"`
	SeparateAV    bool     `json:"separateStreams"`
	NeedsProxy    bool     `json:"needsProxy"`
	NeedsLogin    bool     `json:"needsLogin"`
	Providers     []string `json:"providers"`
	Notes         string   `json:"notes"`
	VerifiedOn    string   `json:"verifiedOn,omitempty"`
	ConfigMissing string   `json:"configMissing,omitempty"`
}

// MediaKind 区分合流文件与分离的视频流 / 音频流（如 B 站 DASH）。
type MediaKind string

const (
	MediaMuxed MediaKind = "muxed"
	MediaVideo MediaKind = "video"
	MediaAudio MediaKind = "audio"
)

// MediaAsset 是一个可下载的媒体地址。Headers 只保留 User-Agent / Referer 等安全头，绝不包含 Cookie。
// Backups 是同一文件的备用 CDN 地址，主地址失败时依次尝试。
type MediaAsset struct {
	Kind       MediaKind `json:"kind"`
	URL        string    `json:"-"`
	Backups    []string  `json:"-"`
	Ext        string    `json:"ext,omitempty"`
	Width      int       `json:"width,omitempty"`
	Height     int       `json:"height,omitempty"`
	BitrateK   float64   `json:"bitrateKbps,omitempty"`
	VideoCodec string    `json:"videoCodec,omitempty"`
	AudioCodec string    `json:"audioCodec,omitempty"`
	SizeBytes  int64     `json:"sizeBytes,omitempty"`
	// Watermarked 表示平台在这一路流里烧录了水印；有无水印的流可选时不使用它。
	Watermarked bool              `json:"watermarked,omitempty"`
	Headers     map[string]string `json:"-"`
}

// Metadata 是 getMetadata() 的返回：作品标题、作者、时长、封面与规范化来源地址。
type Metadata struct {
	Platform      string `json:"platform"`
	PlatformLabel string `json:"platformLabel"`
	ID            string `json:"id"`
	Title         string `json:"title"`
	Author        string `json:"author,omitempty"`
	DurationMs    int64  `json:"durationMs,omitempty"`
	CoverURL      string `json:"-"`
	SourceURL     string `json:"sourceUrl"`
}

// Result 是统一的解析结果。Media 里的地址会过期，只能用于随后的一次下载。
type Result struct {
	Meta     Metadata     `json:"metadata"`
	Media    []MediaAsset `json:"media"`
	Provider string       `json:"provider"`
	Cached   bool         `json:"cached"`
}

func (r *Result) Metadata() Metadata { return r.Meta }

func (r *Result) MediaAssets() []MediaAsset { return append([]MediaAsset(nil), r.Media...) }

func (r *Result) clone() *Result {
	if r == nil {
		return nil
	}
	copied := *r
	copied.Media = r.MediaAssets()
	return &copied
}

// ErrorCode 是平台无关的错误分类；前端按 Retryable 决定是否显示重试入口。
type ErrorCode string

const (
	ErrInvalidLink ErrorCode = "invalid_link"
	ErrUnsupported ErrorCode = "unsupported_platform"
	ErrUnavailable ErrorCode = "platform_unavailable"
	ErrNeedsLogin  ErrorCode = "needs_login"
	ErrNotVideo    ErrorCode = "not_video"
	ErrNotFound    ErrorCode = "not_found"
	ErrDRM         ErrorCode = "drm_protected"
	ErrUpstream    ErrorCode = "upstream_error"
	ErrTimeout     ErrorCode = "timeout"
	ErrTooLarge    ErrorCode = "too_large"
	ErrTooLong     ErrorCode = "too_long"
	ErrBlocked     ErrorCode = "blocked_address"
	ErrBusy        ErrorCode = "busy"
	ErrMedia       ErrorCode = "invalid_media"
)

// Error 携带给用户看的中文说明；Err 是内部原因，只写日志，不返回给浏览器。
type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Platform  string
	Provider  string
	Err       error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

func newError(code ErrorCode, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Err: cause, Retryable: code.retryable()}
}

func (c ErrorCode) retryable() bool {
	switch c {
	case ErrUpstream, ErrTimeout, ErrBusy:
		return true
	}
	return false
}

// terminal 表示换供应商也不会有不同结果的内容类错误（图文作品、DRM、超长），直接停止降级。
func (c ErrorCode) terminal() bool {
	switch c {
	case ErrNotVideo, ErrDRM, ErrTooLong, ErrTooLarge, ErrInvalidLink, ErrBlocked:
		return true
	}
	return false
}

// AsError 把任意错误归一为 *Error；未知错误按上游错误处理（可重试）。
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var parsed *Error
	if errors.As(err, &parsed) {
		return parsed
	}
	return newError(ErrUpstream, "视频解析失败，请稍后重试", err)
}

// redactURL 只保留协议、主机和路径，去掉查询串里的签名、设备号等信息，用于日志。
func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "invalid-url"
	}
	path := parsed.EscapedPath()
	if len(path) > 96 {
		path = path[:96] + "…"
	}
	return parsed.Scheme + "://" + parsed.Host + path
}

func clip(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
