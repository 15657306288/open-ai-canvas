package app

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// 商品视频复刻的参考图 / 参考视频 / 参考音频数量、时长和提示词字数，全部以后台渠道给所选模型配的能力为准
// （channel_models.capability_config_json），不在代码里写死：管理员把 Seedance 2.5 配成 30 张图、10 个视频、
// 10 段音频，复刻就按 30 / 10 / 10 用。前端提交的数值只作初值，开始生成时服务端按模型配置重写。

// videoReplicationParamSanityMax 只用来挡明显异常的数值（不是模型上限）。
const videoReplicationParamSanityMax = 200

// videoReplicationDefaultPromptBytes 是读不到模型提示词上限时的兜底（旧行为，约 1300 个汉字）。
const videoReplicationDefaultPromptBytes = 3800

// applyVideoReplicationModelLimits 用所选模型的能力配置重写参考素材上限；读不到配置时保留前端传来的值。
// 原片编辑与否仍由前端按模型能力决定（MaxReferenceVideos 为 0 表示不走原片编辑），这里不擅自打开。
func (s *Service) applyVideoReplicationModelLimits(userID string, params *VideoReplicationParameters) bool {
	provider, err := s.resolveVideoReplicationProvider(userID, EcommerceAgentModelRef{LogicalModelID: params.LogicalModelID, ChannelID: params.ChannelID, ModelKey: params.Model}, "video")
	if err != nil {
		return false
	}
	profile := s.videoReplicationModelProfile(provider)
	if profile == nil {
		return false
	}
	refs := profile.References
	if params.ReferenceMode == "reference" && refs.MaxImages > 0 {
		params.MaxReferenceImages = min(refs.MaxImages, videoReplicationParamSanityMax)
	}
	if params.MaxReferenceVideos > 0 {
		params.MaxReferenceVideos = min(refs.MaxVideos, videoReplicationParamSanityMax)
		if refs.MaxVideoDuration > 0 {
			params.MaxReferenceVideoSeconds = refs.MaxVideoDuration
		}
	}
	if params.ReferenceMode == "reference" {
		params.MaxReferenceAudios = min(refs.MaxAudios, videoReplicationParamSanityMax)
		params.MaxReferenceAudioSeconds = refs.MaxAudioDuration
	}
	params.PromptMaxChars = refs.PromptMaxChars
	// 单段时长档位也按后台能力配置重写：管理员可以用 enum 排除上游实际不支持的个别时长
	// （例如 yuxibox seedance2.5-flash-官转 声明 5–30 整秒，但上游单独拒绝 12 秒）。
	// 旧项目里存的过期 ShotSeconds 因此在开始/再次生成时被刷新，不必等前端重建项目。
	if shotSeconds := videoReplicationDurationSecondsFromProfile(profile); len(shotSeconds) > 0 {
		params.ShotSeconds = shotSeconds
	}
	return true
}

// videoReplicationDurationSecondsFromProfile 把模型能力配置的 duration 展开成允许的单段秒数列表。
// enum 直接取 values；range 按 min/max/step 展开。durationSupported=false 或配置不可用时返回 nil（不重写）。
func videoReplicationDurationSecondsFromProfile(profile *VideoCapabilityConfig) []int {
	if profile == nil {
		return nil
	}
	if profile.DurationSupported != nil && !*profile.DurationSupported {
		return nil
	}
	duration := profile.Duration
	var result []int
	switch duration.Selection {
	case "enum":
		seen := make(map[int]bool, len(duration.Values))
		for _, value := range duration.Values {
			if value >= videoReplicationMinSegmentSeconds && value <= 60 && !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
	case "range":
		step := duration.Step
		if step <= 0 {
			step = 1
		}
		if duration.Min > 0 && duration.Max >= duration.Min {
			for value := duration.Min; value <= duration.Max && value <= 60; value += step {
				if value >= videoReplicationMinSegmentSeconds {
					result = append(result, value)
				}
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	sort.Ints(result)
	return result
}

// videoReplicationElementLimit 是元素替换清单最多几条：模型每段能收的参考图减去替换目标（商品 / 人物……各至少一张），
// 每条元素一张参考图；读不到模型上限时按 6 条。
func videoReplicationElementLimit(videoType string, params VideoReplicationParameters) int {
	if params.MaxReferenceImages <= 0 {
		return videoReplicationElementMax
	}
	return max(1, min(params.MaxReferenceImages-len(videoReplicationTargets(videoType)), videoReplicationElementSanityMax))
}

// videoReplicationPromptBudget 是视频提示词的长度上限：模型配了字数（PromptMaxChars）时按字数，否则按旧的 3800 字节。
type videoReplicationPromptBudget struct {
	limit int
	runes bool
}

func videoReplicationPromptBudgetFor(params VideoReplicationParameters) videoReplicationPromptBudget {
	if params.PromptMaxChars > 0 {
		return videoReplicationPromptBudget{limit: params.PromptMaxChars, runes: true}
	}
	return videoReplicationPromptBudget{limit: videoReplicationDefaultPromptBytes}
}

func (b videoReplicationPromptBudget) size(text string) int {
	if b.runes {
		return utf8.RuneCountInString(text)
	}
	return len(text)
}

// without 给接在末尾的内容（反提示词等）预留位置。
func (b videoReplicationPromptBudget) without(text string) videoReplicationPromptBudget {
	if text == "" {
		return b
	}
	b.limit = max(b.limit-b.size(text)-1, 0)
	return b
}

func (b videoReplicationPromptBudget) clip(text string) string {
	if b.runes {
		return videoReplicationClipRunes(text, b.limit)
	}
	return strings.ToValidUTF8(clipText(text, b.limit), "")
}

// videoReplicationVoiceBudgetMs 是本段参考音色最长多少毫秒：模型参考时长上限减去本段原片片段时长（留 300 毫秒余量）。
// 没有上限或本段不带原片片段时返回 -1（不限）。
func videoReplicationVoiceBudgetMs(params VideoReplicationParameters, clipSeconds int) int64 {
	limit := params.MaxReferenceVideoSeconds
	if limit <= 0 || clipSeconds <= 0 {
		return -1
	}
	return max(0, int64(limit-clipSeconds)*1000-300)
}

func videoReplicationVoiceSegmentSeconds(params VideoReplicationParameters, voiceCount int) int {
	seconds := params.SegmentSeconds
	if voiceCount == 0 || params.AudioMode != "replace" || !videoReplicationEditMode(params) || params.MaxReferenceVideoSeconds <= 0 {
		return seconds
	}
	reserve := max(5, (voiceCount*2000+300+999)/1000)
	limit := min(seconds, params.MaxReferenceVideoSeconds-reserve)
	if limit < videoReplicationMinSegmentSeconds {
		return 0
	}
	options := videoReplicationSegmentOptions(params.ShotSeconds)
	if len(options) == 0 {
		return limit
	}
	best := 0
	for _, option := range options {
		if option >= videoReplicationMinSegmentSeconds && option <= limit {
			best = max(best, option)
		}
	}
	return best
}
