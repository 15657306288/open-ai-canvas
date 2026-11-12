package videoparser

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// 小红书：App「复制链接」得到 xhslink.com 短链，跳转到带 xsec_token 的笔记页。访客必须用移动端 UA（桌面 UA 会被转到登录页），
// 页面的 window.__INITIAL_STATE__ 里：移动端结构为 noteData.data.noteData，桌面端旧结构为 note.noteDetailMap.<id>.note。
// 视频流 h264 由服务端烧录了水印（作者昵称 + 小红书 logo），h265 转码流无水印，因此优先取 h265。
// 解析规则参考 wujunwei928/parse-video（MIT）与 wwwzhouhui/video-parser（MIT）的公开实现，代码为本项目独立编写。

var (
	xhsIDPattern    = regexp.MustCompile(`/(?:explore|discovery/item|item|notes?)/([0-9a-fA-F]{24})`)
	xhsInitialState = regexp.MustCompile(`window\.__INITIAL_STATE__\s*=\s*`)
	xhsUndefined    = regexp.MustCompile(`([:\[,])\s*undefined\b`)
	xhsNoteURL      = regexp.MustCompile(`https?:(?://|\\u002F\\u002F)(?:www\.)?xiaohongshu\.com(?:/|\\u002F)(?:discovery(?:/|\\u002F)item|explore)(?:/|\\u002F)[0-9a-fA-F]{24}[^"'<>\s\\]*`)
)

type xiaohongshuAdapter struct{ basePlatform }

func newXiaohongshuAdapter() PlatformAdapter {
	return &xiaohongshuAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformXiaohongshu, Label: "小红书", Region: RegionDomestic,
			Domains: []string{"xiaohongshu.com", "xhslink.com"}, ShortDomains: []string{"xhslink.com"}},
		caps: Capabilities{Support: SupportPartial, Video: true, Metadata: true, VerifiedOn: "2026-11-05",
			Notes: "解析 App「复制链接」得到的笔记链接（带 xsec_token），取无水印的 h265 流；链接过期、笔记不可见或触发平台验证时会提示重新分享。本次没有拿到新分享的链接，真实解析待补测"},
		providers: []string{"xiaohongshu-page"},
		idPattern: xhsIDPattern,
		canonical: func(id string, _ *url.URL) string { return "https://www.xiaohongshu.com/explore/" + id },
	}}
}

type xiaohongshuPageProvider struct{ svc *Service }

func (p *xiaohongshuPageProvider) Name() string       { return "xiaohongshu-page" }
func (p *xiaohongshuPageProvider) Kind() ProviderKind { return ProviderSelfHosted }
func (p *xiaohongshuPageProvider) Available() error   { return nil }

func (p *xiaohongshuPageProvider) Resolve(ctx context.Context, req Request) (*Result, error) {
	final, body, err := p.svc.fetchPage(ctx, req.Platform, req.URL.String(), mobileUserAgent, "", 6<<20)
	if err != nil {
		return nil, err
	}
	switch path := final.Path; {
	case strings.HasPrefix(path, "/404"):
		return nil, newError(ErrNotFound, "小红书笔记暂时无法浏览：可能已删除、仅自己可见，或分享链接已过期，请在 App 里重新复制分享链接", nil)
	case strings.HasPrefix(path, "/website-login/captcha"):
		return nil, &Error{Code: ErrNeedsLogin, Message: "小红书要求人机验证，服务器暂时无法读取这条笔记，请稍后重试或下载后本地上传", Retryable: true}
	case strings.HasPrefix(path, "/login"):
		return nil, newError(ErrNeedsLogin, "小红书要求登录后才能查看这条笔记，请在 App 里重新复制分享链接或下载后本地上传", nil)
	}
	return parseXiaohongshuPage(body, req.ID)
}

type xhsStream struct {
	MasterURL  string   `json:"masterUrl"`
	BackupURLs []string `json:"backupUrls"`
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	VideoCodec string   `json:"videoCodec"`
	AvgBitrate int      `json:"avgBitrate"`
	Size       int64    `json:"size"`
	Duration   int64    `json:"duration"`
}

type xhsNote struct {
	NoteID string `json:"noteId"`
	Type   string `json:"type"`
	Title  string `json:"title"`
	Desc   string `json:"desc"`
	User   struct {
		Nickname string `json:"nickname"`
		NickName string `json:"nickName"`
	} `json:"user"`
	ImageList []struct {
		URLDefault string `json:"urlDefault"`
		URL        string `json:"url"`
	} `json:"imageList"`
	Video *struct {
		Capa struct {
			Duration float64 `json:"duration"`
		} `json:"capa"`
		Consumer struct {
			OriginVideoKey string `json:"originVideoKey"`
		} `json:"consumer"`
		Media struct {
			Video struct {
				Duration float64 `json:"duration"`
			} `json:"video"`
			Stream map[string][]xhsStream `json:"stream"`
		} `json:"media"`
	} `json:"video"`
}

func parseXiaohongshuPage(body []byte, id string) (*Result, error) {
	loc := xhsInitialState.FindIndex(body)
	if loc == nil {
		return nil, newError(ErrUpstream, "小红书页面没有返回笔记数据，可能需要在 App 里重新分享", nil)
	}
	raw := body[loc[1]:]
	if end := bytes.Index(raw, []byte("</script>")); end >= 0 {
		raw = raw[:end]
	}
	// 页面状态是 JS 对象字面量，会出现 undefined，转成 null 后按 JSON 解析。
	raw = xhsUndefined.ReplaceAll(bytes.TrimRight(bytes.TrimSpace(raw), ";"), []byte("${1}null"))
	var state struct {
		NoteData struct {
			Data struct {
				NoteData *xhsNote `json:"noteData"`
			} `json:"data"`
		} `json:"noteData"`
		Note struct {
			FirstNoteID   string `json:"firstNoteId"`
			CurrentNoteID string `json:"currentNoteId"`
			NoteDetailMap map[string]struct {
				Note xhsNote `json:"note"`
			} `json:"noteDetailMap"`
		} `json:"note"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, newError(ErrUpstream, "小红书页面数据格式已变化，暂时无法解析", err)
	}
	note := state.NoteData.Data.NoteData
	if note == nil || (note.Type == "" && note.Title == "" && note.Video == nil && len(note.ImageList) == 0) {
		note = nil
		for _, key := range []string{id, state.Note.CurrentNoteID, state.Note.FirstNoteID} {
			if detail, ok := state.Note.NoteDetailMap[key]; ok && key != "" {
				note = &detail.Note
				break
			}
		}
	}
	if note == nil {
		return nil, newError(ErrNotFound, "小红书笔记暂时无法浏览，请在 App 里重新复制分享链接", nil)
	}
	streams := map[string][]xhsStream{}
	if note.Video != nil {
		streams = note.Video.Media.Stream
	}
	hasStream := false
	for _, list := range streams {
		hasStream = hasStream || len(list) > 0
	}
	if (note.Type != "" && note.Type != "video") || (note.Video == nil && !hasStream) {
		return nil, newError(ErrNotVideo, "这条小红书笔记是图文，没有可用的视频", nil)
	}
	result := &Result{Meta: Metadata{ID: firstNonEmpty(note.NoteID, id), Title: firstNonEmpty(note.Title, clip(note.Desc, 60)), Author: firstNonEmpty(note.User.Nickname, note.User.NickName)}}
	if seconds := firstPositive(note.Video.Capa.Duration, note.Video.Media.Video.Duration); seconds > 0 {
		result.Meta.DurationMs = int64(seconds * 1000)
	}
	if len(note.ImageList) > 0 {
		result.Meta.CoverURL = firstNonEmpty(note.ImageList[0].URLDefault, note.ImageList[0].URL)
	}
	headers := map[string]string{"User-Agent": mobileUserAgent, "Referer": "https://www.xiaohongshu.com/"}
	for _, codec := range []string{"h265", "h266", "av1", "h264"} {
		for _, stream := range streams[codec] {
			urls := httpURLs(append([]string{stream.MasterURL}, stream.BackupURLs...))
			if len(urls) == 0 {
				continue
			}
			result.Media = append(result.Media, MediaAsset{Kind: MediaMuxed, URL: urls[0], Backups: urls[1:], Ext: "mp4", Width: stream.Width, Height: stream.Height,
				VideoCodec: firstNonEmpty(stream.VideoCodec, codec), BitrateK: float64(stream.AvgBitrate) / 1000, SizeBytes: stream.Size, Watermarked: codec == "h264", Headers: headers})
			if result.Meta.DurationMs <= 0 && stream.Duration > 0 {
				result.Meta.DurationMs = stream.Duration
			}
		}
	}
	if len(result.Media) == 0 && note.Video != nil && note.Video.Consumer.OriginVideoKey != "" {
		key := strings.TrimPrefix(unescapeJSONText(note.Video.Consumer.OriginVideoKey), "/")
		result.Media = append(result.Media, MediaAsset{Kind: MediaMuxed, URL: "https://sns-video-bd.xhscdn.com/" + key, Ext: "mp4", Headers: headers})
	}
	if len(result.Media) == 0 {
		return nil, newError(ErrUpstream, "小红书笔记没有返回视频地址", nil)
	}
	return result, nil
}

// xhsShareTarget 处理 xhslink 短链落地页用脚本跳转的情况：从页面里找出笔记地址。
func xhsShareTarget(body []byte) string {
	match := xhsNoteURL.Find(body)
	if match == nil {
		return ""
	}
	return unescapeJSONText(strings.ReplaceAll(string(match), `\u002F`, "/"))
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
