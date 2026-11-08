package videoparser

import (
	"net/url"
	"regexp"
)

// TikTok：由自部署的 yt-dlp 解析公开视频。服务器所在网络无法直连，需要部署者配置海外代理；
// 本次环境不可达，未完成真实测试。

var tiktokIDPattern = regexp.MustCompile(`/video/(\d{8,})`)

type tiktokAdapter struct{ basePlatform }

func newTikTokAdapter() PlatformAdapter {
	return &tiktokAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformTikTok, Label: "TikTok", Region: RegionOverseas,
			Domains: []string{"tiktok.com"}, ShortDomains: []string{"vm.tiktok.com", "vt.tiktok.com"}},
		caps: Capabilities{Support: SupportPartial, Video: true, Metadata: true, NeedsProxy: true,
			Notes: "需要海外代理；当前服务器网络无法访问，真实解析未验证"},
		providers: []string{"yt-dlp"},
		idPattern: tiktokIDPattern,
		canonical: func(id string, u *url.URL) string {
			stripped := *u
			stripped.RawQuery, stripped.Fragment = "", ""
			return stripped.String()
		},
	}}
}
