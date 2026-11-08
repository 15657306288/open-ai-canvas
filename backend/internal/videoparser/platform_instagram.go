package videoparser

import (
	"net/url"
	"regexp"
)

// Instagram：由自部署的 yt-dlp 解析公开 Reels / 帖子视频。需要海外代理，且平台经常要求登录才返回媒体；
// 本次环境不可达，未完成真实测试。不会使用任何账号 Cookie。

var instagramIDPattern = regexp.MustCompile(`/(?:p|reel|reels|tv)/([0-9A-Za-z_-]{5,})`)

type instagramAdapter struct{ basePlatform }

func newInstagramAdapter() PlatformAdapter {
	return &instagramAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformInstagram, Label: "Instagram", Region: RegionOverseas,
			Domains: []string{"instagram.com", "instagr.am"}},
		caps: Capabilities{Support: SupportPartial, Video: true, Metadata: true, NeedsProxy: true, NeedsLogin: true,
			Notes: "需要海外代理；平台常对访客要求登录，公开 Reels 也可能失败；当前服务器网络无法访问，真实解析未验证"},
		providers: []string{"yt-dlp"},
		idPattern: instagramIDPattern,
		canonical: func(id string, _ *url.URL) string { return "https://www.instagram.com/reel/" + id + "/" },
	}}
}
