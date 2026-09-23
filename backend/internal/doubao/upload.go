package doubao

// 图生视频参考图上传：豆包网页端「上传图片」端点 /samantha/pages/upload_image。
// 实测要点（对照 doubao2api 参考实现修正）：
//   - multipart 文件字段名是 "data"（不是 file），另带 "file_type" 字段（扩展名，无点）；
//   - 响应 data.uri 是短 uri，需再调 /alice/message/get_file_url 换完整 tos uri；
//   - 返回的完整 uri 作为 content_type=2020 消息 attachments 的 key：
//     "attachments": [{"type": "image", "key": "<uri>"}]

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

const (
	uploadImagePath  = "/samantha/pages/upload_image"
	uploadMaxRetries = 2
)

// UploadedImage 上传结果：URI 是 2020 payload 引用键，CDNURL 仅调试用。
type UploadedImage struct {
	URI    string `json:"uri"`
	CDNURL string `json:"cdnUrl"`
}

// UploadImageOnce 用固定 Cookie 上传一张参考图，返回 TOS uri。
// 上传与 get_file_url 解析各带一次瞬时重试（params invalid 之外的网络抖动）。
func UploadImageOnce(ctx context.Context, cookieHeader, filename string, data []byte) (*UploadedImage, error) {
	if strings.TrimSpace(cookieHeader) == "" {
		return nil, errors.New("缺少 Cookie，请重新登录账号")
	}
	if len(data) == 0 {
		return nil, errors.New("参考图内容为空")
	}
	if strings.TrimSpace(filename) == "" {
		filename = "reference.jpg"
	}
	ext := filename
	if idx := strings.LastIndex(filename, "."); idx >= 0 && idx+1 < len(filename) {
		ext = filename[idx+1:]
	} else {
		ext = "jpg"
	}
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))

	var uploaded *UploadedImage
	var lastErr error
	for attempt := 0; attempt < uploadMaxRetries; attempt++ {
		uploaded, lastErr = uploadImageAttempt(ctx, cookieHeader, filename, ext, data)
		if lastErr == nil {
			return uploaded, nil
		}
		// 风控/额度类错误换号才有意义，不重试。
		var ce *ClassifyError
		if errors.As(lastErr, &ce) {
			return nil, lastErr
		}
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func uploadImageAttempt(ctx context.Context, cookieHeader, filename, ext string, data []byte) (*UploadedImage, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("data", filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	// 网页端表单字段：file_type=扩展名（无点）。缺失会报 671010000 params invalid。
	_ = writer.WriteField("file_type", ext)
	if err := writer.Close(); err != nil {
		return nil, err
	}

	tabID := randomUUID()
	reqURL := originFromCtx(ctx) + uploadImagePath + "?" + buildQuery(cookieHeader, tabID).Encode()
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, reqURL, body)
	if err != nil {
		return nil, err
	}
	req.Header = buildBrowserHeaders(ctx, cookieHeader)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json, text/plain, */*")
	res, err := ctxHTTPClient(req.Context()).Do(req)
	if err != nil {
		return nil, err
	}
	text, readErr := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	_ = res.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	raw := string(text)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if block := detectBlockRaw(raw); block != nil {
			return nil, block
		}
		return nil, fmt.Errorf("豆包参考图上传失败 HTTP %d: %.300s", res.StatusCode, raw)
	}

	uri, cdn, perr := parseUploadResponse(raw)
	if perr != nil {
		return nil, perr
	}
	// 短 uri 再换完整 tos uri（网页端两步流程）；失败时退回短 uri（可能仍可用）。
	full, fullCDN, ferr := resolveFileURL(ctx, cookieHeader, uri, ext)
	if ferr == nil && full != "" {
		return &UploadedImage{URI: full, CDNURL: fullCDN}, nil
	}
	return &UploadedImage{URI: uri, CDNURL: cdn}, nil
}

// parseUploadResponse 解析 upload_image 响应：{"code":0,"data":{"uri":"...","main_url":"..."}}。
func parseUploadResponse(raw string) (uri, cdn string, err error) {
	var root struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			URI     string   `json:"uri"`
			MainURL string   `json:"main_url"`
			URLList []string `json:"url_list"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(raw), &root) != nil {
		return "", "", fmt.Errorf("豆包参考图上传响应不可解析：%.300s", raw)
	}
	if root.Code != 0 {
		if block := detectBlockRaw(raw); block != nil {
			return "", "", block
		}
		return "", "", fmt.Errorf("豆包参考图上传被拒绝（code=%d）：%.200s", root.Code, root.Msg)
	}
	if strings.TrimSpace(root.Data.URI) == "" {
		return "", "", fmt.Errorf("豆包参考图上传未返回 uri：%.300s", raw)
	}
	cdn = root.Data.MainURL
	if cdn == "" && len(root.Data.URLList) > 0 {
		cdn = root.Data.URLList[0]
	}
	return root.Data.URI, cdn, nil
}

// resolveFileURL 调 /alice/message/get_file_url 把短 uri 换成完整 tos uri 与 CDN 直链。
func resolveFileURL(ctx context.Context, cookieHeader, uri, ext string) (string, string, error) {
	payload := map[string]any{
		"uris":          []string{uri},
		"type":          "image",
		"format":        ext,
		"expire_second": 3600,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	tabID := randomUUID()
	reqURL := originFromCtx(ctx) + getFileURLPath + "?" + buildQuery(cookieHeader, tabID).Encode()
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, reqURL, bytes.NewReader(buf))
	if err != nil {
		return "", "", err
	}
	req.Header = buildBrowserHeaders(ctx, cookieHeader)
	res, err := ctxHTTPClient(req.Context()).Do(req)
	if err != nil {
		return "", "", err
	}
	text, readErr := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	_ = res.Body.Close()
	if readErr != nil {
		return "", "", readErr
	}
	var root struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			FileURLs []struct {
				URI     string `json:"uri"`
				MainURL string `json:"main_url"`
			} `json:"file_urls"`
		} `json:"data"`
	}
	if json.Unmarshal(text, &root) != nil || root.Code != 0 || len(root.Data.FileURLs) == 0 {
		return "", "", fmt.Errorf("get_file_url 未返回结果：%.200s", string(text))
	}
	info := root.Data.FileURLs[0]
	return strings.TrimSpace(info.URI), strings.TrimSpace(info.MainURL), nil
}
