package app

import (
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func TestVideoReplicationBriefEditsScene(t *testing.T) {
	for brief, want := range map[string]bool{
		"画面里所有梨、枇杷、小梨车都换成香蕉和香蕉小推车": true,
		"整体色调改成香蕉的黄色清新色":           true,
		"跑鞋复刻": false,
		"":     false,
	} {
		if got := videoReplicationBriefEditsScene(brief); got != want {
			t.Fatalf("videoReplicationBriefEditsScene(%q) = %v, want %v", brief, got, want)
		}
	}
}

func TestVideoReplicationCleanEditsKeepsOnlyRealSwaps(t *testing.T) {
	got := videoReplicationCleanEdits("左上飘浮的黄梨->一根香蕉;无→无；摩天轮梨形吊舱→香蕉形吊舱；树→树；只有描述没有箭头")
	if got != "左上飘浮的黄梨→一根香蕉；摩天轮梨形吊舱→香蕉形吊舱" {
		t.Fatalf("unexpected cleaned edits: %q", got)
	}
}

// 用户写了换元素的补充要求时，原片编辑提示词要把本段识图点名的每个元素写成「把 @视频1 中的 A 替换成 B」，
// 并在「保持不变」之前全片清除被换掉的原元素。
func TestVideoReplicationEditPromptNamesEverySceneElement(t *testing.T) {
	references := []map[string]string{{"category": model.VideoReplicationAssetProductImage}, {"category": model.VideoReplicationAssetModelImage}}
	shot := VideoReplicationShot{SceneEdits: "左上树枝上挂的黄梨→挂着的一根黄色香蕉；远处浮岛上的三个小梨→三根小香蕉；摩天轮上的梨形吊舱→香蕉形吊舱"}
	storyboard := VideoReplicationStoryboard{Banned: "梨、枇杷、梨形小车", Changes: "梨和枇杷换成香蕉，小梨车换成香蕉小推车，树换成香蕉树"}
	brief := "画面里所有梨、枇杷、小梨车都换成香蕉和香蕉小推车，整体色调从秋日暖黄改成香蕉的黄色清新色，然后把树换成香蕉树"
	prompt := videoReplicationEditPrompt("product,model", VideoReplicationParameters{ReplaceMode: "combo"}, shot, storyboard, brief, references)
	for _, want := range []string{
		"把 @视频1 中的左上树枝上挂的黄梨替换成挂着的一根黄色香蕉",
		"把 @视频1 中的远处浮岛上的三个小梨替换成三根小香蕉",
		"把 @视频1 中的摩天轮上的梨形吊舱替换成香蕉形吊舱",
		"全片清除：@视频1 里所有的梨、枇杷、梨形小车",
		"成片任何一帧都不能再出现梨、枇杷、梨形小车",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	firstLine := strings.SplitN(prompt, "\n", 2)[0]
	if !strings.Contains(firstLine, "梨形吊舱替换成香蕉形吊舱") {
		t.Fatalf("scene swaps must be in the first edit sentence, got %q", firstLine)
	}
	for _, want := range []string{"复刻只做替换", "不推近、不拉远、不改拍", "机位、运镜、景别、构图"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt must lock the original shot, missing %q:\n%s", want, prompt)
		}
	}
	adapted := videoReplicationEditPrompt("product", VideoReplicationParameters{}, VideoReplicationShot{Adaptation: "改为拧下尾部滤网盖"}, VideoReplicationStoryboard{}, "", references[:1])
	if !strings.Contains(adapted, "机位、运镜、景别、构图") || strings.Contains(adapted, "改写动作与镜头") {
		t.Fatalf("structure adaptation must only adjust hands, never the camera:\n%s", adapted)
	}
	if strings.Index(prompt, "全片清除") > strings.Index(prompt, "保持不变：@视频1") {
		t.Fatal("banned rule must come before the keep-unchanged rule")
	}

	plain := videoReplicationEditPrompt("product", VideoReplicationParameters{}, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", references[:1])
	if strings.Contains(plain, "全片清除") || strings.Contains(plain, "→") {
		t.Fatalf("no brief must not add scene edits:\n%s", plain)
	}
}

// 元素参考图：原片编辑时新元素的参考图排在替换素材后面，提示词写成「把 @视频1 中的梨替换成 @图片N 中的香蕉」。
func TestVideoReplicationElementImagesAreSentAndReferenced(t *testing.T) {
	references := []map[string]string{{"resourceId": "p1", "category": model.VideoReplicationAssetProductImage, "role": "reference"}, {"resourceId": "m1", "category": model.VideoReplicationAssetModelImage, "role": "reference"}}
	elements := normalizeVideoReplicationElements([]VideoReplicationElement{{From: "梨", To: "黄色香蕉", ResourceID: "el1"}, {From: "梨形小车", To: "香蕉形小车", ResourceID: "el2"}, {From: "梨树", To: "香蕉树"}})
	shot := VideoReplicationShot{SceneEdits: "左侧草地上的梨→一根香蕉；路边的梨形小车→香蕉形小车"}
	storyboard := VideoReplicationStoryboard{Elements: elements, Shots: []VideoReplicationShot{shot}}
	refs := videoReplicationWithElements(references, elements, shot, 9)
	if len(refs) != 4 || refs[2]["resourceId"] != "el1" && refs[2]["resourceId"] != "el2" || refs[2]["category"] != videoReplicationElementCategory {
		t.Fatalf("element images must follow the target images: %#v", refs)
	}
	prompt := videoReplicationEditPrompt("product,model", VideoReplicationParameters{}, shot, storyboard, "所有梨换成香蕉，梨形小车换成香蕉车，梨树换成香蕉树", refs)
	for _, want := range []string{"中的梨替换成 @图片", "中的黄色香蕉", "中的梨形小车替换成 @图片", "梨树", "香蕉树"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// 名额不够时先少带元素图，每类商品 / 人物的唯一参考图不让位。
	tight := videoReplicationWithElements(references, elements, shot, 3)
	if len(tight) != 3 || tight[0]["resourceId"] != "p1" || tight[1]["resourceId"] != "m1" {
		t.Fatalf("targets must keep their slots: %#v", tight)
	}
	params := VideoReplicationParameters{MaxReferenceImages: 9, MaxReferenceVideos: 1, ReferenceMode: "reference"}
	if got := videoReplicationElementLimit("product,model", params); got != 7 {
		t.Fatalf("element limit = %d, want 7", got)
	}
	if !videoReplicationElementsMissing(elements) || videoReplicationElementsMissing(elements[:2]) {
		t.Fatal("missing element detection")
	}
}

func TestVideoReplicationOtherShotElementsDoNotDisplaceOptionalAngles(t *testing.T) {
	references := []map[string]string{
		{"resourceId": "p1", "category": model.VideoReplicationAssetProductImage},
		{"resourceId": "m1", "category": model.VideoReplicationAssetModelImage},
		{"resourceId": "detail1", "category": model.VideoReplicationAssetProductDetailImage},
		{"resourceId": "pack1", "category": model.VideoReplicationAssetPackagingImage},
		{"resourceId": "p2", "category": model.VideoReplicationAssetProductImage},
	}
	elements := normalizeVideoReplicationElements([]VideoReplicationElement{
		{From: "花瓶", To: "花篮", ResourceID: "other"},
		{From: "梨", To: "香蕉", ResourceID: "current"},
	})
	refs := videoReplicationWithElements(references, elements, VideoReplicationShot{SceneEdits: "桌上梨→香蕉"}, 6)
	if len(refs) != 6 || refs[4]["resourceId"] != "p2" || refs[5]["resourceId"] != "current" {
		t.Fatalf("unrelated element must not evict an available product angle: %#v", refs)
	}
}
