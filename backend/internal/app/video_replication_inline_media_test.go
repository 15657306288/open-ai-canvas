package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func TestVideoReplicationInlineMediaFromRealVideo(t *testing.T) {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil || !mediaProbeAvailable() {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "ref.mp4")
	if output, err := exec.Command(ffmpegBin, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=720x1280:rate=25:duration=25", "-f", "lavfi", "-i", "sine=frequency=440:duration=25", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", source).CombinedOutput(); err != nil {
		t.Fatalf("make video: %s", output)
	}
	probe, err := probeVideoReplicationFile(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	// 25 秒、上限 120 帧：约 1 帧/秒，带音轨。
	media, err := prepareVideoReplicationInlineMedia(context.Background(), ffmpegBin, source, probe, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(media.Frames) < 24 || len(media.Frames) > 26 || media.Audio == "" || !strings.HasPrefix(media.Frames[0].DataURL, "data:image/jpeg;base64,") || media.Frames[5].AtSec != 5 {
		t.Fatalf("frames=%d audio=%d first=%v at5=%v", len(media.Frames), len(media.Audio), media.Frames[0].AtSec, media.Frames[5].AtSec)
	}
	// 多个参考视频分摊帧数上限：10 帧时约每 2.5 秒一帧。
	limited, err := prepareVideoReplicationInlineMedia(context.Background(), ffmpegBin, source, probe, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Frames) > 10 || len(limited.Frames) < 9 || limited.Frames[1].AtSec != 2.5 {
		t.Fatalf("limited frames=%d second at %v", len(limited.Frames), limited.Frames[1].AtSec)
	}
}

func TestVideoReplicationInlineMessagesRespectImageLimit(t *testing.T) {
	frames := make([]videoReplicationInlineFrame, 40)
	for index := range frames {
		frames[index] = videoReplicationInlineFrame{AtSec: float64(index), DataURL: "data:image/jpeg;base64,AA=="}
	}
	messages := videoReplicationInlineMessages("system", "prompt", []videoReplicationInlineMedia{{DurationMs: 40000, Frames: frames, Audio: "QUJD"}, {DurationMs: 5000, Frames: frames[:3]}})
	if messages[0]["role"] != "system" {
		t.Fatal("system message must come first")
	}
	images, audios := 0, 0
	for _, message := range messages[1:] {
		perMessage := 0
		for _, part := range message["content"].([]map[string]any) {
			switch part["type"] {
			case "image_url":
				perMessage++
			case "input_audio":
				audios++
			case "video_url":
				t.Fatal("inline mode must not send video_url")
			}
		}
		if perMessage > videoReplicationInlineImagesPerMessage {
			t.Fatalf("message carries %d images, limit 16", perMessage)
		}
		images += perMessage
	}
	// 40 帧分 3 条 + 3 帧 1 条 + 最后一条音轨与要求。
	if images != 43 || audios != 1 || len(messages) != 1+3+1+1 {
		t.Fatalf("images=%d audios=%d messages=%d", images, audios, len(messages))
	}
	last := messages[len(messages)-1]["content"].([]map[string]any)
	if last[len(last)-1]["text"] != "prompt" || !strings.Contains(last[2]["text"].(string), "V2 没有音轨") {
		t.Fatalf("last message = %+v", last)
	}
}

func TestVideoReplicationInlineChatSendsFramesAndAudioAndIsMetered(t *testing.T) {
	f := newVideoReplicationCostFixture(t)
	var captured map[string]any
	var auth string
	f.reply.Store(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &captured)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"{\\\"ok\\\":true}\"}}]}\n\n" +
			"data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":500}}\n\ndata: [DONE]\n\n"))
	})
	cfg := f.cfg("vision-test")
	cfg.TextProvider.InterfaceType = "apimart-gemini-video-text"
	cfg.TextProvider.APIFormat = "gemini"
	media := []videoReplicationInlineMedia{{DurationMs: 3000, Frames: []videoReplicationInlineFrame{{AtSec: 0, DataURL: "data:image/jpeg;base64,AA=="}, {AtSec: 1, DataURL: "data:image/jpeg;base64,AA=="}}, Audio: "QUJD"}}
	var out map[string]any
	ctx := withVideoReplicationCostScope(context.Background(), "owner", "proj-1")
	if err := f.svc.videoReplicationInlineChatJSON(ctx, cfg, "vision", "system", "prompt", media, &out); err != nil || out["ok"] != true {
		t.Fatalf("out=%v err=%v", out, err)
	}
	encoded, _ := json.Marshal(captured)
	body := string(encoded)
	if auth != "Bearer fixture-key" || captured["stream"] != true || strings.Contains(body, "video_url") || !strings.Contains(body, `"input_audio"`) || strings.Count(body, `"type":"image_url"`) != 2 {
		t.Fatalf("auth=%q request=%s", auth, clipText(body, 600))
	}
	// 与其他分析调用一样进入调用日志和成本账单。
	logs, orders := f.logs(t), f.orders(t)
	if len(logs) != 1 || logs[0].Operation != videoReplicationAnalysisScene+":vision" || logs[0].InputTokens != 1000 || len(orders) != 1 || orders[0].Status != model.BillingStatusSettled {
		t.Fatalf("logs=%+v orders=%+v", logs, orders)
	}
}

func TestVideoReplicationUpstreamErrorHandling(t *testing.T) {
	fetch := errors.New("视频理解失败：Cannot fetch content from the provided URL. The website is unavailable or did not respond.")
	if !videoReplicationRetryable(fetch) || !strings.Contains(videoReplicationFriendlyError(fetch.Error()), "无法下载参考视频") {
		t.Fatal("fetch failures must be retried once and shown in Chinese")
	}
	tooMany := errors.New("too many images in the message, max allowed is 16")
	if videoReplicationRetryable(tooMany) || !strings.Contains(videoReplicationFriendlyError(tooMany.Error()), "画面数量") {
		t.Fatal("request-shape errors must not be retried")
	}
	if videoReplicationRetryable(nil) || videoReplicationFriendlyError("模型未返回镜头拆解") != "模型未返回镜头拆解" {
		t.Fatal("other errors pass through unchanged")
	}
}
