package app

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"yingce/backend/internal/model"
)

// 商品视频「自动选模型」：用户只上传商品 / 模特 / 背景，模型、分段与参考视频切分全部由后台按参考视频时长决定。
//
// 后台把开放的视频模型分成两条轨道：
//   - reference（参考视频复刻）：模型能接收参考视频，每段把原片片段作为编辑底片，只替换目标；
//   - script（脚本复刻）：模型不接收参考视频，按反推出的逐秒动作脚本 + 首帧 / 尾帧接力复刻结构。
//
// 每条轨道再标记是否「不卡人脸」（FaceSafe）。参考视频按模型的单段参考时长切成多段一起上传，
// 一次任务能覆盖的原片时长 = min(模型单次最长时长, 可传参考视频数 × 单段参考时长, 参考合计上限)。
// 排序默认「连贯优先」：任务数（拼接缝）越少越好，同样次数时按真实报价取最便宜；后台可改为「价格优先」。

const (
	VideoReplicationTrackReference = "reference"
	VideoReplicationTrackScript    = "script"

	VideoReplicationRouteContinuity = "continuity"
	VideoReplicationRouteCost       = "cost"
)

// videoReplicationRouteModel 是一个候选模型在路由时需要的全部能力，来自后台渠道能力配置与电商视频后台标记。
type videoReplicationRouteModel struct {
	Ref                EcommerceAgentModelRef
	Label              string
	Track              string
	FaceSafe           bool
	Durations          []int
	MaxImages          int
	Operations         []string
	MaxRefVideos       int
	MaxRefVideoSeconds int
	RefTotalSeconds    int
	GenerateAudio      bool
}

func (m videoReplicationRouteModel) maxSeconds() int {
	if len(m.Durations) == 0 {
		return 0
	}
	return m.Durations[len(m.Durations)-1]
}

func (m videoReplicationRouteModel) supports(operation string) bool {
	return len(m.Operations) == 0 || containsString(m.Operations, operation)
}

// referenceCapable：后台显式标为脚本复刻的不走参考视频；未标记时按能力配置判断。
func (m videoReplicationRouteModel) referenceCapable() bool {
	if m.Track == VideoReplicationTrackScript {
		return false
	}
	return m.MaxRefVideos > 0 && m.MaxRefVideoSeconds > 0 && m.MaxImages > 0 && m.supports("reference_to_video")
}

func (m videoReplicationRouteModel) track() string {
	if m.referenceCapable() {
		return VideoReplicationTrackReference
	}
	return VideoReplicationTrackScript
}

// referenceCoverageSeconds 是一次任务最多能带上的原片时长（多段参考视频首尾相接）。
func videoReplicationReferenceCoverageSeconds(maxVideos, perClipSeconds, totalSeconds int) int {
	if maxVideos <= 0 || perClipSeconds <= 0 {
		return 0
	}
	cover := maxVideos * perClipSeconds
	if totalSeconds > 0 {
		cover = min(cover, totalSeconds)
	}
	return cover
}

// taskSeconds 是这个模型一次任务的最长时长：参考视频轨道还受参考视频覆盖时长限制，否则后半段没有原片底片。
func (m videoReplicationRouteModel) taskSeconds() int {
	limit := m.maxSeconds()
	if m.track() == VideoReplicationTrackReference {
		if cover := videoReplicationReferenceCoverageSeconds(m.MaxRefVideos, m.MaxRefVideoSeconds, m.RefTotalSeconds); cover > 0 {
			limit = min(limit, cover)
		}
	}
	return limit
}

// videoReplicationClipParts 把一段 lengthMs 的原片按单段参考时长切成若干段（等分，尽量不留极短尾巴），
// 最多 maxClips 段，合计不超过 totalSeconds（>0 时）；超出覆盖范围的部分截掉。
func videoReplicationClipParts(lengthMs, perClipMs int64, maxClips, totalSeconds int) []int64 {
	if lengthMs <= 0 {
		return nil
	}
	if perClipMs <= 0 {
		return []int64{lengthMs}
	}
	maxClips = max(1, maxClips)
	cover := perClipMs * int64(maxClips)
	if totalSeconds > 0 {
		cover = min(cover, int64(totalSeconds)*1000)
	}
	lengthMs = min(lengthMs, cover)
	count := int((lengthMs + perClipMs - 1) / perClipMs)
	count = max(1, min(count, maxClips))
	parts := make([]int64, count)
	base := lengthMs / int64(count)
	for index := range parts {
		parts[index] = base
	}
	parts[count-1] += lengthMs - base*int64(count)
	return parts
}

// videoReplicationRouteTaskSeconds 估算整片按 taskSeconds 切分后每个任务的秒数（与服务端分段算法同口径：
// 只比单段上限多出不到 1 秒时裁尾成一段；尾巴不足 4 秒时与前一段均摊）。
func videoReplicationRouteTaskSeconds(totalMs int64, taskSeconds int, allowed []int) []int {
	if totalMs <= 0 || taskSeconds <= 0 {
		return nil
	}
	limitMs := int64(taskSeconds) * 1000
	if over := totalMs - limitMs; over > 0 && over <= 1000 {
		totalMs = limitMs
	}
	count := int((totalMs + limitMs - 1) / limitMs)
	totalSeconds := max(int(math.Round(float64(totalMs)/1000)), count*videoReplicationMinSegmentSeconds)
	seconds := make([]int, 0, count)
	for index := 0; index < count; index++ {
		value := totalSeconds / count
		if index < totalSeconds%count {
			value++
		}
		value = max(videoReplicationMinSegmentSeconds, min(value, taskSeconds))
		if len(allowed) > 0 {
			value = snapVideoReplicationSeconds(value, allowed)
		}
		seconds = append(seconds, value)
	}
	return seconds
}

// VideoReplicationRouteOption 是一个候选方案；Blocked 非空表示这个模型做不了这条视频。
type VideoReplicationRouteOption struct {
	Model                 EcommerceAgentModelRef `json:"model"`
	Label                 string                 `json:"label"`
	Track                 string                 `json:"track"`
	FaceSafe              bool                   `json:"faceSafe"`
	Tasks                 int                    `json:"tasks"`
	TaskSeconds           []int                  `json:"taskSeconds"`
	ClipsPerTask          int                    `json:"clipsPerTask"`
	ClipSeconds           int                    `json:"clipSeconds,omitempty"`
	EstimatedMicrocredits int64                  `json:"estimatedMicrocredits"`
	Priced                bool                   `json:"priced"`
	Blocked               string                 `json:"blocked,omitempty"`
}

type VideoReplicationRoute struct {
	SourceSeconds float64                       `json:"sourceSeconds"`
	Preference    string                        `json:"preference"`
	Recommended   *VideoReplicationRouteOption  `json:"recommended,omitempty"`
	Reason        string                        `json:"reason"`
	Options       []VideoReplicationRouteOption `json:"options"`
}

type videoReplicationPriceFunc func(candidate videoReplicationRouteModel, seconds int, clipMs []int64) (int64, bool)

// planVideoReplicationRouteOption 计算一个模型做这条视频的任务数、每段参考视频切分与总价。
func planVideoReplicationRouteOption(candidate videoReplicationRouteModel, sourceMs int64, price videoReplicationPriceFunc) VideoReplicationRouteOption {
	option := VideoReplicationRouteOption{Model: candidate.Ref, Label: candidate.Label, Track: candidate.track(), FaceSafe: candidate.FaceSafe}
	taskSeconds := candidate.taskSeconds()
	switch {
	case taskSeconds < videoReplicationMinSegmentSeconds:
		option.Blocked = "模型没有可用的单次时长（至少 4 秒）"
		return option
	case candidate.MaxImages <= 0 || !(candidate.supports("reference_to_video") || candidate.supports("image_to_video")):
		option.Blocked = "模型不能接收商品 / 人物参考图"
		return option
	}
	option.TaskSeconds = videoReplicationRouteTaskSeconds(sourceMs, taskSeconds, candidate.Durations)
	option.Tasks = len(option.TaskSeconds)
	if option.Tasks > videoReplicationMaxShotsOut {
		option.Blocked = fmt.Sprintf("需要切成 %d 段，超过上限 %d 段", option.Tasks, videoReplicationMaxShotsOut)
		return option
	}
	option.Priced = true
	for _, seconds := range option.TaskSeconds {
		var clips []int64
		if option.Track == VideoReplicationTrackReference {
			clips = videoReplicationClipParts(int64(seconds)*1000, int64(candidate.MaxRefVideoSeconds)*1000, candidate.MaxRefVideos, candidate.RefTotalSeconds)
			option.ClipsPerTask = max(option.ClipsPerTask, len(clips))
			option.ClipSeconds = candidate.MaxRefVideoSeconds
		}
		if price == nil {
			option.Priced = false
			continue
		}
		amount, ok := price(candidate, seconds, clips)
		if !ok {
			option.Priced = false
			continue
		}
		option.EstimatedMicrocredits += amount
	}
	return option
}

// rankVideoReplicationRoute 选出推荐方案：
//  1. 需要换人（模特 / 人脸 / 精准人物）且后台标了「不卡人脸」的模型时，只在这些模型里选；
//  2. 参考视频复刻优先于脚本复刻（结构一致性更高）；
//  3. continuity：任务数少的优先，再比价格；cost：价格低的优先，再比任务数。没有报价的排在有报价的后面。
func rankVideoReplicationRoute(options []VideoReplicationRouteOption, needsFaceSafe bool, preference string) (*VideoReplicationRouteOption, string) {
	usable := []VideoReplicationRouteOption{}
	for _, option := range options {
		if option.Blocked == "" {
			usable = append(usable, option)
		}
	}
	if len(usable) == 0 {
		return nil, "没有能完成这条视频的已开放模型"
	}
	reasons := []string{}
	if needsFaceSafe {
		safe := []VideoReplicationRouteOption{}
		for _, option := range usable {
			if option.FaceSafe {
				safe = append(safe, option)
			}
		}
		if len(safe) > 0 {
			usable = safe
			reasons = append(reasons, "需要换人，只在「不卡人脸」的模型里选")
		} else {
			reasons = append(reasons, "后台没有标记「不卡人脸」的可用模型，换人任务可能被上游拒绝")
		}
	}
	reference := []VideoReplicationRouteOption{}
	for _, option := range usable {
		if option.Track == VideoReplicationTrackReference {
			reference = append(reference, option)
		}
	}
	if len(reference) > 0 {
		usable = reference
		reasons = append(reasons, "参考视频复刻：原片切段作为编辑底片，只换上传的目标")
	} else {
		reasons = append(reasons, "没有可用的参考视频模型，按反推脚本逐秒复刻并首尾帧接力")
	}
	sort.SliceStable(usable, func(i, j int) bool {
		a, b := usable[i], usable[j]
		if a.Priced != b.Priced {
			return a.Priced
		}
		if preference == VideoReplicationRouteCost {
			if a.EstimatedMicrocredits != b.EstimatedMicrocredits {
				return a.EstimatedMicrocredits < b.EstimatedMicrocredits
			}
			return a.Tasks < b.Tasks
		}
		if a.Tasks != b.Tasks {
			return a.Tasks < b.Tasks
		}
		return a.EstimatedMicrocredits < b.EstimatedMicrocredits
	})
	best := usable[0]
	if best.Tasks == 1 {
		reasons = append(reasons, fmt.Sprintf("一次生成 %d 秒即可覆盖原片", best.TaskSeconds[0]))
	} else {
		reasons = append(reasons, fmt.Sprintf("按原片切点分 %d 段生成后自动合成", best.Tasks))
	}
	if best.ClipsPerTask > 1 {
		reasons = append(reasons, fmt.Sprintf("每段原片超过单个参考视频 %d 秒上限，自动切成 %d 个参考视频一起上传", best.ClipSeconds, best.ClipsPerTask))
	}
	return &best, strings.Join(reasons, "；")
}

func videoReplicationNeedsFaceSafe(videoType string, params VideoReplicationParameters) bool {
	return videoReplicationHasTarget(videoType, VideoReplicationTypeModel) || videoReplicationHasTarget(videoType, VideoReplicationTypeFace) || params.ReplaceMode == "precise-person"
}

func (setting VideoReplicationSetting) routePreference() string {
	if setting.RoutePreference == VideoReplicationRouteCost {
		return VideoReplicationRouteCost
	}
	return VideoReplicationRouteContinuity
}

// 已设名单即使全部失效也保持关闭；只有从未限定的空名单才使用当前用户的公开目录。
func (s *Service) videoReplicationRouteModels(userID string, setting VideoReplicationSetting) ([]VideoReplicationVideoModel, error) {
	if setting.RestrictVideoModels || len(setting.VideoModels) != 0 {
		return setting.VideoModels, nil
	}
	access, err := s.userGroupAccess(userID)
	if err != nil {
		return nil, err
	}
	logical, err := s.FeatureEnabled(FeatureFrontendModels)
	if err != nil {
		return nil, err
	}
	var items []VideoReplicationVideoModel
	if logical {
		models, err := s.PublicLogicalModels(access, nil)
		if err != nil {
			return nil, err
		}
		for _, m := range models {
			if m.Available && m.Capability == "video" {
				items = append(items, VideoReplicationVideoModel{EcommerceAgentModelRef: EcommerceAgentModelRef{LogicalModelID: m.ID}, Label: firstNonEmpty(m.Name, m.Code), Enabled: true})
			}
		}
		return items, nil
	}
	channels, err := s.publicSystemChannelCatalog(access, nil)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		for _, m := range channel.Models {
			if m.Available && m.Capability == "video" {
				ref := EcommerceAgentModelRef{ChannelID: channel.ID, ChannelModelID: m.ID, ModelKey: m.ModelKey}
				items = append(items, VideoReplicationVideoModel{EcommerceAgentModelRef: ref, Label: firstNonEmpty(m.DisplayName, m.ModelKey), Enabled: true})
			}
		}
	}
	return items, nil
}

// videoReplicationRouteCandidates 读取后台开放的全部视频模型及其渠道能力；解析不了的模型带着原因返回，便于后台排查。
func (s *Service) videoReplicationRouteCandidates(userID string, items []VideoReplicationVideoModel) ([]videoReplicationRouteModel, []VideoReplicationRouteOption) {
	candidates := []videoReplicationRouteModel{}
	broken := []VideoReplicationRouteOption{}
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		provider, err := s.resolveVideoReplicationProvider(userID, item.EcommerceAgentModelRef, "video")
		if err != nil {
			broken = append(broken, VideoReplicationRouteOption{Model: item.EcommerceAgentModelRef, Label: item.Label, Blocked: "模型不可用：" + err.Error()})
			continue
		}
		profile := s.videoReplicationModelProfile(provider)
		if profile == nil {
			broken = append(broken, VideoReplicationRouteOption{Model: item.EcommerceAgentModelRef, Label: item.Label, Blocked: "渠道没有配置视频能力"})
			continue
		}
		candidates = append(candidates, videoReplicationRouteModel{
			Ref: item.EcommerceAgentModelRef, Label: firstNonEmpty(item.Label, item.ModelKey), Track: item.Track, FaceSafe: item.FaceSafe,
			Durations: videoReplicationDurationValues(profile), MaxImages: profile.References.MaxImages, Operations: profile.Operations,
			MaxRefVideos: profile.References.MaxVideos, MaxRefVideoSeconds: profile.References.MaxVideoDuration, RefTotalSeconds: item.ReferenceTotalSeconds,
			GenerateAudio: item.GenerateAudio && profile.GenerateAudio.Supported,
		})
	}
	return candidates, broken
}

// quoteVideoReplicationTask 用计费系统的真实报价估一个任务的价格（含参考视频按秒计费的部分）。
func (s *Service) quoteVideoReplicationTask(userID string, candidate videoReplicationRouteModel, seconds int, clipMs []int64) (int64, bool) {
	operation := "image_to_video"
	inputs := map[string]int{"image": 1}
	if len(clipMs) > 0 {
		operation = "reference_to_video"
		inputs["video"] = len(clipMs)
	}
	intent := ModelRequestIntent{Capability: "video", Operation: operation, Inputs: inputs, ReferenceVideoDurationsMs: clipMs,
		Options: map[string]any{"videoSeconds": itoa(seconds), "videoGenerateAudio": fmt.Sprint(candidate.GenerateAudio)}}
	var quote *LogicalModelQuote
	var err error
	if candidate.Ref.LogicalModelID != "" {
		quote, err = s.QuoteLogicalModel(userID, candidate.Ref.LogicalModelID, intent)
	} else {
		quote, err = s.QuoteChannelModel(userID, ChannelModelQuoteRequest{ChannelID: candidate.Ref.ChannelID, ModelKey: candidate.Ref.ModelKey, Intent: intent})
	}
	if err != nil || quote == nil {
		return 0, false
	}
	return quote.AmountMicrocredits, true
}

// RouteVideoReplication 按项目主参考视频（裁剪后）的时长，为用户推荐模型、分段与参考视频切分方案。
func (s *Service) RouteVideoReplication(userID, projectID string) (*VideoReplicationRoute, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	params := videoReplicationParameters(project)
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return nil, err
	}
	sourceMs := int64(0)
	for _, asset := range assets {
		if asset.Category == model.VideoReplicationAssetReferenceVideo && (sourceMs == 0 || asset.IsPrimary) {
			sourceMs = videoReplicationEffectiveDurationMs(params, asset.DurationMs)
		}
	}
	if sourceMs <= 0 {
		return nil, BadAuthRequest("请先上传参考视频或粘贴视频链接")
	}
	return s.routeVideoReplication(userID, project.VideoType, params, sourceMs)
}

// VideoReplicationRouteInput 是提交前的推荐请求：页面读到参考视频（裁剪后）时长后即可预览推荐方案。
type VideoReplicationRouteInput struct {
	DurationMs  int64  `json:"durationMs"`
	VideoType   string `json:"videoType"`
	ReplaceMode string `json:"replaceMode"`
}

func (s *Service) PreviewVideoReplicationRoute(userID string, input VideoReplicationRouteInput) (*VideoReplicationRoute, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	if input.DurationMs < videoReplicationMinDurationMs-videoReplicationDurationSlackMs || input.DurationMs > videoReplicationMaxSourceMs {
		return nil, BadAuthRequest("参考视频时长无效")
	}
	return s.routeVideoReplication(userID, strings.TrimSpace(input.VideoType), VideoReplicationParameters{ReplaceMode: strings.TrimSpace(input.ReplaceMode)}, input.DurationMs)
}

func (s *Service) routeVideoReplication(userID, videoType string, params VideoReplicationParameters, sourceMs int64) (*VideoReplicationRoute, error) {
	setting, err := s.videoReplicationSetting()
	if err != nil {
		return nil, err
	}
	items, err := s.videoReplicationRouteModels(userID, setting)
	if err != nil {
		return nil, err
	}
	candidates, broken := s.videoReplicationRouteCandidates(userID, items)
	route := &VideoReplicationRoute{SourceSeconds: math.Round(float64(sourceMs)/100) / 10, Preference: setting.routePreference()}
	price := func(candidate videoReplicationRouteModel, seconds int, clips []int64) (int64, bool) {
		return s.quoteVideoReplicationTask(userID, candidate, seconds, clips)
	}
	for _, candidate := range candidates {
		route.Options = append(route.Options, planVideoReplicationRouteOption(candidate, sourceMs, price))
	}
	route.Options = append(route.Options, broken...)
	route.Recommended, route.Reason = rankVideoReplicationRoute(route.Options, videoReplicationNeedsFaceSafe(videoType, params), route.Preference)
	if route.Recommended == nil {
		return route, BadAuthRequest("没有能完成这条视频的已开放模型：请在「电商视频 Agent → 视频生成模型」开放至少一个能接收参考图的视频模型")
	}
	return route, nil
}

// applyVideoReplicationRoute 把推荐方案写回项目参数：模型、参考方式、单段时长档位、参考视频上限与声音开关都由服务端决定，
// 前端不需要再按模型能力推导。
func (s *Service) applyVideoReplicationRoute(userID string, params *VideoReplicationParameters, option VideoReplicationRouteOption) error {
	setting, err := s.videoReplicationSetting()
	if err != nil {
		return err
	}
	items, err := s.videoReplicationRouteModels(userID, setting)
	if err != nil {
		return err
	}
	candidates, _ := s.videoReplicationRouteCandidates(userID, items)
	var chosen *videoReplicationRouteModel
	var item *VideoReplicationVideoModel
	for index := range candidates {
		if candidates[index].Ref == option.Model {
			chosen = &candidates[index]
			break
		}
	}
	for index := range items {
		if items[index].EcommerceAgentModelRef == option.Model {
			item = &items[index]
			break
		}
	}
	if chosen == nil || item == nil {
		return errors.New("推荐的模型已下线，请重新分析")
	}
	params.LogicalModelID, params.ChannelID, params.Model = option.Model.LogicalModelID, option.Model.ChannelID, option.Model.ModelKey
	params.ModelLabel = chosen.Label
	params.ShotSeconds = chosen.Durations
	params.ReferenceMode, params.MaxReferenceImages = "first_frame", 1
	if chosen.supports("reference_to_video") && chosen.MaxImages > 0 {
		params.ReferenceMode, params.MaxReferenceImages = "reference", chosen.MaxImages
	}
	params.MaxReferenceVideos, params.MaxReferenceVideoSeconds, params.MaxReferenceTotalSeconds = 0, 0, 0
	if option.Track == VideoReplicationTrackReference {
		params.MaxReferenceVideos, params.MaxReferenceVideoSeconds, params.MaxReferenceTotalSeconds = chosen.MaxRefVideos, chosen.MaxRefVideoSeconds, chosen.RefTotalSeconds
	}
	params.GenerateAudio = chosen.GenerateAudio
	if len(item.Ratios) > 0 && !containsString(item.Ratios, params.Ratio) {
		params.Ratio = firstNonEmpty(item.DefaultRatio, item.Ratios[0])
	}
	if len(item.Resolutions) > 0 && !containsString(item.Resolutions, params.Resolution) {
		params.Resolution = firstNonEmpty(item.DefaultResolution, item.Resolutions[0])
	}
	if params.Resolution == "" {
		params.Resolution = item.DefaultResolution
	}
	if params.Ratio == "" {
		params.Ratio = item.DefaultRatio
	}
	params.SegmentSeconds = chosen.taskSeconds()
	return nil
}

// videoReplicationDurationValues 列出模型可选的单次时长（升序），与前端 durationValues 同口径。
func videoReplicationDurationValues(profile *VideoCapabilityConfig) []int {
	if profile == nil || (profile.DurationSupported != nil && !*profile.DurationSupported) {
		return nil
	}
	if profile.Duration.Selection == "enum" {
		values := append([]int(nil), profile.Duration.Values...)
		sort.Ints(values)
		return values
	}
	low, high, step := max(profile.Duration.Min, 1), profile.Duration.Max, max(profile.Duration.Step, 1)
	if high <= 0 {
		high = 15
	}
	values := []int{}
	for value := low; value <= high; value += step {
		values = append(values, value)
	}
	return values
}
