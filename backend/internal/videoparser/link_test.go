package videoparser

import (
	"strings"
	"testing"
)

func TestIdentifyShareTextPerPlatform(t *testing.T) {
	svc := New(Options{}, nil)
	cases := []struct {
		name, text, platform, host string
	}{
		{"douyin", "7.43 复制打开抖音，看看【某某的作品】好物推荐 https://v.douyin.com/iRNBho6u/ wUf:/ 05/12", PlatformDouyin, "v.douyin.com"},
		{"douyin-bare", "4.82 去抖音看看【测试】v.douyin.com/AbCd123/ 复制此链接", PlatformDouyin, "v.douyin.com"},
		{"kuaishou", "https://v.kuaishou.com/2Bd8Xx 好物 该作品在快手被播放过", PlatformKuaishou, "v.kuaishou.com"},
		{"xiaohongshu", "【标题】😆 2Q1xXc http://xhslink.com/a/AbCdEf，复制本条信息，打开【小红书】App查看", PlatformXiaohongshu, "xhslink.com"},
		{"bilibili", "【测试视频-哔哩哔哩】 https://b23.tv/AbCdEf9", PlatformBilibili, "b23.tv"},
		{"bilibili-full", "https://www.bilibili.com/video/BV1GJ411x7h7/?spm_id_from=333.1007", PlatformBilibili, "www.bilibili.com"},
		{"weibo", "分享一条微博 https://weibo.com/7827771738/N4xlMvjhI?from=page_100 来自微博", PlatformWeibo, "weibo.com"},
		{"weibo-short", "看这个 http://t.cn/A6abcdEf 。", PlatformWeibo, "t.cn"},
		{"xigua", "https://www.ixigua.com/6996881461559165471?logTag=abc", PlatformXigua, "www.ixigua.com"},
		{"tiktok", "Check this out! https://vm.tiktok.com/ZMabc123/ #fyp", PlatformTikTok, "vm.tiktok.com"},
		{"youtube", "Watch: https://youtu.be/dQw4w9WgXcQ?si=xyz", PlatformYouTube, "youtu.be"},
		{"youtube-shorts", "https://www.youtube.com/shorts/abcdefghijk", PlatformYouTube, "www.youtube.com"},
		{"instagram", "https://www.instagram.com/reel/C1a2B3c4D5e/?igsh=abc", PlatformInstagram, "www.instagram.com"},
		{"x", "https://x.com/someone/status/1790000000000000000?s=20", PlatformX, "x.com"},
		{"twitter", "https://twitter.com/someone/status/1790000000000000000", PlatformX, "twitter.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			link, err := svc.Identify(tc.text)
			if err != nil {
				t.Fatalf("identify %q: %v", tc.text, err)
			}
			if link.Platform.Info().Key != tc.platform || link.URL.Host != tc.host {
				t.Fatalf("got platform=%s host=%s, want %s %s", link.Platform.Info().Key, link.URL.Host, tc.platform, tc.host)
			}
			if strings.ContainsAny(link.URL.String(), "，。！") {
				t.Fatalf("chinese punctuation leaked into url: %s", link.URL)
			}
		})
	}
}

func TestIdentifyRejectsUnsupportedAndUnsafeLinks(t *testing.T) {
	svc := New(Options{}, nil)
	cases := map[string]ErrorCode{
		"":                                  ErrInvalidLink,
		"没有链接":                              ErrInvalidLink,
		"https://example.com/video.mp4":     ErrUnsupported,
		"https://evil-douyin.com/video/1":   ErrUnsupported,
		"https://douyin.com.evil.com/x":     ErrUnsupported,
		"https://127.0.0.1/video/1":         ErrUnsupported,
		"https://user:pass@www.douyin.com/": ErrBlocked,
		"https://www.douyin.com:8443/video/7618253960368885046": ErrBlocked,
	}
	for text, want := range cases {
		_, err := svc.Identify(text)
		if got := AsError(err); got == nil || got.Code != want {
			t.Fatalf("%q => %v, want %s", text, err, want)
		}
	}
	if _, err := svc.Identify(strings.Repeat("长", maxShareTextRunes+1)); AsError(err).Code != ErrInvalidLink {
		t.Fatalf("overlong share text must be rejected: %v", err)
	}
	// 文案里先出现不支持的网站、后出现支持的平台时，取支持的那个。
	link, err := svc.Identify("先看 https://example.com/a 再看 https://www.bilibili.com/video/BV1GJ411x7h7")
	if err != nil || link.Platform.Info().Key != PlatformBilibili {
		t.Fatalf("mixed links => %v %v", link, err)
	}
}

func TestPlatformNormalizeExtractsStableIDs(t *testing.T) {
	registry := DefaultRegistry()
	cases := []struct {
		raw, platform, id, canonical string
	}{
		{"https://www.iesdouyin.com/share/video/7618253960368885046/?from_ssr=1&did=MS4w", PlatformDouyin, "7618253960368885046", "https://www.douyin.com/video/7618253960368885046"},
		{"https://www.douyin.com/note/7396822576074460467", PlatformDouyin, "7396822576074460467", "https://www.douyin.com/video/7396822576074460467"},
		{"https://v.m.chenzhongtech.com/fw/photo/3x8zha3ipq6bg8q?fid=1&cc=share_copylink", PlatformKuaishou, "3x8zha3ipq6bg8q", "https://www.kuaishou.com/short-video/3x8zha3ipq6bg8q"},
		{"https://www.kuaishou.com/short-video/3xwyjn4ipdhss5c?authorId=3xasa", PlatformKuaishou, "3xwyjn4ipdhss5c", "https://www.kuaishou.com/short-video/3xwyjn4ipdhss5c"},
		{"https://www.xiaohongshu.com/discovery/item/674051740000000007027a15?xsec_token=CBge", PlatformXiaohongshu, "674051740000000007027a15", "https://www.xiaohongshu.com/explore/674051740000000007027a15"},
		{"https://www.bilibili.com/video/BV13x41117TL?p=2&spm=1", PlatformBilibili, "BV13x41117TL", "https://www.bilibili.com/video/BV13x41117TL/?p=2"},
		{"https://weibo.com/tv/show/1034:4797699866951785?from=old_pc_videoshow", PlatformWeibo, "1034:4797699866951785", "https://weibo.com/tv/show/1034:4797699866951785"},
		{"https://m.weibo.cn/detail/5120561132606436", PlatformWeibo, "5120561132606436", "https://m.weibo.cn/detail/5120561132606436"},
		{"https://www.ixigua.com/6996881461559165471?logTag=1", PlatformXigua, "6996881461559165471", "https://www.ixigua.com/6996881461559165471"},
		{"https://www.tiktok.com/@user/video/7300000000000000000?lang=en", PlatformTikTok, "7300000000000000000", "https://www.tiktok.com/@user/video/7300000000000000000"},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10", PlatformYouTube, "dQw4w9WgXcQ", "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		{"https://www.instagram.com/p/C1a2B3c4D5e/", PlatformInstagram, "C1a2B3c4D5e", "https://www.instagram.com/reel/C1a2B3c4D5e/"},
		{"https://twitter.com/user/status/1790000000000000000/video/1", PlatformX, "1790000000000000000", "https://x.com/i/status/1790000000000000000"},
	}
	for _, tc := range cases {
		u, err := parseShareURL(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		adapter, ok := registry.Detect(u)
		if !ok || adapter.Info().Key != tc.platform {
			t.Fatalf("%s detected as %v", tc.raw, adapter)
		}
		id, canonical := adapter.Normalize(u)
		if id != tc.id || canonical != tc.canonical {
			t.Fatalf("%s => id=%q canonical=%q", tc.raw, id, canonical)
		}
	}
}
