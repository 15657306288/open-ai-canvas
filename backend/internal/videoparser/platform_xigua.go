package videoparser

import (
	"net/url"
	"regexp"
)

// 西瓜视频：2026-11-05 实测，网页返回字节跳动 acrawler 脚本挑战页（__ac_nonce），yt-dlp 提示需要 Cookie，
// 无头浏览器访客也没有取得视频地址，因此暂不可用。保留 yt-dlp 作为供应商，平台恢复访客访问后改能力标记即可。

var xiguaIDPattern = regexp.MustCompile(`ixigua\.com/(?:video/)?(\d{8,})`)

type xiguaAdapter struct{ basePlatform }

func newXiguaAdapter() PlatformAdapter {
	return &xiguaAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformXigua, Label: "西瓜视频", Region: RegionDomestic,
			Domains: []string{"ixigua.com"}, ShortDomains: []string{"v.ixigua.com"}},
		caps: Capabilities{Support: SupportUnavailable, Video: true, Metadata: true, NeedsLogin: true, VerifiedOn: "2026-11-05",
			Notes: "网页对访客返回反爬脚本挑战，yt-dlp 需要 Cookie，没有不依赖账号的可用实现"},
		providers: []string{"yt-dlp"},
		idPattern: xiguaIDPattern,
		canonical: func(id string, _ *url.URL) string { return "https://www.ixigua.com/" + id },
	}}
}
