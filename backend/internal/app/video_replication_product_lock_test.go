package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func productLockTestParams() VideoReplicationParameters {
	return VideoReplicationParameters{ReplaceMode: "precise-product", ReferenceMode: "reference", MaxReferenceVideos: 1, MaxReferenceImages: 4, RegionFrameMs: 1500, Regions: []VideoReplicationRegion{{X: 0.1, Y: 0.2, W: 0.2, H: 0.3}, {X: 0.6, Y: 0.2, W: 0.2, H: 0.3}}, RegionTarget: "左侧及右侧的两瓶洗发水"}
}

func TestVideoReplicationProductLockRequiresEverySelectedObject(t *testing.T) {
	params := productLockTestParams()
	positions := []string{"框1在左侧", "框2在右侧"}
	for name, targets := range map[string][]string{
		"empty": nil, "partial": {"左侧粉瓶"}, "extra": {"左侧粉瓶", "右侧粉瓶", "蓝瓶"},
		"blank": {"左侧粉瓶", ""}, "uncertain": {"左侧粉瓶", "无法确认"}, "url": {"左侧粉瓶", "https://invalid.example"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := videoReplicationDescribeRegionsResult(params, videoReplicationRegionDescription{Targets: targets}, positions, "fallback"); err == nil {
				t.Fatal("incomplete product selection must stop instead of silently replacing fewer objects")
			}
		})
	}
	text, err := videoReplicationDescribeRegionsResult(params, videoReplicationRegionDescription{Targets: []string{"左侧粉瓶", "右侧粉瓶"}, Keep: "中间蓝瓶"}, positions, "fallback")
	if err != nil || !strings.Contains(text, "2 个") || !strings.Contains(text, "中间蓝瓶保持原样") {
		t.Fatalf("complete selection = %q, %v", text, err)
	}
	params.ReplaceMode = "precise-person"
	if text, err := videoReplicationDescribeRegionsResult(params, videoReplicationRegionDescription{}, positions, "fallback"); err != nil || text != "fallback" {
		t.Fatalf("existing person selection fallback changed: %q %v", text, err)
	}
}

func TestVideoReplicationProductLockRejectsMissingFrameAndInvalidTrim(t *testing.T) {
	params := productLockTestParams()
	svc := &Service{}
	missing := filepath.Join(t.TempDir(), "missing.jpg")
	if _, err := svc.describeVideoReplicationRegions(context.Background(), ecommerceProviderConfig{}, "project", missing, params); err == nil {
		t.Fatal("missing product anchor must fail")
	}
	params.ReplaceMode = "precise-person"
	if text, err := svc.describeVideoReplicationRegions(context.Background(), ecommerceProviderConfig{}, "project", missing, params); err != nil || text == "" {
		t.Fatalf("person fallback changed: %q %v", text, err)
	}
	params = productLockTestParams()
	params.TrimStartMs, params.TrimEndMs = 2000, 8000
	if err := validateVideoReplicationClip(&params); err == nil {
		t.Fatal("product frame outside retained trim must fail")
	}
	params.RegionFrameMs = 8000
	if err := validateVideoReplicationClip(&params); err == nil {
		t.Fatal("trim end is exclusive")
	}
	params.RegionFrameMs = 2000
	if err := validateVideoReplicationClip(&params); err != nil {
		t.Fatal(err)
	}
}

func TestVideoReplicationProductLockReservesRealReferenceCapacity(t *testing.T) {
	assets := []model.VideoReplicationAsset{
		{ID: "product", ResourceID: "product-image", Category: model.VideoReplicationAssetProductImage},
		{ID: "detail", ResourceID: "detail-image", Category: model.VideoReplicationAssetProductDetailImage},
		{ID: "package", ResourceID: "package-image", Category: model.VideoReplicationAssetPackagingImage},
	}
	params := productLockTestParams()
	selected, err := videoReplicationProductLockReferenceParams("product", params, assets)
	if err != nil || selected.MaxReferenceImages != 3 || params.MaxReferenceImages != 4 {
		t.Fatalf("reserve = %+v, %v", selected, err)
	}
	refs := videoReplicationReferences("product", selected, assets, nil)
	if err := videoReplicationValidateProductLockReferences("product", params, assets, refs); err != nil {
		t.Fatal(err)
	}
	if err := videoReplicationValidateProductLockReferences("product", params, assets, refs[:2]); err == nil {
		t.Fatal("uploaded packaging cannot silently disappear")
	}
	params.MaxReferenceImages = 3
	if _, err := videoReplicationProductLockReferenceParams("product", params, assets); err == nil {
		t.Fatal("insufficient capacity must not drop detail or packaging to make room for anchor")
	}
	for _, mode := range []string{"first_frame", "none"} {
		params = productLockTestParams()
		params.ReferenceMode = mode
		if _, err := videoReplicationProductLockReferenceParams("product", params, assets); err == nil {
			t.Fatalf("%s cannot perform product lock", mode)
		}
	}
	params = productLockTestParams()
	params.MaxReferenceVideos = 0
	if _, err := videoReplicationProductLockReferenceParams("product", params, assets); err == nil {
		t.Fatal("precise product lock needs original video input")
	}
	params.ReplaceMode = "precise-person"
	if _, err := videoReplicationProductLockReferenceParams("model", params, assets); err != nil {
		t.Fatal("person behavior must stay unchanged", err)
	}
}

func TestVideoReplicationProductLockReferenceRequiresOwnedReadyImage(t *testing.T) {
	svc, db := mockEcommerceAgentService(t)
	params := productLockTestParams()
	assets := []model.VideoReplicationAsset{{ID: "p", ResourceID: "new-product", Category: model.VideoReplicationAssetProductImage}}
	analysis := VideoReplicationAnalysis{Videos: []VideoReplicationVideoAnalysis{{AssetID: "source-video", DurationMs: 5000}}, MediaDigest: videoReplicationAnalysisDigest(assets, params), RegionTarget: params.RegionTarget, RegionFrameResourceID: "anchor"}
	encoded, _ := json.Marshal(analysis)
	project := &model.VideoReplicationProject{ID: "project", UserID: "owner", VideoType: "product", AnalysisJSON: string(encoded)}
	refs := []map[string]string{{"resourceId": "new-product", "category": model.VideoReplicationAssetProductImage, "role": "reference"}}
	if err := db.Create(&model.Resource{ID: "anchor", UserID: "someone-else", Kind: "image", Status: model.ResourceStatusReady}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.videoReplicationWithProductLockReference(project, params, assets, refs); err == nil || !strings.Contains(err.Error(), "定位图不可用") {
		t.Fatalf("another user's anchor must not be sent: %v", err)
	}
	if err := db.Model(&model.Resource{}).Where("id = ?", "anchor").Updates(map[string]any{"user_id": "owner", "status": "pending"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.videoReplicationWithProductLockReference(project, params, assets, refs); err == nil || !strings.Contains(err.Error(), "定位图不可用") {
		t.Fatalf("pending anchor must not be sent: %v", err)
	}
	if err := db.Model(&model.Resource{}).Where("id = ?", "anchor").Update("status", model.ResourceStatusReady).Error; err != nil {
		t.Fatal(err)
	}
	got, err := svc.videoReplicationWithProductLockReference(project, params, assets, refs)
	if err != nil || len(got) != 2 || len(refs) != 1 || got[1]["resourceId"] != "anchor" || got[1]["role"] != "reference" || got[1]["category"] != videoReplicationProductLockCategory {
		t.Fatalf("anchor must be appended as a separate reference without mutating source refs: %+v %v", got, err)
	}
	if rule := videoReplicationProductLockRule(params, got); !strings.Contains(rule, "@图片2") || !strings.Contains(rule, "2 个红框") || !strings.Contains(rule, "不把固定屏幕坐标当成目标") || !strings.Contains(rule, "禁止复制其原商品外观、红框") {
		t.Fatalf("anchor rule must bind to actual image index: %s", rule)
	}
	for _, invalid := range []map[string]string{
		{"category": videoReplicationProductLockCategory, "role": "reference"},
		{"category": videoReplicationProductLockCategory, "role": "first_frame", "resourceId": "anchor"},
	} {
		if rule := videoReplicationProductLockRule(params, []map[string]string{invalid}); rule != "" {
			t.Fatalf("invalid anchor must not create a successful lock contract: %q", rule)
		}
	}
	for name, broken := range map[string]VideoReplicationAnalysis{
		"missing frame":  {MediaDigest: analysis.MediaDigest, RegionTarget: analysis.RegionTarget},
		"missing target": {MediaDigest: analysis.MediaDigest, RegionFrameResourceID: "anchor"},
		"old digest":     {MediaDigest: "old-analysis", RegionTarget: analysis.RegionTarget, RegionFrameResourceID: "anchor"},
	} {
		t.Run(name, func(t *testing.T) {
			broken.Videos = analysis.Videos
			brokenJSON, _ := json.Marshal(broken)
			copy := *project
			copy.AnalysisJSON = string(brokenJSON)
			if _, err := svc.videoReplicationWithProductLockReference(&copy, params, assets, refs); err == nil {
				t.Fatal("missing or stale positioning must not become successful references")
			}
		})
	}
	params.RegionFrameMs++
	if _, err := svc.videoReplicationWithProductLockReference(project, params, assets, refs); err == nil {
		t.Fatal("changed selection must invalidate saved anchor")
	}
}

func TestVideoReplicationProductLockElementsKeepRequiredReferenceRoles(t *testing.T) {
	refs := []map[string]string{
		{"resourceId": "product", "category": model.VideoReplicationAssetProductImage, "role": "reference"},
		{"resourceId": "detail", "category": model.VideoReplicationAssetProductDetailImage, "role": "reference"},
		{"resourceId": "packaging", "category": model.VideoReplicationAssetPackagingImage, "role": "reference"},
		{"resourceId": "anchor", "category": videoReplicationProductLockCategory, "role": "reference"},
	}
	elements := []VideoReplicationElement{{ID: "e1", ResourceID: "tree", From: "梨树", To: "苹果树"}}
	shot := VideoReplicationShot{SceneEdits: "背景梨树→苹果树"}
	for _, limit := range []int{4, 5} {
		got := videoReplicationWithElements(refs, elements, shot, limit)
		if len(got) > limit {
			t.Fatalf("limit %d exceeded: %+v", limit, got)
		}
		for _, required := range []string{"product", "detail", "packaging", "anchor"} {
			found := false
			for _, ref := range got {
				found = found || ref["resourceId"] == required
			}
			if !found {
				t.Fatalf("limit %d lost required reference %s: %+v", limit, required, got)
			}
		}
	}
	withExtra := append(append([]map[string]string{}, refs...), map[string]string{"resourceId": "detail-second-angle", "category": model.VideoReplicationAssetProductDetailImage, "role": "reference"})
	got := videoReplicationWithElements(withExtra, elements, shot, 5)
	if len(got) != 5 || got[4]["resourceId"] != "detail-second-angle" || got[3]["resourceId"] != "anchor" {
		t.Fatalf("every uploaded detail and the anchor must retain its slot: %+v", got)
	}
	assets := []model.VideoReplicationAsset{
		{Category: model.VideoReplicationAssetProductImage, ResourceID: "product"},
		{Category: model.VideoReplicationAssetProductDetailImage, ResourceID: "detail"},
		{Category: model.VideoReplicationAssetProductDetailImage, ResourceID: "detail-second-angle"},
		{Category: model.VideoReplicationAssetPackagingImage, ResourceID: "packaging"},
	}
	if err := videoReplicationValidateReferences("product", assets, got); err != nil {
		t.Fatalf("locked references must pass the actual submission validator: %v", err)
	}
	plain := append(append([]map[string]string{}, refs[:3]...), withExtra[4])
	plain = append(plain, map[string]string{"resourceId": "product-second-angle", "category": model.VideoReplicationAssetProductImage, "role": "reference"})
	got = videoReplicationWithElements(plain, elements, shot, 5)
	if err := videoReplicationValidateReferences("product", assets, got); err != nil {
		t.Fatalf("ordinary product replacement must also retain every uploaded detail and packaging image: %v", err)
	}
	if len(got) != 5 || got[4]["resourceId"] != "tree" {
		t.Fatalf("only the optional product angle should yield to the current scene element: %+v", got)
	}
}

func TestVideoReplicationProductLockStructureCannotGuessMissingSegments(t *testing.T) {
	valid := []VideoReplicationStructureSegment{{Index: 1, Source: "手持框选粉瓶", Presence: "hand"}, {Index: 2, Source: "目标离开画面", Presence: "none"}}
	if err := videoReplicationValidateProductLockStructure(valid, 2); err != nil {
		t.Fatal(err)
	}
	for name, segments := range map[string][]VideoReplicationStructureSegment{
		"missing":     valid[:1],
		"duplicate":   {valid[0], valid[0]},
		"unknown":     {{Index: 1, Source: "无法确认哪个瓶子", Presence: "hand"}, valid[1]},
		"no presence": {{Index: 1, Source: "手持瓶子"}, valid[1]},
	} {
		t.Run(name, func(t *testing.T) {
			if err := videoReplicationValidateProductLockStructure(segments, 2); err == nil {
				t.Fatal("unresolved target tracking must stop instead of general product replacement")
			}
		})
	}
}
