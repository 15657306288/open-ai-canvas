package app

import (
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func TestVideoReplicationCombinationDoesNotLockReplacedCategories(t *testing.T) {
	refs := []map[string]string{
		{"category": model.VideoReplicationAssetProductImage, "label": "辣条商品图"},
		{"category": model.VideoReplicationAssetModelImage, "label": "新人物"},
		{"category": model.VideoReplicationAssetBackgroundImage, "label": "新厨房"},
	}
	params := VideoReplicationParameters{ReplaceMode: "combination", PromptMaxChars: 8000}
	shot := VideoReplicationShot{ProductPresence: "hero"}
	for name, prompt := range map[string]string{
		"edit": videoReplicationEditPrompt("product,model,background", params, shot, VideoReplicationStoryboard{}, "", refs),
		"shot": videoReplicationShotPrompt("product,model,background", params, shot, "", refs),
	} {
		t.Run(name, func(t *testing.T) {
			for _, conflict := range []string{"场景背景仍是 @视频1 的", "人物和商品不动", "人物、商品、动作与运镜保持与原视频一致", "场景背景按原视频本段画面来"} {
				if strings.Contains(prompt, conflict) {
					t.Fatalf("combined replacement locks a replaced category with %q:\n%s", conflict, prompt)
				}
			}
			for _, want := range []string{"服装", "接触阴影", "反射", "不引入背景图里的人物或商品"} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("combined replacement misses %q:\n%s", want, prompt)
				}
			}
		})
	}
	edit := videoReplicationEditPrompt("product,model,background", params, shot, VideoReplicationStoryboard{}, "", refs)
	for _, want := range []string{"替换成 @图片1 中的商品", "替换成 @图片2 中的人物", "替换成 @图片3 中的环境"} {
		if !strings.Contains(edit, want) {
			t.Fatalf("reference role mapping misses %q:\n%s", want, edit)
		}
	}
}

func TestVideoReplicationFoodReplacementCoversInstancesAndStates(t *testing.T) {
	refs := []map[string]string{{"category": model.VideoReplicationAssetProductImage}}
	params := VideoReplicationParameters{PromptMaxChars: 8000}
	for name, prompt := range map[string]string{
		"edit": videoReplicationEditPrompt("product", params, VideoReplicationShot{}, VideoReplicationStoryboard{SourceLook: "沙琪玛", NewSubject: "辣条"}, "", refs),
		"shot": videoReplicationShotPrompt("product", params, VideoReplicationShot{}, "", refs),
	} {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{"所有实例", "桌面", "盘中", "手持", "袋内", "入口", "咬后", "碎块", "反射中的同一商品", "不改无关物品", "不把食物变成包装或平面照片"} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("food replacement misses %q:\n%s", want, prompt)
				}
			}
		})
	}
	precise := params
	precise.ReplaceMode = "precise-product"
	precise.Regions = []VideoReplicationRegion{{X: .2, Y: .2, W: .2, H: .2}}
	precise.RegionTarget = "手里的沙琪玛"
	for name, prompt := range map[string]string{
		"edit": videoReplicationEditPrompt("product", precise, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", refs),
		"shot": videoReplicationShotPrompt("product", precise, VideoReplicationShot{}, "", refs),
	} {
		if strings.Contains(prompt, "所有实例") || !strings.Contains(prompt, "未框选的同款和其他物品保持原样") {
			t.Fatalf("%s broadens precise selection:\n%s", name, prompt)
		}
	}
}

func TestVideoReplicationCombinationPreservesUnselectedCategories(t *testing.T) {
	params := VideoReplicationParameters{PromptMaxChars: 8000}
	modelRefs := []map[string]string{{"category": model.VideoReplicationAssetModelImage}}
	edit := videoReplicationEditPrompt("model", params, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", modelRefs)
	if !strings.Contains(edit, "场景背景仍是 @视频1 的") || strings.Contains(edit, "商品范围：") {
		t.Fatalf("person-only edit changes the product or background:\n%s", edit)
	}
	shot := videoReplicationShotPrompt("model", params, VideoReplicationShot{}, "", modelRefs)
	if !strings.Contains(shot, "未选择替换的商品、服装与配饰、场景背景沿用原视频") || strings.Contains(shot, "商品范围：") {
		t.Fatalf("person-only generation changes unselected categories:\n%s", shot)
	}
	product := videoReplicationEditPrompt("product", params, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", []map[string]string{{"category": model.VideoReplicationAssetProductImage}})
	if !strings.Contains(product, "桌面摆设（不含被替换商品及其包装、展示资料）") {
		t.Fatalf("tabletop keep rule must exclude replacement subjects:\n%s", product)
	}
}

func TestVideoReplicationCombinationCorePrecedesLongShotDescription(t *testing.T) {
	refs := []map[string]string{
		{"category": model.VideoReplicationAssetProductImage, "label": "辣条"},
		{"category": model.VideoReplicationAssetModelImage, "label": "人物"},
		{"category": model.VideoReplicationAssetBackgroundImage, "label": "厨房"},
	}
	params := VideoReplicationParameters{ReplaceMode: "combination", PromptMaxChars: 1500}
	prompt := videoReplicationShotPrompt("product,model,background", params, VideoReplicationShot{Prompt: strings.Repeat("原片描述。", 1000)}, "", refs)
	if len([]rune(prompt)) > params.PromptMaxChars {
		t.Fatal("prompt exceeded configured budget")
	}
	for _, want := range []string{"模特参考图中的人物", "背景参考图中的环境", "所有实例", "不把食物变成包装或平面照片"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("long description displaced core replacement rule %q:\n%s", want, prompt)
		}
	}
	for _, family := range []string{VideoReplicationKnowledgeSeedance, VideoReplicationKnowledgeKling} {
		negative := videoReplicationKnowledgeNegative(family)
		if strings.Contains(negative, "不要文字乱码或新生成文字") || !strings.Contains(negative, "编造产品参数") {
			t.Fatalf("negative prompt must permit verified product text: %s", negative)
		}
	}
}

func TestVideoReplicationVoiceSubtitlesPreserveVerifiedProductText(t *testing.T) {
	refs := []map[string]string{
		{"category": model.VideoReplicationAssetProductImage},
		{"category": model.VideoReplicationAssetProductDetailImage},
		{"category": model.VideoReplicationAssetPackagingImage},
		{"category": model.VideoReplicationAssetModelImage},
	}
	params := VideoReplicationParameters{PromptMaxChars: 8000, AudioMode: "replace", ReplaceMode: "combination"}
	negative := videoReplicationKnowledgeNegative(VideoReplicationKnowledgeSeedance)
	budget := videoReplicationPromptBudgetFor(params).without(negative)
	visual := videoReplicationEditPrompt("product,model", params, VideoReplicationShot{Packaging: true}, VideoReplicationStoryboard{}, "", refs)
	prompt := videoReplicationWithVoicePrompt(visual, "这款辣条的规格请看介绍卡", 1, refs, videoReplicationSound{Edit: true, Subtitled: true, SpeechSegment: true}, budget) + "\n" + negative
	for _, want := range []string{"产品参数表 / 介绍卡", "@图片2", "@图片3", "不生成烧录字幕或悬浮文案", "载体上的真实文字按已提供资料保留", "台词：人物逐字说", "不沿用旧数值"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("final voice prompt misses %q:\n%s", want, prompt)
		}
	}
	for _, conflict := range []string{"不要生成任何字幕或文字", "不要文字乱码或新生成文字"} {
		if strings.Contains(prompt, conflict) {
			t.Fatalf("voice or negative suffix erases verified material text: %q", conflict)
		}
	}
	if len([]rune(prompt)) > params.PromptMaxChars {
		t.Fatal("final voice prompt exceeds model budget")
	}
}

func TestVideoReplicationFoodChangeAdaptsContactWithoutStructureAnalysis(t *testing.T) {
	refs := []map[string]string{{"category": model.VideoReplicationAssetProductImage}, {"category": model.VideoReplicationAssetModelImage}}
	prompt := videoReplicationEditPrompt("product,model", VideoReplicationParameters{PromptMaxChars: 8000}, VideoReplicationShot{}, VideoReplicationStoryboard{}, "把沙琪玛换成辣条", refs)
	for _, conflict := range []string{"手部姿势与动作、机位", "动作和走位与 @视频1 一致", "握持位置、朝向、大小、遮挡关系和运动轨迹与原商品一致"} {
		if strings.Contains(prompt, conflict) {
			t.Fatalf("missing structure analysis froze old food contact: %q:\n%s", conflict, prompt)
		}
	}
	for _, want := range []string{"按新商品结构微调握持与手口接触姿势", "人物走位与节奏", "手口接触姿势按新商品或服装结构微调", "镜头数量、机位、景别、构图"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("food change lost action adaptation or camera lock %q:\n%s", want, prompt)
		}
	}
}

func TestVideoReplicationFinalPromptRejectsTruncatedReplacementCore(t *testing.T) {
	refs := []map[string]string{
		{"category": model.VideoReplicationAssetProductImage},
		{"category": model.VideoReplicationAssetModelImage},
		{"category": model.VideoReplicationAssetBackgroundImage},
		{"category": model.VideoReplicationAssetProductDetailImage},
		{"category": model.VideoReplicationAssetPackagingImage},
	}
	for _, tc := range []struct {
		name  string
		limit int
		want  bool
	}{{"enough", 8000, true}, {"900 characters", 900, false}, {"default 3800 bytes", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			params := VideoReplicationParameters{PromptMaxChars: tc.limit, ReplaceMode: "combination"}
			budget := videoReplicationPromptBudgetFor(params)
			negative := videoReplicationKnowledgeNegative(VideoReplicationKnowledgeSeedance)
			core := videoReplicationReplacementCore("product,model,background", params, refs, true)
			visual := videoReplicationEditPrompt("product,model,background", params, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", refs)
			prompt := videoReplicationWithVoicePrompt(visual, strings.Repeat("详细介绍这款产品。", 45), 1, refs, videoReplicationSound{Edit: true, Subtitled: true}, budget.without(negative)) + "\n" + negative
			err := videoReplicationValidateReplacementPrompt(prompt, core, budget)
			if tc.want {
				if err != nil || !strings.Contains(prompt, core) {
					t.Fatalf("adequate budget lost core: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "提示词上限不足") || !strings.Contains(err.Error(), "缩短台词") {
				t.Fatalf("insufficient final budget must reject before submission: %v", err)
			}
		})
	}
	if core := videoReplicationReplacementCore("", VideoReplicationParameters{}, nil, false); core != "" {
		t.Fatalf("no-replacement request has a replacement core: %q", core)
	}
}

func TestVideoReplicationReferenceCapacityRejectsDroppedTargetsAndMaterials(t *testing.T) {
	assets := []model.VideoReplicationAsset{
		{ID: "p", Category: model.VideoReplicationAssetProductImage, ResourceID: "product"},
		{ID: "p-angle", Category: model.VideoReplicationAssetProductImage, ResourceID: "product-angle", SortOrder: 1},
		{ID: "m", Category: model.VideoReplicationAssetModelImage, ResourceID: "model"},
		{ID: "b", Category: model.VideoReplicationAssetBackgroundImage, ResourceID: "background"},
		{ID: "d", Category: model.VideoReplicationAssetProductDetailImage, ResourceID: "detail"},
		{ID: "k", Category: model.VideoReplicationAssetPackagingImage, ResourceID: "packaging"},
	}
	for _, limit := range []int{2, 3, 5} {
		params := VideoReplicationParameters{ReferenceMode: "reference", MaxReferenceImages: limit}
		refs := videoReplicationReferences("product,model,background", params, assets, nil)
		err := videoReplicationValidateReferences("product,model,background", assets, refs)
		if limit == 5 {
			if err != nil {
				t.Fatalf("five slots should cover three targets and both material roles: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "参考图名额不足") {
			t.Fatalf("limit %d silently dropped requested references: %v", limit, err)
		}
	}
}

func TestVideoReplicationReferencesKeepRequiredMaterialsBeforeExtraAngles(t *testing.T) {
	assets := []model.VideoReplicationAsset{
		{ID: "p-main", Category: model.VideoReplicationAssetProductImage, ResourceID: "product-main", IsPrimary: true},
		{ID: "p-mentioned", Category: model.VideoReplicationAssetProductImage, ResourceID: "product-mentioned", SortOrder: 10},
		{ID: "p-angle", Category: model.VideoReplicationAssetProductImage, ResourceID: "product-angle", SortOrder: 20},
		{ID: "m-main", Category: model.VideoReplicationAssetModelImage, ResourceID: "model-main", IsPrimary: true},
		{ID: "m-angle", Category: model.VideoReplicationAssetModelImage, ResourceID: "model-angle", SortOrder: 10},
		{ID: "b-main", Category: model.VideoReplicationAssetBackgroundImage, ResourceID: "background-main", IsPrimary: true},
		{ID: "b-angle", Category: model.VideoReplicationAssetBackgroundImage, ResourceID: "background-angle", SortOrder: 10},
		{ID: "d-plain", Category: model.VideoReplicationAssetProductDetailImage, ResourceID: "detail-plain"},
		{ID: "d-primary", Category: model.VideoReplicationAssetProductDetailImage, ResourceID: "detail-primary", IsPrimary: true, SortOrder: 1},
		{ID: "d-mentioned", Category: model.VideoReplicationAssetProductDetailImage, ResourceID: "detail-mentioned", SortOrder: 2},
		{ID: "k", Category: model.VideoReplicationAssetPackagingImage, ResourceID: "packaging"},
	}
	mentions := []VideoReplicationMention{{AssetID: "p-mentioned"}, {AssetID: "d-mentioned"}}
	want := []string{"p-mentioned", "m-main", "b-main", "d-mentioned", "d-primary", "d-plain", "k", "p-main", "m-angle"}
	for _, limit := range []int{6, 7, 9} {
		refs := videoReplicationReferences("product,model,background", VideoReplicationParameters{ReferenceMode: "reference", MaxReferenceImages: limit}, assets, mentions)
		if len(refs) != limit {
			t.Fatalf("limit %d selected %d references", limit, len(refs))
		}
		for i, ref := range refs {
			if ref["assetId"] != want[i] {
				t.Fatalf("limit %d reference %d=%q, want %q; materials must precede extra angles without losing mention/primary priority", limit, i, ref["assetId"], want[i])
			}
		}
		err := videoReplicationValidateReferences("product,model,background", assets, refs)
		if limit < 7 {
			if err == nil || !strings.Contains(err.Error(), "参考图名额不足") {
				t.Fatalf("six slots cannot fit seven required references: %v", err)
			}
		} else if err != nil {
			t.Fatalf("limit %d can fit all three targets, three detail images and packaging: %v", limit, err)
		}
	}
	// 全片有四种元素，但本段只有两种：9 个名额足以放下 7 张必需资料和本段 2 张元素图。
	elements := normalizeVideoReplicationElements([]VideoReplicationElement{
		{From: "花瓶", To: "花篮", ResourceID: "other-vase"},
		{From: "时钟", To: "挂画", ResourceID: "other-clock"},
		{From: "梨", To: "黄色香蕉", ResourceID: "shot-pear"},
		{From: "茶杯", To: "玻璃杯", ResourceID: "shot-cup"},
	})
	shot := VideoReplicationShot{SceneEdits: "桌上梨→黄色香蕉；左侧茶杯→玻璃杯"}
	storyboard := VideoReplicationStoryboard{Elements: elements, Shots: []VideoReplicationShot{shot}}
	for _, limit := range []int{8, 9} {
		base := videoReplicationReferences("product,model,background", VideoReplicationParameters{ReferenceMode: "reference", MaxReferenceImages: limit}, assets, mentions)
		if err := videoReplicationValidateReferences("product,model,background", assets, base); err != nil {
			t.Fatalf("full model capacity must preserve required materials before per-shot selection: %v", err)
		}
		refs := videoReplicationWithElements(base, elements, shot, limit)
		if len(refs) != limit {
			t.Fatalf("limit %d selected %d references", limit, len(refs))
		}
		if err := videoReplicationValidateReferences("product,model,background", assets, refs); err != nil {
			t.Fatalf("scene elements displaced required materials: %v", err)
		}
		for i, id := range want[:7] {
			if refs[i]["assetId"] != id {
				t.Fatalf("limit %d required reference %d changed: %#v", limit, i, refs)
			}
		}
		if refs[7]["resourceId"] != "shot-pear" || (limit == 9 && refs[8]["resourceId"] != "shot-cup") {
			t.Fatalf("current-shot elements must take slots before other-shot elements: %#v", refs)
		}
		clauses := strings.Join(videoReplicationElementClauses(elements, shot, storyboard, refs, "@图片%d", "@视频1"), "；")
		if !strings.Contains(clauses, "梨替换成 @图片8 中的黄色香蕉") {
			t.Fatalf("sent element image has the wrong index: %s", clauses)
		}
		if limit == 8 && (!strings.Contains(clauses, "茶杯替换成 玻璃杯") || strings.Contains(clauses, "@图片9")) {
			t.Fatalf("unsent element must use text without an invented image reference: %s", clauses)
		}
	}
}
