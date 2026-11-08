package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"yingce/backend/internal/model"
)

// 结构对齐：用户的商品 / 服装和参考视频里的往往结构不同（例如原片吹风机是环形出风口、从手柄底部拔出长滤芯，
// 新吹风机是格栅出风口、尾部旋拧滤网盖；原片外套有侧袋、模特把手插进口袋，新外套没有口袋）。只给模型一句
// 「替换成参考图里的商品 / 服装」时，它会照搬原片的动作，把原商品的部件画到新商品上、让手插进不存在的口袋。
// 这里在分段规划前让识图模型同时看「每段原片的关键帧」和「新商品 / 新服装的参考图」，逐段写出：原片这段在展示
// 哪个部件、新商品上对应的是什么、这一段改成怎样的动作与镜头；服装还给出正确穿法和整体搭配，再写进每段的生成提示词。

const (
	// videoReplicationStructureImageLimit 是一次结构对齐请求的图片总数上限（部分接口单条消息最多 16 张）。
	videoReplicationStructureImageLimit = 16
	// videoReplicationStructureFrameLimit 是参考视频关键帧的上限，剩余名额留给商品图。
	videoReplicationStructureFrameLimit = 10
	// videoReplicationStructureAssetLimit 是商品 / 服装 / 细节 / 包装 / 模特图合计上限，保证关键帧至少有 8 张。
	videoReplicationStructureAssetLimit = 8
	videoReplicationStructureProductMax = 3
	videoReplicationStructureDetailMax  = 3
	videoReplicationStructureApparelMax = 3
)

type VideoReplicationStructure struct {
	SourceProduct string `json:"sourceProduct,omitempty"`
	NewProduct    string `json:"newProduct,omitempty"`
	Packaging     string `json:"packaging,omitempty"`
	// SourceLook 是原商品在画面里的外观（形状、颜色、盖子、标签），原片编辑时用它让视频模型认准要换掉的东西；
	// 只写「主要商品」时模型常认不出，只给原商品调个色就交差。
	SourceLook string `json:"sourceLook,omitempty"`
	// NewSubject 是新商品图里真正要放进视频的商品本体；ReferenceProps 是图里不属于商品的东西（包装盒、道具、桌面、背景），
	// 商品图是带场景的宣传图时，模型会把整张图的布景搬进视频，必须点名排除。
	NewSubject     string `json:"newSubject,omitempty"`
	ReferenceProps string `json:"referenceProps,omitempty"`
	// Apparel 是新服装的结构（口袋、门襟、领型、袖长、衣长、版型等）；Wearing 是正确穿法；Styling 是整体搭配。
	Apparel string `json:"apparel,omitempty"`
	Wearing string `json:"wearing,omitempty"`
	Styling string `json:"styling,omitempty"`
	// Banned 是用户补充要求换掉、全片都不能再出现的原元素。
	Banned string `json:"banned,omitempty"`
	// Swaps 是全片统一的元素替换清单（每条一张元素参考图）。
	Swaps    []VideoReplicationStructureSwap    `json:"swaps,omitempty"`
	Segments []VideoReplicationStructureSegment `json:"segments"`
	Model    string                             `json:"model,omitempty"`
}

// VideoReplicationStructureSwap 是一条全片统一的元素替换；Kind 只收 scene（与商品无关的景物 / 道具 / 装饰）。
type VideoReplicationStructureSwap struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind,omitempty"`
}

type VideoReplicationStructureSegment struct {
	Index  int    `json:"index"`
	Source string `json:"source"`
	Match  string `json:"match"`
	Action string `json:"action"`
	// Camera 是这一段需要调整的镜头（原片特写的部位新商品上没有时改拍哪里）；不用调整时为空。
	Camera    string `json:"camera,omitempty"`
	Packaging bool   `json:"packaging"`
	// Presence 是原商品在这一段的存在感：hero / hand / minor / none。
	Presence string `json:"presence,omitempty"`
	// Edits 是本段画面里看得到、要按用户补充要求改掉的元素，逐个写位置（「左上飘浮的黄梨→一根黄色香蕉；…」）。
	Edits string `json:"edits,omitempty"`
}

// 原商品在一段画面里的存在感。商品只在边缘或背景出现时，视频模型拿到商品图后常把整段改成商品展示特写，
// 所以每段按存在感约束新商品出现的位置和大小。
const (
	videoReplicationPresenceHero  = "hero"
	videoReplicationPresenceHand  = "hand"
	videoReplicationPresenceMinor = "minor"
	videoReplicationPresenceNone  = "none"
)

func normalizeVideoReplicationPresence(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case videoReplicationPresenceHero, videoReplicationPresenceHand, videoReplicationPresenceMinor, videoReplicationPresenceNone:
		return value
	}
	return ""
}

// videoReplicationNeedsStructure：替换商品或服装、或者用户补充要求里要改画面元素时，才需要结构对齐（看关键帧逐段点名）。
func videoReplicationNeedsStructure(videoType, brief string) bool {
	return videoReplicationHasTarget(videoType, VideoReplicationTypeProduct) || videoReplicationHasTarget(videoType, VideoReplicationTypeApparel) || videoReplicationBriefEditsScene(brief)
}

// videoReplicationBriefEditsScene：补充要求里有改画面的意图（换 / 改 / 替换 / 变成……）。只写「跑鞋复刻」这类不改画面的要求时
// 不让识图自己发挥列改动，否则它会把原商品的部件、Logo、背景文字都当成要换的元素。
func videoReplicationBriefEditsScene(brief string) bool {
	brief = strings.TrimSpace(brief)
	for _, key := range []string{"换", "改", "替换", "变成", "变为", "去掉", "删掉", "删除", "加上", "增加", "添加"} {
		if strings.Contains(brief, key) {
			return true
		}
	}
	return false
}

// videoReplicationStructureFrames 为每段挑选参考视频关键帧：先保证每段一张（与本段重叠最长的镜头），名额有剩再轮流补第二、第三张。
func videoReplicationStructureFrames(video VideoReplicationVideoAnalysis, segments []videoReplicationAutoSegment, budget int) [][]VideoReplicationSourceShot {
	candidates := make([][]VideoReplicationSourceShot, len(segments))
	for index, segment := range segments {
		type scored struct {
			shot    VideoReplicationSourceShot
			overlap int64
		}
		items := []scored{}
		for _, shot := range video.Shots {
			overlap := min(shot.EndMs, segment.EndMs) - max(shot.StartMs, segment.StartMs)
			if shot.KeyframeResourceID != "" && overlap > 0 {
				items = append(items, scored{shot, overlap})
			}
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].overlap > items[j].overlap })
		for _, item := range items {
			candidates[index] = append(candidates[index], item.shot)
		}
	}
	picked := make([][]VideoReplicationSourceShot, len(segments))
	total := 0
	for round := 0; total < budget; round++ {
		added := false
		for index := range segments {
			if round < len(candidates[index]) && total < budget {
				picked[index] = append(picked[index], candidates[index][round])
				total++
				added = true
			}
		}
		if !added {
			break
		}
	}
	for index := range picked {
		sort.SliceStable(picked[index], func(i, j int) bool { return picked[index][i].StartMs < picked[index][j].StartMs })
	}
	return picked
}

// alignVideoReplicationStructure 调用识图模型做逐段结构对齐。没有商品图也没有服装图时返回 nil。
func (s *Service) alignVideoReplicationStructure(ctx context.Context, project *model.VideoReplicationProject, params VideoReplicationParameters, assets []model.VideoReplicationAsset, analysis *VideoReplicationAnalysis, segments []videoReplicationAutoSegment) (*VideoReplicationStructure, error) {
	if len(analysis.Videos) == 0 || len(segments) == 0 {
		return nil, nil
	}
	swapProduct := videoReplicationHasTarget(project.VideoType, VideoReplicationTypeProduct)
	swapApparel := videoReplicationHasTarget(project.VideoType, VideoReplicationTypeApparel)
	var products, details, packaging, apparel, wearers []VideoReplicationAssetView
	for _, view := range videoReplicationAssetViews(assets) {
		switch view.Category {
		case model.VideoReplicationAssetProductImage:
			if swapProduct {
				products = append(products, view)
			}
		case model.VideoReplicationAssetProductDetailImage:
			if swapProduct {
				details = append(details, view)
			}
		case model.VideoReplicationAssetPackagingImage:
			if swapProduct {
				packaging = append(packaging, view)
			}
		case model.VideoReplicationAssetApparelImage:
			if swapApparel {
				apparel = append(apparel, view)
			}
		case model.VideoReplicationAssetModelImage:
			wearers = append(wearers, view)
		}
	}
	userBrief := strings.TrimSpace(videoReplicationReadablePrompt(project.Prompt, assets))
	editsScene := videoReplicationBriefEditsScene(userBrief)
	if len(products) == 0 && len(apparel) == 0 && !editsScene {
		return nil, nil
	}
	primaryFirst := func(views []VideoReplicationAssetView) {
		sort.SliceStable(views, func(i, j int) bool { return views[i].IsPrimary && !views[j].IsPrimary })
	}
	primaryFirst(products)
	primaryFirst(apparel)
	primaryFirst(wearers)
	products = products[:min(len(products), videoReplicationStructureProductMax)]
	apparel = apparel[:min(len(apparel), videoReplicationStructureApparelMax)]
	details = details[:min(len(details), videoReplicationStructureDetailMax)]
	packaging = packaging[:min(len(packaging), 1)]
	// 服装搭配要看穿在谁身上：替换服装且有模特图时带一张。
	if len(apparel) == 0 {
		wearers = nil
	}
	wearers = wearers[:min(len(wearers), 1)]

	cfg, err := s.videoReplicationTextConfig(project.UserID, true)
	if err != nil {
		return nil, err
	}
	if err := validateVideoReplicationVisionProtocol(cfg); err != nil {
		return nil, err
	}
	cfg.TextTimeout = 3 * time.Minute

	images, labels := []string{}, []string{}
	addImage := func(resourceID, label string) error {
		dataURL, err := s.videoReplicationImageDataURL(ctx, project.UserID, resourceID)
		if err != nil {
			return fmt.Errorf("%s：%w", label, err)
		}
		images = append(images, dataURL)
		labels = append(labels, fmt.Sprintf("图%d=%s", len(images), label))
		return nil
	}
	if videoReplicationProductLocked(params) {
		if strings.TrimSpace(analysis.RegionFrameResourceID) == "" || strings.TrimSpace(analysis.RegionTarget) == "" {
			return nil, BadAuthRequest("商品框选定位结果不完整，请重新分析参考视频")
		}
		if err := addImage(analysis.RegionFrameResourceID, "原片商品框选定位图（红框只标记要替换的原商品，不是新商品图）"); err != nil {
			return nil, err
		}
	}
	names := map[string]string{
		model.VideoReplicationAssetProductImage:       "新商品图",
		model.VideoReplicationAssetApparelImage:       "新服装图",
		model.VideoReplicationAssetProductDetailImage: "新商品细节/拆解图",
		model.VideoReplicationAssetPackagingImage:     "新商品包装图",
		model.VideoReplicationAssetModelImage:         "模特图",
	}
	// 要逐段点名画面元素时，关键帧要多给：每段至少一张，素材图让出名额。
	assetLimit := videoReplicationStructureAssetLimit
	frameLimit := videoReplicationStructureFrameLimit
	if editsScene {
		assetLimit = max(3, min(assetLimit, videoReplicationStructureImageLimit-min(len(segments), 12)))
		frameLimit = max(frameLimit, min(len(segments), 12))
	}
	added := 0
	for _, group := range [][]VideoReplicationAssetView{products, apparel, details, packaging, wearers} {
		for _, view := range group {
			if added >= assetLimit {
				break
			}
			if err := addImage(view.ResourceID, names[view.Category]+"（"+view.Label+"）"); err != nil {
				return nil, err
			}
			added++
		}
	}
	budget := min(frameLimit, videoReplicationStructureImageLimit-len(images))
	type segmentContext struct {
		Index  int      `json:"index"`
		Start  float64  `json:"startSec"`
		End    float64  `json:"endSec"`
		Frames []string `json:"frames"`
		Shots  []string `json:"referenceShots"`
	}
	contexts := make([]segmentContext, 0, len(segments))
	primary := analysis.Videos[0]
	for index, frames := range videoReplicationStructureFrames(primary, segments, budget) {
		segment := segments[index]
		item := segmentContext{Index: index + 1, Start: segment.Start, End: segment.End}
		for _, shot := range frames {
			// 关键帧读取失败只少一张图，不影响其他段。
			if err := addImage(shot.KeyframeResourceID, fmt.Sprintf("原片第%d段 %.1fs 关键帧", index+1, float64((shot.StartMs+shot.EndMs)/2)/1000)); err != nil {
				slog.Warn("video replication structure keyframe skipped", "projectId", project.ID, "error", err)
				continue
			}
			item.Frames = append(item.Frames, fmt.Sprintf("图%d", len(images)))
		}
		for _, shot := range primary.Shots {
			if shot.StartMs < segment.EndMs && shot.EndMs > segment.StartMs {
				parts := []string{}
				for _, value := range []string{shot.ShotSize, shot.Action, shot.ProductDisplay, shot.PersonAction} {
					if value = strings.TrimSpace(value); value != "" && value != "无" {
						parts = append(parts, value)
					}
				}
				if len(parts) > 0 {
					item.Shots = append(item.Shots, fmt.Sprintf("%.1f-%.1fs：%s", float64(shot.StartMs)/1000, float64(shot.EndMs)/1000, strings.Join(parts, "；")))
				}
			}
		}
		if videoReplicationProductLocked(params) && len(item.Frames) == 0 {
			return nil, BadAuthRequest(fmt.Sprintf("第 %d 段缺少可核对框选商品的画面，请重新分析或缩短参考视频", index+1))
		}
		contexts = append(contexts, item)
	}
	targets := []string{}
	if len(products) > 0 {
		targets = append(targets, "商品")
	}
	if len(apparel) > 0 {
		targets = append(targets, "服装")
	}
	// 元素清单条数跟随视频模型的参考图名额（每条一张元素参考图），读不到时按 6 条。
	elementLimit := videoReplicationElementLimit(project.VideoType, params)
	replace := strings.Join(targets, "、")
	if replace == "" {
		replace = "无（只按 userBrief 改画面元素）"
	}
	brief := map[string]any{
		"replace": replace, "segments": contexts, "sourceProductHint": strings.TrimSpace(params.SubjectHint), "productName": params.ProductName, "lockedTargets": analysis.RegionTarget,
		"userBrief": userBrief, "newProductProfile": analysis.Product, "modelProfile": analysis.Model,
	}
	if videoReplicationProductLocked(params) {
		brief["lockedTargetCount"] = len(params.Regions)
	}
	contextJSON, _ := json.Marshal(brief)
	system := "你是电商商品视频的结构对齐师兼服装搭配师，熟悉服装、鞋包、美妆个护、家电数码、食品、家居、母婴、玩具等各品类。对比参考视频里的原商品 / 原服装和用户的新商品 / 新服装，只描述画面里看得到的结构与动作，不复述品牌名，不识别真实人物身份。输出严格 JSON。"
	prompt := fmt.Sprintf(`附带图片：%s。
任务：参考视频要逐段复刻（结构对齐目标：%s），只分析商品 / 服装的结构和动作；其它已选择替换的目标由各自素材决定，未替换的内容与镜头节奏沿用原片。原来的和新的结构可能不同：
- 商品：外形、开口 / 出风口 / 喷头、按键位置、盖子与开合方式（旋盖 / 翻盖 / 按压泵 / 滴管 / 拉链 / 磁吸）、可拆部件、配件、使用方式等；
- 服装鞋包：有无口袋及位置、门襟（拉链 / 纽扣 / 套头）、领型、帽子、袖长、衣长、版型松紧、腰带、开衩、鞋带 / 魔术贴 / 一脚蹬、包的开合与背法等。
- 食品：识别桌面、盘中、手持、袋内、入口及咬后同商品的全部实例，区分包装与可食用本体；例如沙琪玛换辣条，散装块状食物要换成新食品真实的条状形态，拆袋、拿取、咬断、咀嚼按新食品结构调整，不把食物换成包装图或凭空添加包装。不推断图片未展示的内馅、配料或功效。
上下文（JSON）：%s
请逐段判断并改写：
1. source：原片这一段人物 / 手在对原商品或原服装做什么（如「从手柄底部拔出长条滤芯」「双手插进外套侧袋」「拉上拉链」「按压泵头挤出乳液」）；同商品多处出现时写清各位置与状态（桌面多份、手持、拆袋、入口、咬后），不要只看最显眼的一件；精准框选时仅分析指定实例。
2. match：新商品 / 新服装上对应的部件（以新图为准）；没有对应部件时写「无」。
3. action：这一段改写后的动作（不超过 80 字），手部位置、节奏、走位尽量沿用原片：有对应部件就改成新商品自己的结构和用法（如「改为拧下尾部圆形滤网盖」）；没有对应部件就改成同样节奏下自然、合理的动作（如原片插口袋而新衣服没有口袋 → 双手自然垂放、轻抚衣摆或叉腰；原片拉拉链而新衣服是套头 → 整理领口），绝不凭空画出新商品 / 新服装没有的部件；原片只是常规握持、走动或展示，新旧结构没有差异时写「沿用原片动作」。
4. camera：复刻不改原片镜头，一律写空串。
5. packaging：这一段原片画面里是否出现包装盒 / 包装袋，包括桌面与手里、已打开或被遮挡的包装。出现产品参数表 / 介绍卡时在 source、action 说明如何手持、翻页或展示；新文字只能来自用户提供且清楚可读的新商品资料，不能套用旧表格的参数或编造缺失值。
6. presence（替换商品时必填）：原商品在这一段画面里的存在感——hero：原商品是画面主体（特写、静物展示、占画面主要位置）；hand：被人拿在手里 / 使用，人物仍是画面主体；minor：只在画面边缘、远处、背景里小面积出现或被虚化；none：这一段看不到原商品。
7. userBrief 里关于结构、穿法、搭配、动作的说明优先，必须遵守。
8. edits：userBrief 要求把画面里的某类元素换掉时（如「所有梨、枇杷、小梨车都换成香蕉和香蕉小推车」「树换成香蕉树」），看着这一段的关键帧，把这一段画面里看得到的每一个要换的元素都逐个点名，写清位置和新样子，格式「位置+原元素→新元素」，用「；」分隔，不超过 150 字，如「左上树枝上挂的黄梨→挂着的一根黄色香蕉；右侧画面边缘巨大的梨→同样大小的香蕉；远处浮岛草地上的三个小梨→三根小香蕉；摩天轮上的梨形吊舱→香蕉形吊舱；左侧的梨树→香蕉树」。背景里、远处、角落里、被遮挡一半、很小的也要列出来，一个都不能漏；做成该元素形状的造型物（梨形小车、梨形吊舱）改成新元素形状，保留原来的功能。同一个原元素全片只对应同一个新元素。替换目标本身（用上传素材替换的商品、人物、服装、背景）不写进 edits。userBrief 没有要求换画面元素、或这一段看不到要换的元素时写空串。
替换服装时另外给出：
- apparel：新服装的结构概述（口袋有无与位置、门襟、领型、袖长、衣长、版型、面料厚薄），不超过 150 字；
- wearing：这件衣服正常、合身的穿法（如「拉链拉到胸口」「下摆塞进裤腰」「袖口自然垂到手腕」「外套敞开露出内搭」），不超过 100 字；
- styling：整体搭配：新服装是上装就搭下装和鞋，是下装就搭上装和鞋，连衣裙搭鞋和简单配饰；选和新服装风格、颜色协调的常见款（写清单品、颜色、款式），简洁不抢主体；原片里与新服装风格冲突或被它取代的单品要换掉，不超过 120 字。
不替换服装时 apparel、wearing、styling 写空串。
替换商品时另外给出（不替换商品写空串）：
- sourceLook：原商品在画面里的外观，用来在视频里认出它（形状、材质、颜色、盖子、标签颜色与版式、大小），不写品牌名，不超过 60 字；
- newSubject：新商品图里真正要放进视频的商品本体（如「金色旋盖的玻璃罐装浓浆，红色标签」）；图里同时摆着包装盒、赠品时只写商品本体，不超过 60 字；
- referenceProps：新商品图里除商品本体以外的所有东西（包装盒、水果、杯子、勺子、花、桌面、布料、窗格、背景景物等），这些不能被搬进视频，不超过 80 字，没有写空串。
全片另给出 swaps：按 userBrief 要做的元素替换清单，全片统一、最多 %d 条，格式 [{"from":"原片里看得到的元素","to":"换成的新元素和样子","kind":"scene"}]，如 [{"from":"梨","to":"黄色香蕉","kind":"scene"},{"from":"梨形小车","to":"香蕉形小车","kind":"scene"},{"from":"梨树","to":"香蕉树","kind":"scene"}]。同类元素合并成一条（所有梨写一条「梨」），造型不同的单独一条（「梨形小车」「梨形吊舱」）；造型物保留原来的功能结构只换造型。kind 只能是 scene（与商品无关的景物 / 道具 / 装饰）；原商品本身及其部件、标签、Logo、包装、画面文字都不写。userBrief 没要求换画面元素时为空数组。
全片另给出 banned：userBrief 要求换掉、全片任何一帧都不能再出现的原元素（如「梨、枇杷、梨形小车、梨形吊舱」），不超过 40 字；userBrief 没有要求换画面元素时写空串。
输出 JSON：{"sourceProduct":"原商品 / 原服装结构概述","newProduct":"新商品结构概述（不超过 150 字，不替换商品写空串）","sourceLook":"","newSubject":"","referenceProps":"","apparel":"","wearing":"","styling":"","swaps":[],"banned":"","packaging":"原片包装出现情况与新包装建议，没有包装写无","segments":[{"index":1,"source":"","match":"","action":"","camera":"","packaging":false,"presence":"hand","edits":""}]}
segments 必须有 %d 项，index 从 1 开始与上下文的段一一对应。`, strings.Join(labels, "，"), replace, string(contextJSON), elementLimit, len(segments))
	if videoReplicationProductLocked(params) {
		prompt += "\n商品定位图只识别红框中的目标，原图中的商品不能作为新商品外观。逐段追踪同一目标及其对应的拆解部件、包装；发生移动、旋转、遮挡和镜头切换仍按外观与动作连续性对应，禁止按固定屏幕坐标套框。source、sourceLook、presence、packaging 只描述框选商品及与它明确对应的部件/包装，没框的同类商品保持不变；框选对象不在本段时 presence=none，不新增商品。无法确认目标对应关系时在 source 写明无法确认，禁止猜测。"
	}
	var result VideoReplicationStructure
	if err := s.videoReplicationChatJSON(ctx, cfg, "structure", project.ID, system, prompt, images, nil, &result); err != nil {
		return nil, err
	}
	if videoReplicationProductLocked(params) {
		if err := videoReplicationValidateProductLockStructure(result.Segments, len(segments)); err != nil {
			return nil, err
		}
	}
	byIndex := map[int]VideoReplicationStructureSegment{}
	for position, item := range result.Segments {
		if item.Index <= 0 {
			item.Index = position + 1
		}
		item.Source, item.Match, item.Action = videoReplicationClipRunes(strings.TrimSpace(item.Source), 200), videoReplicationClipRunes(strings.TrimSpace(item.Match), 120), videoReplicationClipRunes(strings.TrimSpace(item.Action), 240)
		item.Camera = videoReplicationClipRunes(strings.TrimSpace(item.Camera), 120)
		item.Presence = normalizeVideoReplicationPresence(item.Presence)
		edits := item.Edits
		item.Edits = ""
		if editsScene {
			item.Edits = videoReplicationCleanEdits(edits)
		}
		byIndex[item.Index] = item
	}
	clip := func(value string, limit int) string {
		return videoReplicationClipRunes(strings.TrimSpace(value), limit)
	}
	aligned := &VideoReplicationStructure{SourceProduct: clip(result.SourceProduct, 400), Packaging: clip(result.Packaging, 200), Model: cfg.TextModel}
	if len(products) > 0 {
		aligned.NewProduct = clip(result.NewProduct, 400)
		aligned.SourceLook, aligned.NewSubject, aligned.ReferenceProps = clip(result.SourceLook, 80), clip(result.NewSubject, 80), clip(result.ReferenceProps, 120)
	}
	if editsScene {
		aligned.Banned = clip(result.Banned, 60)
		for _, swap := range result.Swaps {
			from, to := clip(swap.From, 30), clip(swap.To, 40)
			if kind := strings.TrimSpace(swap.Kind); kind != "" && kind != "scene" {
				continue
			}
			if from != "" && to != "" && from != to && len(aligned.Swaps) < elementLimit {
				aligned.Swaps = append(aligned.Swaps, VideoReplicationStructureSwap{From: from, To: to, Kind: "scene"})
			}
		}
	}
	if len(apparel) > 0 {
		aligned.Apparel, aligned.Wearing, aligned.Styling = clip(result.Apparel, 300), clip(result.Wearing, 200), clip(result.Styling, 240)
	}
	for index := range segments {
		item := byIndex[index+1]
		item.Index = index + 1
		if len(products) == 0 {
			item.Packaging, item.Presence = false, ""
		}
		aligned.Segments = append(aligned.Segments, item)
	}
	return aligned, nil
}

func videoReplicationValidateProductLockStructure(segments []VideoReplicationStructureSegment, expected int) error {
	if len(segments) != expected {
		return BadAuthRequest("商品框选追踪未覆盖全部视频片段，请重新分析")
	}
	seen := map[int]bool{}
	for _, segment := range segments {
		if segment.Index < 1 || segment.Index > expected || seen[segment.Index] || normalizeVideoReplicationPresence(segment.Presence) == "" {
			return BadAuthRequest("商品框选追踪结果不完整，请重新分析")
		}
		seen[segment.Index] = true
		if strings.Contains(segment.Source, "无法确认") || strings.Contains(segment.Source, "无法确定") || strings.Contains(segment.Source, "不确定") {
			return BadAuthRequest(fmt.Sprintf("无法确认第 %d 段中的框选商品，请选择更清晰的参考视频或重新框选", segment.Index))
		}
	}
	return nil
}

// videoReplicationCleanEdits 整理识图逐个点名的元素替换：只留「原元素→新元素」格式、新旧不同的条目，最多 12 条。
func videoReplicationCleanEdits(value string) string {
	value = strings.NewReplacer("->", "→", "=>", "→", "＞", "→", ";", "；", "\n", "；").Replace(strings.TrimSpace(value))
	items := []string{}
	for _, item := range strings.Split(value, "；") {
		from, to, ok := strings.Cut(strings.TrimSpace(item), "→")
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if !ok || from == "" || to == "" || from == to || videoReplicationNoneText(from) || videoReplicationNoneText(to) {
			continue
		}
		items = append(items, from+"→"+to)
		if len(items) >= 12 {
			break
		}
	}
	return videoReplicationClipRunes(strings.Join(items, "；"), 300)
}

// videoReplicationStructureAdaptation 把结构对齐结果写成这一段的动作与镜头说明；与原片一致时不写，免得提示词里多出无效要求。
func videoReplicationStructureAdaptation(item VideoReplicationStructureSegment) string {
	parts := []string{}
	if action := strings.TrimSpace(item.Action); action != "" && !strings.Contains(action, "沿用原片动作") {
		if source := strings.TrimSpace(item.Source); source != "" {
			action = "原片「" + source + "」→ " + action
		}
		parts = append(parts, action)
	}
	// 复刻不改原片镜头：识图给出的 camera 不写进提示词。
	return videoReplicationClipRunes(strings.Join(parts, "；"), 300)
}

// 包装图优先；缺少证据时保持载体类型，不臆造新商品的品牌包装。
func videoReplicationPackagingRule(packagingTokens string) string {
	if packagingTokens != "" {
		return "原片包装换成 " + packagingTokens + " 中的包装，开合、遮挡与拿取连续，清除旧品牌、文字和图案；原片无包装不新增。"
	}
	return "原片包装按商品参考图可见的新包装还原；资料不足用同类无品牌包装（袋仍是袋，盒仍是盒），不编造图文；原片无包装不新增。"
}

// 各载体分别取对应参考素材，商品图不能被整张贴到食物、包装或信息卡上。
func videoReplicationProductMaterialRule(productTokens, detailTokens, packagingTokens string) string {
	if productTokens == "" {
		return ""
	}
	cardSource := "只用用户提供的新商品资料与 " + productTokens + " 中可读的信息"
	if detailTokens != "" {
		cardSource = "若 " + detailTokens + " 含对应资料，按其真实图文替换；否则" + cardSource
	}
	return videoReplicationPackagingRule(packagingTokens) + "\n产品参数表 / 介绍卡：仅替换原有载体，保持位置、透视、遮挡和翻页；" + cardSource + "；清除旧品牌、参数与二维码，不沿用旧数值，不编造成分、规格、认证或功效，缺失字段省略。本体、包装、参数表分别还原，不贴整张商品图。"
}

// 去掉烧录字幕，不把商品、包装与展示卡上有依据的文字一起清掉。
const videoReplicationSubtitleRule = "去掉原视频画面里烧录的字幕、贴纸文字和被替换商品的旧品牌文字 / Logo，还原自然画面；保留新商品、包装和参数表上由用户资料支持的真实文字，不新增字幕、水印或无依据文字。"
