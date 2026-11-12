package videoparser

import (
	"net/url"
	"strings"
)

// 微博：由自部署的 yt-dlp 解析（自行申请访客身份，不需要账号），返回合流 MP4。
// t.cn 是微博短链，可能指向任意网站；展开时每一跳都必须落在已支持平台的域名内。

type weiboAdapter struct{ basePlatform }

func newWeiboAdapter() PlatformAdapter {
	return &weiboAdapter{basePlatform{
		info: PlatformInfo{Key: PlatformWeibo, Label: "微博", Region: RegionDomestic,
			Domains: []string{"weibo.com", "weibo.cn", "t.cn"}, ShortDomains: []string{"t.cn"}},
		caps: Capabilities{Support: SupportSupported, Video: true, Metadata: true, VerifiedOn: "2026-11-05",
			Notes: "公开视频微博无需登录；纯文字或图片微博会提示没有视频"},
		providers: []string{"yt-dlp"},
	}}
}

// Normalize：微博地址形如 weibo.com/<uid>/<mid>、m.weibo.cn/detail/<id>、weibo.com/tv/show/1034:<id>，取最后一段作为作品 ID。
func (a *weiboAdapter) Normalize(u *url.URL) (string, string) {
	if u == nil {
		return "", ""
	}
	segments := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	id := ""
	if len(segments) >= 2 {
		id = segments[len(segments)-1]
	}
	stripped := *u
	stripped.RawQuery, stripped.Fragment, stripped.User = "", "", nil
	return id, stripped.String()
}
