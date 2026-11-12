package app

import (
	"strings"
	"testing"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

func TestVideoReplicationStructureFramesCoverEverySegmentFirst(t *testing.T) {
	video := VideoReplicationVideoAnalysis{Shots: []VideoReplicationSourceShot{
		{StartMs: 0, EndMs: 2000, KeyframeResourceID: "f1"},
		{StartMs: 2000, EndMs: 5000, KeyframeResourceID: "f2"},
		{StartMs: 5000, EndMs: 6000, KeyframeResourceID: "f3"},
		{StartMs: 6000, EndMs: 10000, KeyframeResourceID: "f4"},
		{StartMs: 10000, EndMs: 11000},
	}}
	segments := []videoReplicationAutoSegment{{StartMs: 0, EndMs: 5000}, {StartMs: 5000, EndMs: 11000}}
	picked := videoReplicationStructureFrames(video, segments, 2)
	if len(picked[0]) != 1 || picked[0][0].KeyframeResourceID != "f2" || len(picked[1]) != 1 || picked[1][0].KeyframeResourceID != "f4" {
		t.Fatalf("budget 2 must give each segment its longest shot: %+v", picked)
	}
	picked = videoReplicationStructureFrames(video, segments, 10)
	if len(picked[0]) != 2 || picked[0][0].KeyframeResourceID != "f1" || len(picked[1]) != 2 || picked[1][1].KeyframeResourceID != "f4" {
		t.Fatalf("extra budget adds frames in time order, skipping shots without keyframes: %+v", picked)
	}
}

func TestVideoReplicationStructureAdaptationSkipsUnchangedSegments(t *testing.T) {
	if got := videoReplicationStructureAdaptation(VideoReplicationStructureSegment{Source: "握持展示", Action: "沿用原片动作"}); got != "" {
		t.Fatalf("unchanged segment = %q", got)
	}
	got := videoReplicationStructureAdaptation(VideoReplicationStructureSegment{Source: "从手柄底部拔出长条滤芯", Match: "尾部滤网盖", Action: "改为拧下尾部圆形滤网盖"})
	if !strings.Contains(got, "拔出长条滤芯") || !strings.Contains(got, "拧下尾部圆形滤网盖") {
		t.Fatalf("adaptation = %q", got)
	}
}

func TestVideoReplicationEditPromptUsesStructurePackagingAndRemovesSubtitles(t *testing.T) {
	references := []map[string]string{
		{"category": model.VideoReplicationAssetProductImage},
		{"category": model.VideoReplicationAssetProductImage},
		{"category": model.VideoReplicationAssetProductDetailImage},
		{"category": model.VideoReplicationAssetPackagingImage},
	}
	shot := VideoReplicationShot{Adaptation: "原片「从手柄底部拔出长条滤芯」→ 改为拧下尾部圆形滤网盖", Packaging: true}
	brief := strings.Repeat("我的吹风机滤网在尾部，是旋拧的。", 14)
	prompt := videoReplicationEditPrompt("product", VideoReplicationParameters{ReplaceMode: "product", SubjectHint: "吹风机"}, shot, VideoReplicationStoryboard{ProductStructure: "格栅出风口，尾部旋拧滤网盖，手柄两颗按键"}, brief, references)
	for _, want := range []string{"@图片1、@图片2 中的商品", "@图片3 是新商品的细节", "新商品结构：格栅出风口", "拧下尾部圆形滤网盖", "本段出现包装", "@图片4 中的包装", "去掉原视频画面里烧录的字幕", "用户补充要求（最高优先级", "除用户要求改动的地方外"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if !utf8.ValidString(prompt) || len(prompt) > 3800 {
		t.Fatalf("prompt must be valid UTF-8 within 3800 bytes, got %d bytes", len(prompt))
	}
	if strings.Index(prompt, "补充要求") > strings.Index(prompt, "新商品结构") {
		t.Fatal("user brief must come before the generic product rules so truncation never drops it")
	}
	// 没有结构对齐结果时退回通用规则，仍然换掉原包装。
	plain := videoReplicationEditPrompt("product", VideoReplicationParameters{}, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", references[:1])
	if !strings.Contains(plain, "展示新商品对应的部位或整机") || !strings.Contains(plain, "同类无品牌包装（袋仍是袋，盒仍是盒）") {
		t.Fatalf("fallback prompt:\n%s", plain)
	}
}

func TestVideoReplicationEditPromptAnchorsProductPresenceAndExcludesReferenceScene(t *testing.T) {
	references := []map[string]string{{"category": model.VideoReplicationAssetProductImage}, {"category": model.VideoReplicationAssetModelImage}}
	storyboard := VideoReplicationStoryboard{
		SourceLook: "细长透明塑料瓶，黑色瓶盖，白底竖版标签", NewSubject: "金色旋盖玻璃罐装浓浆，红色标签",
		ReferenceProps: "红色包装盒、苹果、茶杯、蜂蜜勺、木窗格", Changes: "画面中的梨→红苹果；暖黄色调→红色调",
	}
	product := videoReplicationEditPrompt("product", VideoReplicationParameters{}, VideoReplicationShot{ProductPresence: "minor"}, storyboard, "梨换成苹果", references[:1])
	for _, want := range []string{"主要商品（细长透明塑料瓶", "中的商品（金色旋盖玻璃罐装浓浆", "小面积出现", "不要改成商品特写", "红色包装盒、苹果、茶杯、蜂蜜勺、木窗格以及图中的背景", "不是给原商品改颜色", "场景布局与机位", "不要变成商品静物展示", "成片也不要出现包装盒"} {
		if !strings.Contains(product, want) {
			t.Fatalf("edit prompt missing %q:\n%s", want, product)
		}
	}
	if !utf8.ValidString(product) || len(product) > 3800 {
		t.Fatalf("prompt must be valid UTF-8 within 3800 bytes, got %d", len(product))
	}
	if strings.Index(product, "小面积出现") > strings.Index(product, "用户补充要求") {
		t.Fatal("presence rule must sit right after the swap clause")
	}
	none := videoReplicationEditPrompt("product", VideoReplicationParameters{}, VideoReplicationShot{ProductPresence: "none"}, storyboard, "", references[:1])
	if !strings.Contains(none, "@视频1 中如果出现主要商品") || !strings.Contains(none, "不新增商品") {
		t.Fatalf("absent-product segment must not force a product in:\n%s", none)
	}
	// 旧方案没有结构对齐字段时退回通用写法。
	legacy := videoReplicationEditPrompt("product", VideoReplicationParameters{}, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", references[:1])
	if !strings.Contains(legacy, "把 @视频1 中的主要商品替换成 @图片1 中的商品。") || strings.Contains(legacy, "本段原片没有包装盒") {
		t.Fatalf("legacy prompt:\n%s", legacy)
	}
	if normalizeVideoReplicationPresence(" Hero ") != "hero" || normalizeVideoReplicationPresence("big") != "" {
		t.Fatal("presence normalization")
	}
}

func TestVideoReplicationUserBriefOverridesKeepRules(t *testing.T) {
	references := []map[string]string{{"category": model.VideoReplicationAssetProductImage}}
	params := VideoReplicationParameters{ReplaceMode: "product", SubjectHint: "饮料瓶"}
	locked := videoReplicationEditPrompt("product", params, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", references)
	if !strings.Contains(locked, "场景、背景、桌面摆设") || strings.Contains(locked, "用户补充要求") || strings.Contains(locked, "除用户要求改动的地方外") {
		t.Fatalf("edit prompt without a brief must keep the scene locked:\n%s", locked)
	}
	// 任何品类：只写了要求、规划没整理出清单时，要求也放在「保持不变」前面并声明优先。
	brief := "模特全程坐在沙发上，最后一个镜头加上一只猫"
	onlyBrief := videoReplicationEditPrompt("apparel", params, VideoReplicationShot{}, VideoReplicationStoryboard{}, brief, nil)
	if strings.Index(onlyBrief, "用户补充要求（最高优先级") < 0 || strings.Index(onlyBrief, brief) > strings.Index(onlyBrief, "保持不变：@视频1") || !strings.Contains(onlyBrief, "除用户要求改动的地方外") {
		t.Fatalf("brief must lead and override the keep list:\n%s", onlyBrief)
	}
	changes := "画面中的梨、枇杷→红苹果；小梨车→苹果小推车；秋日暖黄色调→清新红绿色调"
	prompt := videoReplicationEditPrompt("product", params, VideoReplicationShot{}, VideoReplicationStoryboard{Changes: changes}, "把梨的元素都换成苹果", references)
	for _, want := range []string{"画面改动清单（必须执行）", changes, "@视频1里对应的内容", "运镜", "节奏和时长", "用户补充要求（最高优先级", "饮料瓶"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("changes prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, banned := range []string{"场景、背景、桌面摆设", "不擅自新增或删除", "、光线、"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("changes prompt must not lock %q:\n%s", banned, prompt)
		}
	}
	if got := videoReplicationWithUserDirective("画面", "", ""); got != "画面" {
		t.Fatalf("empty directive changed prompt: %q", got)
	}
	got := videoReplicationWithUserDirective("画面", brief, changes)
	if !strings.HasPrefix(got, "用户补充要求") || !strings.Contains(got, "画面改动清单") || !strings.HasSuffix(got, "画面") {
		t.Fatalf("directive must lead the shot prompt: %q", got)
	}
	long := videoReplicationWithUserDirective(strings.Repeat("长描述", 2000), brief, changes)
	if !strings.Contains(long, brief) || !utf8.ValidString(long) || len(long) > 3800 {
		t.Fatalf("truncation must keep the user brief, got %d bytes", len(long))
	}
}

func TestVideoReplicationReferencesAddDetailAndPackagingForProduct(t *testing.T) {
	assets := []model.VideoReplicationAsset{
		{ID: "p1", Category: model.VideoReplicationAssetProductImage, ResourceID: "r1"},
		{ID: "d1", Category: model.VideoReplicationAssetProductDetailImage, ResourceID: "r2"},
		{ID: "k1", Category: model.VideoReplicationAssetPackagingImage, ResourceID: "r3"},
		{ID: "m1", Category: model.VideoReplicationAssetModelImage, ResourceID: "r4"},
	}
	refs := videoReplicationReferences("product", VideoReplicationParameters{ReferenceMode: "reference", MaxReferenceImages: 5}, assets, nil)
	if len(refs) != 3 || refs[0]["assetId"] != "p1" || refs[1]["assetId"] != "d1" || refs[2]["assetId"] != "k1" {
		t.Fatalf("product refs = %+v", refs)
	}
	refs = videoReplicationReferences("model", VideoReplicationParameters{ReferenceMode: "reference", MaxReferenceImages: 5}, assets, nil)
	if len(refs) != 1 || refs[0]["assetId"] != "m1" {
		t.Fatalf("detail/packaging images only follow product replacement: %+v", refs)
	}
}
