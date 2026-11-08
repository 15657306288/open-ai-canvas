package app

import (
	"fmt"
	"strings"

	"yingce/backend/internal/model"
)

// 定位图只描述原片里要换的对象，不参与新商品外观或首帧选择。
const videoReplicationProductLockCategory = "product_lock_anchor"

func videoReplicationProductLocked(params VideoReplicationParameters) bool {
	return params.ReplaceMode == "precise-product" && len(params.Regions) > 0
}

func videoReplicationProductRequiredCategories(videoType string, assets []model.VideoReplicationAsset) []string {
	categories := []string{}
	for _, target := range videoReplicationTargets(videoType) {
		categories = append(categories, videoReplicationTargetCategory[target])
	}
	if videoReplicationHasTarget(videoType, VideoReplicationTypeProduct) {
		for _, category := range []string{model.VideoReplicationAssetProductDetailImage, model.VideoReplicationAssetPackagingImage} {
			for _, asset := range assets {
				if asset.Category == category {
					categories = append(categories, category)
					break
				}
			}
		}
	}
	return categories
}

// 为锁定帧预留一个真实参考图名额，不能因此挤掉用户已上传的细节或包装类别。
func videoReplicationProductLockReferenceParams(videoType string, params VideoReplicationParameters, assets []model.VideoReplicationAsset) (VideoReplicationParameters, error) {
	if !videoReplicationProductLocked(params) {
		return params, nil
	}
	if !videoReplicationEditMode(params) {
		return params, BadAuthRequest("商品框选锁定需要支持参考视频编辑的模型，请更换模型后重新分析")
	}
	needed := len(videoReplicationProductRequiredCategories(videoType, assets)) + 1
	if params.MaxReferenceImages < needed {
		return params, BadAuthRequest(fmt.Sprintf("商品框选锁定需要至少 %d 张参考图名额（含定位图、替换素材及已上传的细节/包装图），当前模型名额不足，请更换模型", needed))
	}
	params.MaxReferenceImages--
	return params, nil
}

func videoReplicationValidateProductLockReferences(videoType string, params VideoReplicationParameters, assets []model.VideoReplicationAsset, references []map[string]string) error {
	if !videoReplicationProductLocked(params) {
		return nil
	}
	for _, category := range videoReplicationProductRequiredCategories(videoType, assets) {
		found := false
		for _, reference := range references {
			if reference["category"] == category && strings.TrimSpace(reference["resourceId"]) != "" {
				found = true
				break
			}
		}
		if !found {
			return BadAuthRequest("商品框选锁定缺少" + videoReplicationCategoryLabels[category] + "参考图名额，请更换支持更多参考图的模型")
		}
	}
	return nil
}

func (s *Service) videoReplicationWithProductLockReference(project *model.VideoReplicationProject, params VideoReplicationParameters, assets []model.VideoReplicationAsset, references []map[string]string) ([]map[string]string, error) {
	if !videoReplicationProductLocked(params) {
		return references, nil
	}
	if _, err := videoReplicationProductLockReferenceParams(project.VideoType, params, assets); err != nil {
		return nil, err
	}
	if err := videoReplicationValidateProductLockReferences(project.VideoType, params, assets, references); err != nil {
		return nil, err
	}
	analysis := videoReplicationSavedAnalysis(project)
	if analysis == nil || analysis.MediaDigest != videoReplicationAnalysisDigest(assets, params) || strings.TrimSpace(analysis.RegionTarget) == "" || strings.TrimSpace(analysis.RegionFrameResourceID) == "" {
		return nil, BadAuthRequest("商品框选定位结果已失效，请重新分析参考视频后再生成")
	}
	resource, err := s.repo.ResourceForUser(project.UserID, analysis.RegionFrameResourceID)
	if err != nil || resource == nil || resource.Status != model.ResourceStatusReady || resource.Kind != "image" {
		return nil, BadAuthRequest("商品框选定位图不可用，请重新分析参考视频后再生成")
	}
	if len(references) >= params.MaxReferenceImages {
		return nil, BadAuthRequest("参考图名额不足，无法发送商品框选定位图，请更换支持更多参考图的模型")
	}
	out := append([]map[string]string{}, references...)
	return append(out, map[string]string{"resourceId": resource.ID, "category": videoReplicationProductLockCategory, "label": "原视频商品框选定位图（只定位，不作商品外观或首帧）", "role": "reference"}), nil
}

func videoReplicationProductLockRule(params VideoReplicationParameters, references []map[string]string) string {
	if !videoReplicationProductLocked(params) {
		return ""
	}
	for index, reference := range references {
		if reference["category"] == videoReplicationProductLockCategory && reference["role"] == "reference" && strings.TrimSpace(reference["resourceId"]) != "" {
			return fmt.Sprintf("商品框选定位：@图片%d 是原视频 %.2f 秒处的定位帧，%d 个红框只标识要替换的原商品。%s。只跟踪这些对象及其对应部件和包装，在移动、旋转、遮挡后再出现和镜头切换时保持同一目标，不把固定屏幕坐标当成目标，不替换后来进入原框位置的其他物品；没框的同类商品保持原样，目标离开画面时不凭空补出。定位图只用于辨认目标，禁止复制其原商品外观、红框、标注、背景或定格画面到成片；新商品外观、细节和包装分别取对应上传参考图。", index+1, float64(params.RegionFrameMs)/1000, len(params.Regions), strings.TrimSpace(params.RegionTarget))
		}
	}
	return ""
}
