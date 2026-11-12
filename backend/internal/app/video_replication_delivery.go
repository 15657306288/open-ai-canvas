package app

import (
	"fmt"
	"strings"

	"yingce/backend/internal/model"
)

const videoReplicationDeliveryAnalysisRule = "分析原视频的销售表达，而非只看剪辑快慢：区分直播带货、普通测评、慢讲解和未知。直播带货要实际听取说话快慢与快慢切换、重读、短停顿、换气、连说卖点、观众互动及展示商品时的停顿；逐镜头注明这些声音与表情、手势、商品展示的配合和时间依据。静态直播间画面只能支持场景判断，不能证明说话快；没有可用人声时 pace、pauses、emphasis 留空，不虚构每秒字数。划算感与紧迫感属于表达方式，原片的价格、秒杀、限量、倒计时、最低价和直播间专属福利不能作为新商品事实。"

const videoReplicationDeliveryPlanningRule = "summary.delivery 是原片声音表达分析，referenceShots[].speechDelivery 是对应镜头的局部节奏。mode=livestream 且有音轨依据时，保留主播快慢变化、短促换气、重音与观众互动；结合新商品调性改写情绪，不把快速直播口播统一改成温柔聊天。每段 prompt 写出对应时间范围的说话、停顿展示、表情和手势，口播按该段字数上限说完整；文案精简后可以短促说完再展示商品，不要把短句拖满整段。用户明确要求的语气优先。原片只有旁白时仍用旁白，静音或无台词时不新增口播。新文案不得照搬或编造原片的秒杀、限量、价格、倒计时、最低价、专属福利等优惠事实，只使用已确认的商品信息。"

// 仅有音轨和可定位依据的直播分析进入生成；用户的明确语气选择优先。
func videoReplicationReferenceDelivery(project *model.VideoReplicationProject, start, end int64) (delivery, pace string) {
	if project == nil {
		return "", ""
	}
	for _, role := range videoReplicationVoiceCast(project).Roles {
		if strings.TrimSpace(role.Instructions) != "" && role.InstructionsSource != "agent" {
			return "", ""
		}
	}
	analysis := videoReplicationSavedAnalysis(project)
	if analysis == nil || analysis.Summary.Delivery == nil {
		return "", ""
	}
	profile := analysis.Summary.Delivery
	if analysis.Voice != nil && (analysis.Voice.Type == "music" || analysis.Voice.Type == "silent") {
		return "", ""
	}
	if profile.Mode != "livestream" || strings.TrimSpace(profile.Pace) == "" || strings.TrimSpace(profile.Evidence) == "" || (!analysis.Videos[0].HasAudio && (analysis.Voice == nil || !analysis.Voice.HasAudio)) {
		return "", ""
	}
	parts := []string{}
	for _, value := range []string{profile.Pace, profile.Pauses, profile.Emphasis, profile.Interaction} {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, videoReplicationClipRunes(value, 45))
		}
	}
	if len(parts) == 0 {
		return "", ""
	}
	for _, shot := range analysis.Videos[0].Shots {
		if end > start && shot.StartMs < end && shot.EndMs > start && strings.TrimSpace(shot.SpeechDelivery) != "" {
			parts = append(parts, fmt.Sprintf("原片 %.1f–%.1fs：%s", float64(shot.StartMs)/1000, float64(shot.EndMs)/1000, videoReplicationClipRunes(shot.SpeechDelivery, 65)))
		}
	}
	delivery = videoReplicationClipRunes(strings.Join(parts, "；"), 300)
	pace = "语速：依照原片本段实际的快慢变化和换气，短句利落说完后留商品展示和观众反应的停顿，不按整段平均时长拖慢；吐字清楚，全部台词在本段内完整说完，不强行加速、不吞字。"
	if analysis.Voice != nil && analysis.Voice.CharsPerSec > 0 && analysis.Voice.CharsPerSec <= 9 {
		pace += fmt.Sprintf("原片 ASR 有声区间平均约 %.1f 字/秒，仅作参考，局部快慢与停顿按原片。", analysis.Voice.CharsPerSec)
	}
	return delivery, pace
}
