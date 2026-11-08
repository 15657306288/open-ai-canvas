package videoparser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// 视频号：微信里「复制链接」得到 https://weixin.qq.com/sph/<短码>，在微信外会跳到官方网页预览
// channels.weixin.qq.com/finder-preview/pages/sph?id=<短码>。预览页调用 finder-preview/api/feed/get_feed_info 取作品信息；
// 作品开放网页预览时返回 h264VideoInfo / h265VideoInfo / videoUrl，否则只返回标题、作者、封面并提示「前往微信观看」。
// 这里只调用这个官方预览接口（无需登录、无签名），不使用微信登录态、不解密任何加密视频；只能在微信内观看的作品如实提示。

var (
	channelsShortPath = regexp.MustCompile(`^/sph/([A-Za-z0-9_-]{4,64})/?$`)
	channelsShortURI  = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)
)

const channelsFeedInfoAPI = "https://channels.weixin.qq.com/finder-preview/api/feed/get_feed_info"

type channelsAdapter struct{ basePlatform }

func newChannelsAdapter() PlatformAdapter {
	return &channelsAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformChannels, Label: "视频号", Region: RegionDomestic,
			Domains: []string{"channels.weixin.qq.com", "weixin.qq.com"}},
		caps: Capabilities{Support: SupportPartial, Video: true, Metadata: true, VerifiedOn: "2026-11-05",
			Notes: "通过视频号官方网页预览解析微信「复制链接」得到的 weixin.qq.com/sph/ 链接；作者或平台只允许在微信内观看的作品无法解析，会提示改为本地上传"},
		providers: []string{"channels-preview"},
	}}
}

// Detect：weixin.qq.com 只接受 /sph/ 分享路径（避免把公众号文章等其他微信页面当成视频号）；channels.weixin.qq.com 全部接受。
func (a *channelsAdapter) Detect(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	switch host {
	case "weixin.qq.com", "www.weixin.qq.com":
		return channelsShortPath.MatchString(u.Path)
	case "channels.weixin.qq.com":
		return true
	}
	return false
}

// Normalize 返回 sph:<短码> 或 eid:<exportId>，作为缓存与去重的作品编号。
func (a *channelsAdapter) Normalize(u *url.URL) (string, string) {
	if u == nil {
		return "", ""
	}
	if match := channelsShortPath.FindStringSubmatch(u.Path); match != nil {
		return "sph:" + match[1], "https://weixin.qq.com/sph/" + match[1]
	}
	query := u.Query()
	if id := query.Get("id"); strings.Contains(u.Path, "/sph") && channelsShortURI.MatchString(id) {
		return "sph:" + id, "https://weixin.qq.com/sph/" + id
	}
	if eid := strings.TrimSpace(query.Get("eid")); eid != "" && len(eid) <= 200 {
		return "eid:" + eid, "https://channels.weixin.qq.com/finder-preview/pages/feed?eid=" + url.QueryEscape(eid)
	}
	stripped := *u
	stripped.RawQuery, stripped.Fragment, stripped.User = "", "", nil
	return "", stripped.String()
}

type channelsPreviewProvider struct{ svc *Service }

func (p *channelsPreviewProvider) Name() string       { return "channels-preview" }
func (p *channelsPreviewProvider) Kind() ProviderKind { return ProviderSelfHosted }
func (p *channelsPreviewProvider) Available() error   { return nil }

func (p *channelsPreviewProvider) Resolve(ctx context.Context, req Request) (*Result, error) {
	kind, value, _ := strings.Cut(req.ID, ":")
	request := map[string]any{"baseReq": map[string]any{"generalToken": strings.TrimSpace(req.URL.Query().Get("token"))}}
	referer := "https://channels.weixin.qq.com/finder-preview/pages/sph?id=" + url.QueryEscape(value)
	switch {
	case kind == "sph" && value != "":
		request["shortUri"] = value
	case kind == "eid" && value != "":
		request["exportId"] = value
		referer = "https://channels.weixin.qq.com/finder-preview/pages/feed?eid=" + url.QueryEscape(value)
	default:
		return nil, newError(ErrInvalidLink, "请粘贴视频号作品的「复制链接」（weixin.qq.com/sph/ 开头）", nil)
	}
	endpoint := channelsFeedInfoAPI + "?_rid=" + channelsRequestID() + "&_pageUrl=" + url.QueryEscape("https://channels.weixin.qq.com/finder-preview/pages/sph")
	body, err := p.svc.postJSON(ctx, req.Platform, endpoint, referer, "https://channels.weixin.qq.com", request, 2<<20)
	if err != nil {
		return nil, err
	}
	return parseChannelsFeedInfo(body, req.ID)
}

// channelsRequestID 与预览页的 _rid 格式一致：十六进制秒级时间戳 + 随机串。
func channelsRequestID() string {
	random := make([]byte, 4)
	_, _ = rand.Read(random)
	return fmt.Sprintf("%x-%s", time.Now().Unix(), hex.EncodeToString(random))
}

type channelsVideoInfo struct {
	VideoURL string  `json:"videoUrl"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Duration float64 `json:"duration"`
	FileSize int64   `json:"fileSize"`
}

type channelsFeedResponse struct {
	ErrCode int    `json:"errCode"`
	ErrMsg  string `json:"errMsg"`
	Data    *struct {
		AuthorInfo struct {
			Nickname string `json:"nickname"`
		} `json:"authorInfo"`
		FeedInfo *struct {
			MediaType     int                `json:"mediaType"`
			Description   string             `json:"description"`
			CoverURL      string             `json:"coverUrl"`
			VideoURL      string             `json:"videoUrl"`
			Duration      float64            `json:"duration"`
			VideoPlayLen  float64            `json:"videoPlayLen"`
			H264VideoInfo *channelsVideoInfo `json:"h264VideoInfo"`
			H265VideoInfo *channelsVideoInfo `json:"h265VideoInfo"`
			PicInfo       []json.RawMessage  `json:"picInfo"`
		} `json:"feedInfo"`
	} `json:"data"`
}

func parseChannelsFeedInfo(body []byte, id string) (*Result, error) {
	var response channelsFeedResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, newError(ErrUpstream, "视频号返回的作品信息无法读取", err)
	}
	if response.ErrCode != 0 || response.Data == nil || response.Data.FeedInfo == nil {
		return nil, newError(ErrNotFound, "视频号作品不存在、已删除或分享链接已失效", fmt.Errorf("errCode=%d %s", response.ErrCode, clip(response.ErrMsg, 80)))
	}
	feed := response.Data.FeedInfo
	result := &Result{Meta: Metadata{ID: id, Title: feed.Description, Author: response.Data.AuthorInfo.Nickname, CoverURL: feed.CoverURL}}
	seconds := feed.Duration
	if seconds <= 0 {
		seconds = feed.VideoPlayLen
	}
	headers := map[string]string{"User-Agent": mobileUserAgent, "Referer": "https://channels.weixin.qq.com/"}
	add := func(info *channelsVideoInfo, codec string) {
		if info == nil || !strings.HasPrefix(info.VideoURL, "http") {
			return
		}
		if info.Duration > seconds {
			seconds = info.Duration
		}
		result.Media = append(result.Media, MediaAsset{Kind: MediaMuxed, URL: info.VideoURL, Ext: "mp4", Width: info.Width, Height: info.Height, VideoCodec: codec, SizeBytes: info.FileSize, Headers: headers})
	}
	add(feed.H264VideoInfo, "h264")
	add(feed.H265VideoInfo, "h265")
	if len(result.Media) == 0 && strings.HasPrefix(feed.VideoURL, "http") {
		result.Media = append(result.Media, MediaAsset{Kind: MediaMuxed, URL: feed.VideoURL, Ext: "mp4", Headers: headers})
	}
	if seconds > 0 && seconds < 36000 {
		result.Meta.DurationMs = int64(seconds * 1000)
	}
	if len(result.Media) > 0 {
		return result, nil
	}
	if feed.MediaType == 2 || len(feed.PicInfo) > 0 {
		return nil, newError(ErrNotVideo, "这条视频号是图片动态，没有可用的视频", nil)
	}
	return nil, newError(ErrNeedsLogin, "这条视频号作品只能在微信内观看（视频号网页预览没有提供视频），请在手机上保存视频后本地上传", nil)
}
