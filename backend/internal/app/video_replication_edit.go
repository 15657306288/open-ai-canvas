package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"yingce/backend/internal/model"
)

// 原片编辑：所选视频模型能接收参考视频（如 Seedance 2.0 / 2.5）时，每一段把对应的原视频片段作为参考视频发送，
// 提示词只要求「以原片为底，只替换目标」。场景、人物、手部动作、运镜、构图与时长都沿用原片，
// 不再让模型按文字描述重新想象画面（那样会凭空编出原视频没有的场景、把原商品的配件画到新商品上）。
// 时长与比例照常按本段秒数和原片比例发送：部分中转协议只接受正整数时长，
// 官方编辑任务的 duration=-1 / ratio=adaptive 会被它直接拒绝（VIDEO_PARAMETER_CONFLICT「视频时长必须为有效正整数」）。
// TopView 的 Seedance 2.5 编辑使用 duration=-1；2.0 只接受 4–15 秒，不能共用该覆盖。

// videoReplicationTopViewProtocol 是 TopView 视频协议的 providerOptions 命名空间（plugin-packages/topview）。
// TopView 中转的 Seedance 2.5 编辑段通过该命名空间使用 duration=-1。
const videoReplicationTopViewProtocol = "topview-video"

func videoReplicationTopViewSourceDuration(modelName string) bool {
	name := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(strings.TrimSpace(modelName)))
	return name == "seedance2.5" || name == "seedance2.5style"
}

// videoReplicationEditMaxClipBytes 是单段参考视频的大小上限，留出余量低于渠道常见的 20MB 限制。
const videoReplicationEditMaxClipBytes int64 = 18 << 20

// videoReplicationEditClipVersion 随截取规格（画面尺寸、帧率）变化递增，避免重新提交时复用旧规格的片段文件。
const videoReplicationEditClipVersion = 3

func videoReplicationEditMode(params VideoReplicationParameters) bool {
	return params.MaxReferenceVideos > 0 && params.ReferenceMode == "reference"
}

func videoReplicationProductNameRule(name string) string {
	return fmt.Sprintf("新商品名称是 %q。台词或已有字幕提到新商品时，沿用这个名称；外观和卖点以商品素材为准，不额外添加画面文字。", name)
}

// videoReplicationEditSwap 是一条替换：把原片段里的 from 换成参考图 tokens 中的 to。
type videoReplicationEditSwap struct {
	target, from, tokens, to string
}

// videoReplicationEditSwaps 按替换类型列出「原片段里的什么 → 换成哪几张参考图里的什么」；没有对应参考图的目标不写。
// 原片段里的对象：用户填写的主体 > 框选 > 按替换类型的通称。
func videoReplicationEditSwaps(videoType string, params VideoReplicationParameters, references []map[string]string) []videoReplicationEditSwap {
	framed := len(params.Regions) > 0 && strings.TrimSpace(params.RegionTarget) != ""
	product := "主要商品"
	if hint := strings.TrimSpace(params.SubjectHint); hint != "" {
		product = hint
	}
	person := "主要人物"
	if framed {
		if params.ReplaceMode == "precise-person" {
			person = "被框选的人物"
		} else {
			product = "被框选的" + product
		}
	}
	swaps := []videoReplicationEditSwap{}
	for _, target := range videoReplicationTargets(videoType) {
		tokens := videoReplicationEditImageTokens(references, videoReplicationTargetCategory[target])
		if tokens == "" {
			continue
		}
		swap := videoReplicationEditSwap{target: target, tokens: tokens}
		switch target {
		case VideoReplicationTypeProduct:
			swap.from, swap.to = product, "商品"
		case VideoReplicationTypeModel:
			swap.from, swap.to = person, "人物"
		case VideoReplicationTypeApparel:
			swap.from, swap.to = "人物身上的服装", "服装"
		case VideoReplicationTypeFace:
			swap.from, swap.to = "人物的脸", "脸"
		case VideoReplicationTypeBackground:
			swap.from, swap.to = "背景环境", "环境"
		default:
			continue
		}
		swaps = append(swaps, swap)
	}
	return swaps
}

// videoReplicationEditImageTokens 返回某类参考图在请求里的编号（@图片1、@图片2……，按发送顺序从 1 开始）。
func videoReplicationEditImageTokens(references []map[string]string, category string) string {
	tokens := []string{}
	for index, reference := range references {
		if reference["category"] == category {
			tokens = append(tokens, fmt.Sprintf("@图片%d", index+1))
		}
	}
	return strings.Join(tokens, "、")
}

// videoReplicationEditPrompt 是原片编辑的提示词。素材按 Seedance 规定的「素材类型+序号」写法引用：
// @视频1 是要编辑的原视频片段（每段只发这一个视频），@图片N 是第 N 张参考图。开头直接写「编辑视频：把 @视频1 中的…替换成…」，
// 让模型按视频编辑处理：成片就是原片段本身，只把目标换掉。
// shot.Adaptation / shot.Packaging 与 storyboard 里的商品 / 服装结构、穿法、搭配来自结构对齐，按段写入。
func videoReplicationEditPrompt(videoType string, params VideoReplicationParameters, shot VideoReplicationShot, storyboard VideoReplicationStoryboard, brief string, references []map[string]string) string {
	productStructure := storyboard.ProductStructure
	var b strings.Builder
	swaps := videoReplicationEditSwaps(videoType, params, references)
	presence := normalizeVideoReplicationPresence(shot.ProductPresence)
	clauses := make([]string, 0, len(swaps))
	for index, swap := range swaps {
		if swap.target == VideoReplicationTypeProduct {
			// 用结构对齐看到的原商品外观点名要换掉的东西，用商品本体点名换成什么：只写「主要商品」时模型常认不出原商品，
			// 只给它调个色；商品图里同时摆着包装盒、道具时，不点名本体就会整套搬进视频。
			if look := strings.TrimSpace(storyboard.SourceLook); look != "" && strings.TrimSpace(params.SubjectHint) == "" {
				swap.from += "（" + look + "）"
			}
			if subject := strings.TrimSpace(storyboard.NewSubject); subject != "" {
				swap.to += "（" + subject + "）"
			}
			swaps[index] = swap
			if presence == videoReplicationPresenceNone {
				clauses = append(clauses, "@视频1 中如果出现"+swap.from+"，替换成 "+swap.tokens+" 中的"+swap.to)
				continue
			}
		}
		clauses = append(clauses, "把 @视频1 中的"+swap.from+"替换成 "+swap.tokens+" 中的"+swap.to)
	}
	// 用户要求换掉的画面元素：识图看过本段关键帧后逐个点名，和换商品一样写成「把 A 替换成 B」，模型才会逐个改到；
	// 只给一句「所有梨换成香蕉」时，它只改最显眼的几个。
	// 清单里的新元素带参考图：「把 @视频1 中的梨替换成 @图片5 中的香蕉」，和换商品一样可靠。
	clauses = append(clauses, videoReplicationElementClauses(storyboard.Elements, shot, storyboard, references, "@图片%d", "@视频1")...)
	clauses = append(clauses, videoReplicationSceneEditClauses(shot.SceneEdits, "@视频1")...)
	if len(clauses) > 0 {
		b.WriteString("编辑视频：" + strings.Join(clauses, "；") + "。\n")
	} else {
		b.WriteString("编辑视频：按下面的补充要求修改 @视频1。\n")
	}
	swapProduct := false
	for _, swap := range swaps {
		swapProduct = swapProduct || swap.target == VideoReplicationTypeProduct
	}
	// 商品在本段的存在感紧跟替换句：商品只在边缘 / 手里出现的段落，模型拿到商品图后最容易把整段改成商品展示特写。
	if swapProduct {
		if rule := videoReplicationPresenceRule(presence, "@视频1"); rule != "" {
			b.WriteString(rule + "\n")
		}
	}
	// 保持不变的清单不列要被替换的部分，免得和替换要求互相矛盾。
	replaced := map[string]bool{}
	for _, swap := range swaps {
		replaced[swap.target] = true
	}
	// 用户补充要求紧跟在替换句后面、在「保持不变」前面：它是最高优先级，提示词超长截断时也不会被截掉。
	b.WriteString(videoReplicationUserDirective(brief, storyboard.Changes, "@视频1"))
	if core := videoReplicationReplacementCore(videoType, params, references, true); core != "" {
		b.WriteString(core + "\n")
	}
	if rule := videoReplicationBannedRule(storyboard.Banned, "@视频1"); rule != "" {
		b.WriteString(rule + "\n")
	}
	adaptation := strings.TrimSpace(shot.Adaptation)
	if adaptation != "" {
		b.WriteString("本段按新结构调整手部动作：" + adaptation + "；镜头、机位、景别和构图不变。\n")
	}
	if swapProduct {
		if details := videoReplicationEditImageTokens(references, model.VideoReplicationAssetProductDetailImage); details != "" {
			b.WriteString(details + " 是新商品的细节 / 拆解图，部件按它还原。\n")
		}
		if structure := strings.TrimSpace(productStructure); structure != "" {
			b.WriteString("新商品结构：" + structure + "。\n")
		}
		b.WriteString(videoReplicationReferenceOnlyRule(videoReplicationEditImageTokens(references, model.VideoReplicationAssetProductImage), storyboard) + "\n")
		b.WriteString("这是换商品，不是给原商品改颜色或换标签：按新商品形状和比例重画，清除旧轮廓、Logo及配件。\n")
		if shot.Packaging {
			b.WriteString("本段出现包装，按前面的包装规则替换。\n")
		} else if presence != "" {
			b.WriteString("本段原片没有包装盒，成片也不要出现包装盒，只换商品本体。\n")
		}
	}
	b.WriteString(videoReplicationSubtitleRule + "\n")
	b.WriteString(videoReplicationLifelikeRule + "\n")
	// 有画面改动清单时，场景道具、配色和光线可能都要改，不能再列进「保持不变」，否则模型以原片为底只会原样保留。
	restyle := strings.TrimSpace(storyboard.Changes)
	keep := []string{}
	if !replaced[VideoReplicationTypeBackground] && restyle == "" {
		keep = append(keep, "场景", "背景", "桌面摆设")
		if swapProduct {
			keep[len(keep)-1] += "（不含被替换商品及其包装、展示资料）"
		}
	} else if !replaced[VideoReplicationTypeBackground] {
		// 有改动清单时只放开清单里的元素；场景布局仍锁在原片上，否则模型会把商品图里的布景（桌面、窗格、茶杯）整套搬进来。
		keep = append(keep, "场景布局与机位", "清单没提到的背景景物")
	}
	if !replaced[VideoReplicationTypeModel] && !replaced[VideoReplicationTypeFace] {
		keep = append(keep, "人物")
	} else {
		// 只换人 / 换脸：没换的那部分人物外观也锁在原片上，不能让模型连衣服、发型一起换（用户：换人物就只换人物）。
		if replaced[VideoReplicationTypeFace] && !replaced[VideoReplicationTypeModel] {
			keep = append(keep, "人物发型", "身材")
		}
		if !replaced[VideoReplicationTypeApparel] {
			keep = append(keep, "人物身上的服装与配饰")
		}
	}
	// 复刻只做替换：镜头、机位、景别、构图、运镜永远锁在原片上。
	// 结构对齐只允许按新商品 / 新服装结构微调手部动作，不能改镜头。
	if adaptation == "" && !swapProduct && !replaced[VideoReplicationTypeApparel] {
		keep = append(keep, "手部姿势与动作", "机位", "运镜", "景别", "构图")
	} else {
		keep = append(keep, "人物走位与节奏", "机位", "运镜", "景别", "构图")
	}
	if !replaced[VideoReplicationTypeBackground] && restyle == "" {
		keep = append(keep, "光线")
	}
	keep = append(keep, "剪辑切点", "节奏和时长")
	exception := ""
	if strings.TrimSpace(brief) != "" {
		exception = "除用户要求改动的地方外，"
	}
	if restyle == "" {
		b.WriteString("保持不变：@视频1 是要编辑的原视频片段，成片以它为底，" + exception + strings.Join(keep, "、") + "都与 @视频1 一致，不擅自新增或删除镜头、人物和物体，只改动被替换的部分（以及去掉原片字幕）。\n")
	} else {
		b.WriteString("保持不变：@视频1 是要编辑的原视频片段，成片以它为底，" + exception + strings.Join(keep, "、") + "都与 @视频1 一致，改动清单没提到的内容沿用原片（去掉原片字幕）。\n")
	}
	if replaced[VideoReplicationTypeBackground] {
		b.WriteString("成片每一帧都是 @视频1 对应帧改出来的：画面主体、人物位置和镜头与原帧对应，不要变成商品静物展示或商品海报。\n")
	} else {
		b.WriteString("成片每一帧都是 @视频1 对应帧改出来的：画面主体、人物位置和镜头与原帧对应，不要变成商品静物展示、商品海报或另一个场景。\n")
	}
	b.WriteString(videoReplicationLockShotRule)
	if strings.TrimSpace(brief) != "" || restyle != "" {
		b.WriteString(videoReplicationLockShotBriefNote)
	}
	b.WriteString("\n")
	for _, swap := range swaps {
		switch swap.target {
		case VideoReplicationTypeProduct:
			if strings.Contains(swap.tokens, "、") {
				b.WriteString(swap.tokens + " 是同一件新商品的不同角度和细节。")
			}
			b.WriteString("新商品的外形结构、比例、颜色、材质、Logo 与文字严格按 " + swap.tokens + " 还原。")
			b.WriteString("新商品的位置、朝向、大小和运动轨迹参照原商品，握持与手口接触按新商品结构微调，遮挡和光影贴合画面，不变形、不穿模、不与手或其他物体粘连。")
			if adaptation == "" {
				b.WriteString("原视频里拆装或展示原商品特有部件的动作，改为按原片节奏自然展示新商品对应的部位或整机。")
			}
			b.WriteString("新商品按自身结构正确拿取和使用（开盖、按压、旋拧、佩戴方式符合新商品），大小比例真实。")
			if params.ReplaceMode == "precise-product" {
				b.WriteString("镜头靠近商品时细节清晰，Logo 与文字的位置和 " + swap.tokens + " 一致。")
			}
			b.WriteString("\n")
		case VideoReplicationTypeModel, VideoReplicationTypeFace:
			// 参考图只锁长相：模型照搬照片上的表情和角度时，整段脸会定格成照片的样子，显得僵硬。
			// 照片里的背景、衣服也不能带进来：用户只要求换人时，场景和服装都沿用原片。
			outfit := "服装沿用 @视频1 原人物的"
			if replaced[VideoReplicationTypeApparel] {
				outfit = "服装按服饰参考图换"
			}
			if swap.target == VideoReplicationTypeFace && !replaced[VideoReplicationTypeModel] {
				b.WriteString("只换脸：人物的五官、脸型和肤色按 " + swap.tokens + " 还原，发型、身材和配饰沿用 @视频1 原人物，" + outfit + "；")
			} else {
				b.WriteString("人物的五官、脸型、发型、肤色和身材按 " + swap.tokens + " 还原，" + outfit + "；")
			}
			b.WriteString(swap.tokens + " 只提供人物长相，不采用图中的表情、姿势、头部角度、光线、背景、服装和道具。")
			scene := "场景背景仍是 @视频1 的"
			if replaced[VideoReplicationTypeBackground] {
				scene = "场景背景按背景参考图替换"
			}
			b.WriteString(scene + "。")
			if swapProduct || replaced[VideoReplicationTypeApparel] {
				b.WriteString("走位、动作意图和表演节奏跟随 @视频1，手口接触姿势按新商品或服装结构微调；")
			} else {
				b.WriteString("动作和走位与 @视频1 一致；")
			}
			b.WriteString("表情、眼神、眨眼和头部转动跟着 @视频1 中的原人物自然变化，" + videoReplicationNaturalFaceRule + "\n")
		case VideoReplicationTypeApparel:
			b.WriteString(videoReplicationApparelRules(swap.tokens, storyboard, adaptation == ""))
		case VideoReplicationTypeBackground:
			b.WriteString("背景换成 " + swap.tokens + " 中的环境，只取环境，不引入背景图里的人物或商品；主体位置和动作节奏与原片对应，身份外观按各自替换目标处理，接触阴影、反射、光线与透视匹配新背景。\n" + videoReplicationBackgroundPhysics + "\n")
		}
	}
	if target := strings.TrimSpace(params.RegionTarget); target != "" && len(params.Regions) > 0 {
		if params.ReplaceMode == "precise-person" {
			b.WriteString("框选锁定：" + target + "。只换被框选的人物，其他人物保持原样，不要换错人。\n")
		} else {
			b.WriteString("框选锁定：" + target + "。框了几件就换几件，没被框选的物品保持原样。\n")
		}
	}
	if params.Talking {
		if params.AudioMode == "replace" || storyboard.VoiceAdapted {
			b.WriteString("人物开口说话的时间点与 @视频1 一致，嘴型跟随本段新生成的人声逐字同步。\n")
		} else {
			b.WriteString("人物开口说话的口型与 @视频1 一致。\n")
		}
	}
	// 长度上限按所选模型在后台配置的提示词字数。超出时真人规则让位到末尾，先保住替换细节；
	// 反提示词里的「表情僵硬、动作僵硬」每段仍会带上。
	budget := videoReplicationPromptBudgetFor(params)
	text := b.String()
	if budget.size(text) > budget.limit {
		text = strings.Replace(text, videoReplicationLifelikeRule+"\n", "", 1) + "\n" + videoReplicationLifelikeRule
	}
	return budget.clip(text)
}

// videoReplicationLockShotRule：复刻的本质是替换，不改原片镜头；用户补充要求也只改元素和色调。
const videoReplicationLockShotRule = "复刻只做替换：镜头数量、机位、景别、构图、运镜方向和速度与剪辑切点全部与 @视频1 一致，不推近、不拉远、不改拍、不新增或删减镜头，商品和人物不要被移到镜头前展示；人物沿用原片的动作意图、走位和节奏，替换商品或服装时允许按新结构微调手口接触姿势，要像真人一样流畅自然，不要为了对齐原片而僵住或一顿一顿。"

const videoReplicationLockShotBriefNote = "上面的要求只改画面里的元素和色调，不改镜头。"

// 商品范围不依赖逐元素场景改写：同一商品的桌面、手持和食用状态都属于商品替换。
// 框选只跟踪选中的实例，不能把未选中的同款商品也一起换掉。
func videoReplicationProductScopeRule(params VideoReplicationParameters) string {
	scope := "商品范围：逐帧替换所有实例（桌面、盘中、手持、袋内、入口、咬后、碎块、反射中的同一商品），不改无关物品。"
	if len(params.Regions) > 0 && strings.TrimSpace(params.RegionTarget) != "" && params.ReplaceMode != "precise-person" {
		scope = "商品范围：只跟踪框选实例及其反射，未框选的同款和其他物品保持原样。"
	}
	return scope + "按新商品结构微调握持与手口接触姿势；食品按真实形态衔接散装、拆袋、食用及剩余量，不把食物变成包装或平面照片。"
}

// 核心规则在任何描述前保留；最终提交时核验它未被台词、负向提示词的预算裁掉。
func videoReplicationReplacementCore(videoType string, params VideoReplicationParameters, references []map[string]string, edit bool) string {
	swaps := videoReplicationEditSwaps(videoType, params, references)
	if len(swaps) == 0 {
		return ""
	}
	bindings := []string{}
	product, person, background := false, false, false
	for _, swap := range swaps {
		bindings = append(bindings, swap.from+"按"+swap.tokens+"中的"+swap.to+"替换")
		product = product || swap.target == VideoReplicationTypeProduct
		person = person || swap.target == VideoReplicationTypeModel || swap.target == VideoReplicationTypeFace
		background = background || swap.target == VideoReplicationTypeBackground
	}
	parts := []string{"替换绑定：" + strings.Join(bindings, "；") + "。参考图各只取指定对象，未选类别除用户明确改动外沿用原片；复刻只做替换，镜头、走位、节奏不变，不推近、不拉远、不改拍。"}
	if person {
		parts = append(parts, "人物图只取长相，表情与动作意图随原片自然变化。")
	}
	if background {
		parts = append(parts, "背景图只取环境，不引入其人物商品，匹配光影透视。")
	}
	if product {
		parts = append(parts, videoReplicationProductScopeRule(params), videoReplicationProductMaterialRule(
			videoReplicationEditImageTokens(references, model.VideoReplicationAssetProductImage),
			videoReplicationEditImageTokens(references, model.VideoReplicationAssetProductDetailImage),
			videoReplicationEditImageTokens(references, model.VideoReplicationAssetPackagingImage)))
	}
	if videoReplicationHasTarget(videoType, VideoReplicationTypeApparel) {
		parts = append(parts, videoReplicationProductMaterialRule(
			videoReplicationEditImageTokens(references, model.VideoReplicationAssetApparelImage),
			videoReplicationEditImageTokens(references, model.VideoReplicationAssetProductDetailImage),
			videoReplicationEditImageTokens(references, model.VideoReplicationAssetPackagingImage)))
	}
	core := strings.Join(parts, "\n")
	if !edit {
		core = strings.ReplaceAll(core, "@图片", "参考图")
	}
	return core
}

// videoReplicationSceneEditClauses 把「位置+原元素→新元素；…」写成逐条替换句。
func videoReplicationSceneEditClauses(edits, source string) []string {
	clauses := []string{}
	for _, item := range strings.Split(strings.TrimSpace(edits), "；") {
		from, to, ok := strings.Cut(strings.TrimSpace(item), "→")
		if from, to = strings.TrimSpace(from), strings.TrimSpace(to); ok && from != "" && to != "" {
			clauses = append(clauses, "把 "+source+" 中的"+from+"替换成"+to)
		}
	}
	return clauses
}

// videoReplicationBannedRule：用户要求换掉的原元素全片清除，包括没被逐个点名的（远处、角落、被遮挡、很小的、同形状的造型物）。
func videoReplicationBannedRule(banned, source string) string {
	banned = strings.TrimSpace(banned)
	if banned == "" {
		return ""
	}
	return "全片清除：" + source + " 里所有的" + banned + "（包括背景、远处、角落、被遮挡一半、很小的，以及做成它们形状的造型物）每一帧都按用户要求换成新元素，位置、大小和运动沿用原元素；成片任何一帧都不能再出现" + banned + "。"
}

// videoReplicationPresenceRule 按原商品在本段的存在感约束新商品出现的位置和大小；source 是原片在提示词里的称呼。
// 存在感未知（旧方案、结构对齐失败）时返回空串，沿用通用规则。
func videoReplicationPresenceRule(presence, source string) string {
	switch presence {
	case videoReplicationPresenceHero:
		return "本段原商品是画面主体：新商品放在原商品的位置，按原画面的角度、大小和机位出现；周围场景、道具和背景按本轮替换目标与改动清单处理，未要求替换的沿用" + source + "，不用商品图里的布景。"
	case videoReplicationPresenceHand:
		return "本段商品在人物手中：画面主体仍是人物，新商品按原商品在手中的位置、朝向和大小出现，不放大成商品特写。"
	case videoReplicationPresenceMinor:
		return "本段原商品只在画面边缘 / 背景里小面积出现：新商品放在同一位置，大小和虚化程度与原商品相同；保持" + source + "的人物位置和场景布局，身份和背景按本轮替换目标处理，不要改成商品特写或商品展示。"
	case videoReplicationPresenceNone:
		return "本段原片基本看不到商品：保持" + source + "的人物位置和场景布局，身份和背景按本轮替换目标处理，不新增商品、包装或商品特写。"
	}
	return ""
}

// videoReplicationReferenceOnlyRule：商品图只提供商品本体的外观。用户的商品图常是带布景的宣传图（桌面、窗格、茶杯、包装盒），
// 模型会把整张图搬进视频，所以点名列出不能出现的东西。
func videoReplicationReferenceOnlyRule(tokens string, storyboard VideoReplicationStoryboard) string {
	subject := "商品本体"
	if value := strings.TrimSpace(storyboard.NewSubject); value != "" {
		subject = value
	}
	excluded := "背景、桌面摆设、道具和光线"
	if props := strings.TrimSpace(storyboard.ReferenceProps); props != "" {
		excluded = props + "以及图中的背景、桌面摆设和光线"
	}
	return tokens + " 只提供" + subject + "的外观，图中的" + excluded + "不作为商品本体搬进视频；包装和展示资料仅在原片对应载体上按各自规则替换。"
}

// videoReplicationApparelRules 是替换服装的细则：按参考图还原、正常合身地穿、不穿帮，并补齐整体搭配。
// genericAction 为 true 时（没有逐段改写结果）补一条通用规则：新服装没有的结构，原片针对它的动作要改掉。
func videoReplicationApparelRules(tokens string, storyboard VideoReplicationStoryboard, genericAction bool) string {
	var b strings.Builder
	b.WriteString("服装的款式、版型、颜色、图案和面料严格按 " + tokens + " 还原（只取服装，不取图中模特和背景）")
	if structure := strings.TrimSpace(storyboard.ApparelStructure); structure != "" {
		b.WriteString("，结构：" + videoReplicationClipRunes(structure, 90))
	}
	b.WriteString("；新服装没有的口袋、拉链、纽扣、帽子、图案不要出现。")
	if genericAction {
		b.WriteString("原片插口袋、拉拉链等动作在新服装上没有对应结构时，改成自然动作（手自然垂放、轻抚衣摆、整理领口）。")
	}
	b.WriteString("\n穿着：正常合身地穿好")
	if wearing := strings.TrimSpace(storyboard.Wearing); wearing != "" {
		b.WriteString("（" + videoReplicationClipRunes(wearing, 60) + "）")
	}
	b.WriteString("，领口、肩线、袖口、衣摆位置正确，门襟对齐；手和身体不穿透衣服，不走光，无原服装残留、无叠穿闪烁、无吊牌；转身抬手时始终同一件衣服。\n")
	b.WriteString(videoReplicationFabricPhysics + "\n")
	b.WriteString("搭配：")
	if styling := strings.TrimSpace(storyboard.Styling); styling != "" {
		b.WriteString(videoReplicationClipRunes(styling, 80) + "；")
	}
	b.WriteString("未给出的单品选与新服装风格颜色协调的简洁款，原片中冲突的同类单品换掉。\n")
	return b.String()
}

// videoReplicationSourceClip 从（裁剪后的）参考视频截出这一段，转成适合模型输入的 mp4 并登记为资源，返回资源 ID。
// 同一段、同一裁剪区间重复提交时复用同一份文件。
func (s *Service) videoReplicationSourceClip(project *model.VideoReplicationProject, params VideoReplicationParameters, shotID string, startMs int64, seconds int, withAudio bool) (string, error) {
	assets, err := s.repo.VideoReplicationAssets(project.UserID, project.ID)
	if err != nil {
		return "", err
	}
	var source *model.VideoReplicationAsset
	for index := range assets {
		if assets[index].Category == model.VideoReplicationAssetReferenceVideo && (source == nil || assets[index].IsPrimary) {
			source = &assets[index]
		}
	}
	if source == nil {
		return "", fmt.Errorf("没有参考视频")
	}
	lengthMs := int64(seconds) * 1000
	if limit := int64(params.MaxReferenceVideoSeconds) * 1000; limit > 0 && lengthMs > limit {
		lengthMs = limit
	}
	absStart := params.TrimStartMs + startMs
	if source.DurationMs > 0 && absStart+lengthMs > source.DurationMs {
		absStart = max(0, source.DurationMs-lengthMs)
	}
	identity := fmt.Sprintf("video-replication-clip:v%d:%s:%s:%s:%d:%d:audio=%t", videoReplicationEditClipVersion, project.ID, source.ResourceID, shotID, absStart, lengthMs, withAudio)
	ffmpegBin, err := renderFfmpegBinary()
	if err != nil {
		return "", err
	}
	fps := videoReplicationEditClipFPS(source.FPS)
	var clipID string
	err = s.withVideoReplicationResourceFile(project.UserID, source.ResourceID, videoReplicationMaxVideoBytes, func(_ *model.Resource, path string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		output := filepath.Join(filepath.Dir(path), "edit-clip.mp4")
		// 参考片段是模型重绘人脸和手部的依据：先按高画质编码，超过大小上限才逐级压缩，片段糊了成片的人也会发糊、发假。
		for _, crf := range []string{"18", "22", "26", "30", "33"} {
			if err := cutVideoReplicationEditClip(ctx, ffmpegBin, path, absStart, lengthMs, fps, crf, output, withAudio); err != nil {
				return err
			}
			info, err := os.Stat(output)
			if err != nil {
				return err
			}
			if info.Size() <= videoReplicationEditMaxClipBytes {
				saved, err := s.saveVideoReplicationFile(project.UserID, output, fmt.Sprintf("原视频片段-%s.mp4", shotID), "video", 0, 0, lengthMs, identity)
				if err != nil {
					return err
				}
				clipID = saved.ID
				return nil
			}
		}
		return fmt.Errorf("原视频片段压缩后仍超过 %dMB", videoReplicationEditMaxClipBytes>>20)
	})
	return clipID, err
}

// videoReplicationEditClipFPS 返回片段要统一到的帧率：Seedance 只接收 24–60 帧的参考视频，低于 24 帧补到 24，
// 高于 60 帧降到 30；范围内或未知时返回 0，保持原帧率。
func videoReplicationEditClipFPS(source float64) int {
	switch {
	case source > 0 && source < 24:
		return 24
	case source > 60:
		return 30
	}
	return 0
}

// videoReplicationEditClipFilter 是片段的画面滤镜：总像素落在 Seedance 参考视频允许的 [409600, 927408]
// （2.0 与 2.5 的交集）之内时保持原尺寸，否则按原比例缩放到约 720p 的面积；宽高取偶数以满足 yuv420p 编码。
func videoReplicationEditClipFilter(fps int) string {
	fit := "sqrt(921600/(iw*ih))"
	filter := "scale='if(between(iw*ih,409600,927408),trunc(iw/2)*2,trunc(iw*" + fit + "/2)*2)':'if(between(iw*ih,409600,927408),trunc(ih/2)*2,trunc(ih*" + fit + "/2)*2)':flags=lanczos"
	if fps > 0 {
		filter += fmt.Sprintf(",fps=%d", fps)
	}
	return filter
}

// cutVideoReplicationEditClip 精确截取 [start, start+length)，画面尺寸与帧率按 videoReplicationEditClipFilter 规整，
// 默认去掉音轨；改写口播且没有独立参考音色时，必须保留原音轨供模型听取真实音色。
func cutVideoReplicationEditClip(ctx context.Context, ffmpegBin, input string, startMs, lengthMs int64, fps int, crf, output string, withAudio bool) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", fmt.Sprintf("%.3f", float64(startMs)/1000), "-i", input, "-t", fmt.Sprintf("%.3f", float64(lengthMs)/1000),
		"-map", "0:v:0"}
	if withAudio {
		args = append(args, "-map", "0:a:0", "-c:a", "aac", "-b:a", "128k")
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-vf", videoReplicationEditClipFilter(fps), "-c:v", "libx264", "-preset", "medium", "-tune", "film", "-crf", crf, "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-y", output)
	cmd := exec.CommandContext(ctx, ffmpegBin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("截取原视频片段失败：%s", clipText(strings.TrimSpace(string(out)), 300))
	}
	return nil
}
