package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestValidateVideoReplicationClip(t *testing.T) {
	ok := VideoReplicationParameters{TrimStartMs: 2000, TrimEndMs: 12000, Regions: []VideoReplicationRegion{{X: 0.9, Y: 0.1, W: 0.5, H: 0.3}}, RegionFrameMs: 3000, RegionTarget: "client"}
	if err := validateVideoReplicationClip(&ok); err != nil {
		t.Fatalf("valid clip rejected: %v", err)
	}
	if ok.Regions[0].W > 0.1001 || ok.RegionTarget != "" {
		t.Fatalf("region should be clamped and server-only target cleared: %+v", ok)
	}
	for name, params := range map[string]VideoReplicationParameters{
		"too short":  {TrimStartMs: 1000, TrimEndMs: 3000},
		"start only": {TrimStartMs: 1000},
		"tiny box":   {Regions: []VideoReplicationRegion{{X: 0.1, Y: 0.1, W: 0.005, H: 0.3}}},
		"too many":   {Regions: make([]VideoReplicationRegion, videoReplicationMaxRegions+1)},
	} {
		if err := validateVideoReplicationClip(&params); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestVideoReplicationClipDigestAndRules(t *testing.T) {
	plain := VideoReplicationParameters{}
	trimmed := VideoReplicationParameters{TrimStartMs: 0, TrimEndMs: 8000}
	if videoReplicationAnalysisDigest(nil, plain) == videoReplicationAnalysisDigest(nil, trimmed) {
		t.Fatal("trim must change the analysis digest")
	}
	if got := videoReplicationEffectiveDurationMs(trimmed, 30000); got != 8000 {
		t.Fatalf("effective duration = %d", got)
	}
	if got := videoReplicationEffectiveDurationMs(plain, 30000); got != 30000 {
		t.Fatalf("untrimmed duration = %d", got)
	}
	product := VideoReplicationParameters{ReplaceMode: "precise-product", Regions: []VideoReplicationRegion{{X: 0.2, Y: 0.2, W: 0.2, H: 0.5}, {X: 0.5, Y: 0.2, W: 0.2, H: 0.5}}, RegionTarget: "两个粉色洗发水瓶"}
	if rule := videoReplicationRegionRule(product); !strings.Contains(rule, "框了几件就替换几件") || !strings.Contains(rule, "两个粉色洗发水瓶") {
		t.Fatalf("product rule = %q", rule)
	}
	person := VideoReplicationParameters{ReplaceMode: "precise-person", Regions: product.Regions[:1], RegionTarget: "右边的女生"}
	if rule := videoReplicationRegionRule(person); !strings.Contains(rule, "不要换错人") {
		t.Fatalf("person rule = %q", rule)
	}
	if videoReplicationRegionRule(VideoReplicationParameters{ReplaceMode: "precise-product"}) != "" {
		t.Fatal("no regions should add no rule")
	}
	if pos := videoReplicationRegionPosition(VideoReplicationRegion{X: 0.7, Y: 0.7, W: 0.2, H: 0.2}); pos != "画面下部右侧" {
		t.Fatalf("position = %s", pos)
	}
	prompt := videoReplicationShotPrompt("product", VideoReplicationParameters{AudioMode: "keep", KeepOriginalAudio: true, Talking: true, ReplaceMode: "precise-product", Regions: product.Regions, RegionTarget: "两个粉色洗发水瓶"}, VideoReplicationShot{Prompt: "手持商品"}, "", nil)
	for _, want := range []string{"框选锁定", "卡准原音乐节拍", "口播视频"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("shot prompt missing %q: %s", want, prompt)
		}
	}
	if lip := videoReplicationWithLipSyncPrompt("画面", "大家好", videoReplicationPromptBudgetFor(VideoReplicationParameters{})); !strings.Contains(lip, "口型") || !strings.Contains(lip, "大家好") {
		t.Fatalf("lip sync prompt = %q", lip)
	}
	if !videoReplicationNeedsTranscript(VideoReplicationParameters{Auto: true, Talking: true}) {
		t.Fatal("talking videos need transcript")
	}
}

func TestVideoReplicationFFmpegFailureNeverBlank(t *testing.T) {
	expired, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-expired.Done()
	cases := []struct {
		ctx  context.Context
		out  string
		err  error
		want string
	}{
		{expired, "", errors.New("signal: killed"), "处理超时"},
		{context.Background(), "", errors.New("signal: killed"), "内存不足"},
		{context.Background(), "", errors.New("exit status 1"), "exit status 1"},
		{context.Background(), "Invalid data found", errors.New("exit status 1"), "Invalid data found"},
	}
	for _, item := range cases {
		got := videoReplicationFFmpegFailure(item.ctx, "裁剪参考视频失败", []byte(item.out), item.err).Error()
		if !strings.Contains(got, item.want) || strings.HasSuffix(got, "：") {
			t.Fatalf("want %q in %q", item.want, got)
		}
	}
}
