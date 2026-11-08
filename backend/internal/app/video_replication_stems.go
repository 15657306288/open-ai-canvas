package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"yingce/backend/internal/model"
)

// 人声 / 伴奏分离：参考视频往往同时有人声和背景音乐。替换音频时视频模型会连人声带音乐一起重新生成，
// 原片的背景音乐就被换掉了。配置了分离服务（Compose 内网的 Demucs 容器）时：
//   1. 一键生成提交前，把原视频音轨分离出伴奏（背景音乐 + 环境声），写进方案 plan.Instrumental；
//   2. 每段视频提示词改成「只生成人声，不要背景音乐」；
//   3. 每段生成完成后再分离一次，只取人声（模型即使还是混进了音乐也会被去掉）；
//   4. 合成时 = 各段新人声 + 整条原伴奏，背景音乐与原片完全一致，也不会两层音乐叠在一起。
// 分离服务没配置或原片伴奏分离失败时退回原来的做法（模型按原片风格配音乐），不阻止生成。

const (
	// videoReplicationSeparatorEnv 是分离服务地址（自行部署 audio-separator 等兼容服务后填写）；为空时不启用。
	videoReplicationSeparatorEnv = "VIDEO_REPLICATION_SEPARATOR_URL"
	// videoReplicationStemVersion 随分离模型或输出格式变化递增，避免复用旧结果。
	videoReplicationStemVersion = 1
	videoReplicationStemVocals  = "vocals"
	videoReplicationStemMusic   = "no_vocals"
)

func videoReplicationSeparatorURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(videoReplicationSeparatorEnv)), "/")
}

func videoReplicationStemIdentity(sourceResourceID, stem string) string {
	return fmt.Sprintf("video-replication-stem:v%d:%s:%s", videoReplicationStemVersion, sourceResourceID, stem)
}

// videoReplicationCachedStem 返回已分离好的结果；没有时返回空。
func (s *Service) videoReplicationCachedStem(userID, sourceResourceID, stem string) string {
	resource, err := s.resourceForUploadKey(userID, normalizedResourceUploadKey([]string{videoReplicationStemIdentity(sourceResourceID, stem)}))
	if err != nil || resource == nil || resource.Status != model.ResourceStatusReady {
		return ""
	}
	return resource.ID
}

// separateVideoReplicationStem 把一条音频 / 视频资源送去分离，保存指定的一轨并返回资源 ID；已分离过的直接复用。
func (s *Service) separateVideoReplicationStem(ctx context.Context, userID, sourceResourceID, stem string) (string, error) {
	base := videoReplicationSeparatorURL()
	if base == "" {
		return "", fmt.Errorf("未配置人声分离服务")
	}
	if cached := s.videoReplicationCachedStem(userID, sourceResourceID, stem); cached != "" {
		return cached, nil
	}
	var stemID string
	ffmpegBin, err := renderFfmpegBinary()
	if err != nil {
		return "", err
	}
	err = s.withVideoReplicationResourceFile(userID, sourceResourceID, videoReplicationMaxVideoBytes, func(source *model.Resource, path string) error {
		// 分离服务只收 44.1k 立体声 wav：视频片段和各种音频格式都在这里先抽音轨转好。
		wav := filepath.Join(filepath.Dir(path), "stem-input.wav")
		convert := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", path, "-vn", "-ac", "2", "-ar", "44100", "-c:a", "pcm_s16le", "-y", wav)
		if out, err := convert.CombinedOutput(); err != nil {
			return fmt.Errorf("抽取音轨失败（可能没有声音）：%s", clipText(strings.TrimSpace(string(out)), 300))
		}
		input, err := os.Open(wav)
		if err != nil {
			return err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/separate?stem="+stem, input)
		if err != nil {
			return err
		}
		request.ContentLength = info.Size()
		request.Header.Set("Content-Type", "application/octet-stream")
		response, err := (&http.Client{Timeout: 20 * time.Minute}).Do(request)
		if err != nil {
			return fmt.Errorf("人声分离服务不可用：%w", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 600))
			return fmt.Errorf("人声分离失败（HTTP %d）：%s", response.StatusCode, strings.TrimSpace(string(body)))
		}
		output := filepath.Join(filepath.Dir(path), "stem-"+stem+".flac")
		file, err := os.Create(output)
		if err != nil {
			return err
		}
		if _, err := io.Copy(file, io.LimitReader(response.Body, 200<<20)); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		label := map[string]string{videoReplicationStemVocals: "人声", videoReplicationStemMusic: "伴奏"}[stem]
		saved, err := s.saveVideoReplicationFile(userID, output, label+"-"+sourceResourceID+".flac", "audio", 0, 0, source.DurationMs, videoReplicationStemIdentity(sourceResourceID, stem))
		if err != nil {
			return err
		}
		stemID = saved.ID
		return nil
	})
	return stemID, err
}

// videoReplicationStemJob 是进程内的后台分离任务；结果落成资源后由 videoReplicationCachedStem 命中，任务表只用来去重和记失败。
type videoReplicationStemJob struct {
	done     bool
	err      error
	resultID string
}

var videoReplicationStemJobs sync.Map

// videoReplicationStemAsync 取分离结果：已有直接返回；没有就在后台开始分离并返回 pending；分离失败返回错误（调用方退回原始音频）。
func (s *Service) videoReplicationStemAsync(userID, sourceResourceID, stem string) (string, bool, error) {
	if cached := s.videoReplicationCachedStem(userID, sourceResourceID, stem); cached != "" {
		return cached, false, nil
	}
	key := videoReplicationStemIdentity(sourceResourceID, stem)
	job := &videoReplicationStemJob{}
	if existing, loaded := videoReplicationStemJobs.LoadOrStore(key, job); loaded {
		current := existing.(*videoReplicationStemJob)
		if !current.done {
			return "", true, nil
		}
		return current.resultID, false, current.err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
		defer cancel()
		id, err := s.separateVideoReplicationStem(ctx, userID, sourceResourceID, stem)
		if err != nil {
			slog.Warn("video replication stem separation failed", "source", sourceResourceID, "stem", stem, "error", err)
		}
		videoReplicationStemJobs.Store(key, &videoReplicationStemJob{done: true, err: err, resultID: id})
	}()
	return "", true, nil
}

// errVideoReplicationStemsPending：各段人声还在分离，合成稍后由同步阶段自动进行。
var errVideoReplicationStemsPending = BadAuthRequest("正在分离各段人声，完成后会自动合成，请稍候")

// videoReplicationSegmentVocals 为参与合成的每段画面取「只有人声」的音轨。返回 runID → 人声资源；
// 还有段在分离时 pending 为 true；某段分离失败时不放进结果，合成时该段沿用原始音频。
func (s *Service) videoReplicationSegmentVocals(userID string, runs map[string]model.VideoReplicationShotRun) (map[string]string, bool) {
	vocals, pending := map[string]string{}, false
	for _, run := range runs {
		if run.OutputResourceID == "" {
			continue
		}
		id, waiting, err := s.videoReplicationStemAsync(userID, run.OutputResourceID, videoReplicationStemVocals)
		switch {
		case waiting:
			pending = true
		case err == nil && id != "":
			vocals[run.ID] = id
		}
	}
	return vocals, pending
}

// applyVideoReplicationStems 把时间线改成「各段人声 + 原伴奏」：分离成功的段画面静音、在人声轨放这段人声，
// 分离失败的段保留模型原始音频；伴奏从 0 开始铺满整片。
func applyVideoReplicationStems(timeline *renderProject, runByClip map[string]string, vocals map[string]string, instrumental string) {
	timeline.Tracks = append(timeline.Tracks, renderTrack{ID: "stem-vocals", Kind: "audio"}, renderTrack{ID: "stem-music", Kind: "audio"})
	clips := make([]renderClip, 0, len(timeline.Clips)+len(vocals)+1)
	for _, clip := range timeline.Clips {
		voice := vocals[runByClip[clip.ID]]
		if clip.Kind == "video" && voice != "" {
			clip.Volume = -1
			clips = append(clips, clip, renderClip{ID: "vocals-" + clip.ID, Kind: "audio", TrackID: "stem-vocals", StartMs: clip.StartMs, DurationMs: clip.DurationMs, SourceDurationMs: clip.DurationMs, Volume: 1, DirectMedia: videoReplicationMediaRef(voice, "audio")})
			continue
		}
		clips = append(clips, clip)
	}
	clips = append(clips, renderClip{ID: "stem-music", Kind: "audio", TrackID: "stem-music", StartMs: 0, DurationMs: timeline.DurationMs, SourceDurationMs: timeline.DurationMs, Volume: 1, DirectMedia: videoReplicationMediaRef(instrumental, "audio")})
	timeline.Clips = clips
}
