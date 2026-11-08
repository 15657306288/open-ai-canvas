package videoparser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 抖音作品详情固定数据：结构与 2026-11-05 官方嵌入播放器实际拿到的响应一致（地址已替换为测试域名路径）。
const douyinDetailFixture = `{"status_code":0,"aweme_detail":{"aweme_id":"7703069533934942826","desc":"测试抖音视频 #好物","duration":10134,"author":{"nickname":"测试作者"},"images":null,
"video":{"duration":10134,"play_addr":{"uri":"v0300fg","url_list":["https://v26-web.douyinvod.com/play/fallback.mp4","https://www.douyin.com/aweme/v1/play/?video_id=v0300fg"],"width":1080,"height":1920,"data_size":2021125},
"download_addr":{"url_list":["https://v26-web.douyinvod.com/watermark/wm.mp4"]},
"cover":{"url_list":["https://p3-pc-sign.douyinpic.com/cover.jpeg"]},
"bit_rate":[
{"gear_name":"normal_1080_0","bit_rate":1595000,"is_h265":0,"format":"mp4","play_addr":{"url_list":["https://v26-web.douyinvod.com/h264-1080.mp4","https://v11-weba.douyinvod.com/h264-1080.mp4"],"width":1080,"height":1920,"data_size":2021125}},
{"gear_name":"normal_720_0","bit_rate":900000,"is_h265":0,"format":"mp4","play_addr":{"url_list":["https://v26-web.douyinvod.com/h264-720.mp4"],"width":720,"height":1280,"data_size":1100000}},
{"gear_name":"adapt_lowest_1080_1","bit_rate":500000,"is_h265":1,"format":"mp4","play_addr":{"url_list":["https://v26-web.douyinvod.com/h265-1080.mp4"],"width":1080,"height":1920,"data_size":640000}},
{"gear_name":"low_720_0","bit_rate":278894,"is_h265":0,"format":"dash","play_addr":{"url_list":["https://v26-web.douyinvod.com/dash-720.m4s"],"width":720,"height":1280}}]}}}`

// fakeCapturer 代替无头浏览器，按顺序回放页面会拿到的接口响应。
type fakeCapturer struct {
	mu        sync.Mutex
	pages     []string
	responses []capturedResponse
	err       error
}

func (f *fakeCapturer) Available() error { return nil }

func (f *fakeCapturer) Capture(_ context.Context, pageURL string, match func(string) bool, onResponse func(capturedResponse) (bool, error)) error {
	f.mu.Lock()
	f.pages = append(f.pages, pageURL)
	f.mu.Unlock()
	for _, response := range f.responses {
		if !match(response.URL) {
			continue
		}
		done, err := onResponse(response)
		if err != nil || done {
			return err
		}
	}
	if f.err != nil {
		return f.err
	}
	return newError(ErrTimeout, "页面加载超时，请稍后重试", nil)
}

func douyinResponses(auth, detail string) []capturedResponse {
	return []capturedResponse{
		{URL: "https://open.douyin.com/api/douyin/v1/open/auth/embed_video/", Status: 200, Body: []byte(auth)},
		{URL: "https://www.douyin.com/aweme/v1/web/aweme/related/?aweme_id=7703069533934942826", Status: 200, Body: []byte(`{"aweme_list":[]}`)},
		{URL: "https://www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=7703069533934942826&aid=6383&X-Bogus=abc", Status: 200, Body: []byte(detail)},
	}
}

func TestDouyinShareTextThroughOfficialEmbedPlayer(t *testing.T) {
	media := testMedia(t)
	var mediaHeaders http.Header
	var headersMu sync.Mutex
	m := newMockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "v.douyin.com":
			http.Redirect(w, r, "https://www.iesdouyin.com/share/video/7703069533934942826/?region=CN&mid=1&u_code=0", http.StatusFound)
		case r.Host == "www.iesdouyin.com":
			_, _ = w.Write([]byte("<html>share</html>"))
		case r.URL.Path == "/h264-1080.mp4" && r.Host == "v26-web.douyinvod.com":
			// 主 CDN 地址失效，必须改用备用地址。
			http.Error(w, "forbidden", http.StatusForbidden)
		case strings.HasSuffix(r.URL.Path, ".mp4"):
			headersMu.Lock()
			mediaHeaders = r.Header.Clone()
			headersMu.Unlock()
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(media)
		default:
			http.NotFound(w, r)
		}
	})
	capturer := &fakeCapturer{responses: douyinResponses(`{"auth_result":true,"tips":""}`, douyinDetailFixture)}
	svc := New(m.options(), nil, &douyinEmbedProvider{browser: capturer})
	result, file := parseAndDownload(t, svc, "7.43 复制打开抖音，看看【测试作者的作品】测试抖音视频 # 好物 https://v.douyin.com/AbCdEf9/ wUf:/ 05/12")
	if len(capturer.pages) != 1 || capturer.pages[0] != "https://open.douyin.com/player/video?vid=7703069533934942826&autoplay=0" {
		t.Fatalf("embed page = %v", capturer.pages)
	}
	if result.Meta.ID != "7703069533934942826" || result.Meta.Title != "测试抖音视频 #好物" || result.Meta.Author != "测试作者" || result.Meta.DurationMs != 10134 || result.Provider != "douyin-embed-player" {
		t.Fatalf("douyin meta = %+v", result.Meta)
	}
	if result.Meta.SourceURL != "https://www.douyin.com/video/7703069533934942826" {
		t.Fatalf("source url = %s", result.Meta.SourceURL)
	}
	for _, item := range result.Media {
		if strings.Contains(item.URL, "watermark") || strings.Contains(item.URL, ".m4s") {
			t.Fatalf("watermarked download_addr or dash must not be used: %+v", item)
		}
	}
	headersMu.Lock()
	defer headersMu.Unlock()
	if mediaHeaders.Get("Referer") != "https://www.douyin.com/" || mediaHeaders.Get("Cookie") != "" || file.Size == 0 {
		t.Fatalf("media request headers = %v", mediaHeaders)
	}
	if m.count("v11-weba.douyinvod.com/h264-1080.mp4") != 1 {
		t.Fatal("backup CDN url was not used after the primary failed")
	}
}

func TestDouyinDetailStreamSelection(t *testing.T) {
	result, err := parseDouyinDetail([]byte(douyinDetailFixture), "7703069533934942826")
	if err != nil {
		t.Fatal(err)
	}
	muxed, _, _ := pickMedia(result.Media)
	if muxed == nil || muxed.URL != "https://v26-web.douyinvod.com/h264-1080.mp4" || len(muxed.Backups) != 1 || muxed.VideoCodec != "h264" {
		t.Fatalf("douyin pick = %+v", muxed)
	}
	// 横屏作品：H.264 最高 720p、H.265 有 1080p，同档优先 H.264（后续 FFmpeg 与视频模型兼容性更好）。
	landscape := strings.NewReplacer(`"width":1080,"height":1920,"data_size":2021125}},`, `"width":1280,"height":720,"data_size":2021125}},`).Replace(douyinDetailFixture)
	landscape = strings.Replace(landscape, `"normal_1080_0"`, `"normal_720_0"`, 1)
	result, err = parseDouyinDetail([]byte(landscape), "7703069533934942826")
	if err != nil {
		t.Fatal(err)
	}
	if muxed, _, _ := pickMedia(result.Media); muxed == nil || muxed.VideoCodec != "h264" {
		t.Fatalf("landscape pick = %+v", muxed)
	}
	// 没有 bit_rate 档位时退回 play_addr，并把 playwm 换成无水印的 play。
	noRates := `{"aweme_detail":{"aweme_id":"1234567890","desc":"x","video":{"play_addr":{"url_list":["https://aweme.snssdk.com/aweme/v1/playwm/?video_id=v1"],"width":720,"height":1280}}}}`
	result, err = parseDouyinDetail([]byte(noRates), "1234567890")
	if err != nil || result.Media[0].URL != "https://aweme.snssdk.com/aweme/v1/play/?video_id=v1" {
		t.Fatalf("play_addr fallback = %+v %v", result, err)
	}
	cases := map[string]ErrorCode{
		`{"status_code":0,"aweme_detail":{"aweme_id":"1234567890","images":[{"url_list":["https://p3/x.jpg"]}],"video":{"play_addr":{"url_list":["https://a/b.mp4"]}}}}`: ErrNotVideo,
		`{"status_code":0,"aweme_detail":null,"filter_detail":{"aweme_id":"1234567890","filter_reason":"status_deleted","detail_msg":"作品已删除"}}`:                          ErrNotFound,
		`{"status_code":2053,"status_msg":"not found","aweme_detail":null}`:                                                                                              ErrNotFound,
		`{"status_code":0,"aweme_detail":{"aweme_id":"9999999999","video":{"play_addr":{"url_list":["https://a/b.mp4"]}}}}`:                                              ErrUpstream,
		`not json`: ErrUpstream,
	}
	for body, want := range cases {
		if _, err := parseDouyinDetail([]byte(body), "1234567890"); AsError(err).Code != want {
			t.Fatalf("%s => %v, want %s", body, err, want)
		}
	}
}

func TestDouyinEmbedRefusalsAndNotes(t *testing.T) {
	capturer := &fakeCapturer{responses: douyinResponses(`{"auth_result":false,"tips":"作者已关闭外部播放"}`, douyinDetailFixture)}
	svc := New(Options{NoBrowser: true, QueueTimeout: time.Second}, nil, &douyinEmbedProvider{browser: capturer})
	link, _ := svc.Identify("https://www.douyin.com/video/7703069533934942826")
	_, err := svc.Parse(context.Background(), link, nil)
	if parsed := AsError(err); parsed.Code != ErrNeedsLogin || !strings.Contains(parsed.Message, "作者已关闭外部播放") || parsed.Retryable {
		t.Fatalf("embed refusal => %v", err)
	}
	note, _ := svc.Identify("https://www.douyin.com/note/7396822576074460467")
	before := len(capturer.pages)
	if _, err := svc.Parse(context.Background(), note, nil); AsError(err).Code != ErrNotVideo || len(capturer.pages) != before {
		t.Fatalf("douyin note must be rejected before opening the browser: %v", err)
	}
	// 浏览器超时属于可重试错误：重试一次后如实返回超时。
	slow := &fakeCapturer{}
	svc = New(Options{NoBrowser: true, QueueTimeout: time.Second}, nil, &douyinEmbedProvider{browser: slow})
	link, _ = svc.Identify("https://www.douyin.com/jingxuan?modal_id=7703069533934942826")
	if _, err := svc.Parse(context.Background(), link, nil); AsError(err).Code != ErrTimeout || len(slow.pages) != 2 {
		t.Fatalf("timeout => %v pages=%d", err, len(slow.pages))
	}
}

func channelsFeedFixture(withVideo bool) string {
	video := ""
	if withVideo {
		video = `"mediaType":4,"h264VideoInfo":{"videoUrl":"https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=h264","width":1080,"height":1920,"duration":15.5},"h265VideoInfo":{"videoUrl":"https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=h265","width":1080,"height":1920},`
	}
	return `{"data":{"authorInfo":{"nickname":"测试视频号"},"feedInfo":{` + video + `"picInfo":[],"description":"测试视频号作品 #好物","coverUrl":"https://finder.video.qq.com/251/20304/stodownload?cover=1","createtime":1759734749},"errMsg":{"type":0}},"errCode":0,"errMsg":""}`
}

func TestChannelsShareLinkThroughOfficialPreview(t *testing.T) {
	media := testMedia(t)
	var apiBody map[string]any
	var apiHeaders, mediaHeaders http.Header
	var mu sync.Mutex
	m := newMockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "channels.weixin.qq.com" && r.URL.Path == "/finder-preview/api/feed/get_feed_info" && r.Method == http.MethodPost:
			mu.Lock()
			apiHeaders = r.Header.Clone()
			_ = json.NewDecoder(r.Body).Decode(&apiBody)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, channelsFeedFixture(true))
		case r.Host == "finder.video.qq.com":
			mu.Lock()
			mediaHeaders = r.Header.Clone()
			mu.Unlock()
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(media)
		default:
			http.NotFound(w, r)
		}
	})
	svc := New(m.options(), nil)
	result, _ := parseAndDownload(t, svc, "【测试视频号作品】https://weixin.qq.com/sph/AkBAEKjbtY 在微信中打开")
	mu.Lock()
	defer mu.Unlock()
	if apiBody["shortUri"] != "AkBAEKjbtY" || apiHeaders.Get("Origin") != "https://channels.weixin.qq.com" || !strings.Contains(apiHeaders.Get("Referer"), "AkBAEKjbtY") {
		t.Fatalf("preview api request = %v %v", apiBody, apiHeaders)
	}
	if result.Meta.ID != "sph:AkBAEKjbtY" || result.Meta.Title != "测试视频号作品 #好物" || result.Meta.Author != "测试视频号" || result.Meta.DurationMs != 15500 || result.Provider != "channels-preview" {
		t.Fatalf("channels meta = %+v", result.Meta)
	}
	if muxed, _, _ := pickMedia(result.Media); muxed == nil || !strings.Contains(muxed.URL, "token=h264") {
		t.Fatalf("channels pick = %+v", muxed)
	}
	if mediaHeaders.Get("Referer") != "https://channels.weixin.qq.com/" || mediaHeaders.Get("Cookie") != "" {
		t.Fatalf("media headers = %v", mediaHeaders)
	}
}

func TestChannelsWechatOnlyAndOtherOutcomes(t *testing.T) {
	cases := map[string]ErrorCode{
		channelsFeedFixture(false): ErrNeedsLogin,
		`{"data":{"authorInfo":{"nickname":"a"},"feedInfo":{"mediaType":2,"picInfo":[{"url":"https://x"}],"description":"图片"}},"errCode":0}`: ErrNotVideo,
		`{"errCode":-1,"errMsg":"feed not exist"}`: ErrNotFound,
		`{"errCode":0,"data":{}}`:                  ErrNotFound,
	}
	for body, want := range cases {
		_, err := parseChannelsFeedInfo([]byte(body), "sph:AkBAEKjbtY")
		if parsed := AsError(err); parsed.Code != want {
			t.Fatalf("%s => %v, want %s", body, err, want)
		}
	}
	_, err := parseChannelsFeedInfo([]byte(channelsFeedFixture(false)), "sph:x")
	if !strings.Contains(AsError(err).Message, "只能在微信内观看") || AsError(err).Retryable {
		t.Fatalf("wechat-only message = %v", err)
	}
}

func TestChannelsLinkRecognition(t *testing.T) {
	svc := New(Options{NoBrowser: true}, nil)
	ok := map[string]string{
		"https://weixin.qq.com/sph/AkBAEKjbtY":                                          "sph:AkBAEKjbtY",
		"复制链接 https://weixin.qq.com/sph/A1b2C3d4 ，打开微信观看":                               "sph:A1b2C3d4",
		"https://channels.weixin.qq.com/finder-preview/pages/sph?id=AkBAEKjbtY":         "sph:AkBAEKjbtY",
		"https://channels.weixin.qq.com/web/pages/feed?eid=export%2FUzFfBgAAxJGgAElTOV": "eid:export/UzFfBgAAxJGgAElTOV",
	}
	for text, want := range ok {
		link, err := svc.Identify(text)
		if err != nil || link.Platform.Info().Key != PlatformChannels {
			t.Fatalf("%q => %v %v", text, link, err)
		}
		if id, _ := link.Platform.Normalize(link.URL); id != want {
			t.Fatalf("%q => id %q, want %q", text, id, want)
		}
	}
	for _, text := range []string{"https://mp.weixin.qq.com/s/abcdef", "https://weixin.qq.com/", "https://weixin.qq.com/sph/../../etc"} {
		if link, err := svc.Identify(text); err == nil && link.Platform.Info().Key == PlatformChannels {
			t.Fatalf("%q must not be treated as a 视频号 link", text)
		}
	}
}

func TestDefaultPlatformAllowList(t *testing.T) {
	svc := New(Options{NoBrowser: true, Platforms: DefaultPlatforms()}, nil)
	_, err := svc.Identify("https://www.bilibili.com/video/BV1GJ411x7h7")
	parsed := AsError(err)
	if parsed.Code != ErrUnsupported || !strings.Contains(parsed.Message, "暂不支持哔哩哔哩") || !strings.Contains(parsed.Message, "抖音、快手、小红书、视频号") {
		t.Fatalf("disabled platform => %v", err)
	}
	if user := svc.Platforms(false); len(user) != 4 || user[0].Key != PlatformDouyin || user[3].Key != PlatformChannels {
		t.Fatalf("user platforms = %+v", user)
	}
	admin := svc.Platforms(true)
	if len(admin) != len(svc.Registry().Adapters()) {
		t.Fatalf("admin platforms = %d", len(admin))
	}
	for _, status := range admin {
		if status.Key == PlatformBilibili && status.Capabilities.Support != SupportUnavailable {
			t.Fatalf("bilibili should be closed by default: %+v", status.Capabilities)
		}
	}
	for _, text := range []string{"https://v.douyin.com/AbCd/", "https://v.kuaishou.com/2Bd8Xx", "http://xhslink.com/a/AbCdEf", "https://weixin.qq.com/sph/AkBAEKjbtY"} {
		if _, err := svc.Identify(text); err != nil {
			t.Fatalf("%s should be open: %v", text, err)
		}
	}
}

// 真实 Chromium：验证调试管道、出口代理与响应读取；页面里直连回环地址的请求必须被出口代理拦截。
func TestChromiumCapturerReadsPageRequestsThroughEgress(t *testing.T) {
	path := findChromium()
	if path == "" || !browserPipeSupported {
		t.Skip("chromium not installed")
	}
	var leaked atomic.Int32
	secret := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		leaked.Add(1)
		_, _ = io.WriteString(w, `{"secret":true}`)
	}))
	defer secret.Close()
	secretURL, _ := url.Parse(secret.URL)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<html><body><script>
fetch('http://`+secretURL.Host+`/api/leak').catch(() => {});
fetch('/api/other').then(r => r.text());
setTimeout(() => fetch('/api/detail?aweme_id=42').then(r => r.text()), 300);
</script></body></html>`)
		case "/api/detail":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"aweme_detail":{"aweme_id":"42"}}`)
		case "/api/other":
			_, _ = io.WriteString(w, `{"other":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	siteURL, _ := url.Parse(site.URL)
	_, sitePort, _ := net.SplitHostPort(siteURL.Host)
	_, secretPort, _ := net.SplitHostPort(secretURL.Host)
	opts := Options{
		ChromiumPath: path,
		ResolveHost: func(_ context.Context, host string) ([]string, error) {
			if host == "player.example.test" {
				return []string{"127.0.0.1"}, nil
			}
			return nil, errors.New("blocked by test policy")
		},
		egressPortAllowed: func(port string) bool { return port == sitePort || port == secretPort },
	}
	svc := New(opts, nil)
	t.Cleanup(svc.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var seen []string
	var detail []byte
	err := newChromiumCapturer(svc).Capture(ctx, "http://player.example.test:"+sitePort+"/", func(raw string) bool {
		return strings.Contains(raw, "/api/")
	}, func(resp capturedResponse) (bool, error) {
		seen = append(seen, resp.URL)
		if strings.Contains(resp.URL, "/api/detail") {
			detail = resp.Body
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if string(detail) != `{"aweme_detail":{"aweme_id":"42"}}` {
		t.Fatalf("detail = %q seen=%v", detail, seen)
	}
	if leaked.Load() != 0 {
		t.Fatal("page reached a loopback address through the browser")
	}
}
