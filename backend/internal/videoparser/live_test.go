package videoparser

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// 真实授权集成测试：访问真实平台的公开作品，不在 CI 默认运行。
//
//	VIDEO_PARSER_LIVE=1 go test ./internal/videoparser -run TestLive -v
//
// 每个平台的测试链接可用 VIDEO_PARSER_LIVE_<PLATFORM>=<分享链接或文案> 覆盖；没有默认链接的平台需要显式提供。
// VIDEO_PARSER_LIVE_REPORT=<文件> 时把每个平台的结果写成 JSON Lines，作为测试记录。
// 海外平台需要部署者提供 CANVAS_VIDEO_PARSER_OVERSEAS_PROXY。

var liveDefaults = map[string]string{
	// 2026-11-05 实测使用的公开作品。小红书需要 App 内新分享的链接，没有默认值。
	PlatformDouyin:   "7.43 复制打开抖音，看看【作品】 https://www.iesdouyin.com/share/video/7703069533934942826/ 复制此链接",
	PlatformKuaishou: "https://www.kuaishou.com/short-video/3x8zha3ipq6bg8q",
	PlatformChannels: "https://weixin.qq.com/sph/AkBAEKjbtY",
	PlatformBilibili: "https://www.bilibili.com/video/BV1GJ411x7h7",
	PlatformWeibo:    "https://weibo.com/tv/show/1034:4797699866951785",
	PlatformXigua:    "https://www.ixigua.com/6996881461559165471",
}

type liveRecord struct {
	Platform      string  `json:"platform"`
	Link          string  `json:"link"`
	Detected      bool    `json:"detected"`
	Support       string  `json:"support"`
	Parsed        bool    `json:"parsed"`
	Provider      string  `json:"provider,omitempty"`
	Title         string  `json:"title,omitempty"`
	Author        string  `json:"author,omitempty"`
	MetaDuration  int64   `json:"metadataDurationMs,omitempty"`
	Downloaded    bool    `json:"downloaded"`
	Bytes         int64   `json:"bytes,omitempty"`
	Merged        bool    `json:"merged"`
	ProbeVideo    string  `json:"probeVideo,omitempty"`
	ProbeAudio    string  `json:"probeAudio,omitempty"`
	ProbeDuration float64 `json:"probeDurationSeconds,omitempty"`
	ErrorCode     string  `json:"errorCode,omitempty"`
	Error         string  `json:"error,omitempty"`
	Seconds       float64 `json:"seconds"`
}

func TestLivePlatforms(t *testing.T) {
	if os.Getenv("VIDEO_PARSER_LIVE") != "1" {
		t.Skip("set VIDEO_PARSER_LIVE=1 to run real platform tests")
	}
	opts := OptionsFromEnv()
	opts.ProviderTimeout, opts.DownloadTimeout = 90*time.Second, 4*time.Minute
	if os.Getenv("VIDEO_PARSER_LIVE_ALL") == "1" {
		opts.Platforms = nil
	}
	svc := NewDefault(opts)
	t.Cleanup(svc.Close)
	var report *os.File
	if path := os.Getenv("VIDEO_PARSER_LIVE_REPORT"); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		report = file
	}
	for _, adapter := range svc.Registry().Adapters() {
		key := adapter.Info().Key
		t.Run(key, func(t *testing.T) {
			link := strings.TrimSpace(os.Getenv("VIDEO_PARSER_LIVE_" + strings.ToUpper(key)))
			if link == "" {
				link = liveDefaults[key]
			}
			if link == "" {
				t.Skipf("no public test link for %s; set VIDEO_PARSER_LIVE_%s", key, strings.ToUpper(key))
			}
			record := runLive(t, svc, key, link)
			if report != nil {
				line, _ := json.Marshal(record)
				_, _ = report.Write(append(line, '\n'))
			}
			t.Logf("%+v", record)
			if record.Support == string(SupportSupported) && (!record.Downloaded || record.ProbeVideo == "") {
				t.Errorf("%s is marked supported but the real test failed: %s %s", key, record.ErrorCode, record.Error)
			}
		})
	}
}

func runLive(t *testing.T, svc *Service, key, text string) liveRecord {
	started := time.Now()
	record := liveRecord{Platform: key, Link: redactURL(text)}
	fail := func(err error) liveRecord {
		parsed := AsError(err)
		record.ErrorCode, record.Error = string(parsed.Code), parsed.Message
		if parsed.Err != nil {
			record.Error += "（" + clip(parsed.Err.Error(), 200) + "）"
		}
		record.Seconds = time.Since(started).Seconds()
		return record
	}
	adapter, _ := svc.Registry().Get(key)
	record.Support = string(svc.EffectiveCapabilities(adapter).Support)
	link, err := svc.Identify(text)
	if err != nil {
		return fail(err)
	}
	record.Link = redactURL(link.URL.String())
	record.Detected = link.Platform.Info().Key == key
	record.Support = string(svc.EffectiveCapabilities(link.Platform).Support)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	result, err := svc.Parse(ctx, link, nil)
	if err != nil {
		return fail(err)
	}
	record.Parsed, record.Provider, record.Title, record.Author, record.MetaDuration = true, result.Provider, clip(result.Meta.Title, 40), result.Meta.Author, result.Meta.DurationMs
	file, err := svc.Download(ctx, result, t.TempDir(), nil)
	if err != nil {
		return fail(err)
	}
	record.Downloaded, record.Bytes, record.Merged = true, file.Size, file.Merged
	output, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name:format=duration", "-of", "json", file.Path).Output()
	if err != nil {
		return fail(newError(ErrMedia, "ffprobe 读取失败", err))
	}
	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	_ = json.Unmarshal(output, &probe)
	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			record.ProbeVideo = stream.CodecName
		case "audio":
			record.ProbeAudio = stream.CodecName
		}
	}
	if seconds, err := json.Number(probe.Format.Duration).Float64(); err == nil {
		record.ProbeDuration = seconds
	}
	record.Seconds = time.Since(started).Seconds()
	return record
}
