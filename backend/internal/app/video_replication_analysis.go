package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

// 商品视频复刻分析：ffmpeg 解析参考视频 → 识图模型理解镜头 → 识图模型建立商品/模特档案 → 策划模型重构原创分镜。
// 识图与策划模型复用后台为电商 Agent 配置的系统模型（同一套渠道、密钥只在服务端）；
// 与电商 Agent 一致，分析阶段不单独计费，积分只在逐镜头视频生成时按模型报价预扣。

const (
	videoReplicationAnalysisLease    = 25 * time.Minute
	videoReplicationAnalysisTimeout  = 24 * time.Minute
	videoReplicationAnalysisVideos   = 4
	videoReplicationVisionFrameLimit = 12
	videoReplicationProfileImages    = 4
)

// videoReplicationAnalysisSlots 限制单个进程同时运行的分析数，避免 ffmpeg 与模型调用挤占后端。
var videoReplicationAnalysisSlots = make(chan struct{}, 2)

type videoReplicationFrame struct {
	Ref     string
	DataURL string
}

// videoReplicationTextConfig 解析分析所用的文本模型：优先使用「商品视频复刻」后台指定的视频理解 / 分镜策划模型，
// 未指定时沿用电商 Agent 的识图 / 策划模型。
func (s *Service) videoReplicationTextConfig(userID string, vision bool) (ecommerceProviderConfig, error) {
	// 分镜要一次写完整条分镜与口播，输出长，慢渠道上常超过 150 秒；识图输入大但输出短。
	timeout := map[bool]time.Duration{true: 240 * time.Second, false: 360 * time.Second}[vision]
	cfg := ecommerceProviderConfig{Mode: "live", TextTimeout: timeout}
	stage := map[bool]string{true: "视频理解", false: "分镜策划"}[vision]
	replication, err := s.videoReplicationSetting()
	if err != nil {
		return cfg, err
	}
	selected := replication.Planning
	if vision {
		selected = replication.Vision
	}
	var ref EcommerceAgentModelRef
	source := "电商视频 Agent 后台"
	if !videoReplicationRefEmpty(selected) {
		ref = *selected
	} else {
		setting, err := s.EcommerceAgentSetting()
		if err != nil {
			return cfg, err
		}
		ref, source = setting.planningRef(), "电商 Agent（电商视频 Agent 后台未单独指定）"
		if vision {
			ref = setting.visionRef()
		}
	}
	resolved, err := s.ecommerceConfiguredTextFor(userID, cfg, ref, stage)
	if err != nil {
		return resolved, &AppError{Status: 503, Code: 503, Message: "商品视频复刻的" + stage + "模型不可用（来源：" + source + "）：" + err.Error()}
	}
	return resolved, nil
}

// videoReplicationRequirement 返回当前视频类型缺少的素材说明；空串表示素材齐全。
func videoReplicationRequirement(videoType string, params VideoReplicationParameters, assets []model.VideoReplicationAsset) string {
	counts := map[string]int{}
	for _, asset := range assets {
		counts[asset.Category]++
	}
	// 只有参考视频是必需的；商品 / 模特 / 服饰 / 人脸都是可选的替换目标，选了哪个就需要对应的参考图。
	if counts[model.VideoReplicationAssetReferenceVideo] == 0 {
		return "请先上传参考视频"
	}
	for _, target := range videoReplicationTargets(videoType) {
		category := videoReplicationTargetCategory[target]
		if counts[category] == 0 {
			return "已选择替换「" + videoReplicationTargetLabel(target) + "」，请上传至少 1 张" + videoReplicationCategoryLabels[category] + "，或取消该替换目标"
		}
	}
	if videoReplicationHasTarget(videoType, VideoReplicationTypeFace) && !params.FaceConsent {
		return "使用人脸照片前，请确认已取得肖像授权"
	}
	// 替换音频：成片里人物要用用户上传的声音说原视频台词，没有参考音色就无法保证声音来源，不能用模型随机音色冒充。
	if params.AudioMode == "replace" && counts[model.VideoReplicationAssetVoiceAudio] == 0 {
		return "替换音频需要上传参考音色（1–3 段）；重新生成时请先点「编辑」上传参考音色"
	}
	return ""
}

func videoReplicationTargetLabel(target string) string {
	return map[string]string{VideoReplicationTypeProduct: "商品", VideoReplicationTypeModel: "模特", VideoReplicationTypeApparel: "服饰", VideoReplicationTypeFace: "人脸", VideoReplicationTypeBackground: "背景"}[target]
}

func (s *Service) StartVideoReplicationAnalysis(userID, projectID string) (*VideoReplicationProjectView, error) {
	return s.startVideoReplicationAnalysis(userID, projectID, nil)
}

func (s *Service) startVideoReplicationAnalysis(userID, projectID string, startValues map[string]any) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if raw, ok := startValues["parameters_json"].(string); ok {
		project.ParametersJSON = raw
	}
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return nil, err
	}
	if missing := videoReplicationRequirement(project.VideoType, videoReplicationParameters(project), assets); missing != "" {
		return nil, BadAuthRequest(missing)
	}
	if _, err := videoReplicationMentions(project.Prompt, assets); err != nil {
		return nil, err
	}
	runs, err := s.repo.VideoReplicationRuns(userID, projectID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		if videoReplicationRunActive(run.Status) {
			return nil, BadAuthRequest("镜头仍在生成中，请等待完成或取消后再重新分析")
		}
	}
	if _, err := renderFfmpegBinary(); err != nil || !mediaProbeAvailable() {
		return nil, &AppError{Status: 503, Code: 503, Message: "服务器缺少 ffmpeg/ffprobe，暂时无法解析参考视频", Retryable: true}
	}
	if _, err := s.videoReplicationTextConfig(userID, true); err != nil {
		return nil, err
	}
	if _, err := s.videoReplicationTextConfig(userID, false); err != nil {
		return nil, err
	}
	lease := time.Now().Add(videoReplicationAnalysisLease).UTC().Truncate(time.Second)
	claimed, err := s.repo.ClaimVideoReplicationAnalysis(userID, projectID, lease, startValues)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, BadAuthRequest("参考视频正在分析中")
	}
	go func() { _ = s.runVideoReplicationAnalysis(userID, projectID, lease) }()
	return s.VideoReplicationProject(userID, projectID)
}

func (s *Service) runVideoReplicationAnalysis(userID, projectID string, lease time.Time) (result error) {
	ctx, cancel := context.WithTimeout(withVideoReplicationCostScope(context.Background(), userID, projectID), videoReplicationAnalysisTimeout)
	defer cancel()
	update := func(values map[string]any) {
		if _, err := s.repo.UpdateVideoReplicationAnalysis(userID, projectID, lease, values); err != nil {
			slog.Warn("video replication analysis update failed", "projectId", projectID, "error", err)
		}
	}
	fail := func(err error) {
		result = err
		slog.Warn("video replication analysis failed", "projectId", projectID, "error", err)
		update(map[string]any{"status": model.VideoReplicationStatusFailed, "stage": "分析失败", "error": clipText(videoReplicationFriendlyError(err.Error()), 600), "analysis_lease_until": nil})
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			fail(fmt.Errorf("分析异常：%v", recovered))
		}
	}()
	select {
	case videoReplicationAnalysisSlots <- struct{}{}:
		defer func() { <-videoReplicationAnalysisSlots }()
	case <-ctx.Done():
		fail(ctx.Err())
		return
	}
	analysis, storyboard, err := s.analyzeVideoReplicationBase(ctx, userID, projectID, lease, func(stage string, progress int) {
		update(map[string]any{"stage": stage, "progress": progress})
	})
	if err != nil {
		fail(err)
		return
	}
	analysisJSON, _ := json.Marshal(analysis)
	voiceCastJSON, _ := json.Marshal(analysis.voiceCast)
	storyboardJSON, _ := json.Marshal(storyboard)
	stage := "分镜已生成，等待确认"
	if project, err := s.repo.VideoReplicationProject(userID, projectID); err == nil && videoReplicationParameters(project).ConfirmFirst {
		stage = "复刻方案已生成，确认后开始生成"
	}
	update(map[string]any{
		"status": model.VideoReplicationStatusAnalyzed, "stage": stage, "progress": 100, "error": "",
		"analysis_json": string(analysisJSON), "storyboard_json": string(storyboardJSON), "voice_cast_json": string(voiceCastJSON),
		"storyboard_revision": gorm.Expr("storyboard_revision + 1"), "analysis_lease_until": nil,
	})
	// 一键生成：分析完成后直接按分段提交生成，全部完成后由同步阶段自动合成成片。
	s.continueVideoReplicationAuto(userID, projectID)
	return nil
}

func (s *Service) analyzeVideoReplicationBase(ctx context.Context, userID, projectID string, lease time.Time, progress func(string, int)) (*VideoReplicationAnalysis, *VideoReplicationStoryboard, error) {
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, nil, err
	}
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return nil, nil, err
	}
	params := videoReplicationParameters(project)
	views := videoReplicationAssetViews(assets)
	setting, err := s.videoReplicationSetting()
	if err != nil {
		return nil, nil, err
	}
	var videos, products, models []VideoReplicationAssetView
	for _, view := range views {
		switch view.Category {
		case model.VideoReplicationAssetReferenceVideo:
			videos = append(videos, view)
		case model.VideoReplicationAssetProductImage, model.VideoReplicationAssetApparelImage:
			products = append(products, view)
		case model.VideoReplicationAssetModelImage, model.VideoReplicationAssetFaceImage:
			models = append(models, view)
		}
	}
	sortPrimaryFirst := func(items []VideoReplicationAssetView) {
		sort.SliceStable(items, func(i, j int) bool { return items[i].IsPrimary && !items[j].IsPrimary })
	}
	sortPrimaryFirst(videos)
	sortPrimaryFirst(products)
	sortPrimaryFirst(models)
	if len(videos) > videoReplicationAnalysisVideos {
		videos = videos[:videoReplicationAnalysisVideos]
	}
	// 素材没变时复用已保存的视频理解结果，只重新规划分段（要求、单段时长等变化），不再调用识图模型。
	if saved := videoReplicationSavedAnalysis(project); params.ConfirmFirst && saved != nil && saved.MediaDigest != "" && saved.MediaDigest == videoReplicationAnalysisDigest(assets, params) {
		progress("素材未变化，复用已保存的视频分析，重新规划分段", 75)
		planningCfg, err := s.videoReplicationTextConfig(userID, false)
		if err != nil {
			return nil, nil, err
		}
		saved.PlanningModel = planningCfg.TextModel
		return s.planVideoReplicationAuto(ctx, planningCfg, project, params, assets, saved, progress)
	}
	visionCfg, err := s.videoReplicationTextConfig(userID, true)
	if err != nil {
		return nil, nil, err
	}
	if err := validateVideoReplicationVisionProtocol(visionCfg); err != nil {
		return nil, nil, err
	}
	visionCfg.TextTimeout = 5 * time.Minute

	// 第二步：ffprobe/ffmpeg 解析每个参考视频。
	ffmpegBin, err := renderFfmpegBinary()
	if err != nil {
		return nil, nil, err
	}
	analysis := &VideoReplicationAnalysis{FFmpegSceneMode: fmt.Sprintf("ffmpeg scene>%.2f", videoReplicationSceneThreshold)}
	frames := []videoReplicationFrame{}
	fullVideos := make([]string, 0, len(videos))
	inlineMedia := make([]videoReplicationInlineMedia, 0, len(videos))
	identity := lease.Format("20060102150405")
	for videoIndex, video := range videos {
		progress(fmt.Sprintf("解析参考视频 %d/%d：镜头检测与关键帧", videoIndex+1, len(videos)), 8+videoIndex*20/len(videos))
		item := VideoReplicationVideoAnalysis{AssetID: video.ID, Name: video.Label, Primary: video.IsPrimary}
		err := s.withVideoReplicationResourceFile(userID, video.ResourceID, videoReplicationMaxVideoBytes, func(_ *model.Resource, path string) error {
			probe, err := probeVideoReplicationFile(ctx, path)
			if err != nil {
				return err
			}
			// 框选：在原视频（裁剪前）对应时间的画面上画红框，让识图模型说出要替换的目标。
			if videoIndex == 0 && len(params.Regions) > 0 {
				progress("识别你框选的替换目标", 6)
				if videoReplicationProductLocked(params) && (probe.DurationMs <= 0 || params.RegionFrameMs >= probe.DurationMs || (videoReplicationTrimmed(params) && (params.RegionFrameMs < params.TrimStartMs || params.RegionFrameMs >= params.TrimEndMs))) {
					return BadAuthRequest("商品框选画面超出了有效视频区间，请重新框选")
				}
				atMs := min(params.RegionFrameMs, max(0, probe.DurationMs-100))
				if videoReplicationProductLocked(params) {
					atMs = params.RegionFrameMs
				}
				marked := filepath.Join(filepath.Dir(path), "regions.jpg")
				if err := drawVideoReplicationRegions(ctx, ffmpegBin, path, atMs, params.Regions, marked); err != nil {
					return err
				}
				if saved, err := s.saveVideoReplicationFile(userID, marked, video.Label+"-框选目标.jpg", "image", 0, 0, 0, "video-replication-regions:"+video.ID+":"+identity); err == nil {
					analysis.RegionFrameResourceID = saved.ID
				} else if videoReplicationProductLocked(params) {
					return fmt.Errorf("保存商品框选定位图失败：%w", err)
				}
				analysis.RegionTarget, err = s.describeVideoReplicationRegions(ctx, visionCfg, projectID, marked, params)
				if err != nil {
					return err
				}
			}
			// 裁剪：后续镜头检测、视频理解、音轨与分段都只用选定的这一段。
			if videoIndex == 0 && videoReplicationTrimmed(params) {
				progress("按你选的区间裁剪参考视频", 7)
				end := params.TrimEndMs
				if probe.DurationMs > 0 && end > probe.DurationMs {
					end = probe.DurationMs
				}
				if end-params.TrimStartMs < videoReplicationMinDurationMs-videoReplicationDurationSlackMs {
					return BadAuthRequest("裁剪后的参考视频不足 4 秒，请重新裁剪")
				}
				trimmed, err := trimVideoReplicationFile(ctx, ffmpegBin, path, params.TrimStartMs, end)
				if err != nil {
					return err
				}
				path = trimmed
				if probe, err = probeVideoReplicationFile(ctx, path); err != nil {
					return err
				}
			}
			if err := validateVideoReplicationDurationLimit(probe.DurationMs, videoReplicationMaxReferenceSeconds(setting)); err != nil {
				return err
			}
			item.DurationMs, item.Width, item.Height, item.FPS, item.HasAudio = probe.DurationMs, probe.Width, probe.Height, probe.FPS, probe.HasAudio
			progress(fmt.Sprintf("准备完整视频与音轨 %d/%d", videoIndex+1, len(videos)), 10+videoIndex*20/len(videos))
			if videoReplicationUsesInlineMedia(visionCfg) {
				// 只收公网视频链接的渠道：境外回源下载国内存储会超时，改为内嵌画面与音轨。
				media, err := prepareVideoReplicationInlineMedia(ctx, ffmpegBin, path, probe, videoReplicationInlineMaxFrames/len(videos))
				if err != nil {
					return err
				}
				inlineMedia = append(inlineMedia, media)
			} else {
				fullVideo, err := prepareVideoReplicationVisionVideo(ctx, ffmpegBin, path, probe, videoReplicationVisionTotalBytes/int64(len(videos)))
				if err != nil {
					return err
				}
				fullVideos = append(fullVideos, fullVideo)
			}
			cuts, err := detectVideoReplicationCuts(ctx, ffmpegBin, path)
			if err != nil {
				return err
			}
			item.CutCount = len(cuts)
			spans := videoReplicationSpans(cuts, probe.DurationMs)
			frameBudget := 2
			if videoIndex == 0 {
				frameBudget = videoReplicationVisionFrameLimit - 2*(len(videos)-1)
			}
			for shotIndex, span := range spans {
				shot := VideoReplicationSourceShot{Index: shotIndex, StartMs: span.StartMs, EndMs: span.EndMs}
				output := filepath.Join(filepath.Dir(path), fmt.Sprintf("frame-%02d.jpg", shotIndex))
				if err := extractVideoReplicationFrame(ctx, ffmpegBin, path, (span.StartMs+span.EndMs)/2, output); err != nil {
					return err
				}
				saved, err := s.saveVideoReplicationFile(userID, output, fmt.Sprintf("%s-镜头%02d.jpg", video.Label, shotIndex+1), "image", 0, 0, 0, "video-replication-frame:"+video.ID+":"+identity+":"+fmt.Sprint(shotIndex))
				if err != nil {
					return err
				}
				shot.KeyframeResourceID = saved.ID
				if shotIndex < frameBudget || shotIndex == len(spans)-1 && len(frames) < videoReplicationVisionFrameLimit {
					data, err := os.ReadFile(output)
					if err != nil {
						return err
					}
					frames = append(frames, videoReplicationFrame{Ref: fmt.Sprintf("V%d-S%d", videoIndex+1, shotIndex+1), DataURL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)})
				}
				item.Shots = append(item.Shots, shot)
			}
			if probe.HasAudio {
				audioPath := filepath.Join(filepath.Dir(path), "audio.m4a")
				if err := extractVideoReplicationAudio(ctx, ffmpegBin, path, audioPath); err == nil {
					if saved, err := s.saveVideoReplicationFile(userID, audioPath, video.Label+"-音轨.m4a", "audio", 0, 0, probe.DurationMs, "video-replication-audio:"+video.ID+":"+identity); err == nil {
						item.AudioResourceID = saved.ID
					}
				}
			}
			// 一键生成默认不做语音识别：成片沿用参考视频原音轨或静音时用不到台词。
			// 先分析再生成（ConfirmFirst）要给出台词时间轴与声音处理建议；替换音频要让人物用参考音色说原台词，都需要识别。
			if videoIndex == 0 && (videoReplicationNeedsTranscript(params) || videoReplicationAdaptVoice(project.VideoType, params, project.Prompt)) {
				progress("提取口播与旁白（ASR）", 30)
				transcriber, unavailable := s.videoReplicationTranscriberFor(userID)
				analysis.Voice = transcribeVideoReplicationAudio(ctx, path, probe, transcriber, unavailable)
			}
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("%s 解析失败：%w", video.Label, err)
		}
		analysis.Videos = append(analysis.Videos, item)
	}
	if len(frames) > videoReplicationVisionFrameLimit {
		frames = frames[:videoReplicationVisionFrameLimit]
	}

	// 第三步：将完整视频（包含原有音轨）交给视频理解模型；关键帧继续用于界面预览。
	progress("AI 理解完整视频、动作、声音与字幕", 35)
	analysis.VisionModel = visionCfg.TextModel
	if err := s.understandVideoReplicationShots(ctx, visionCfg, projectID, analysis, frames, inlineMedia, fullVideos); err != nil {
		return nil, nil, fmt.Errorf("视频理解失败：%w", err)
	}
	// 第四步：商品与模特档案。
	if len(products) > 0 || len(models) > 0 {
		progress("识别上传的商品、服饰与人物形象", 58)
		if err := s.profileVideoReplicationAssets(ctx, visionCfg, userID, projectID, analysis, products, models, params.ProductName); err != nil {
			return nil, nil, fmt.Errorf("商品/模特识别失败：%w", err)
		}
	}

	// 第五步：策划模型重构原创分镜。
	progress("AI 重构原创分镜", 75)
	planningCfg, err := s.videoReplicationTextConfig(userID, false)
	if err != nil {
		return nil, nil, err
	}
	analysis.PlanningModel = planningCfg.TextModel
	analysis.MediaDigest = videoReplicationAnalysisDigest(assets, params)
	if params.Auto {
		return s.planVideoReplicationAuto(ctx, planningCfg, project, params, assets, analysis, progress)
	}
	storyboard, err := s.planVideoReplicationStoryboard(ctx, planningCfg, project, params, assets, analysis)
	if err != nil {
		return nil, nil, fmt.Errorf("分镜重构失败：%w", err)
	}
	progress("Agent 推荐说话人音色", 92)
	analysis.voiceCast = s.recommendVideoReplicationVoices(ctx, planningCfg, project, assets, analysis, storyboard)
	analysis.AnalyzedAt = time.Now()
	return analysis, storyboard, nil
}

func (s *Service) understandVideoReplicationShots(ctx context.Context, cfg ecommerceProviderConfig, projectID string, analysis *VideoReplicationAnalysis, frames []videoReplicationFrame, inlineMedia []videoReplicationInlineMedia, fullVideos []string) error {
	type timing struct {
		Ref      string  `json:"ref"`
		Video    string  `json:"video"`
		Primary  bool    `json:"primary"`
		StartSec float64 `json:"startSec"`
		EndSec   float64 `json:"endSec"`
		HasImage bool    `json:"hasImage"`
	}
	withImage := map[string]bool{}
	imageOrder := make([]string, 0, len(frames))
	for _, frame := range frames {
		withImage[frame.Ref] = true
		imageOrder = append(imageOrder, frame.Ref)
	}
	timings := []timing{}
	for videoIndex, video := range analysis.Videos {
		for _, shot := range video.Shots {
			ref := fmt.Sprintf("V%d-S%d", videoIndex+1, shot.Index+1)
			timings = append(timings, timing{Ref: ref, Video: video.Name, Primary: video.Primary, StartSec: math.Round(float64(shot.StartMs)/100) / 10, EndSec: math.Round(float64(shot.EndMs)/100) / 10, HasImage: withImage[ref]})
		}
	}
	timingJSON, _ := json.Marshal(timings)
	transcript := "没有可用的语音识别结果"
	if analysis.Voice != nil {
		switch {
		case !analysis.Voice.HasAudio:
			transcript = "参考视频没有音轨"
		case analysis.Voice.ASRAvailable && len(analysis.Voice.Segments) > 0:
			parts := []string{}
			for _, segment := range analysis.Voice.Segments {
				parts = append(parts, fmt.Sprintf("[%.1f-%.1fs]%s", float64(segment.StartMs)/1000, float64(segment.EndMs)/1000, strings.TrimSpace(segment.Text)))
			}
			transcript = clipText(strings.Join(parts, " "), 3000)
		case analysis.Voice.ASRAvailable:
			transcript = "语音识别未发现人声台词"
		}
	}
	system := "你是资深广告片导演与分镜师。请实际观看提供的完整视频并聆听音轨，按时间顺序分析动作、运镜、场景、字幕和声音，输出严格 JSON。不识别或猜测真实人物身份。不确定的细节明确写无法判断。"
	system += videoReplicationDeliveryAnalysisRule
	prompt := fmt.Sprintf(`附件按 V1、V2 顺序提供完整参考视频（保留原有音轨），请完整理解时间线。以下是 ffmpeg 检测得到的镜头时间，仅用于对齐 ref；界面预览帧索引为 %s，预览帧不代替视频输入。
镜头列表：%s
主参考视频语音识别（带时间戳）：%s
请输出 JSON：
{"overall":{"scenes":"场景数量与概括","people":"出镜人物数量","products":"出现的商品数量","voiceType":"on_camera(人物对镜口播)/narration(画外旁白)/dialogue(多人对话)/music(纯背景音乐)/silent(无声)/unknown 之一，结合语音文本与画面中人物是否对镜说话判断","speakerCount":"说话人数推断","music":"背景音乐判断","soundEffects":"音效判断","marketing":"营销结构（如 痛点-卖点-证明-行动号召）","speakers":[{"id":"S1","label":"说话人称呼，如 出镜女主播 / 画外旁白","gender":"男/女/不确定","age":"年龄段","tone":"语气语速","onCamera":true}]},
"summary":{"structure":"整体镜头结构与叙事顺序","rhythm":"节奏（慢/中/快及依据）","editingPace":"剪辑节奏与平均镜头时长","style":"视觉风格","mood":"氛围","hook":"开场钩子","subtitles":"字幕形式与内容概括，没有则写无","voiceover":"口播/旁白推断，无法判断写无法判断"},
"shots":[{"ref":"V1-S1","shotSize":"景别","camera":"运镜","scene":"场景","action":"主体动作","personAction":"人物动作与表情神态（如微笑、眨眼、看向镜头、开口说话），无人物写无","productDisplay":"商品展示方式","lighting":"光线","composition":"构图","subtitle":"画面字幕原文，没有写空串","voiceover":"这个镜头时间段内音轨里人物说出的台词原文（逐字听写，不改写、不总结；背景音乐歌词不算），没有人声写空串"}],
"recommendation":{"camera":"推荐运镜（一句话）","rhythm":"慢节奏/正常/快速/跟随参考视频 之一","lighting":"自然光/柔光/电影光/商业棚拍 之一或组合","mood":"推荐氛围（一句话）"}}
shots 必须包含所有镜头，按视频时间线实际观察动作、运镜、声音与字幕，不要从静态图片猜测。`, strings.Join(imageOrder, "、"), string(timingJSON), transcript)
	prompt += `
在 summary 中增加 delivery：{"mode":"livestream/review/explanation/unknown 之一","pace":"实际听到的快慢变化","pauses":"停顿、换气与展示时机","emphasis":"重音落点与语调变化，不抄旧优惠词","interaction":"观众互动与表情手势的配合","evidence":"原视频时间区间和可听见的判断依据"}；每个 shots 项增加 speechDelivery，描述该镜头内的局部声音表达节奏，无人声留空。`
	var result struct {
		Summary        VideoReplicationSummary `json:"summary"`
		Shots          []map[string]any        `json:"shots"`
		Recommendation VideoReplicationRecommendation
	}
	var raw map[string]any
	if len(fullVideos) == 0 && len(inlineMedia) == 0 {
		return fmt.Errorf("完整视频未传入，无法执行视频理解")
	}
	call := func() error {
		if len(inlineMedia) > 0 {
			inlinePrompt := "附件不是视频文件，而是每个参考视频按时间顺序均匀抽取的画面（每张标注了所在秒数，约 1 帧/秒）和该视频的完整音轨；请把它们当作完整视频来理解动作、运镜、节奏与声音。\n" + prompt
			return s.videoReplicationInlineChatJSON(ctx, cfg, "vision", system, inlinePrompt, inlineMedia, &raw)
		}
		return s.videoReplicationChatJSON(ctx, cfg, "vision", projectID, system, prompt, nil, fullVideos, &raw)
	}
	err := call()
	// 上游超时、繁忙、抓取失败这类偶发错误自动重试一次（失败的那次没有用量，不计成本）。
	if videoReplicationRetryable(err) && ctx.Err() == nil {
		raw = nil
		err = call()
	}
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(raw)
	_ = json.Unmarshal(encoded, &result)
	if recommendation, ok := raw["recommendation"].(map[string]any); ok {
		result.Recommendation = VideoReplicationRecommendation{Camera: stringValue(recommendation["camera"]), Rhythm: stringValue(recommendation["rhythm"]), Lighting: stringValue(recommendation["lighting"]), Mood: stringValue(recommendation["mood"])}
	}
	if len(result.Shots) == 0 {
		return fmt.Errorf("模型未返回镜头拆解")
	}
	byRef := map[string]map[string]any{}
	for _, shot := range result.Shots {
		byRef[strings.ToUpper(strings.TrimSpace(stringValue(shot["ref"])))] = shot
	}
	for videoIndex := range analysis.Videos {
		for shotIndex := range analysis.Videos[videoIndex].Shots {
			shot := &analysis.Videos[videoIndex].Shots[shotIndex]
			detail := byRef[fmt.Sprintf("V%d-S%d", videoIndex+1, shot.Index+1)]
			if detail == nil {
				continue
			}
			shot.ShotSize, shot.Camera, shot.Scene, shot.Action = stringValue(detail["shotSize"]), stringValue(detail["camera"]), stringValue(detail["scene"]), stringValue(detail["action"])
			shot.PersonAction, shot.ProductDisplay, shot.Lighting, shot.Composition = stringValue(detail["personAction"]), stringValue(detail["productDisplay"]), stringValue(detail["lighting"]), stringValue(detail["composition"])
			shot.Subtitle, shot.Voiceover = stringValue(detail["subtitle"]), stringValue(detail["voiceover"])
			shot.SpeechDelivery = videoReplicationClipRunes(strings.TrimSpace(stringValue(detail["speechDelivery"])), 300)
		}
	}
	analysis.Summary = result.Summary
	analysis.Recommendation = result.Recommendation
	shotCount := 0
	for _, video := range analysis.Videos {
		shotCount += len(video.Shots)
	}
	analysis.Counts.Shots = shotCount
	if overall, ok := raw["overall"].(map[string]any); ok {
		analysis.Counts.Scenes, analysis.Counts.People, analysis.Counts.Products = stringValue(overall["scenes"]), stringValue(overall["people"]), stringValue(overall["products"])
		analysis.Marketing = stringValue(overall["marketing"])
		if analysis.Voice == nil {
			analysis.Voice = &VideoReplicationVoice{Type: "unknown"}
		}
		analysis.Voice.Music, analysis.Voice.SoundEffects, analysis.Voice.SpeakerCount = stringValue(overall["music"]), stringValue(overall["soundEffects"]), stringValue(overall["speakerCount"])
		if speakers, ok := overall["speakers"].([]any); ok {
			for index, item := range speakers {
				speaker, _ := item.(map[string]any)
				if speaker == nil || index >= 8 {
					continue
				}
				onCamera, _ := speaker["onCamera"].(bool)
				analysis.Voice.Speakers = append(analysis.Voice.Speakers, VideoReplicationSpeaker{ID: clipText(firstNonEmpty(stringValue(speaker["id"]), fmt.Sprintf("S%d", index+1)), 24), Label: clipText(stringValue(speaker["label"]), 30), Gender: clipText(stringValue(speaker["gender"]), 8), Age: clipText(stringValue(speaker["age"]), 16), Tone: clipText(stringValue(speaker["tone"]), 40), OnCamera: onCamera})
			}
		}
		// 没有音轨或识别不到人声时以真实探测为准，不采纳模型对说话类型的猜测。
		if analysis.Voice.HasAudio && analysis.Voice.Type != "music" {
			switch kind := strings.TrimSpace(stringValue(overall["voiceType"])); kind {
			case "on_camera", "narration", "dialogue", "music", "silent":
				analysis.Voice.Type = kind
			}
		}
	}
	if analysis.Voice != nil {
		analysis.Voice.TypeLabel = videoReplicationVoiceLabel(analysis.Voice.Type)
		analysis.Voice.LipSyncNeeded = analysis.Voice.Type == "on_camera" || analysis.Voice.Type == "dialogue"
	}
	if len(analysis.Videos) > 0 {
		primary := analysis.Videos[0]
		analysis.Recommendation.Seconds = int(math.Round(float64(primary.DurationMs) / 1000))
		analysis.Recommendation.Ratio = videoReplicationRatioName(primary.Width, primary.Height)
	}
	return nil
}

func videoReplicationRatioName(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	value := float64(width) / float64(height)
	best, bestDiff := "", math.MaxFloat64
	for name, ratio := range map[string]float64{"9:16": 9.0 / 16, "3:4": 0.75, "1:1": 1, "4:3": 4.0 / 3, "16:9": 16.0 / 9} {
		if diff := math.Abs(value - ratio); diff < bestDiff {
			best, bestDiff = name, diff
		}
	}
	return best
}

func (s *Service) profileVideoReplicationAssets(ctx context.Context, cfg ecommerceProviderConfig, userID, projectID string, analysis *VideoReplicationAnalysis, products, models []VideoReplicationAssetView, productName string) error {
	images := []string{}
	labels := []string{}
	add := func(items []VideoReplicationAssetView) error {
		for index, item := range items {
			if index >= videoReplicationProfileImages {
				break
			}
			dataURL, err := s.videoReplicationImageDataURL(ctx, userID, item.ResourceID)
			if err != nil {
				return fmt.Errorf("%s：%w", item.Label, err)
			}
			images = append(images, dataURL)
			labels = append(labels, item.Label)
		}
		return nil
	}
	if err := add(products); err != nil {
		return err
	}
	if err := add(models); err != nil {
		return err
	}
	system := "你是电商视觉策划。只描述图片中可见的外观信息，输出严格 JSON。不识别、不猜测真实人物身份、姓名、年龄或民族。"
	prompt := fmt.Sprintf(`附带图片依次是：%s。商品图片是同一商品的不同角度/细节/场景；服饰图片是要换穿的服装；模特图片与人脸照片是同一人物形象的参考。
请输出 JSON：
{"product":{"category":"品类","appearance":"整体外观","structure":"结构与形态","colors":"主色与配色","material":"材质质感","details":"必须保留的细节（logo 位置、纹理、按钮、包装文字区域等）","sellingPoints":["可视化卖点"],"deliveryTone":"根据这件新商品选择自然的口语情绪、重音与停顿，例如食品的品尝分享或服饰的试穿讲解；这是表演建议，不推断功效或成分"},
"model":{"appearance":"外观气质","hair":"发型发色","outfit":"服装","framing":"照片中呈现的身体范围（正面/侧面/半身/全身）","consistency":"生成时需保持一致的要点"}}
没有商品图片时 product 为 null；没有模特图片时 model 为 null。`, strings.Join(labels, "、"))
	if productName != "" && len(products) > 0 {
		prompt += "\n" + videoReplicationProductNameRule(productName)
	}
	var result struct {
		Product map[string]any `json:"product"`
		Model   map[string]any `json:"model"`
	}
	if err := s.videoReplicationChatJSON(ctx, cfg, "profile", projectID, system, prompt, images, nil, &result); err != nil {
		return err
	}
	if len(products) > 0 {
		if productName != "" {
			if result.Product == nil {
				result.Product = map[string]any{}
			}
			result.Product["name"] = productName
		}
		analysis.Product = result.Product
		if raw, err := json.Marshal(result.Product); err == nil {
			_ = s.repo.UpdateVideoReplicationAssetProfile(products[0].ID, string(raw))
		}
	}
	if len(models) > 0 {
		analysis.Model = result.Model
		if raw, err := json.Marshal(result.Model); err == nil {
			_ = s.repo.UpdateVideoReplicationAssetProfile(models[0].ID, string(raw))
		}
	}
	return nil
}

func videoReplicationTypeBrief(videoType string) string {
	briefs := map[string]string{
		VideoReplicationTypeProduct:    "替换商品：以用户商品图片为参考替换原视频商品，保持商品外观、结构和颜色。",
		VideoReplicationTypeModel:      "替换模特：以用户模特图片为人物参考，把原视频人物替换为用户模特，保持外观、发型与身份一致。",
		VideoReplicationTypeApparel:    "替换服饰：以用户服饰图片为参考，人物换穿该服装，保持款式、颜色与细节，保留合理的人物动作和场景。",
		VideoReplicationTypeFace:       "替换人脸：以用户已授权的人脸照片作为人物身份参考，尽量保持面部一致。",
		VideoReplicationTypeBackground: "换背景：保留原视频的人物、商品、动作与运镜，只把场景与背景换成用户背景图中的环境，光线与透视要和新背景匹配。",
	}
	parts := []string{}
	for _, target := range videoReplicationTargets(videoType) {
		parts = append(parts, briefs[target])
	}
	if len(parts) == 0 {
		return "不替换商品或人物：保留参考视频的镜头结构与节奏，按用户的创作描述重新生成。"
	}
	return strings.Join(parts, " ")
}

func (s *Service) planVideoReplicationStoryboard(ctx context.Context, cfg ecommerceProviderConfig, project *model.VideoReplicationProject, params VideoReplicationParameters, assets []model.VideoReplicationAsset, analysis *VideoReplicationAnalysis) (*VideoReplicationStoryboard, error) {
	target := params.TargetSeconds
	if target <= 0 && len(analysis.Videos) > 0 {
		target = int(math.Round(float64(analysis.Videos[0].DurationMs) / 1000))
	}
	if target <= 0 {
		target = 10
	}
	brief := map[string]any{
		"referenceAnalysis": analysis.Videos, "summary": analysis.Summary, "product": analysis.Product, "productName": params.ProductName, "model": analysis.Model, "voice": analysis.Voice,
		"videoType": videoReplicationTypeBrief(project.VideoType), "userBrief": videoReplicationReadablePrompt(project.Prompt, assets),
		"targetSeconds": target, "allowedShotSeconds": params.ShotSeconds, "ratio": params.Ratio,
		"preferences": map[string]string{"camera": params.Camera, "rhythm": params.Rhythm, "lighting": params.Lighting, "mood": params.Mood},
	}
	contextJSON, _ := json.Marshal(brief)
	knowledge := s.videoReplicationKnowledgeFor(params)
	system := "你是商业广告导演。根据参考视频的镜头结构与节奏，为用户的商品重新设计一条原创广告分镜。只借鉴镜头语言（景别、运镜、节奏、构图、光线），不得照搬参考视频中的品牌、文案、人物身份或受版权保护的内容。输出严格 JSON。" + videoReplicationKnowledgeSystem(knowledge)
	prompt := fmt.Sprintf(`创作上下文（JSON）：%s
要求：
1. 主参考视频（primary=true）决定镜头结构与节奏，其他参考视频只作辅助参考。
2. 镜头数 2-%d 个，durationSec 必须取 allowedShotSeconds 中的值（为空时取 4-8 的整数），总时长尽量接近 targetSeconds。
3. 每个镜头的 prompt 是交给图生视频模型的中文画面描述（80-200 字）：写清主体、商品外观要点、动作、景别、运镜、光线、背景与结尾画面；商品外观必须与商品档案一致；有模特档案时描述人物需与模特一致。
4. 用户要求 userBrief 中的 @素材名 指用户上传的素材，必须遵守。
5. subtitle 是可选的短字幕（不超过 16 字）。
6. 参考视频有口播/旁白时（见 referenceAnalysis 与 voice），为对应镜头写 voiceover：按用户商品改写成原创台词，长度适合镜头时长（约每秒 4 字），speaker 填 voice.speakers 中的说话人 id；参考视频无人声且用户没有要求配音时 voiceover 留空。
输出 JSON：{"notes":"整体创意说明（一句话）","shots":[{"title":"镜头标题","durationSec":3,"shotSize":"景别","camera":"运镜","scene":"场景","action":"动作","productFocus":"商品展示重点","lighting":"光线","mood":"氛围","subtitle":"","voiceover":"","speaker":"S1","sourceShot":"借鉴的参考镜头 ref，如 V1-S2","prompt":"画面描述"}]}`, string(contextJSON), videoReplicationMaxShotsOut)
	var storyboard VideoReplicationStoryboard
	if err := s.videoReplicationChatJSON(ctx, cfg, "planning", project.ID, system, prompt, nil, nil, &storyboard); err != nil {
		return nil, err
	}
	if knowledge != nil {
		storyboard.Knowledge, storyboard.KnowledgeID = knowledge.Name, knowledge.ID
	}
	normalized, err := normalizeVideoReplicationStoryboard(storyboard, params.ShotSeconds)
	if err != nil {
		return nil, fmt.Errorf("模型返回的分镜不可用：%w", err)
	}
	return &normalized, nil
}
