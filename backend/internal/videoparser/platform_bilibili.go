package videoparser

import (
	"net/url"
	"regexp"
	"strconv"
)

// 哔哩哔哩：由自部署的 yt-dlp 解析（含 WBI 签名），访客可取得公开视频的 DASH 分离音视频流，由 FFmpeg 合并为 MP4。
// 付费、大会员或仅登录可见的清晰度不会返回，也不会尝试绕过。

var bilibiliIDPattern = regexp.MustCompile(`/video/(BV[0-9A-Za-z]{10}|av\d+)`)

type bilibiliAdapter struct{ basePlatform }

func newBilibiliAdapter() PlatformAdapter {
	return &bilibiliAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformBilibili, Label: "哔哩哔哩", Region: RegionDomestic,
			Domains: []string{"bilibili.com", "b23.tv", "bili2233.cn"}, ShortDomains: []string{"b23.tv", "bili2233.cn"}},
		caps: Capabilities{Support: SupportSupported, Video: true, Metadata: true, SeparateAV: true, VerifiedOn: "2026-11-05",
			Notes: "公开视频无需登录；音视频分离流由 FFmpeg 合并；付费、大会员专享内容不支持；多 P 视频取链接指定的分 P"},
		providers: []string{"yt-dlp"},
		idPattern: bilibiliIDPattern,
		canonical: func(id string, u *url.URL) string {
			canonical := "https://www.bilibili.com/video/" + id + "/"
			if page, err := strconv.Atoi(u.Query().Get("p")); err == nil && page > 1 {
				canonical += "?p=" + strconv.Itoa(page)
			}
			return canonical
		},
	}}
}
