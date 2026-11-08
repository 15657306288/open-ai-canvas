package app

import (
	"fmt"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func TestVideoReplicationNativeBudgetKeepsReplacementAndEverySegment(t *testing.T) {
	refs := []map[string]string{
		{"category": model.VideoReplicationAssetProductImage, "label": "辣条"},
		{"category": model.VideoReplicationAssetModelImage, "label": "人物"},
		{"category": model.VideoReplicationAssetBackgroundImage, "label": "厨房"},
		{"category": model.VideoReplicationAssetProductDetailImage, "label": "参数表"},
		{"category": model.VideoReplicationAssetPackagingImage, "label": "包装"},
	}
	board := VideoReplicationStoryboard{}
	for i := 0; i < 6; i++ {
		board.Shots = append(board.Shots, VideoReplicationShot{DurationSec: 5, Prompt: fmt.Sprintf("第%d段展示辣条。", i+1) + strings.Repeat("人物动作和镜头描述。", 150)})
	}
	params := VideoReplicationParameters{ReplaceMode: "combination", PromptMaxChars: 16000}
	videoType := "product,model,background"
	compose := func(params VideoReplicationParameters) string {
		negative := videoReplicationKnowledgeNegative(VideoReplicationKnowledgeSeedance)
		budget := videoReplicationPromptBudgetFor(params).without(negative)
		visual := videoReplicationNativePrompt(videoType, params, board, 30, "", refs)
		return videoReplicationWithVoicePrompt(visual, "这款辣条请看介绍卡", 1, refs, videoReplicationSound{Subtitled: true, SpeechSegment: true}, budget) + "\n" + negative
	}
	prompt := compose(params)
	core := videoReplicationReplacementCore(videoType, params, refs, false)
	if core == "" || videoReplicationValidateReplacementPrompt(prompt, core, videoReplicationPromptBudgetFor(params)) != nil {
		t.Fatal("long timeline displaced required replacement instructions")
	}
	for i := 0; i < 6; i++ {
		want := fmt.Sprintf("[%d-%d秒] 第%d段展示辣条。", i*5, (i+1)*5, i+1)
		if !strings.Contains(prompt, want) {
			t.Fatalf("long early shot displaced timeline segment %q", want)
		}
	}
	if len([]rune(prompt)) > params.PromptMaxChars {
		t.Fatal("native prompt exceeded model limit")
	}
	params.PromptMaxChars = 2200
	if err := videoReplicationValidateReplacementPrompt(compose(params), core, videoReplicationPromptBudgetFor(params)); err == nil {
		t.Fatal("overlong native timeline must not be submitted without its replacement rules")
	}
}
