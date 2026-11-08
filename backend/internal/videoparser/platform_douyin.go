package videoparser

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// 抖音：2026-11-05 实测，移动分享页（iesdouyin.com/share/video）已改为由页面脚本取数，HTML 里不再有作品数据；
// 网页版 www.douyin.com 对无头浏览器访客弹出风控验证；yt-dlp 需要新鲜 Cookie。
// 可用的公开渠道是抖音官方嵌入播放器 open.douyin.com/player/video?vid=<作品ID>（供第三方网站嵌入公开作品）：
// 它先调用嵌入授权接口，再由页面脚本请求作品详情，返回无水印的播放地址。这里用无头浏览器正常打开该官方页面，
// 读取页面自己拿到的作品详情；不注入脚本、不伪造签名、不使用 Cookie。作者关闭外部播放的作品会如实提示。

var (
	douyinIDPattern  = regexp.MustCompile(`(?:/video/|/note/|/share/video/|/share/note/|/slides/|modal_id=|aweme_id=|vid=)(\d{8,})`)
	douyinNumericID  = regexp.MustCompile(`^\d{8,32}$`)
	douyinPlayAddr   = regexp.MustCompile(`"play_addr"\s*:\s*\{\s*"uri"\s*:\s*"([^"]+)"\s*,\s*"url_list"\s*:\s*\[\s*"([^"]+)"`)
	douyinDescriptor = regexp.MustCompile(`"desc"\s*:\s*"([^"]{0,400})"`)
)

const (
	douyinEmbedAuthPath = "/api/douyin/v1/open/auth/embed_video/"
	douyinDetailPath    = "/aweme/v1/web/aweme/detail/"
	douyinMediaReferer  = "https://www.douyin.com/"
)

type douyinAdapter struct{ basePlatform }

func newDouyinAdapter() PlatformAdapter {
	return &douyinAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformDouyin, Label: "抖音", Region: RegionDomestic,
			Domains: []string{"douyin.com", "iesdouyin.com", "snssdk.com", "amemv.com"}, ShortDomains: []string{"v.douyin.com"}},
		caps: Capabilities{Support: SupportSupported, Video: true, Metadata: true, VerifiedOn: "2026-11-05",
			Notes: "通过抖音官方嵌入播放器解析公开作品，取无水印播放地址（服务器需安装 Chromium）；作者关闭外部播放、仅自己可见、已删除的作品和图文作品无法解析"},
		providers: []string{"douyin-embed-player"},
		idPattern: douyinIDPattern,
		canonical: func(id string, _ *url.URL) string { return "https://www.douyin.com/video/" + id },
	}}
}

// douyinEmbedProvider 在无头浏览器里打开抖音官方嵌入播放器，读取页面请求到的作品详情。
type douyinEmbedProvider struct {
	svc     *Service
	browser pageCapturer
}

func (p *douyinEmbedProvider) Name() string       { return "douyin-embed-player" }
func (p *douyinEmbedProvider) Kind() ProviderKind { return ProviderSelfHosted }
func (p *douyinEmbedProvider) Available() error   { return p.browser.Available() }

func (p *douyinEmbedProvider) Resolve(ctx context.Context, req Request) (*Result, error) {
	if strings.Contains(req.URL.Path, "/note/") || strings.Contains(req.URL.Path, "/slides/") {
		return nil, newError(ErrNotVideo, "这条抖音是图文作品，没有可用的视频", nil)
	}
	if !douyinNumericID.MatchString(req.ID) {
		return nil, newError(ErrInvalidLink, "没有从抖音链接里识别到作品编号", nil)
	}
	page := "https://open.douyin.com/player/video?vid=" + req.ID + "&autoplay=0"
	var detail []byte
	err := p.browser.Capture(ctx, page, func(raw string) bool {
		return strings.Contains(raw, douyinEmbedAuthPath) || strings.Contains(raw, douyinDetailPath)
	}, func(resp capturedResponse) (bool, error) {
		if strings.Contains(resp.URL, douyinEmbedAuthPath) {
			return false, douyinEmbedAuth(resp.Body)
		}
		if resp.Status != 200 || !strings.Contains(resp.URL, "aweme_id="+req.ID) {
			return false, nil
		}
		detail = resp.Body
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return parseDouyinDetail(detail, req.ID)
}

// douyinEmbedAuth 解析嵌入授权结果：作者关闭外部播放时 auth_result=false。
func douyinEmbedAuth(body []byte) error {
	var auth struct {
		AuthResult *bool  `json:"auth_result"`
		Tips       string `json:"tips"`
	}
	if json.Unmarshal(body, &auth) != nil || auth.AuthResult == nil || *auth.AuthResult {
		return nil
	}
	message := "这条抖音作品的作者未开放外部播放，无法解析，请下载后本地上传"
	if tips := clip(auth.Tips, 60); tips != "" {
		message = "抖音提示：" + tips + "。请下载后本地上传"
	}
	return newError(ErrNeedsLogin, message, nil)
}

type douyinAddr struct {
	URI      string   `json:"uri"`
	URLList  []string `json:"url_list"`
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	DataSize int64    `json:"data_size"`
}

type douyinDetailBody struct {
	StatusCode   int    `json:"status_code"`
	StatusMsg    string `json:"status_msg"`
	FilterDetail *struct {
		FilterReason string `json:"filter_reason"`
		DetailMsg    string `json:"detail_msg"`
		Notice       string `json:"notice"`
	} `json:"filter_detail"`
	AwemeDetail *struct {
		AwemeID  string            `json:"aweme_id"`
		Desc     string            `json:"desc"`
		Duration int64             `json:"duration"`
		Images   []json.RawMessage `json:"images"`
		Author   struct {
			Nickname string `json:"nickname"`
		} `json:"author"`
		Video struct {
			Duration int64      `json:"duration"`
			PlayAddr douyinAddr `json:"play_addr"`
			Cover    struct {
				URLList []string `json:"url_list"`
			} `json:"cover"`
			BitRate []struct {
				GearName string     `json:"gear_name"`
				BitRate  int64      `json:"bit_rate"`
				IsH265   int        `json:"is_h265"`
				Format   string     `json:"format"`
				PlayAddr douyinAddr `json:"play_addr"`
			} `json:"bit_rate"`
		} `json:"video"`
	} `json:"aweme_detail"`
}

// parseDouyinDetail 从作品详情取视频：bit_rate 里的 MP4 档位（play_addr 为无水印播放地址），没有档位时用 play_addr。
// download_addr 是带水印的下载版本，不使用；DASH 档位只有分片，不使用。
func parseDouyinDetail(body []byte, id string) (*Result, error) {
	if len(body) == 0 {
		return nil, newError(ErrUpstream, "抖音没有返回作品信息，请稍后重试", nil)
	}
	var detail douyinDetailBody
	if err := json.Unmarshal(body, &detail); err != nil {
		return nil, newError(ErrUpstream, "抖音返回的作品信息无法读取", err)
	}
	aweme := detail.AwemeDetail
	if aweme == nil {
		if detail.FilterDetail != nil {
			reason := firstNonEmpty(detail.FilterDetail.DetailMsg, detail.FilterDetail.Notice, detail.FilterDetail.FilterReason)
			return nil, newError(ErrNotFound, "抖音作品无法查看："+clip(reason, 60), nil)
		}
		return nil, newError(ErrNotFound, "抖音作品不存在、已删除或仅作者可见", errors.New(clip(detail.StatusMsg, 120)))
	}
	if id != "" && aweme.AwemeID != "" && aweme.AwemeID != id {
		return nil, newError(ErrUpstream, "抖音返回了其他作品的信息，请稍后重试", nil)
	}
	if len(aweme.Images) > 0 {
		return nil, newError(ErrNotVideo, "这条抖音是图文作品，没有可用的视频", nil)
	}
	headers := map[string]string{"User-Agent": desktopUserAgent, "Referer": douyinMediaReferer}
	result := &Result{Meta: Metadata{ID: firstNonEmpty(aweme.AwemeID, id), Title: aweme.Desc, Author: aweme.Author.Nickname, DurationMs: aweme.Duration}}
	if result.Meta.DurationMs <= 0 {
		result.Meta.DurationMs = aweme.Video.Duration
	}
	if len(aweme.Video.Cover.URLList) > 0 {
		result.Meta.CoverURL = aweme.Video.Cover.URLList[0]
	}
	for _, rate := range aweme.Video.BitRate {
		if format := strings.ToLower(rate.Format); format != "" && format != "mp4" {
			continue
		}
		urls := httpURLs(rate.PlayAddr.URLList)
		if len(urls) == 0 {
			continue
		}
		codec := "h264"
		if rate.IsH265 == 1 {
			codec = "h265"
		}
		result.Media = append(result.Media, MediaAsset{Kind: MediaMuxed, URL: urls[0], Backups: urls[1:], Ext: "mp4", Width: rate.PlayAddr.Width, Height: rate.PlayAddr.Height,
			VideoCodec: codec, BitrateK: float64(rate.BitRate) / 1000, SizeBytes: rate.PlayAddr.DataSize, Headers: headers})
	}
	if len(result.Media) == 0 {
		urls := httpURLs(aweme.Video.PlayAddr.URLList)
		for i := range urls {
			urls[i] = strings.Replace(urls[i], "/playwm/", "/play/", 1)
		}
		if len(urls) > 0 {
			result.Media = append(result.Media, MediaAsset{Kind: MediaMuxed, URL: urls[0], Backups: urls[1:], Ext: "mp4", Width: aweme.Video.PlayAddr.Width, Height: aweme.Video.PlayAddr.Height, SizeBytes: aweme.Video.PlayAddr.DataSize, Headers: headers})
		}
	}
	if len(result.Media) == 0 {
		return nil, newError(ErrUpstream, "抖音没有返回可用的播放地址", nil)
	}
	return result, nil
}

func httpURLs(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
			out = append(out, value)
		}
	}
	return out
}

// douyinSharePageProvider 读取移动分享页 _ROUTER_DATA 里的 play_addr。抖音已改为脚本取数，默认不在供应商链里；
// 平台恢复服务端渲染时可用 CANVAS_VIDEO_PARSER_PRIORITY=douyin=douyin-share-page,douyin-embed-player 重新启用。
type douyinSharePageProvider struct{ svc *Service }

func (p *douyinSharePageProvider) Name() string       { return "douyin-share-page" }
func (p *douyinSharePageProvider) Kind() ProviderKind { return ProviderSelfHosted }
func (p *douyinSharePageProvider) Available() error   { return nil }

func (p *douyinSharePageProvider) Resolve(ctx context.Context, req Request) (*Result, error) {
	if strings.Contains(req.URL.Path, "/note/") {
		return nil, newError(ErrNotVideo, "这条抖音是图文作品，没有可用的视频", nil)
	}
	page, err := p.page(ctx, req)
	if err != nil {
		return nil, err
	}
	match := douyinPlayAddr.FindStringSubmatch(page)
	if len(match) < 3 {
		return nil, newError(ErrUpstream, "抖音分享页没有返回视频地址", nil)
	}
	direct := strings.Replace(unescapeJSONText(match[2]), "/playwm/", "/play/", 1)
	if !strings.HasPrefix(direct, "http") {
		if uri := unescapeJSONText(match[1]); uri != "" {
			direct = "https://aweme.snssdk.com/aweme/v1/play/?video_id=" + url.QueryEscape(uri) + "&ratio=720p&line=0"
		}
	}
	title := ""
	if desc := douyinDescriptor.FindStringSubmatch(page); len(desc) > 1 {
		title = unescapeJSONText(desc[1])
	}
	headers := map[string]string{"User-Agent": mobileUserAgent, "Referer": douyinMediaReferer}
	return &Result{Meta: Metadata{ID: req.ID, Title: title}, Media: []MediaAsset{{Kind: MediaMuxed, URL: direct, Ext: "mp4", Headers: headers}}}, nil
}

func (p *douyinSharePageProvider) page(ctx context.Context, req Request) (string, error) {
	_, body, err := p.svc.fetchPage(ctx, req.Platform, req.URL.String(), mobileUserAgent, "", 6<<20)
	if err == nil && douyinPlayAddr.Match(body) {
		return string(body), nil
	}
	if req.ID == "" {
		if err != nil {
			return "", err
		}
		return "", newError(ErrUpstream, "抖音分享页没有返回作品编号", nil)
	}
	_, body, err = p.svc.fetchPage(ctx, req.Platform, "https://www.iesdouyin.com/share/video/"+req.ID+"/", mobileUserAgent, "", 6<<20)
	var parsed *Error
	if err != nil && errors.As(err, &parsed) {
		return "", parsed
	}
	return string(body), err
}

// unescapeJSONText 还原页面内嵌 JSON 字符串里的 \u002F、\/ 与 &amp;。
func unescapeJSONText(value string) string {
	replacer := strings.NewReplacer(`\u002F`, "/", `\u002f`, "/", `\/`, "/", `\u0026`, "&", "&amp;", "&", `\"`, `"`)
	return strings.TrimSpace(replacer.Replace(value))
}
