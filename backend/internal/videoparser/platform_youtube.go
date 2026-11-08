package videoparser

import (
	"net/url"
	"regexp"
)

// YouTube：由自部署的 yt-dlp 解析公开视频。需要海外代理；新版 yt-dlp 获取完整格式还需要 JS 运行时（Deno），
// 机房 IP 可能被要求登录验证。本次环境不可达，未完成真实测试。

var youtubeIDPattern = regexp.MustCompile(`(?:[?&]v=|youtu\.be/|/shorts/|/embed/|/live/)([0-9A-Za-z_-]{11})`)

type youtubeAdapter struct{ basePlatform }

func newYouTubeAdapter() PlatformAdapter {
	return &youtubeAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformYouTube, Label: "YouTube", Region: RegionOverseas,
			Domains: []string{"youtube.com", "youtu.be", "youtube-nocookie.com"}},
		caps: Capabilities{Support: SupportPartial, Video: true, Metadata: true, SeparateAV: true, NeedsProxy: true,
			Notes: "需要海外代理；音视频分离流由 FFmpeg 合并；机房 IP 可能被要求登录验证；当前服务器网络无法访问，真实解析未验证"},
		providers: []string{"yt-dlp"},
		idPattern: youtubeIDPattern,
		canonical: func(id string, _ *url.URL) string { return "https://www.youtube.com/watch?v=" + id },
	}}
}
