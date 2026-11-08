package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"yingce/backend/internal/model"
)

// 商品视频复刻的本地媒体处理：ffprobe 真实探测、ffmpeg 场景切换检测、关键帧与音轨抽取。
// 镜头检测使用 ffmpeg 的 scene 评分（与 PySceneDetect ContentDetector 同类的帧间内容差异），
// 后端镜像只装了 ffmpeg，不额外引入 Python/OpenCV 运行时。

const (
	videoReplicationMinDurationMs   int64 = 4000
	videoReplicationMaxDurationMs   int64 = 120000
	videoReplicationDurationSlackMs int64 = 60
	videoReplicationMaxVideoBytes   int64 = 100 << 20
	videoReplicationMaxImageBytes   int64 = 30 << 20
	videoReplicationDownloadTimeout       = 3 * time.Minute
	// videoReplicationSceneThreshold 是 ffmpeg scene 分数阈值；广告片硬切通常 > 0.4，0.32 能捕获多数硬切又少误报。
	videoReplicationSceneThreshold = 0.32
	videoReplicationMinShotMs      = 700
	videoReplicationMaxShots       = 24
)

var videoReplicationVideoMimes = map[string]bool{"video/mp4": true, "video/quicktime": true}

const videoReplicationBrokenVideoMessage = "参考视频文件已损坏或不完整，无法读取；请重新导出为 MP4 / MOV 后上传"

// videoReplicationVideoFileName 判断文件名是否是 MP4 / MOV，用来区分「格式不支持」和「文件损坏」。
func videoReplicationVideoFileName(name string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	return ext == ".mp4" || ext == ".mov" || ext == ".m4v"
}

type videoReplicationProbe struct {
	Width      int
	Height     int
	DurationMs int64
	FPS        float64
	HasAudio   bool
	VideoCodec string
}

// probeVideoReplicationFile 读取完整文件的视频流、音频流与容器时长；不信任前端上报的任何媒体参数。
func probeVideoReplicationFile(ctx context.Context, path string) (videoReplicationProbe, error) {
	bin, err := exec.LookPath("ffprobe")
	if err != nil {
		return videoReplicationProbe{}, errMediaProbeUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, mediaProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, bin, "-v", "error",
		"-show_entries", "stream=codec_type,codec_name,width,height,avg_frame_rate,r_frame_rate:stream_side_data=rotation:format=duration",
		"-of", "json", path).Output()
	if err != nil {
		return videoReplicationProbe{}, fmt.Errorf("视频探测失败：%w", err)
	}
	return parseVideoReplicationProbe(output)
}

func parseVideoReplicationProbe(raw []byte) (videoReplicationProbe, error) {
	var document struct {
		Streams []struct {
			CodecType    string `json:"codec_type"`
			CodecName    string `json:"codec_name"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			AvgFrameRate string `json:"avg_frame_rate"`
			RFrameRate   string `json:"r_frame_rate"`
			SideDataList []struct {
				Rotation *float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return videoReplicationProbe{}, fmt.Errorf("视频探测结果无效：%w", err)
	}
	var probe videoReplicationProbe
	videoFound := false
	for _, stream := range document.Streams {
		switch stream.CodecType {
		case "audio":
			probe.HasAudio = true
		case "video":
			if videoFound {
				continue
			}
			videoFound = true
			probe.VideoCodec = stream.CodecName
			if stream.Width > 0 && stream.Height > 0 && stream.Width <= mediaProbeMaxDimension && stream.Height <= mediaProbeMaxDimension {
				probe.Width, probe.Height = stream.Width, stream.Height
				for _, side := range stream.SideDataList {
					if side.Rotation != nil && int(math.Abs(math.Round(*side.Rotation)))%180 == 90 {
						probe.Width, probe.Height = probe.Height, probe.Width
						break
					}
				}
			}
			probe.FPS = parseFrameRate(stream.AvgFrameRate)
			if probe.FPS <= 0 {
				probe.FPS = parseFrameRate(stream.RFrameRate)
			}
		}
	}
	if !videoFound || probe.Width == 0 || probe.Height == 0 {
		return probe, errors.New("文件不包含可解码的视频画面")
	}
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(document.Format.Duration), 64); err == nil && seconds > 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
		probe.DurationMs = int64(math.Round(seconds * 1000))
	}
	if probe.DurationMs <= 0 {
		return probe, errors.New("无法读取视频时长")
	}
	return probe, nil
}

func parseFrameRate(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" || value == "0/0" {
		return 0
	}
	if numerator, denominator, ok := strings.Cut(value, "/"); ok {
		n, errN := strconv.ParseFloat(numerator, 64)
		d, errD := strconv.ParseFloat(denominator, 64)
		if errN != nil || errD != nil || d == 0 {
			return 0
		}
		return math.Round(n/d*100) / 100
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return n
}

// validateVideoReplicationDuration 按 4–15 秒校验真实时长；容器时长会有几十毫秒的取整误差。
func validateVideoReplicationDuration(durationMs int64) error {
	return validateVideoReplicationDurationLimit(durationMs, 120)
}

func validateVideoReplicationDurationLimit(durationMs int64, maxSeconds int) error {
	if durationMs < videoReplicationMinDurationMs-videoReplicationDurationSlackMs {
		return BadAuthRequest(fmt.Sprintf("参考视频时长 %.1f 秒，短于 4 秒", float64(durationMs)/1000))
	}
	if durationMs > int64(maxSeconds)*1000+videoReplicationDurationSlackMs {
		return BadAuthRequest(fmt.Sprintf("参考视频时长 %.1f 秒，超过 %d 秒", float64(durationMs)/1000, maxSeconds))
	}
	return nil
}

// withVideoReplicationResourceFile 把用户资源完整下载到临时文件后交给 fn；超过 maxBytes 或读取卡住时失败。
func (s *Service) withVideoReplicationResourceFile(userID, resourceID string, maxBytes int64, fn func(resource *model.Resource, path string) error) error {
	resource, body, err := s.OpenResource(userID, resourceID)
	if err != nil {
		return err
	}
	defer body.Close()
	var stalled atomic.Bool
	watchdog := time.AfterFunc(videoReplicationDownloadTimeout, func() {
		stalled.Store(true)
		_ = body.Close()
	})
	defer watchdog.Stop()
	dir, err := os.MkdirTemp("", "yingce-replication-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "input"+videoReplicationExt(resource.MimeType))
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(body, maxBytes+1))
	watchdog.Stop()
	closeErr := file.Close()
	if copyErr != nil {
		if stalled.Load() {
			return errors.New("读取素材文件超时，请稍后重试")
		}
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxBytes {
		return BadAuthRequest(fmt.Sprintf("文件超过 %d MB", maxBytes>>20))
	}
	return fn(resource, path)
}

func videoReplicationExt(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "video/quicktime":
		return ".mov"
	case "video/mp4":
		return ".mp4"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	}
	return ".bin"
}

var sceneTimePattern = regexp.MustCompile(`pts_time:([0-9]+(?:\.[0-9]+)?)`)

// detectVideoReplicationCuts 返回场景切换点（毫秒，升序，不含 0 与结尾）。
func detectVideoReplicationCuts(ctx context.Context, ffmpegBin, path string) ([]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	filter := fmt.Sprintf("select='gt(scene,%.2f)',showinfo", videoReplicationSceneThreshold)
	cmd := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-nostdin", "-i", path, "-an", "-vf", filter, "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("镜头检测失败：%s", clipText(strings.TrimSpace(stderr.String()), 300))
	}
	var cuts []int64
	for _, match := range sceneTimePattern.FindAllStringSubmatch(stderr.String(), -1) {
		seconds, err := strconv.ParseFloat(match[1], 64)
		if err == nil && seconds > 0 {
			cuts = append(cuts, int64(math.Round(seconds*1000)))
		}
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i] < cuts[j] })
	return cuts, nil
}

type videoReplicationSpan struct{ StartMs, EndMs int64 }

// videoReplicationSpans 把切点变成镜头区间：过短镜头并入前一个，超过上限时合并最短的相邻镜头。
func videoReplicationSpans(cuts []int64, durationMs int64) []videoReplicationSpan {
	spans := []videoReplicationSpan{}
	start := int64(0)
	for _, cut := range cuts {
		if cut <= start || cut >= durationMs {
			continue
		}
		spans = append(spans, videoReplicationSpan{StartMs: start, EndMs: cut})
		start = cut
	}
	spans = append(spans, videoReplicationSpan{StartMs: start, EndMs: durationMs})
	merged := []videoReplicationSpan{}
	for _, span := range spans {
		if len(merged) > 0 && span.EndMs-span.StartMs < videoReplicationMinShotMs {
			merged[len(merged)-1].EndMs = span.EndMs
			continue
		}
		merged = append(merged, span)
	}
	if len(merged) > 1 && merged[0].EndMs-merged[0].StartMs < videoReplicationMinShotMs {
		merged[1].StartMs = merged[0].StartMs
		merged = merged[1:]
	}
	for len(merged) > videoReplicationMaxShots {
		shortest := 0
		for i := 1; i < len(merged)-1; i++ {
			if merged[i].EndMs-merged[i].StartMs+merged[i+1].EndMs-merged[i+1].StartMs < merged[shortest].EndMs-merged[shortest].StartMs+merged[shortest+1].EndMs-merged[shortest+1].StartMs {
				shortest = i
			}
		}
		merged[shortest].EndMs = merged[shortest+1].EndMs
		merged = append(merged[:shortest+1], merged[shortest+2:]...)
	}
	return merged
}

// extractVideoReplicationFrame 抽取 atMs 处的一帧，缩放到长边 768 内并输出 JPEG。
func extractVideoReplicationFrame(ctx context.Context, ffmpegBin, input string, atMs int64, output string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	at := fmt.Sprintf("%.3f", float64(atMs)/1000)
	cmd := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-ss", at, "-i", input, "-frames:v", "1",
		"-vf", "scale='if(gt(iw,ih),min(768,iw),-2)':'if(gt(iw,ih),-2,min(768,ih))'", "-q:v", "4", "-y", output)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("抽取关键帧失败：%s", clipText(strings.TrimSpace(string(out)), 300))
	}
	return nil
}

// extractVideoReplicationAudio 抽出单声道 AAC 音轨，供分析记录与后续配音参考。
func extractVideoReplicationAudio(ctx context.Context, ffmpegBin, input, output string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", input, "-vn", "-ac", "1", "-ar", "44100", "-c:a", "aac", "-b:a", "96k", "-y", output)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("抽取音轨失败：%s", clipText(strings.TrimSpace(string(out)), 300))
	}
	return nil
}

// videoReplicationImageDataURL 把图片资源缩放成长边 1024 的 JPEG data URL，供识图模型读取。
func (s *Service) videoReplicationImageDataURL(ctx context.Context, userID, resourceID string) (string, error) {
	ffmpegBin, err := renderFfmpegBinary()
	if err != nil {
		return "", err
	}
	var dataURL string
	err = s.withVideoReplicationResourceFile(userID, resourceID, videoReplicationMaxImageBytes, func(resource *model.Resource, input string) error {
		if resource.Kind != "image" {
			return errors.New("素材不是图片")
		}
		output := filepath.Join(filepath.Dir(input), "scaled.jpg")
		callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(callCtx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", input, "-frames:v", "1",
			"-vf", "scale='if(gt(iw,ih),min(1024,iw),-2)':'if(gt(iw,ih),-2,min(1024,ih))'", "-q:v", "4", "-y", output)
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			return fmt.Errorf("读取图片失败：%s", clipText(strings.TrimSpace(string(out)), 300))
		}
		data, readErr := os.ReadFile(output)
		if readErr != nil {
			return readErr
		}
		dataURL = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
		return nil
	})
	return dataURL, err
}

// saveVideoReplicationFile 把本地处理产物保存为当前用户的资源。
func (s *Service) saveVideoReplicationFile(userID, path, name, kind string, width, height int, durationMs int64, identity string) (*model.Resource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return s.UploadResourceFile(userID, name, info.Size(), kind, width, height, durationMs, file, identity)
}

// videoReplicationTranscriber 把 16k 单声道 wav 转成带时间戳的台词段。
type videoReplicationTranscriber func(ctx context.Context, wavPath string) ([]timelineTranscriptionSegment, string, error)

// videoReplicationTranscriberFor 选择语音识别方式：后台指定的语音识别模型优先（OpenAI 兼容 /audio/transcriptions），
// 其次服务器 whisper.cpp 服务；都没有时返回 nil，分析继续但不提取台词。
func (s *Service) videoReplicationTranscriberFor(userID string) (videoReplicationTranscriber, string) {
	setting, err := s.videoReplicationSetting()
	if err == nil && setting.asrVisionMode() {
		return s.videoReplicationVisionTranscriber(userID, setting.ASR.Language)
	}
	if err == nil && setting.asrModelMode() {
		provider, err := s.resolveVideoReplicationProvider(userID, setting.ASR.Model, "")
		if err != nil {
			return nil, "后台语音识别模型不可用：" + err.Error()
		}
		language := setting.ASR.Language
		return func(ctx context.Context, wavPath string) ([]timelineTranscriptionSegment, string, error) {
			data, err := os.ReadFile(wavPath)
			if err != nil {
				return nil, "", err
			}
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", "audio.wav")
			if err != nil {
				return nil, "", err
			}
			if _, err := part.Write(data); err != nil {
				return nil, "", err
			}
			_ = writer.WriteField("model", provider.Model)
			_ = writer.WriteField("response_format", "verbose_json")
			_ = writer.WriteField("timestamp_granularities[]", "segment")
			if language != "" {
				_ = writer.WriteField("language", language)
			}
			if err := writer.Close(); err != nil {
				return nil, "", err
			}
			var raw json.RawMessage
			// 语音识别按音频秒数计量：16 kHz 单声道 16 位 wav，去掉 44 字节文件头后每秒 32000 字节。
			meteredCtx, meter := s.meterVideoReplicationCall(ctx, "asr", "audio", &provider, int64(math.Ceil(float64(max(0, len(data)-44))/32000)))
			err = postForm(meteredCtx, provider, "/audio/transcriptions", writer.FormDataContentType(), &body, &raw)
			s.settleVideoReplicationMeter(meter)
			if err != nil {
				return nil, "", err
			}
			return decodeWhisperVerboseJSON(raw)
		}, ""
	}
	baseURL := strings.TrimSpace(os.Getenv(whisperLangEnv))
	if baseURL == "" {
		return nil, "未配置语音识别：请在电商视频 Agent 后台选择语音识别模型"
	}
	return func(ctx context.Context, wavPath string) ([]timelineTranscriptionSegment, string, error) {
		started := time.Now()
		segments, language, err := newWhisperClient(baseURL).transcribe(ctx, wavPath, "")
		s.logSelfHostedVideoReplicationASR(ctx, started, err)
		return segments, language, err
	}, ""
}

// transcribeVideoReplicationAudio 提取口播/旁白台词与时间戳。
// 未配置或失败时如实记录原因，不伪造台词；转写不做说话人分离，说话人由画面理解推断。
func transcribeVideoReplicationAudio(ctx context.Context, path string, probe videoReplicationProbe, transcribe videoReplicationTranscriber, unavailable string) *VideoReplicationVoice {
	voice := &VideoReplicationVoice{HasAudio: probe.HasAudio, Type: "unknown"}
	if !probe.HasAudio {
		voice.Type = "silent"
		return voice
	}
	if transcribe == nil {
		voice.ASRError = unavailable
		return voice
	}
	file, err := os.Open(path)
	if err != nil {
		voice.ASRError = err.Error()
		return voice
	}
	defer file.Close()
	wavPath, cleanup, err := prepareWhisperWav(ctx, file, "video/mp4")
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		voice.ASRError = clipText(err.Error(), 300)
		return voice
	}
	callCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	segments, language, err := transcribe(callCtx, wavPath)
	if err != nil {
		voice.ASRError = clipText(err.Error(), 300)
		return voice
	}
	voice.ASRAvailable, voice.Language = true, language
	chars := 0
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		voice.Segments = append(voice.Segments, segment)
		voice.SpeechMs += segment.EndMs - segment.StartMs
		chars += len([]rune(text))
	}
	if voice.SpeechMs > 0 {
		voice.CharsPerSec = math.Round(float64(chars)/(float64(voice.SpeechMs)/1000)*10) / 10
	}
	if len(voice.Segments) == 0 {
		voice.Type = "music"
	}
	return voice
}

// videoReplicationVoiceLabel 是声音类型的展示名。
func videoReplicationVoiceLabel(kind string) string {
	switch kind {
	case "on_camera":
		return "人物对镜口播"
	case "narration":
		return "画外旁白"
	case "dialogue":
		return "多人对话"
	case "music":
		return "纯背景音乐"
	case "silent":
		return "无声视频"
	}
	return "无法判断"
}
