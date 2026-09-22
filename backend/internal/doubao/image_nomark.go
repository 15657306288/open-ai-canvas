package doubao

// 图片无水印升级通道。
//
// 实测（2026-09-18，真机字节级验证，见 media_nomark_test.go / fallback_e2e_cdn_test.go 同日记录）：
//   - 豆包文生图 SSE（content_type 2010）下发的全部图片变体都带水印模板：
//     image_thumb → ~tplv-*-cthumb_wm1（缩略图）、image_raw/preview_img → cpreview_wm1（预览，左上角「AI 生成」标）、
//     image_ori → cdld_wm3（下载版，右下角「豆包AI生成」水印）——水印烧录在图片像素里，改 URL 参数无效；
//   - 真无水印原片凭裸 key（如 tos-cn-i-xxx/rc_gen_image/<hash>.jpeg，即 image_thumb_ori 指向的 key）
//     调 POST /alice/message/get_file_url {uris, type:"image"} 换取签名直链，
//     返回 ~tplv-*-image-qvalue 模板（无水印），与豆包前端「打开/下载图片」走的是同一接口
//     （前端代码 g.SI.GetFileURL → /alice/message/get_file_url，响应 data.file_urls[0].main_url）。

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"strings"
)

// imageKeyPattern 从带模板的图片 URL 中提取裸 key（模板前的原始 tos 路径）。
var imageKeyPattern = regexp.MustCompile(`(tos-cn-i-[a-z0-9]+/[A-Za-z0-9_/-]+\.(?:jpeg|jpg|png|webp))`)

// getFileURLPath 豆包前端签名直链接口（前端服务名 GetFileURL）。
const getFileURLPath = "/alice/message/get_file_url"

type getFileURLResponse struct {
	Code int64 `json:"code"`
	Data struct {
		FileURLs []struct {
			URI     string `json:"uri"`
			MainURL string `json:"main_url"`
			BackURL string `json:"back_url"`
		} `json:"file_urls"`
	} `json:"data"`
}

// fetchSignedImageURL 凭裸 key 换取无水印签名直链；失败返回空串。
func fetchSignedImageURL(ctx context.Context, cookieHeader, key string) string {
	if key == "" {
		return ""
	}
	raw, err := postMediaInfo(ctx, cookieHeader, getFileURLPath, map[string]any{
		"uris":          []string{key},
		"type":          "image",
		"expire_second": 3600,
	})
	if err != nil || len(raw) == 0 {
		return ""
	}
	var parsed getFileURLResponse
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Code != 0 || len(parsed.Data.FileURLs) == 0 {
		return ""
	}
	fu := parsed.Data.FileURLs[0]
	if strings.HasPrefix(fu.MainURL, "http") {
		return fu.MainURL
	}
	if strings.HasPrefix(fu.BackURL, "http") {
		return fu.BackURL
	}
	return ""
}

// UpgradeImagesNoWatermark 把一组图片 URL 升级为无水印签名直链。
// 逐个 URL 提取裸 key → get_file_url 换链；换不到的保留原 URL。
// 返回值约定：只要有任何一个升级成功就返回升级后的列表，全部失败时原样返回。
func UpgradeImagesNoWatermark(ctx context.Context, cookieHeader string, urls []string) []string {
	if len(urls) == 0 || strings.TrimSpace(cookieHeader) == "" {
		return urls
	}
	out := make([]string, 0, len(urls))
	upgraded := 0
	cache := make(map[string]string)
	for _, u := range urls {
		best := ""
		if m := imageKeyPattern.FindString(u); m != "" {
			if c, ok := cache[m]; ok {
				best = c
			} else {
				best = fetchSignedImageURL(ctx, cookieHeader, m)
				cache[m] = best
			}
		}
		if strings.HasPrefix(best, "http") {
			out = append(out, best)
			upgraded++
		} else {
			out = append(out, u)
		}
	}
	if upgraded > 0 {
		log.Printf("[doubao] 图片无水印升级成功 %d/%d（get_file_url → image-qvalue 原片）", upgraded, len(urls))
		return out
	}
	return urls
}
