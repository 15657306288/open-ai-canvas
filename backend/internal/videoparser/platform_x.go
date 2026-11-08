package videoparser

import (
	"net/url"
	"regexp"
)

// X / Twitter：由自部署的 yt-dlp 解析公开推文视频（访客令牌）。需要海外代理；t.co 短链展开时每一跳都校验域名。
// 本次环境不可达，未完成真实测试。

var xIDPattern = regexp.MustCompile(`/status(?:es)?/(\d{5,})`)

type xAdapter struct{ basePlatform }

func newXAdapter() PlatformAdapter {
	return &xAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformX, Label: "X / Twitter", Region: RegionOverseas,
			Domains: []string{"x.com", "twitter.com", "t.co"}, ShortDomains: []string{"t.co"}},
		caps: Capabilities{Support: SupportPartial, Video: true, Metadata: true, NeedsProxy: true,
			Notes: "需要海外代理；敏感内容或受保护账号需要登录，不支持；当前服务器网络无法访问，真实解析未验证"},
		providers: []string{"yt-dlp"},
		idPattern: xIDPattern,
		canonical: func(id string, _ *url.URL) string { return "https://x.com/i/status/" + id },
	}}
}
