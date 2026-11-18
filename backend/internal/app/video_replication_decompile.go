package app

import (
	_ "embed"
	"fmt"
	"strings"
)

// 参考视频超细粒度反推：整合 Gemini / Kimi / 千问三版拆解提示词，按电商复刻改造（结构与外观解耦、遮挡审计、
// 转身量化、布料物理、逐镜头英文运动提示词）。结构字段逐秒记录，供参考视频复刻做补充约束，
// 也是脚本复刻（不接收参考视频的模型）逐秒还原动作与运镜的唯一依据。

//go:embed video_replication_knowledge/decompile-system.md
var videoReplicationDecompileSystem string

// videoReplicationShotDecompileFields 读取视频理解返回的逐镜头反推字段。
func videoReplicationShotDecompileFields(shot *VideoReplicationSourceShot, detail map[string]any) {
	shot.Beats = videoReplicationClipRunes(videoReplicationFormatBeats(detail["beats"]), 1200)
	shot.MicroExpression = videoReplicationClipRunes(strings.TrimSpace(stringValue(detail["microExpression"])), 300)
	shot.Turn = videoReplicationClipRunes(strings.TrimSpace(stringValue(detail["turn"])), 120)
	shot.Occlusion = videoReplicationClipRunes(strings.TrimSpace(stringValue(detail["occlusion"])), 200)
	shot.Fabric = videoReplicationClipRunes(strings.TrimSpace(stringValue(detail["fabric"])), 200)
	shot.Sound = videoReplicationClipRunes(strings.TrimSpace(stringValue(detail["sound"])), 300)
	shot.MotionPrompt = videoReplicationClipRunes(strings.TrimSpace(stringValue(detail["motionPrompt"])), 600)
}

// videoReplicationFormatBeats 把节拍数组压成一行一拍的文本；模型直接返回字符串时原样使用。
func videoReplicationFormatBeats(value any) string {
	switch beats := value.(type) {
	case string:
		return strings.TrimSpace(beats)
	case []any:
		lines := []string{}
		for _, item := range beats {
			beat, ok := item.(map[string]any)
			if !ok {
				if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
					lines = append(lines, text)
				}
				continue
			}
			parts := []string{}
			for _, key := range []string{"action", "face", "camera", "physics"} {
				if text := strings.TrimSpace(stringValue(beat[key])); text != "" && !videoReplicationNoneText(text) {
					parts = append(parts, text)
				}
			}
			if len(parts) == 0 {
				continue
			}
			lines = append(lines, "["+strings.TrimSpace(stringValue(beat["t"]))+"] "+strings.Join(parts, "；"))
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

// videoReplicationScriptDirective 汇总与 [startMs, endMs) 重叠的原片镜头的逐秒反推，用于脚本复刻轨道：
// 模型拿不到参考视频，只能靠这段逐秒动作、运镜与物理描述还原原片结构。
func videoReplicationScriptDirective(analysis *VideoReplicationAnalysis, startMs, endMs int64) string {
	if analysis == nil || len(analysis.Videos) == 0 || endMs <= startMs {
		return ""
	}
	b := strings.Builder{}
	motions := []string{}
	for _, shot := range analysis.Videos[0].Shots {
		if shot.EndMs <= startMs || shot.StartMs >= endMs {
			continue
		}
		fmt.Fprintf(&b, "%.1f–%.1f秒：", float64(max(shot.StartMs, startMs)-startMs)/1000, float64(min(shot.EndMs, endMs)-startMs)/1000)
		items := []string{}
		for _, item := range [][2]string{{"景别", shot.ShotSize}, {"运镜", shot.Camera}, {"动作", shot.Action}, {"表情", shot.MicroExpression}, {"转身", shot.Turn}, {"布料", shot.Fabric}} {
			if text := strings.TrimSpace(item[1]); text != "" && !videoReplicationNoneText(text) {
				items = append(items, item[0]+"："+text)
			}
		}
		b.WriteString(strings.Join(items, "；"))
		if beats := strings.TrimSpace(shot.Beats); beats != "" {
			b.WriteString("\n" + beats)
		}
		b.WriteString("\n")
		if motion := strings.TrimSpace(shot.MotionPrompt); motion != "" {
			motions = append(motions, motion)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	text := "逐秒复刻原片结构（时间相对本段开头；只照搬动作、运镜与物理节奏，人物外观、服装和商品按参考图）：\n" + b.String()
	if len(motions) > 0 {
		text += "Motion: " + strings.Join(motions, " ")
	}
	return strings.TrimSpace(text)
}
