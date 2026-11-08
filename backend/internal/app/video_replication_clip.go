package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

// 电商 Agent 商品视频的「裁剪视频」与「框选替换目标」。
// 裁剪：只记录参考视频的起止时间，分析时用 ffmpeg 截出这一段再做镜头检测、理解与分段，原始素材不变。
// 框选：用户在参考视频某一帧上框出要替换的商品 / 人物；分析时把这一帧画上红框交给识图模型，
// 识别出框里的东西后写进每段画面提示词，只替换框里的目标，框外的保持原样。

const (
	// videoReplicationMaxSourceMs 是可以导入后再裁剪的参考视频原始时长上限；裁剪后的片段仍受 4 秒–后台上限约束。
	videoReplicationMaxSourceMs int64 = 10 * 60 * 1000
	// videoReplicationMaxRegions：框几件换几件，一次最多框 6 个目标。
	videoReplicationMaxRegions = 6
)

// VideoReplicationRegion 是参考视频画面上的一个框，坐标按画面宽高归一化到 0–1。
type VideoReplicationRegion struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

func validateVideoReplicationClip(params *VideoReplicationParameters) error {
	if params.TrimStartMs < 0 || params.TrimEndMs < 0 || params.TrimStartMs > videoReplicationMaxSourceMs || params.TrimEndMs > videoReplicationMaxSourceMs {
		return BadAuthRequest("裁剪时间无效")
	}
	if params.TrimEndMs > 0 && params.TrimEndMs-params.TrimStartMs < videoReplicationMinDurationMs-videoReplicationDurationSlackMs {
		return BadAuthRequest("裁剪后的视频至少 4 秒")
	}
	if params.TrimEndMs == 0 && params.TrimStartMs > 0 {
		return BadAuthRequest("裁剪时间无效")
	}
	if len(params.Regions) > videoReplicationMaxRegions {
		return BadAuthRequest(fmt.Sprintf("最多框选 %d 个目标", videoReplicationMaxRegions))
	}
	for index := range params.Regions {
		region := &params.Regions[index]
		for _, value := range []float64{region.X, region.Y, region.W, region.H} {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return BadAuthRequest("框选区域无效")
			}
		}
		region.X, region.Y = math.Max(0, math.Min(1, region.X)), math.Max(0, math.Min(1, region.Y))
		region.W, region.H = math.Min(1-region.X, region.W), math.Min(1-region.Y, region.H)
		if region.W < 0.02 || region.H < 0.02 {
			return BadAuthRequest("框选区域太小，请框住完整的目标")
		}
	}
	if params.RegionFrameMs < 0 || params.RegionFrameMs > videoReplicationMaxSourceMs {
		return BadAuthRequest("框选画面时间无效")
	}
	if videoReplicationProductLocked(*params) && videoReplicationTrimmed(*params) && (params.RegionFrameMs < params.TrimStartMs || params.RegionFrameMs >= params.TrimEndMs) {
		return BadAuthRequest("商品框选画面不在裁剪区间内，请在保留的视频片段里重新框选")
	}
	if len(params.Regions) == 0 {
		params.RegionFrameMs = 0
	}
	params.RegionTarget = ""
	return nil
}

// videoReplicationTrimmed 返回裁剪区间是否生效（相对原视频，单位毫秒）。
func videoReplicationTrimmed(params VideoReplicationParameters) bool {
	return params.TrimEndMs > params.TrimStartMs
}

// videoReplicationEffectiveDurationMs 是参与复刻的参考视频时长：裁剪后取裁剪区间，否则取原视频时长。
func videoReplicationEffectiveDurationMs(params VideoReplicationParameters, sourceMs int64) int64 {
	if !videoReplicationTrimmed(params) {
		return sourceMs
	}
	end := params.TrimEndMs
	if sourceMs > 0 && end > sourceMs {
		end = sourceMs
	}
	return end - params.TrimStartMs
}

// videoReplicationClipKey 覆盖影响视频理解结果的裁剪与框选；变化时不能复用已保存的分析。
func videoReplicationClipKey(params VideoReplicationParameters) string {
	parts := []string{fmt.Sprintf("trim:%d-%d", params.TrimStartMs, params.TrimEndMs)}
	if len(params.Regions) > 0 {
		if videoReplicationProductLocked(params) {
			parts = append(parts, "product-lock:v2")
		}
		parts = append(parts, fmt.Sprintf("frame:%d", params.RegionFrameMs))
		for _, region := range params.Regions {
			parts = append(parts, fmt.Sprintf("%.3f,%.3f,%.3f,%.3f", region.X, region.Y, region.W, region.H))
		}
	}
	return strings.Join(parts, "|")
}

// trimVideoReplicationFile 把参考视频的 [start, end) 截成新文件（重新编码，切点精确到帧，保留音轨）。
func trimVideoReplicationFile(ctx context.Context, ffmpegBin, input string, startMs, endMs int64) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	output := filepath.Join(filepath.Dir(input), "trimmed.mp4")
	cmd := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", fmt.Sprintf("%.3f", float64(startMs)/1000), "-i", input, "-t", fmt.Sprintf("%.3f", float64(endMs-startMs)/1000),
		"-map", "0:v:0", "-map", "0:a:0?", "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", "-y", output)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("裁剪参考视频失败：%s", clipText(strings.TrimSpace(string(out)), 300))
	}
	return output, nil
}

// drawVideoReplicationRegions 抽取原视频 atMs 处的一帧并画上红框，供识图模型识别要替换的目标。
func drawVideoReplicationRegions(ctx context.Context, ffmpegBin, input string, atMs int64, regions []VideoReplicationRegion, output string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	filters := []string{"scale='if(gt(iw,ih),min(1024,iw),-2)':'if(gt(iw,ih),-2,min(1024,ih))'"}
	for _, region := range regions {
		filters = append(filters, fmt.Sprintf("drawbox=x=iw*%.4f:y=ih*%.4f:w=iw*%.4f:h=ih*%.4f:color=red@0.95:t=4", region.X, region.Y, region.W, region.H))
	}
	cmd := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-ss", fmt.Sprintf("%.3f", float64(atMs)/1000), "-i", input,
		"-frames:v", "1", "-vf", strings.Join(filters, ","), "-q:v", "3", "-y", output)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("抽取框选画面失败：%s", clipText(strings.TrimSpace(string(out)), 300))
	}
	return nil
}

// videoReplicationRegionPosition 用九宫格方位描述一个框，识别失败时也能告诉视频模型目标在画面哪里。
func videoReplicationRegionPosition(region VideoReplicationRegion) string {
	cx, cy := region.X+region.W/2, region.Y+region.H/2
	row := []string{"上部", "中部", "下部"}[min(2, int(cy*3))]
	col := []string{"左侧", "中间", "右侧"}[min(2, int(cx*3))]
	if col == "中间" && row == "中部" {
		return "画面正中"
	}
	return "画面" + row + col
}

// describeVideoReplicationRegions 让识图模型说出红框里的目标，返回写进提示词的一句话；frame 是画好红框的 JPEG。
func (s *Service) describeVideoReplicationRegions(ctx context.Context, cfg ecommerceProviderConfig, projectID, frame string, params VideoReplicationParameters) (string, error) {
	positions := make([]string, 0, len(params.Regions))
	for index, region := range params.Regions {
		position := fmt.Sprintf("框%d 在%s", index+1, videoReplicationRegionPosition(region))
		if videoReplicationProductLocked(params) {
			position += fmt.Sprintf("，归一化区域 x=%.3f y=%.3f w=%.3f h=%.3f", region.X, region.Y, region.W, region.H)
		}
		positions = append(positions, position)
	}
	fallback := fmt.Sprintf("用户在原视频画面上框选了 %d 个要替换的目标（%s）", len(params.Regions), strings.Join(positions, "，"))
	data, err := os.ReadFile(frame)
	if err != nil {
		if videoReplicationProductLocked(params) {
			return "", fmt.Errorf("读取商品框选定位图失败：%w", err)
		}
		return fallback, nil
	}
	person := params.ReplaceMode == "precise-person" || params.ReplaceMode == "model" || params.ReplaceMode == "face"
	kind := "商品 / 物品"
	if person {
		kind = "人物"
	}
	system := "你是电商视频分析助手。只描述图片中可见的外观信息，输出严格 JSON。不识别、不猜测真实人物身份、姓名、年龄或民族。"
	prompt := fmt.Sprintf(`图片是参考视频的一帧，红色方框是用户框出的要替换的%s，共 %d 个框（%s）。
请逐个说出每个红框里是什么，用 8-20 字的外观描述，便于在视频的其他画面里认出同一个目标（例如「左手拿着的粉色洗发水瓶」「穿棕色夹克的长发女生」）。
再用一句话说明框外有哪些相似的%s需要保持不变（没有则为空）。
输出 JSON：{"targets":["框1里的目标","框2里的目标"],"keep":"框外保持不变的相似目标"}`, kind, len(params.Regions), strings.Join(positions, "，"), kind)
	if videoReplicationProductLocked(params) {
		prompt += "\n每个框必须对应一个 targets 项，保持框的顺序，不能合并或漏掉；看不清或无法确定的框写空串，禁止猜测。"
	}
	var result videoReplicationRegionDescription
	images := []string{"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)}
	if err := s.videoReplicationChatJSON(ctx, cfg, "region", projectID, system, prompt, images, nil, &result); err != nil {
		if videoReplicationProductLocked(params) {
			return "", fmt.Errorf("商品框选识别失败，请重试或选择目标清晰的一帧：%w", err)
		}
		return fallback, nil
	}
	return videoReplicationDescribeRegionsResult(params, result, positions, fallback)
}

type videoReplicationRegionDescription struct {
	Targets []string `json:"targets"`
	Keep    string   `json:"keep"`
}

func videoReplicationDescribeRegionsResult(params VideoReplicationParameters, result videoReplicationRegionDescription, positions []string, fallback string) (string, error) {
	strict := videoReplicationProductLocked(params)
	if strict && len(result.Targets) != len(params.Regions) {
		return "", BadAuthRequest("没有完整识别全部框选商品，请选择目标清晰的一帧重新框选")
	}
	targets := []string{}
	for index, target := range result.Targets {
		if index >= len(params.Regions) {
			break
		}
		target = strings.TrimSpace(target)
		if strict && (target == "" || strings.Contains(target, "://") || videoReplicationNoneText(target) || strings.Contains(target, "无法识别") || strings.Contains(target, "无法确定") || strings.Contains(target, "无法确认") || strings.Contains(target, "看不清") || strings.Contains(target, "不确定")) {
			return "", BadAuthRequest(fmt.Sprintf("无法确定第 %d 个框里的商品，请选择目标清晰的一帧重新框选", index+1))
		}
		if target != "" && !strings.Contains(target, "://") {
			targets = append(targets, fmt.Sprintf("「%s」（%s）", videoReplicationClipRunes(target, 40), positions[index]))
		}
	}
	if len(targets) == 0 {
		return fallback, nil
	}
	text := fmt.Sprintf("用户在原视频里框选了 %d 个要替换的目标：%s", len(targets), strings.Join(targets, "、"))
	if keep := strings.TrimSpace(result.Keep); keep != "" && utf8.RuneCountInString(keep) <= 80 && !strings.Contains(keep, "://") {
		text += "；" + keep + "保持原样"
	}
	return text, nil
}

// videoReplicationRegionRule 是写进每段画面提示词的框选约束。
func videoReplicationRegionRule(params VideoReplicationParameters) string {
	target := strings.TrimSpace(params.RegionTarget)
	if len(params.Regions) == 0 || target == "" {
		return ""
	}
	if params.ReplaceMode == "precise-person" || params.ReplaceMode == "model" || params.ReplaceMode == "face" {
		return "框选锁定：" + target + "。只把被框选的人物换成人物参考图中的人，画面里其他人物的长相、服装、动作和位置都保持原视频不变，不要换错人。"
	}
	return "框选锁定：" + target + "。框了几件就替换几件，每一件都换成商品参考图中的商品，数量、手持方式和摆放位置与原视频一致；没被框选的物品保持原样。"
}

// videoReplicationWithRegionTarget 从已保存的分析结果取出框选目标，供生成提示词使用。
func videoReplicationWithRegionTarget(project *model.VideoReplicationProject, params VideoReplicationParameters) VideoReplicationParameters {
	if len(params.Regions) == 0 {
		return params
	}
	if analysis := videoReplicationSavedAnalysis(project); analysis != nil {
		params.RegionTarget = analysis.RegionTarget
	}
	return params
}

// videoReplicationEffectiveDurationAllowed 在开始生成前按裁剪后的时长校验：4 秒到后台设定的参考视频上限。
func (s *Service) videoReplicationEffectiveDurationAllowed(userID, projectID string, params VideoReplicationParameters) error {
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return err
	}
	for _, asset := range assets {
		if asset.Category != model.VideoReplicationAssetReferenceVideo || !asset.IsPrimary {
			continue
		}
		if videoReplicationTrimmed(params) && asset.DurationMs > 0 && params.TrimStartMs >= asset.DurationMs-videoReplicationMinDurationMs+videoReplicationDurationSlackMs {
			return BadAuthRequest("裁剪起点超出了参考视频时长，请重新裁剪")
		}
		duration := videoReplicationEffectiveDurationMs(params, asset.DurationMs)
		if duration > 0 && duration < videoReplicationMinDurationMs-videoReplicationDurationSlackMs {
			return BadAuthRequest(fmt.Sprintf("参考视频片段 %.1f 秒，短于 4 秒", float64(duration)/1000))
		}
		limit := int(videoReplicationMaxDurationMs / 1000)
		if setting, err := s.videoReplicationSetting(); err == nil {
			limit = videoReplicationMaxReferenceSeconds(setting)
		}
		if duration > int64(limit)*1000+videoReplicationDurationSlackMs {
			return BadAuthRequest(fmt.Sprintf("参考视频片段 %.1f 秒，超过 %d 秒上限；请点「裁剪视频」截取其中一段", float64(duration)/1000, limit))
		}
		return nil
	}
	return nil
}

// videoReplicationAnalysisDigest 是视频理解结果的复用键：素材之外还要看裁剪区间与框选。
func videoReplicationAnalysisDigest(assets []model.VideoReplicationAsset, params VideoReplicationParameters) string {
	media := videoReplicationMediaDigest(assets) + "~speech-v1"
	key := videoReplicationClipKey(params)
	if params.ProductName != "" {
		return media + "~" + key + "~name:" + params.ProductName
	}
	if key == "trim:0-0" {
		return media
	}
	return media + "~" + key
}

// videoReplicationWithLipSyncPrompt：口播视频保留原声时，成片直接叠加原音轨，画面里的人物口型要对上原台词。
func videoReplicationWithLipSyncPrompt(prompt, lines string, budget videoReplicationPromptBudget) string {
	lines = strings.TrimSpace(lines)
	if lines == "" {
		return budget.clip(prompt)
	}
	suffix := "口型：成片会直接使用原视频的声音，人物口型与原台词「" + videoReplicationClipRunes(lines, 400) + "」同步，说话节奏与原视频一致；画面不要生成新的人声。"
	return budget.without(suffix).clip(prompt) + "\n" + suffix
}
