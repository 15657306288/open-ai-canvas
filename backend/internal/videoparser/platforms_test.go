package videoparser

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// 每个平台一组独立用例：固定测试数据 + 假上游（mockUpstream / fakeRunner），不依赖真实平台可用性。

const kuaishouFixture = `<!DOCTYPE html><html><head><title>快手</title></head><body>
<script>window.INIT_STATE = {"tusjoh.0sftu0abc":{"result":1,"photo":{"caption":"测试作品 #好物分享","photoType":"VIDEO","photoId":"5253449145901086319","userName":"测试作者","duration":5000,"width":320,"height":240,"mainMvUrls":[{"cdn":"txmov2.a.kwimgs.com","url":"https:\u002F\u002Ftxmov2.a.kwimgs.com\u002Fupic\u002F2025\u002F12\u002F04\u002Ftest_b.mp4?tt=b"}],"coverUrls":[{"cdn":"p2.a.yximgs.com","url":"https://p2.a.yximgs.com/upic/test.jpg"}]}}};</script>
</body></html>`

const kuaishouAtlasFixture = `<script>window.INIT_STATE = {"x":{"photo":{"caption":"图集","photoType":"VERTICAL_ATLAS","mainMvUrls":[]}}}</script>`

const xhsFixture = `<html><script>window.__INITIAL_STATE__={"note":{"firstNoteId":"6411cf99000000001300b6d9","noteDetailMap":{"6411cf99000000001300b6d9":{"note":{"noteId":"6411cf99000000001300b6d9","type":"video","title":"测试笔记","desc":"描述","user":{"nickname":"测试博主"},"imageList":[{"urlDefault":"https://sns-webpic-qc.xhscdn.com/cover.jpg"}],"video":{"capa":{"duration":5},"consumer":{"originVideoKey":"pre_post/origin.mp4"},"media":{"stream":{"h265":[{"masterUrl":"https://sns-video-bd.xhscdn.com/stream/h265.mp4","videoCodec":"hevc","width":1080,"height":1920}],"h264":[{"masterUrl":"https://sns-video-bd.xhscdn.com/stream/h264.mp4","backupUrls":["https://sns-video-hw.xhscdn.com/stream/h264.mp4"],"width":1080,"height":1920,"videoCodec":"h264","avgBitrate":1500000}],"av1":[]}}}}}},"serverRequestInfo":undefined,"user":{"loggedIn":false,"userInfo":undefined}}}</script></html>`

// 移动端 UA 返回的页面结构：笔记数据位于 noteData.data.noteData，作者字段为 nickName。
const xhsMobileFixture = `<html><script>window.__INITIAL_STATE__={"noteData":{"data":{"noteData":{"noteId":"68ab2dd1000000001c0045d0","type":"video","title":"移动端视频笔记","user":{"userId":"u1","nickName":"移动端作者"},"imageList":[{"url":"https://sns-webpic-qc.xhscdn.com/cover.jpg"}],"video":{"capa":{"duration":12},"media":{"stream":{"h264":[{"masterUrl":"https://sns-video-bd.xhscdn.com/mobile/watermarked.mp4","width":720,"height":1280}],"h265":[{"masterUrl":"https://sns-video-bd.xhscdn.com/mobile/clean.mp4","width":720,"height":1280}]}}}}}},"global":{"x":undefined}}</script></html>`

const xhsImageFixture = `<script>window.__INITIAL_STATE__={"note":{"firstNoteId":"6411cf99000000001300b6d9","noteDetailMap":{"6411cf99000000001300b6d9":{"note":{"noteId":"6411cf99000000001300b6d9","type":"normal","title":"图文","imageList":[{"urlDefault":"https://x/1.jpg"}]}}}}}</script>`

const bilibiliFixture = `{"id":"BV1GJ411x7h7","title":"【官方 MV】Never Gonna Give You Up - Rick Astley","uploader":"索尼音乐中国","duration":212.393,"thumbnail":"https://i0.hdslb.com/bfs/archive/cover.jpg","webpage_url":"https://www.bilibili.com/video/BV1GJ411x7h7/","extractor_key":"BiliBili","formats":[
{"format_id":"30280","url":"https://upos-sz-mirrorcos.bilivideo.com/audio.m4s","protocol":"https","ext":"m4a","vcodec":"none","acodec":"mp4a.40.2","tbr":319.4,"http_headers":{"User-Agent":"Mozilla/5.0","Referer":"https://www.bilibili.com/video/BV1GJ411x7h7/","Cookie":"SESSDATA=secret"}},
{"format_id":"30216","url":"https://upos-sz-mirrorcos.bilivideo.com/audio-low.m4s","protocol":"https","ext":"m4a","vcodec":"none","acodec":"mp4a.40.2","tbr":67.2},
{"format_id":"30080","url":"https://upos-sz-mirrorcos.bilivideo.com/video-1080-avc.m4s","protocol":"https","ext":"mp4","vcodec":"avc1.640032","acodec":"none","width":1920,"height":1080,"tbr":2100,"http_headers":{"Referer":"https://www.bilibili.com/video/BV1GJ411x7h7/","Cookie":"SESSDATA=secret"}},
{"format_id":"30077","url":"https://upos-sz-mirrorcos.bilivideo.com/video-1080-hevc.m4s","protocol":"https","ext":"mp4","vcodec":"hev1.1.6.L150.90","acodec":"none","width":1920,"height":1080,"tbr":900},
{"format_id":"30016","url":"https://upos-sz-mirrorcos.bilivideo.com/video-360.m4s","protocol":"https","ext":"mp4","vcodec":"avc1.64001E","acodec":"none","width":640,"height":360,"tbr":400}]}`

const weiboFixture = `{"id":"4797700463137878","title":"测试微博视频","uploader":"测试博主","duration":76,"thumbnail":"https://wx1.sinaimg.cn/orj480/cover.jpg","webpage_url":"https://weibo.com/tv/show/1034:4797699866951785","formats":[
{"format_id":"mp4_ld","url":"https://f.video.weibocdn.com/o0/ld.mp4","ext":"mp4","width":640,"height":360,"tbr":500,"http_headers":{"Referer":"https://weibo.com/"}},
{"format_id":"mp4_1080p","url":"https://f.video.weibocdn.com/o0/1080.mp4","ext":"mp4","width":1920,"height":1080,"tbr":3000},
{"format_id":"hls","url":"https://f.video.weibocdn.com/o0/index.m3u8","protocol":"m3u8_native","ext":"mp4","width":1920,"height":1080}]}`

const tiktokFixture = `{"id":"7300000000000000000","title":"tiktok test","uploader":"tester","duration":15.2,"formats":[
{"format_id":"download","format_note":"watermarked","url":"https://v16-webapp-prime.tiktok.com/video/watermark.mp4","ext":"mp4","vcodec":"h264","acodec":"aac","width":720,"height":1280},
{"format_id":"h264_540p","url":"https://v16-webapp-prime.tiktok.com/video/h264.mp4","ext":"mp4","vcodec":"h264","acodec":"aac","width":576,"height":1024,"http_headers":{"Referer":"https://www.tiktok.com/","Cookie":"tt_chain_token=secret"}}]}`

const youtubeFixture = `{"id":"dQw4w9WgXcQ","title":"Rick Astley - Never Gonna Give You Up","channel":"Rick Astley","duration":213,"formats":[
{"format_id":"sb0","url":"https://i.ytimg.com/sb/x.jpg","protocol":"mhtml","ext":"mhtml","vcodec":"none","acodec":"none"},
{"format_id":"18","url":"https://rr1---sn.googlevideo.com/18.mp4","protocol":"https","ext":"mp4","vcodec":"avc1.42001E","acodec":"mp4a.40.2","width":640,"height":360,"tbr":500},
{"format_id":"137","url":"https://rr1---sn.googlevideo.com/137.mp4","protocol":"https","ext":"mp4","vcodec":"avc1.640028","acodec":"none","width":1920,"height":1080,"tbr":4000},
{"format_id":"248","url":"https://rr1---sn.googlevideo.com/248.webm","protocol":"https","ext":"webm","vcodec":"vp9","acodec":"none","width":1920,"height":1080,"tbr":2500},
{"format_id":"401","url":"https://rr1---sn.googlevideo.com/401.mp4","protocol":"https","ext":"mp4","vcodec":"av01.0.12M.08","acodec":"none","width":3840,"height":2160,"tbr":18000},
{"format_id":"140","url":"https://rr1---sn.googlevideo.com/140.m4a","protocol":"https","ext":"m4a","vcodec":"none","acodec":"mp4a.40.2","tbr":129},
{"format_id":"251","url":"https://rr1---sn.googlevideo.com/251.webm","protocol":"https","ext":"webm","vcodec":"none","acodec":"opus","tbr":160},
{"format_id":"hls-1080","url":"https://manifest.googlevideo.com/x.m3u8","protocol":"m3u8_native","ext":"mp4","vcodec":"avc1","acodec":"mp4a","height":1080}]}`

const instagramFixture = `{"id":"C1a2B3c4D5e","title":"Video by tester","uploader":"tester","duration":12.5,"formats":[
{"format_id":"dash-video","url":"https://scontent.cdninstagram.com/v/dash-v.mp4","ext":"mp4","vcodec":"avc1.4d401f","acodec":"none","width":720,"height":1280},
{"format_id":"dash-audio","url":"https://scontent.cdninstagram.com/v/dash-a.mp4","ext":"m4a","vcodec":"none","acodec":"mp4a.40.5"},
{"format_id":"1","url":"https://scontent.cdninstagram.com/v/muxed.mp4","ext":"mp4","width":720,"height":1280}]}`

const xFixture = `{"id":"1790000000000000000","title":"tester - video","uploader":"tester","duration":30.0,"formats":[
{"format_id":"hls-832","url":"https://video.twimg.com/x.m3u8","protocol":"m3u8_native","ext":"mp4","width":720,"height":1280},
{"format_id":"http-632","url":"https://video.twimg.com/ext_tw_video/1/pu/vid/320x568/a.mp4","protocol":"https","ext":"mp4","width":320,"height":568,"tbr":632},
{"format_id":"http-2176","url":"https://video.twimg.com/ext_tw_video/1/pu/vid/720x1280/b.mp4","protocol":"https","ext":"mp4","width":720,"height":1280,"tbr":2176}]}`

// mediaServer 把所有媒体 CDN 请求统一返回同一份测试视频，并记录请求头，便于断言没有带 Cookie。
func newPlatformUpstream(t *testing.T, pages map[string]string, redirects map[string]string, media []byte) (*mockUpstream, *http.Header) {
	t.Helper()
	lastMediaHeaders := &http.Header{}
	m := newMockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		key := r.Host + r.URL.Path
		if target, ok := redirects[key]; ok {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		if page, ok := pages[key]; ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		}
		if strings.HasSuffix(r.URL.Path, ".mp4") || strings.HasSuffix(r.URL.Path, ".m4s") || strings.HasSuffix(r.URL.Path, ".m4a") {
			*lastMediaHeaders = r.Header.Clone()
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(media)
			return
		}
		http.NotFound(w, r)
	})
	return m, lastMediaHeaders
}

func testMedia(t *testing.T) []byte {
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return makeMedia(t, "muxed", 5)
	}
	return fakeMP4(4096)
}

func parseAndDownload(t *testing.T, svc *Service, text string) (*Result, *DownloadedFile) {
	t.Helper()
	link, err := svc.Identify(text)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	var stages []string
	result, err := svc.Parse(context.Background(), link, func(stage Stage) { stages = append(stages, stage.Name) })
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stages) == 0 || stages[0] != "resolving" {
		t.Fatalf("stages = %v", stages)
	}
	file, err := svc.Download(context.Background(), result, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if info, err := os.Stat(file.Path); err != nil || info.Size() == 0 || sniffContainer(file.Path) != "mp4" {
		t.Fatalf("downloaded file invalid: %v %v", info, err)
	}
	return result, file
}

func TestDouyinWithoutChromiumIsUnavailableAndExplainsWhy(t *testing.T) {
	svc := New(Options{NoBrowser: true}, nil)
	link, err := svc.Identify("复制打开抖音 https://v.douyin.com/FKWYQmtQ79E/")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Parse(context.Background(), link, nil)
	parsed := AsError(err)
	if parsed == nil || parsed.Code != ErrUnavailable || !strings.Contains(parsed.Message, "Chromium") || !strings.Contains(parsed.Message, "本地上传") {
		t.Fatalf("douyin without browser must fail fast with explanation: %v", err)
	}
	if caps := svc.EffectiveCapabilities(link.Platform); caps.Support != SupportUnavailable || !strings.Contains(caps.ConfigMissing, "Chromium") {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestDouyinSharePageProviderParsesRouterData(t *testing.T) {
	page := `<script>window._ROUTER_DATA = {"loaderData":{"video_(id)/page":{"videoInfoRes":{"item_list":[{"desc":"测试抖音","video":{"play_addr":{"uri":"v0200fg10000abc","url_list":["https:\u002F\u002Faweme.snssdk.com\u002Faweme\u002Fv1\u002Fplaywm\u002F?video_id=v0200fg10000abc&ratio=720p"]}}}]}}}}</script>`
	m, _ := newPlatformUpstream(t, map[string]string{"www.iesdouyin.com/share/video/7618253960368885046/": page}, nil, nil)
	svc := New(m.options(), nil)
	adapter, _ := svc.Registry().Get(PlatformDouyin)
	u, _ := parseShareURL("https://www.iesdouyin.com/share/video/7618253960368885046/")
	result, err := (&douyinSharePageProvider{svc: svc}).Resolve(context.Background(), Request{Platform: adapter, URL: u, ID: "7618253960368885046"})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Media[0].URL; got != "https://aweme.snssdk.com/aweme/v1/play/?video_id=v0200fg10000abc&ratio=720p" || result.Meta.Title != "测试抖音" {
		t.Fatalf("douyin result = %+v", result)
	}
	note, _ := parseShareURL("https://www.douyin.com/note/7396822576074460467")
	if _, err := (&douyinSharePageProvider{svc: svc}).Resolve(context.Background(), Request{Platform: adapter, URL: note}); AsError(err).Code != ErrNotVideo {
		t.Fatalf("douyin note must be rejected as not video: %v", err)
	}
}

func TestKuaishouShortLinkParseAndDownload(t *testing.T) {
	media := testMedia(t)
	m, headers := newPlatformUpstream(t,
		map[string]string{"v.m.chenzhongtech.com/fw/photo/3x8zha3ipq6bg8q": kuaishouFixture, "www.kuaishou.com/short-video/3x8zha3ipq6bg8q": "<html>shell</html>"},
		map[string]string{"v.kuaishou.com/2Bd8Xx": "https://www.kuaishou.com/short-video/3x8zha3ipq6bg8q?fid=1"}, media)
	svc := New(m.options(), nil)
	result, file := parseAndDownload(t, svc, "https://v.kuaishou.com/2Bd8Xx 好物 该作品在快手被播放过")
	if result.Meta.ID != "3x8zha3ipq6bg8q" || result.Meta.Title != "测试作品 #好物分享" || result.Meta.Author != "测试作者" || result.Meta.DurationMs != 5000 || result.Provider != "kuaishou-share-page" {
		t.Fatalf("kuaishou meta = %+v", result.Meta)
	}
	if result.Meta.SourceURL != "https://www.kuaishou.com/short-video/3x8zha3ipq6bg8q" || !strings.Contains(result.Media[0].URL, "txmov2.a.kwimgs.com/upic/2025/12/04/test_b.mp4") {
		t.Fatalf("kuaishou media = %+v", result)
	}
	if headers.Get("Cookie") != "" || headers.Get("Referer") != "https://v.m.chenzhongtech.com/" || file.Merged {
		t.Fatalf("media request headers = %v", headers)
	}
	adapter, _ := svc.Registry().Get(PlatformKuaishou)
	if _, err := parseKuaishouPage([]byte(kuaishouAtlasFixture), "x"); AsError(err).Code != ErrNotVideo {
		t.Fatalf("atlas must be not_video: %v", err)
	}
	if caps := svc.EffectiveCapabilities(adapter); caps.Support != SupportSupported {
		t.Fatalf("kuaishou caps = %+v", caps)
	}
}

func TestXiaohongshuPrefersWatermarkFreeH265AndRejectsImageNotes(t *testing.T) {
	result, err := parseXiaohongshuPage([]byte(xhsFixture), "6411cf99000000001300b6d9")
	if err != nil {
		t.Fatal(err)
	}
	// h264 分享流带服务端烧录的水印，h265 流无水印：有 h265 时必须选 h265。
	muxed, _, _ := pickMedia(result.Media)
	if muxed == nil || muxed.URL != "https://sns-video-bd.xhscdn.com/stream/h265.mp4" || result.Meta.Title != "测试笔记" || result.Meta.Author != "测试博主" || result.Meta.DurationMs != 5000 {
		t.Fatalf("xhs result = %+v muxed=%+v", result.Meta, muxed)
	}
	onlyH264 := strings.Replace(xhsFixture, `"h265":[{"masterUrl":"https://sns-video-bd.xhscdn.com/stream/h265.mp4","videoCodec":"hevc","width":1080,"height":1920}],`, "", 1)
	if result, err := parseXiaohongshuPage([]byte(onlyH264), "6411cf99000000001300b6d9"); err != nil {
		t.Fatal(err)
	} else if muxed, _, _ := pickMedia(result.Media); muxed == nil || muxed.URL != "https://sns-video-bd.xhscdn.com/stream/h264.mp4" || len(muxed.Backups) != 1 {
		t.Fatalf("h264-only note must fall back to h264 with backup: %+v", muxed)
	}
	mobile, err := parseXiaohongshuPage([]byte(xhsMobileFixture), "")
	if err != nil {
		t.Fatal(err)
	}
	if muxed, _, _ := pickMedia(mobile.Media); muxed == nil || muxed.URL != "https://sns-video-bd.xhscdn.com/mobile/clean.mp4" || mobile.Meta.Author != "移动端作者" || mobile.Meta.Title != "移动端视频笔记" {
		t.Fatalf("mobile structure = %+v %+v", mobile.Meta, muxed)
	}
	if _, err := parseXiaohongshuPage([]byte(`<script>window.__INITIAL_STATE__={"global":{"appSettings":{}}}</script>`), "x"); AsError(err).Code != ErrNotFound {
		t.Fatalf("empty login shell must be not_found: %v", err)
	}
	if _, err := parseXiaohongshuPage([]byte(xhsImageFixture), "6411cf99000000001300b6d9"); AsError(err).Code != ErrNotVideo {
		t.Fatalf("image note must be not_video: %v", err)
	}
	fallback := strings.Replace(xhsFixture, `"stream":{`, `"streamX":{`, 1)
	if result, err := parseXiaohongshuPage([]byte(fallback), "6411cf99000000001300b6d9"); err != nil || result.Media[0].URL != "https://sns-video-bd.xhscdn.com/pre_post/origin.mp4" {
		t.Fatalf("originVideoKey fallback = %+v %v", result, err)
	}
}

func TestXiaohongshuExpiredTokenRedirectIsReported(t *testing.T) {
	m, _ := newPlatformUpstream(t, map[string]string{"www.xiaohongshu.com/404": "<html>404</html>"},
		map[string]string{"www.xiaohongshu.com/discovery/item/683826d5000000000303ce03": "https://www.xiaohongshu.com/404?error_code=300031"}, nil)
	svc := New(m.options(), nil)
	link, err := svc.Identify("https://www.xiaohongshu.com/discovery/item/683826d5000000000303ce03?xsec_token=ABG&xsec_source=pc_share")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Parse(context.Background(), link, nil)
	if parsed := AsError(err); parsed.Code != ErrNotFound || !strings.Contains(parsed.Message, "分享链接") {
		t.Fatalf("expired xhs link => %v", err)
	}
}

func TestXiaohongshuShortLinkWithScriptRedirect(t *testing.T) {
	media := testMedia(t)
	landing := `<html><script>window.location.replace("https:\u002F\u002Fwww.xiaohongshu.com\u002Fdiscovery\u002Fitem\u002F68ab2dd1000000001c0045d0?xsec_token=CBabc&xsec_source=app_share")</script></html>`
	var gotUA string
	m := newMockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "xhslink.com":
			_, _ = w.Write([]byte(landing))
		case r.Host == "www.xiaohongshu.com":
			gotUA = r.UserAgent()
			_, _ = w.Write([]byte(xhsMobileFixture))
		default:
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(media)
		}
	})
	svc := New(m.options(), nil)
	result, _ := parseAndDownload(t, svc, "67 某某发布了一篇小红书笔记，快来看吧！ 😆 Abc 😆 http://xhslink.com/m/AbCdEf，复制本条信息，打开【小红书】App查看精彩内容！")
	if result.Meta.ID != "68ab2dd1000000001c0045d0" || result.Provider != "xiaohongshu-page" || !strings.Contains(gotUA, "iPhone") {
		t.Fatalf("xhs short link result = %+v ua=%q", result.Meta, gotUA)
	}
}

func ytdlpService(t *testing.T, m *mockUpstream, outputs map[string]string, proxy string) (*Service, *fakeRunner) {
	t.Helper()
	runner := &fakeRunner{outputs: outputs}
	opts := m.options()
	opts.YTDLPPath, opts.Runner, opts.OverseasProxy = "/opt/yt-dlp/bin/yt-dlp", runner, proxy
	svc := New(opts, nil)
	t.Cleanup(svc.Close)
	return svc, runner
}

func TestBilibiliShortLinkSeparateStreamsAreMerged(t *testing.T) {
	t.Setenv("CANVAS_DATABASE_URL", "postgres://user:secret-password@db/app")
	videoBytes, audioBytes := fakeMP4(4096), fakeMP4(2048)
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		videoBytes, audioBytes = makeMedia(t, "video", 5), makeMedia(t, "audio", 5)
	}
	headers := http.Header{}
	var headersMu sync.Mutex
	m := newMockUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "b23.tv":
			http.Redirect(w, r, "https://www.bilibili.com/video/BV1GJ411x7h7/?share_source=copy", http.StatusFound)
		case r.Host == "www.bilibili.com":
			_, _ = w.Write([]byte("<html>bilibili</html>"))
		case strings.Contains(r.URL.Path, "audio"):
			headersMu.Lock()
			headers = r.Header.Clone()
			headersMu.Unlock()
			_, _ = w.Write(audioBytes)
		default:
			_, _ = w.Write(videoBytes)
		}
	})
	svc, runner := ytdlpService(t, m, map[string]string{"https://www.bilibili.com/video/BV1GJ411x7h7/?share_source=copy": bilibiliFixture}, "")
	link, err := svc.Identify("【测试视频-哔哩哔哩】 https://b23.tv/AbCdEf9")
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Parse(context.Background(), link, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, video, audio := pickMedia(result.Media)
	if video == nil || audio == nil || !strings.Contains(video.URL, "video-1080-avc") || !strings.Contains(audio.URL, "/audio.m4s") {
		t.Fatalf("bilibili selection video=%+v audio=%+v", video, audio)
	}
	if _, ok := video.Headers["Cookie"]; ok || video.Headers["Referer"] == "" {
		t.Fatalf("unsafe headers kept: %v", video.Headers)
	}
	if result.Meta.ID != "BV1GJ411x7h7" || result.Meta.DurationMs != 212393 || result.Meta.PlatformLabel != "哔哩哔哩" {
		t.Fatalf("bilibili meta = %+v", result.Meta)
	}
	args := strings.Join(runner.args[0], " ")
	for _, want := range []string{"--ignore-config", "--no-cookies", "--skip-download", "--proxy http://127.0.0.1:"} {
		if !strings.Contains(args, want) {
			t.Fatalf("yt-dlp args missing %q: %s", want, args)
		}
	}
	for _, entry := range runner.env[0] {
		if strings.Contains(entry, "secret-password") || strings.HasPrefix(entry, "HTTP_PROXY") || strings.HasPrefix(entry, "CANVAS_") {
			t.Fatalf("yt-dlp env leaked %s", entry)
		}
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return
	}
	file, err := svc.Download(context.Background(), result, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	headersMu.Lock()
	cookie, referer := headers.Get("Cookie"), headers.Get("Referer")
	headersMu.Unlock()
	if !file.Merged || cookie != "" || referer == "" {
		t.Fatalf("bilibili download merged=%v cookie=%q referer=%q", file.Merged, cookie, referer)
	}
	assertStreams(t, file.Path, true, true)
}

// assertStreams 用 ffprobe 检查文件里确实有画面与音轨（FFmpeg 读取 + 音频保留）。
func assertStreams(t *testing.T, path string, wantVideo, wantAudio bool) {
	t.Helper()
	output, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	streams := string(output)
	if strings.Contains(streams, "video") != wantVideo || strings.Contains(streams, "audio") != wantAudio {
		t.Fatalf("streams = %q", streams)
	}
}

func TestWeiboPicksBestDirectMP4AndSkipsHLS(t *testing.T) {
	media := testMedia(t)
	m, headers := newPlatformUpstream(t, nil, map[string]string{"t.cn/A6abcdEf": "https://weibo.com/tv/show/1034:4797699866951785?from=old_pc_videoshow"}, media)
	svc, _ := ytdlpService(t, m, map[string]string{"https://weibo.com/tv/show/1034:4797699866951785?from=old_pc_videoshow": weiboFixture}, "")
	result, file := parseAndDownload(t, svc, "看这个 https://t.cn/A6abcdEf 。")
	muxed, _, _ := pickMedia(result.Media)
	if muxed == nil || !strings.HasSuffix(muxed.URL, "/1080.mp4") || result.Meta.Title != "测试微博视频" || result.Meta.DurationMs != 76000 {
		t.Fatalf("weibo result = %+v %+v", result.Meta, muxed)
	}
	if headers.Get("Cookie") != "" || file.Merged {
		t.Fatalf("weibo download headers=%v merged=%v", headers, file.Merged)
	}
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		assertStreams(t, file.Path, true, true)
	}
}

func TestWeiboShortLinkToForeignSiteIsBlocked(t *testing.T) {
	m, _ := newPlatformUpstream(t, nil, map[string]string{"t.cn/Evil123": "https://evil.example.com/steal"}, nil)
	svc, _ := ytdlpService(t, m, nil, "")
	link, err := svc.Identify("https://t.cn/Evil123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Parse(context.Background(), link, nil); AsError(err).Code != ErrBlocked {
		t.Fatalf("redirect to foreign host must be blocked: %v", err)
	}
	if m.count("evil.example.com/steal") != 0 {
		t.Fatal("foreign redirect target was requested")
	}
}

func TestXiguaIsMarkedUnavailableWithoutCallingUpstream(t *testing.T) {
	m, _ := newPlatformUpstream(t, nil, nil, nil)
	svc, runner := ytdlpService(t, m, nil, "")
	link, err := svc.Identify("https://www.ixigua.com/6996881461559165471")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Parse(context.Background(), link, nil); AsError(err).Code != ErrUnavailable {
		t.Fatalf("xigua => %v", err)
	}
	if runner.calls != 0 {
		t.Fatal("unavailable platform must not call providers")
	}
}

func TestTikTokRequiresProxyThenSkipsWatermarkAndCookie(t *testing.T) {
	m, headers := newPlatformUpstream(t, nil, nil, testMedia(t))
	noProxy, _ := ytdlpService(t, m, nil, "")
	link, _ := noProxy.Identify("https://www.tiktok.com/@user/video/7300000000000000000")
	if _, err := noProxy.Parse(context.Background(), link, nil); AsError(err).Code != ErrNeedsLogin || !strings.Contains(AsError(err).Message, "海外代理") {
		t.Fatalf("tiktok without proxy => %v", err)
	}
	svc, runner := ytdlpService(t, m, map[string]string{"https://www.tiktok.com/@user/video/7300000000000000000": tiktokFixture}, "socks5://proxy.internal:1080")
	result, _ := parseAndDownload(t, svc, "https://www.tiktok.com/@user/video/7300000000000000000")
	muxed, _, _ := pickMedia(result.Media)
	if muxed == nil || !strings.HasSuffix(muxed.URL, "/h264.mp4") || headers.Get("Cookie") != "" || headers.Get("Referer") != "https://www.tiktok.com/" {
		t.Fatalf("tiktok selection = %+v headers=%v", muxed, headers)
	}
	if !strings.Contains(strings.Join(runner.args[0], " "), "--proxy socks5://proxy.internal:1080") {
		t.Fatalf("overseas proxy not passed: %v", runner.args[0])
	}
}

func TestYouTubePrefersH264UpTo1080AndM4A(t *testing.T) {
	m, _ := newPlatformUpstream(t, nil, nil, nil)
	svc, _ := ytdlpService(t, m, map[string]string{"https://youtu.be/dQw4w9WgXcQ?si=xyz": youtubeFixture}, "http://proxy.internal:3128")
	link, _ := svc.Identify("Watch: https://youtu.be/dQw4w9WgXcQ?si=xyz")
	result, err := svc.Parse(context.Background(), link, nil)
	if err != nil {
		t.Fatal(err)
	}
	muxed, video, audio := pickMedia(result.Media)
	if muxed != nil || video == nil || audio == nil || !strings.HasSuffix(video.URL, "/137.mp4") || !strings.HasSuffix(audio.URL, "/140.m4a") {
		t.Fatalf("youtube selection muxed=%+v video=%+v audio=%+v", muxed, video, audio)
	}
	for _, item := range result.Media {
		if strings.Contains(item.URL, ".m3u8") || item.Ext == "mhtml" {
			t.Fatalf("hls / storyboard formats must be skipped: %+v", item)
		}
	}
}

func TestInstagramPrefersMuxedAtSameResolution(t *testing.T) {
	m, _ := newPlatformUpstream(t, nil, nil, nil)
	svc, _ := ytdlpService(t, m, map[string]string{"https://www.instagram.com/reel/C1a2B3c4D5e/?igsh=abc": instagramFixture}, "http://proxy.internal:3128")
	link, _ := svc.Identify("https://www.instagram.com/reel/C1a2B3c4D5e/?igsh=abc")
	result, err := svc.Parse(context.Background(), link, nil)
	if err != nil {
		t.Fatal(err)
	}
	if muxed, _, _ := pickMedia(result.Media); muxed == nil || !strings.HasSuffix(muxed.URL, "/muxed.mp4") {
		t.Fatalf("instagram selection = %+v", muxed)
	}
	caps := svc.EffectiveCapabilities(link.Platform)
	if !caps.NeedsLogin || caps.Support != SupportPartial {
		t.Fatalf("instagram caps = %+v", caps)
	}
}

func TestXShortLinkAndDirectVariants(t *testing.T) {
	m, _ := newPlatformUpstream(t, nil, map[string]string{"t.co/AbCd": "https://x.com/someone/status/1790000000000000000"}, testMedia(t))
	svc, _ := ytdlpService(t, m, map[string]string{"https://x.com/someone/status/1790000000000000000": xFixture}, "http://proxy.internal:3128")
	result, _ := parseAndDownload(t, svc, "https://t.co/AbCd")
	muxed, _, _ := pickMedia(result.Media)
	if muxed == nil || !strings.HasSuffix(muxed.URL, "/720x1280/b.mp4") || result.Meta.SourceURL != "https://x.com/i/status/1790000000000000000" {
		t.Fatalf("x selection = %+v meta=%+v", muxed, result.Meta)
	}
}

func TestBilibiliLongVideoFallsBackToQualityWithinSizeLimit(t *testing.T) {
	result, err := parseYTDLPOutput([]byte(bilibiliFixture))
	if err != nil {
		t.Fatal(err)
	}
	// 120 秒：H.264 1080p 约 31 MB + 音频 5 MB，保留首选。
	_, video, audio := pickMediaWithin(result.Media, 100<<20, 120_000)
	if video == nil || audio == nil || !strings.Contains(video.URL, "video-1080-avc") {
		t.Fatalf("short video should keep h264 1080p: %+v", video)
	}
	// 600 秒：H.264 1080p 约 157 MB 超限，同档位 H.265（900 kbps，约 67 MB + 音频 24 MB）放得下。
	_, video, _ = pickMediaWithin(result.Media, 100<<20, 600_000)
	if !strings.Contains(video.URL, "video-1080-hevc") {
		t.Fatalf("600s selection = %+v", video)
	}
	// 900 秒：两个 1080p 都超限，退到 360p（约 45 MB + 音频 36 MB）。
	_, video, _ = pickMediaWithin(result.Media, 100<<20, 900_000)
	if !strings.Contains(video.URL, "video-360") {
		t.Fatalf("900s selection = %+v", video)
	}
}

func TestYTDLPErrorClassification(t *testing.T) {
	cases := map[string]ErrorCode{
		"ERROR: [Douyin] 7396822576074460467: Fresh cookies (not necessarily logged in) are needed": ErrNeedsLogin,
		"ERROR: [youtube] x: Sign in to confirm you're not a bot":                                   ErrNeedsLogin,
		"ERROR: [youtube] x: Video unavailable":                                                     ErrNotFound,
		"ERROR: [bilibili] x: HTTP Error 404: Not Found":                                            ErrNotFound,
		"ERROR: This video is DRM protected":                                                        ErrDRM,
		"ERROR: Unable to download webpage: HTTP Error 503: Service Unavailable":                    ErrUpstream,
		"ERROR: Read timed out":                                                                     ErrUpstream,
		"ERROR: Unsupported URL: https://www.bilibili.com/":                                         ErrUnsupported,
		"ERROR: The uploader has not made this video available in your country":                     ErrUnavailable,
	}
	for stderr, want := range cases {
		if got := AsError(classifyYTDLPError(stderr, nil)); got.Code != want {
			t.Fatalf("%q => %s, want %s", stderr, got.Code, want)
		}
	}
	if _, err := parseYTDLPOutput([]byte(`{"id":"x","is_live":true,"formats":[]}`)); AsError(err).Code != ErrNotVideo {
		t.Fatalf("live => %v", err)
	}
	if _, err := parseYTDLPOutput([]byte(`{"id":"x","formats":[{"url":"https://a/b.mp4","ext":"mp4","has_drm":true}]}`)); AsError(err).Code != ErrDRM {
		t.Fatalf("drm => %v", err)
	}
	if _, err := parseYTDLPOutput([]byte(`{"id":"x","availability":"premium_only","formats":[]}`)); AsError(err).Code != ErrNeedsLogin {
		t.Fatalf("premium => %v", err)
	}
	result, err := parseYTDLPOutput([]byte(`{"_type":"playlist","entries":[{"id":"p1","title":"P1","url":"https://upos.bilivideo.com/p1.mp4","ext":"mp4"}]}`))
	if err != nil || result.Meta.ID != "p1" || len(result.Media) != 1 {
		t.Fatalf("playlist first entry = %+v %v", result, err)
	}
}
