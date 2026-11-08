package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	"yingce/backend/internal/videoparser"
)

const videoLinkKuaishouPage = `<html><script>window.INIT_STATE = {"a":{"photo":{"caption":"测试商品视频","photoType":"VIDEO","userName":"测试作者","duration":5000,"width":320,"height":240,"mainMvUrls":[{"cdn":"txmov2.a.kwimgs.com","url":"https://txmov2.a.kwimgs.com/upic/test_b.mp4"}]}}}</script></html>`

// newVideoLinkTestService 组合资源存储（本地存储）、视频复刻与素材库的表；解析服务的网络出口指向本地假上游。
func newVideoLinkTestService(t *testing.T, media []byte) (*Service, *httptest.Server) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+newID()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.UserOSSSetting{}, &model.StorageLocation{}, &model.UserDailyUploadUsage{}, &model.Resource{}, &model.ResourceOffloadJob{}, &model.UploadReservation{}, &model.UserStorageGrant{}, &model.UserStorageState{},
		&model.Task{}, &model.VideoReplicationProject{}, &model.VideoReplicationAsset{}, &model.VideoReplicationShotRun{}, &model.VideoReplicationOutput{}, &model.Asset{}, &model.CanvasProject{}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "v.kuaishou.com":
			http.Redirect(w, r, "https://www.kuaishou.com/short-video/3x8zha3ipq6bg8q", http.StatusFound)
		case r.Host == "www.kuaishou.com":
			_, _ = w.Write([]byte("<html></html>"))
		case r.Host == "v.m.chenzhongtech.com":
			_, _ = w.Write([]byte(videoLinkKuaishouPage))
		case strings.HasSuffix(r.URL.Path, ".mp4"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(media)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	addr := upstream.Listener.Addr().String()
	client := func(timeout time.Duration, _ string) *http.Client {
		return &http.Client{Timeout: timeout, Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 仅测试：本地自签证书
		}}
	}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	svc := &Service{repo: repository.New(db), dataDir: t.TempDir()}
	if err := svc.repo.SaveSystemSetting(&model.SystemSetting{Key: featureAvailabilitySettingKey, ValueJSON: `{"videoWorkbenchEnabled":true}`}); err != nil {
		t.Fatal(err)
	}
	svc.videoLinkParserSvc = videoparser.New(videoparser.Options{HTTPClient: client, FFmpegPath: ffmpeg, YTDLPPath: "/fake/yt-dlp", NoBrowser: true, MaxBytes: videoReplicationMaxVideoBytes, MaxDurationMs: videoReplicationMaxSourceMs}, nil)
	return svc, upstream
}

func videoLinkTestMedia(t *testing.T) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "source.mp4")
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "5", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", "-f", "mp4", out}
	if output, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		args[len(args)-9] = "mpeg4"
		if retry, retryErr := exec.Command("ffmpeg", args...).CombinedOutput(); retryErr != nil {
			t.Fatalf("ffmpeg: %v %s %s", retryErr, output, retry)
		}
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestImportVideoLinkStoresResourceAssetAndReference(t *testing.T) {
	requireFFmpegTools(t)
	svc, _ := newVideoLinkTestService(t, videoLinkTestMedia(t))
	title := "商品视频"
	project, err := svc.CreateVideoReplicationProject("user-1", VideoReplicationProjectInput{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	result, err := svc.ImportVideoReplicationLinkWithProgress(context.Background(), "user-1", project.ID, VideoReplicationLinkInput{URL: "https://v.kuaishou.com/2Bd8Xx 好物 该作品在快手被播放过"}, func(event VideoLinkEvent) {
		stages = append(stages, event.Stage)
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(stages, ",")
	for _, want := range []string{"detecting", "detected", "resolving", "parsing", "parsed", "downloading", "merging", "probing", "uploading", "registering", "done"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stage %q missing: %s", want, joined)
		}
	}
	// 资源：转存完成、类型为视频、媒体参数来自 ffprobe。
	resource, err := svc.repo.ResourceForUser("user-1", result.ResourceID)
	if err != nil || resource.Status != model.ResourceStatusReady || resource.Kind != "video" || resource.MimeType != "video/mp4" || resource.DurationMs < 4900 || resource.Width != 320 {
		t.Fatalf("resource = %+v %v", resource, err)
	}
	// 素材库：素材指向这个资源，来源记录平台信息，不保存上游临时地址。
	asset, err := svc.repo.AssetForUser("user-1", result.AssetID)
	if err != nil || asset.Kind != "video" || asset.Category != model.AssetCategoryMaterial {
		t.Fatalf("asset = %+v %v", asset, err)
	}
	var payload struct {
		Data struct {
			StorageKey string `json:"storageKey"`
			URL        string `json:"url"`
		} `json:"data"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(asset.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.StorageKey != "resource:"+resource.ID || payload.Metadata["platform"] != "kuaishou" || payload.Metadata["sourceUrl"] != "https://www.kuaishou.com/short-video/3x8zha3ipq6bg8q" || payload.Metadata["hasAudio"] != true {
		t.Fatalf("asset payload = %s", asset.PayloadJSON)
	}
	if strings.Contains(asset.PayloadJSON, "kwimgs.com") {
		t.Fatalf("upstream temporary url must not be stored: %s", asset.PayloadJSON)
	}
	// 项目：参考视频就是这个资源，ffprobe 读到画面与音轨（供「保留原音乐和节奏」使用）。
	reference := 0
	for _, item := range result.Project.Assets {
		if item.Category == model.VideoReplicationAssetReferenceVideo {
			reference++
			if item.ID != result.ReferenceAssetID || item.ResourceID != resource.ID || !item.HasAudio || item.FPS <= 0 || item.DurationMs < 4900 {
				t.Fatalf("reference asset = %+v", item)
			}
		}
	}
	if reference != 1 || result.Source.PlatformLabel != "快手" || result.Source.Title != "测试商品视频" || !result.Source.HasAudio || result.Reused {
		t.Fatalf("result = %+v", result)
	}

	// 再次粘贴同一作品：复用已转存的资源与素材，不重复下载、不重复登记。
	again, err := svc.ImportVideoReplicationLinkWithProgress(context.Background(), "user-1", project.ID, VideoReplicationLinkInput{URL: "https://www.kuaishou.com/short-video/3x8zha3ipq6bg8q?fid=9"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Reused || again.ResourceID != result.ResourceID || again.AssetID != result.AssetID || again.ReferenceAssetID != result.ReferenceAssetID {
		t.Fatalf("second import = %+v", again)
	}
	resources, _ := svc.repo.Resources("user-1", 10)
	if len(resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(resources))
	}
}

// 真实授权集成测试：用真实平台的公开作品走完整导入（解析 → 下载 → ffprobe → 资源 → 素材库 → 参考视频），默认跳过。
//
//	VIDEO_PARSER_LIVE=1 VIDEO_PARSER_LIVE_LINK='<分享链接或文案>' go test ./internal/app -run TestImportVideoLinkLive -v
func TestImportVideoLinkLive(t *testing.T) {
	if os.Getenv("VIDEO_PARSER_LIVE") != "1" {
		t.Skip("set VIDEO_PARSER_LIVE=1 to run against real platforms")
	}
	requireFFmpegTools(t)
	links := strings.Split(os.Getenv("VIDEO_PARSER_LIVE_LINK"), "|")
	if strings.TrimSpace(links[0]) == "" {
		links = []string{"7.43 复制打开抖音，看看【作品】 https://www.iesdouyin.com/share/video/7703069533934942826/ 复制此链接", "https://www.kuaishou.com/short-video/3x8zha3ipq6bg8q"}
	}
	svc, _ := newVideoLinkTestService(t, nil)
	opts := videoparser.OptionsFromEnv()
	opts.MaxBytes, opts.MaxDurationMs = videoReplicationMaxVideoBytes, videoReplicationMaxSourceMs
	svc.videoLinkParserSvc = videoparser.NewDefault(opts)
	t.Cleanup(svc.videoLinkParserSvc.Close)
	title := "商品视频"
	for _, link := range links {
		project, err := svc.CreateVideoReplicationProject("user-1", VideoReplicationProjectInput{Title: &title})
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		var stages []string
		result, err := svc.ImportVideoReplicationLinkWithProgress(context.Background(), "user-1", project.ID, VideoReplicationLinkInput{URL: link}, func(event VideoLinkEvent) {
			stages = append(stages, event.Stage)
		})
		if err != nil {
			t.Fatalf("%s: %v (stages %v)", link, err, stages)
		}
		resource, err := svc.repo.ResourceForUser("user-1", result.ResourceID)
		if err != nil || resource.Status != model.ResourceStatusReady || resource.Kind != "video" {
			t.Fatalf("resource = %+v %v", resource, err)
		}
		if _, err := svc.repo.AssetForUser("user-1", result.AssetID); err != nil {
			t.Fatalf("asset missing: %v", err)
		}
		t.Logf("%s: %s %q by %q, %d ms %dx%d fps=%.2f audio=%v, %d bytes, resource=%s asset=%s reference=%s, %s",
			result.Source.PlatformLabel, result.Source.Provider, clipText(result.Source.Title, 30), result.Source.Author, result.Source.DurationMs, result.Source.Width, result.Source.Height, result.Source.FPS, result.Source.HasAudio,
			resource.Size, result.ResourceID, result.AssetID, result.ReferenceAssetID, time.Since(started).Round(time.Millisecond))
		if result.ReferenceAssetID == "" || result.Source.DurationMs <= 0 || result.Source.Width <= 0 {
			t.Fatalf("reference video not registered: %+v", result)
		}
	}
}

func TestImportVideoLinkErrorsAreExplained(t *testing.T) {
	svc, _ := newVideoLinkTestService(t, nil)
	title := "商品视频"
	project, err := svc.CreateVideoReplicationProject("user-1", VideoReplicationProjectInput{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		code   string
		status int
	}{
		"https://example.com/video.mp4":              {"unsupported_platform", http.StatusBadRequest},
		"复制打开抖音 https://v.douyin.com/FKWYQmtQ79E/":   {"platform_unavailable", http.StatusServiceUnavailable},
		"https://www.ixigua.com/6996881461559165471": {"platform_unavailable", http.StatusServiceUnavailable},
		"没有链接": {"invalid_link", http.StatusBadRequest},
	}
	for text, want := range cases {
		_, err := svc.ImportVideoReplicationLinkWithProgress(context.Background(), "user-1", project.ID, VideoReplicationLinkInput{URL: text}, nil)
		view := VideoLinkErrorView(err)
		if view.Code != want.code || view.Status != want.status || view.Message == "" {
			t.Fatalf("%q => %+v (%v)", text, view, err)
		}
	}
	if _, err := svc.ImportVideoReplicationLinkWithProgress(context.Background(), "user-2", project.ID, VideoReplicationLinkInput{URL: "https://v.kuaishou.com/2Bd8Xx"}, nil); err == nil {
		t.Fatal("other users must not import into this project")
	}
	if view := VideoLinkErrorView(context.DeadlineExceeded); view.Code != "timeout" || !view.Retryable {
		t.Fatalf("timeout view = %+v", view)
	}
}
