package videoparser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxPreferredHeight = 1080

// pickMedia 选出要下载的媒体：同等清晰度下优先合流文件；分离流时选 ≤1080p 的视频流 + 最佳音频流。
// 编码优先 H.264（浏览器播放与后续 FFmpeg 处理兼容性最好），其次 H.265 / VP9 / AV1。
func pickMedia(media []MediaAsset) (muxed, video, audio *MediaAsset) {
	return pickMediaWithin(media, 0, 0)
}

// estimatedBytes 估算媒体体积：优先平台给出的文件大小，否则按码率 × 时长估算；未知返回 0。
func estimatedBytes(m *MediaAsset, durationMs int64) int64 {
	if m == nil {
		return 0
	}
	if m.SizeBytes > 0 {
		return m.SizeBytes
	}
	if m.BitrateK > 0 && durationMs > 0 {
		return int64(m.BitrateK * float64(durationMs) / 8)
	}
	return 0
}

// pickMediaWithin 在 pickMedia 的偏好下，跳过估算体积超过 budget 的清晰度（例如长视频的 1080p），退到能下载的最高档。
// 有无水印的流可选时，忽略平台标记为带水印的流。
func pickMediaWithin(media []MediaAsset, budget, durationMs int64) (muxed, video, audio *MediaAsset) {
	clean := make([]MediaAsset, 0, len(media))
	for _, item := range media {
		if !item.Watermarked && item.Kind != MediaAudio {
			clean = append(clean, item)
		}
	}
	if len(clean) > 0 && len(clean) < countVisual(media) {
		filtered := clean
		for _, item := range media {
			if item.Kind == MediaAudio {
				filtered = append(filtered, item)
			}
		}
		media = filtered
	}
	var bestAudio *MediaAsset
	for i := range media {
		if item := &media[i]; item.Kind == MediaAudio && (bestAudio == nil || betterAudio(item, bestAudio)) {
			bestAudio = item
		}
	}
	fits := func(item *MediaAsset, extra int64) bool {
		size := estimatedBytes(item, durationMs)
		return budget <= 0 || size == 0 || size+extra <= budget
	}
	best := func(kind MediaKind, extra int64) *MediaAsset {
		var chosen, fallback *MediaAsset
		for i := range media {
			item := &media[i]
			if item.Kind != kind {
				continue
			}
			if fallback == nil || estimatedBytes(item, durationMs) < estimatedBytes(fallback, durationMs) {
				fallback = item
			}
			if fits(item, extra) && (chosen == nil || betterVideo(item, chosen)) {
				chosen = item
			}
		}
		if chosen == nil {
			return fallback
		}
		return chosen
	}
	bestMuxed := best(MediaMuxed, 0)
	bestVideo := best(MediaVideo, estimatedBytes(bestAudio, durationMs))
	// 分离流只有在清晰度档位更高时才用（需要额外合并）；同档位直接用合流文件。
	if bestMuxed != nil && (bestVideo == nil || bestAudio == nil || heightScore(shortSide(bestVideo)) <= heightScore(shortSide(bestMuxed))) {
		return bestMuxed, bestMuxed, nil
	}
	if bestVideo != nil {
		return nil, bestVideo, bestAudio
	}
	return nil, nil, nil
}

func codecRank(codec string) int {
	codec = strings.ToLower(codec)
	switch {
	case strings.HasPrefix(codec, "avc"), strings.HasPrefix(codec, "h264"):
		return 4
	case codec == "":
		return 3
	case strings.HasPrefix(codec, "hev"), strings.HasPrefix(codec, "hvc"), strings.HasPrefix(codec, "h265"):
		return 2
	case strings.HasPrefix(codec, "vp9"), strings.HasPrefix(codec, "vp09"):
		return 1
	}
	return 0
}

func countVisual(media []MediaAsset) int {
	count := 0
	for _, item := range media {
		if item.Kind != MediaAudio {
			count++
		}
	}
	return count
}

// effectiveHeight 是用于比较清晰度的档位：非 H.264 编码降一档（360），让同档或高一档的 H.264 优先，
// 因为后续 FFmpeg 处理与视频模型对 H.264 的兼容性最好；只有 H.264 明显更低时才用 H.265 / VP9 / AV1。
func effectiveHeight(m *MediaAsset) int {
	height := shortSide(m)
	if height <= 0 || height > maxPreferredHeight {
		return height
	}
	if rank := codecRank(m.VideoCodec); rank < 3 {
		height -= 360
		if height <= 0 {
			height = 1
		}
	}
	return height
}

// shortSide 按短边计算清晰度档位：竖屏 720x1280 属于 720p，横屏 1920x1080 属于 1080p。
func shortSide(m *MediaAsset) int {
	if m.Width > 0 && m.Height > 0 {
		return min(m.Width, m.Height)
	}
	return m.Height
}

// heightScore：不超过 1080p 时越高越好；超过 1080p 的排在所有 ≤1080p 之后（体积与时长限制更容易超）。
func heightScore(height int) int {
	if height <= 0 {
		return 1
	}
	if height <= maxPreferredHeight {
		return 10000 + height
	}
	return 10000 - height
}

func betterVideo(a, b *MediaAsset) bool {
	if ha, hb := heightScore(effectiveHeight(a)), heightScore(effectiveHeight(b)); ha != hb {
		return ha > hb
	}
	if ra, rb := codecRank(a.VideoCodec), codecRank(b.VideoCodec); ra != rb {
		return ra > rb
	}
	if ea, eb := a.Ext == "mp4", b.Ext == "mp4"; ea != eb {
		return ea
	}
	return a.BitrateK > b.BitrateK
}

func betterAudio(a, b *MediaAsset) bool {
	aa, ab := strings.HasPrefix(a.AudioCodec, "mp4a") || a.Ext == "m4a", strings.HasPrefix(b.AudioCodec, "mp4a") || b.Ext == "m4a"
	if aa != ab {
		return aa
	}
	return a.BitrateK > b.BitrateK
}

// DownloadedFile 是下载并整理好的本地 MP4。
type DownloadedFile struct {
	Path    string
	Size    int64
	Merged  bool
	Remuxed bool
}

// Download 把解析结果里的媒体下载到 dir：大小上限、超时、每跳地址校验、内容类型校验；
// 分离的音视频流用 FFmpeg 合并，非 MP4 容器转封装为 MP4（-c copy，不重新编码）。
func (s *Service) Download(ctx context.Context, result *Result, dir string, onStage func(Stage)) (file *DownloadedFile, err error) {
	if result == nil {
		return nil, newError(ErrInvalidLink, "没有可下载的视频", nil)
	}
	platform, _ := s.registry.Get(result.Meta.Platform)
	emit := func(stage Stage) {
		if onStage != nil {
			stage.Platform, stage.PlatformLabel = result.Meta.Platform, result.Meta.PlatformLabel
			onStage(stage)
		}
	}
	var downloaded int64
	defer func() { s.stats.recordDownload(result.Meta.Platform, downloaded, err == nil) }()
	muxed, video, audio := pickMediaWithin(result.Media, s.opts.MaxBytes, result.Meta.DurationMs)
	if video == nil {
		return nil, newError(ErrNotVideo, "这条作品没有可用的视频画面", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.DownloadTimeout)
	defer cancel()
	budget := s.opts.MaxBytes
	total := video.SizeBytes
	if audio != nil {
		total += audio.SizeBytes
	}
	report := func(base int64) func(done int64) {
		return func(done int64) {
			progress := 0.0
			if total > 0 {
				progress = min(0.99, float64(base+done)/float64(total))
			}
			emit(Stage{Name: "downloading", Progress: progress, Message: fmt.Sprintf("正在下载视频 %.1f MB", float64(base+done)/(1<<20))})
		}
	}
	videoPath := filepath.Join(dir, "source-video")
	size, err := s.downloadOne(ctx, platform, *video, videoPath, budget, report(0))
	downloaded += size
	if err != nil {
		return nil, err
	}
	output := filepath.Join(dir, "reference.mp4")
	if muxed != nil || audio == nil {
		container := sniffContainer(videoPath)
		if container == "" || container == "html" {
			return nil, newError(ErrMedia, "链接返回的不是视频文件，可能需要登录或地址已失效", nil)
		}
		if s.opts.FFmpegPath == "" {
			if container != "mp4" {
				return nil, newError(ErrUnavailable, "服务器缺少 ffmpeg，无法转换视频格式", nil)
			}
			if err := os.Rename(videoPath, output); err != nil {
				return nil, err
			}
			return &DownloadedFile{Path: output, Size: size}, nil
		}
		emit(Stage{Name: "merging", Progress: 1, Message: "正在整理视频格式"})
		if err := s.remux(ctx, videoPath, output); err != nil {
			return nil, err
		}
		info, err := os.Stat(output)
		if err != nil {
			return nil, err
		}
		return &DownloadedFile{Path: output, Size: info.Size(), Remuxed: true}, nil
	}
	audioPath := filepath.Join(dir, "source-audio")
	audioSize, err := s.downloadOne(ctx, platform, *audio, audioPath, budget-size, report(size))
	downloaded += audioSize
	if err != nil {
		return nil, err
	}
	if s.opts.FFmpegPath == "" {
		return nil, newError(ErrUnavailable, "服务器缺少 ffmpeg，无法合并音视频", nil)
	}
	emit(Stage{Name: "merging", Progress: 1, Message: "正在合并音频与视频"})
	if err := s.merge(ctx, videoPath, audioPath, output); err != nil {
		return nil, err
	}
	info, err := os.Stat(output)
	if err != nil {
		return nil, err
	}
	if info.Size() > s.opts.MaxBytes {
		return nil, newError(ErrTooLarge, fmt.Sprintf("视频超过 %d MB，请裁剪后本地上传", s.opts.MaxBytes>>20), nil)
	}
	return &DownloadedFile{Path: output, Size: info.Size(), Merged: true}, nil
}

// downloadOne 依次尝试主地址与备用 CDN 地址；体积超限、地址被拦截这类换地址也不会变的错误直接返回。
func (s *Service) downloadOne(ctx context.Context, platform PlatformAdapter, media MediaAsset, path string, budget int64, progress func(int64)) (int64, error) {
	urls := append([]string{media.URL}, media.Backups...)
	var total int64
	var lastErr error
	for index, raw := range urls {
		if index >= 3 || ctx.Err() != nil {
			break
		}
		attempt := media
		attempt.URL = raw
		written, err := s.downloadFrom(ctx, platform, attempt, path, budget, progress)
		total += written
		if err == nil {
			return written, nil
		}
		lastErr = err
		switch AsError(err).Code {
		case ErrTooLarge, ErrBlocked, ErrTimeout:
			return total, err
		}
	}
	if lastErr == nil {
		lastErr = newError(ErrMedia, "视频地址无效", nil)
	}
	return total, lastErr
}

// downloadFrom 流式写入文件，超过 budget 立即中止；每一跳重定向都检查协议与端口，出站地址由 outbound 校验。
func (s *Service) downloadFrom(ctx context.Context, platform PlatformAdapter, media MediaAsset, path string, budget int64, progress func(int64)) (int64, error) {
	target, err := url.Parse(media.URL)
	if err != nil {
		return 0, newError(ErrMedia, "视频地址无效", err)
	}
	if err := checkMediaShape(target); err != nil {
		return 0, err
	}
	if budget <= 0 {
		return 0, newError(ErrTooLarge, fmt.Sprintf("视频超过 %d MB，请裁剪后本地上传", s.opts.MaxBytes>>20), nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return 0, newError(ErrMedia, "视频地址无效", err)
	}
	req.Header.Set("User-Agent", desktopUserAgent)
	req.Header.Set("Accept", "*/*")
	for name, value := range safeHeaders(media.Headers) {
		req.Header.Set(name, value)
	}
	base := s.httpClient(s.opts.DownloadTimeout, s.proxyFor(platform))
	client := *base
	inner := base.CheckRedirect
	client.Jar = nil
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirectHops {
			return newError(ErrBlocked, "视频地址跳转次数过多", nil)
		}
		if err := checkMediaShape(next.URL); err != nil {
			return err
		}
		next.Header.Del("Cookie")
		if inner != nil {
			return inner(next, via)
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, classifyTransportError(err, "下载视频失败")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return 0, newError(ErrNotFound, "视频文件不存在或已下架", nil)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return 0, newError(ErrUpstream, "视频地址已失效或拒绝访问，请重新解析", nil)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return 0, newError(ErrUpstream, fmt.Sprintf("视频地址返回 %d，请稍后重试", resp.StatusCode), nil)
	}
	if resp.ContentLength > budget {
		return 0, newError(ErrTooLarge, fmt.Sprintf("视频超过 %d MB，请裁剪后本地上传", s.opts.MaxBytes>>20), nil)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "json") || strings.Contains(contentType, "xml") {
		return 0, newError(ErrMedia, "链接返回的不是视频文件，可能需要登录或地址已失效", nil)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	reader := &progressReader{reader: io.LimitReader(resp.Body, budget+1), report: progress}
	written, err := io.Copy(file, reader)
	if err != nil {
		return written, classifyTransportError(err, "下载视频中断")
	}
	if written > budget {
		return written, newError(ErrTooLarge, fmt.Sprintf("视频超过 %d MB，请裁剪后本地上传", s.opts.MaxBytes>>20), nil)
	}
	if written == 0 {
		return 0, newError(ErrMedia, "视频文件为空", nil)
	}
	if container := sniffContainer(path); container == "" || container == "html" {
		return written, newError(ErrMedia, "链接返回的不是视频文件，可能需要登录或地址已失效", nil)
	}
	return written, nil
}

// checkMediaShape 校验媒体直链：只允许 http/https、无账号信息。平台 CDN 会用非标准端口（如 B 站 PCDN 的 4483、8082），
// 因此放开 1024 以上端口，但拒绝 1024 以下除 80/443 外的系统端口；目标地址仍由 outbound 拨号器限制为公网 IP。
func checkMediaShape(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return newError(ErrBlocked, "视频地址只支持 http / https", nil)
	}
	if u.User != nil {
		return newError(ErrBlocked, "视频地址不能包含账号信息", nil)
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1024 || value > 65535 {
			return newError(ErrBlocked, "视频地址端口不受支持", nil)
		}
	}
	return nil
}

type progressReader struct {
	reader io.Reader
	report func(int64)
	done   int64
	last   time.Time
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.done += int64(n)
	if r.report != nil && (time.Since(r.last) > 400*time.Millisecond || err == io.EOF) {
		r.last = time.Now()
		r.report(r.done)
	}
	return n, err
}

// sniffContainer 通过文件头识别容器；返回 html 表示拿到的是网页（地址失效或要求登录）。
func sniffContainer(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	head = head[:n]
	if n < 8 {
		return ""
	}
	box := string(head[4:8])
	switch {
	case box == "ftyp" || box == "styp" || box == "moov" || box == "mdat" || box == "free" || box == "sidx" || box == "wide" || box == "skip":
		return "mp4"
	case bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return "webm"
	case bytes.HasPrefix(head, []byte("FLV")):
		return "flv"
	case head[0] == 0x47 && (n < 189 || head[188] == 0x47):
		return "ts"
	case bytes.HasPrefix(head, []byte("OggS")):
		return "ogg"
	case bytes.HasPrefix(head, []byte("ID3")) || (head[0] == 0xFF && head[1]&0xE0 == 0xE0):
		return "audio"
	}
	lower := bytes.ToLower(bytes.TrimSpace(head))
	if bytes.HasPrefix(lower, []byte("<!doctype")) || bytes.HasPrefix(lower, []byte("<html")) || bytes.HasPrefix(lower, []byte("{")) {
		return "html"
	}
	return ""
}

// merge 把视频流与音频流合并为 MP4：先直接拷贝码流，音频编码不适合 MP4 时改为 AAC。
func (s *Service) merge(ctx context.Context, videoPath, audioPath, output string) error {
	copyArgs := []string{"-protocol_whitelist", "file", "-i", videoPath, "-protocol_whitelist", "file", "-i", audioPath, "-map", "0:v:0", "-map", "1:a:0", "-c", "copy", "-movflags", "+faststart", "-f", "mp4", output}
	if err := s.ffmpeg(ctx, copyArgs); err == nil {
		return nil
	}
	aacArgs := []string{"-protocol_whitelist", "file", "-i", videoPath, "-protocol_whitelist", "file", "-i", audioPath, "-map", "0:v:0", "-map", "1:a:0", "-c:v", "copy", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart", "-f", "mp4", output}
	if err := s.ffmpeg(ctx, aacArgs); err != nil {
		return newError(ErrMedia, "音视频合并失败，视频编码可能不受支持", err)
	}
	return nil
}

// remux 把合流文件整理为标准 MP4（moov 前置，便于浏览器边下边播），保留第一路视频与第一路音频。
func (s *Service) remux(ctx context.Context, input, output string) error {
	copyArgs := []string{"-protocol_whitelist", "file", "-i", input, "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-movflags", "+faststart", "-f", "mp4", output}
	if err := s.ffmpeg(ctx, copyArgs); err == nil {
		return nil
	}
	aacArgs := []string{"-protocol_whitelist", "file", "-i", input, "-map", "0:v:0", "-map", "0:a:0?", "-c:v", "copy", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart", "-f", "mp4", output}
	if err := s.ffmpeg(ctx, aacArgs); err != nil {
		return newError(ErrMedia, "视频格式转换失败，文件可能已损坏", err)
	}
	return nil
}

func (s *Service) ffmpeg(ctx context.Context, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	full := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)
	cmd := exec.CommandContext(ctx, s.opts.FFmpegPath, full...)
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{limit: 16 << 10, buf: &stderr}
	if err := cmd.Run(); err != nil {
		return errors.Join(err, errors.New(clip(stderr.String(), 300)))
	}
	return nil
}
