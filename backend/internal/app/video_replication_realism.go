package app

import (
	"strings"

	"yingce/backend/internal/model"
)

const videoReplicationFabricPhysics = "服饰物理：布料按材质厚薄、重力和身体接触自然垂坠；走动、转身或风吹时衣摆与头发有惯性延迟和逐渐衰减的摆动，褶皱随受力连续变化，不无风漂浮、不贴成硬壳、不穿透身体，图案随布面透视弯曲而不游走。"

const videoReplicationBackgroundPhysics = "背景融合：按原镜头运动保持新环境的透视、视差和遮挡连续；人物与商品的受光、色温、接触阴影、反射及景深匹配新环境，脚底和商品落点接地；发丝边缘无抠图光晕，不出现背景滑动、主体漂浮或静止贴片。"

// 在最终请求中独立预留这些短规则，避免长台词或逐段描述截掉物理与素材职责。
func videoReplicationVisualContract(videoType string, params VideoReplicationParameters, references []map[string]string) string {
	rules := []string{"原片有人时：人物身份按替换清单处理，保留真实皮肤纹理，保持自然呼吸、眨眼和目光落点，眉眼、脸颊、下颌随情绪和说话协调联动；动作有重心、支撑和惯性，手指接触真实，不僵硬、不假笑、不机器人念稿、不蜡像脸；原片无人时不新增人物。"}
	if videoReplicationHasTarget(videoType, VideoReplicationTypeProduct) {
		if params.ProductName != "" {
			rules = append(rules, videoReplicationProductNameRule(params.ProductName))
		}
		product := videoReplicationEditImageTokens(references, model.VideoReplicationAssetProductImage)
		if product != "" {
			rules = append(rules, product+" 提供新商品本体；按它的真实比例、结构和材质整体替换原商品，保持抓握、碰撞、遮挡与运动连续，不能只换颜色或标签。")
		}
		if details := videoReplicationEditImageTokens(references, model.VideoReplicationAssetProductDetailImage); details != "" {
			rules = append(rules, details+" 只提供同一新商品的细节和拆解结构，原片的特写、拆装与剖面展示改为对应新部件；不把拆解图当另一件商品，不凭空添加内部结构。")
		}
		if packaging := videoReplicationEditImageTokens(references, model.VideoReplicationAssetPackagingImage); packaging != "" {
			rules = append(rules, packaging+" 只替换原片已出现的对应包装，保持开合、厚度与遮挡；无包装镜头不新增包装，不把包装贴到商品本体。")
		}
	}
	if videoReplicationHasTarget(videoType, VideoReplicationTypeApparel) {
		rules = append(rules, videoReplicationFabricPhysics)
		rules = append(rules, videoReplicationProductMaterialRule(
			videoReplicationEditImageTokens(references, model.VideoReplicationAssetApparelImage),
			videoReplicationEditImageTokens(references, model.VideoReplicationAssetProductDetailImage),
			videoReplicationEditImageTokens(references, model.VideoReplicationAssetPackagingImage)))
	}
	if videoReplicationHasTarget(videoType, VideoReplicationTypeBackground) {
		rules = append(rules, videoReplicationBackgroundPhysics)
	}
	return strings.Join(rules, "\n")
}
