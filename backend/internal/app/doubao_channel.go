package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/doubao"
	"infinite-canvas/backend/internal/model"
)

// 豆包 / Dola 账号池是内置平台渠道：凭据来自账号池而不是渠道表，因此不落 system_channels，
// 任务准入与 provider 配置解析都要为它们走专用分支，避免被当成缺失的外部渠道拒绝。
const (
	DoubaoPoolChannelID     = "doubao-pool"
	DoubaoPoolInterfaceType = "doubao-pool"
	// DolaPoolChannelID 是 Dola 账号池渠道：与豆包池同一套 samantha 协议双站点（www.dola.com），
	// 凭据取自账号池 Dola 站账号，模型键使用 dola- 前缀。
	DolaPoolChannelID = "dola-pool"
	// DolaPoolSite 对应账号池站点标识（internal/doubao 的 SiteDola），用于锁定取号站点。
	DolaPoolSite = "dola"
)

const doubaoPoolImageModel = "doubao-seedream-image"

// doubaoPoolLayerModel 是「AI 图层拆分」的账号池模型键：名字里带 layer-decomposition，
// 前端 isDedicatedLayerDecompositionEndpoint 据此识别为专用接口走「一次请求 → N 个图层」；
// 豆包网页协议并没有真正的图层分解端点，履约方式是由后端在同一任务里按层各发一次图生图。
const doubaoPoolLayerModel = "doubao-seedream-layer-decomposition"

// doubaoPoolMaxLayers 单次图层拆分的层数上限，与前端 LAYER_DECOMPOSITION_HARD_MAX_LAYERS 对齐。
const doubaoPoolMaxLayers = 6

// doubaoPoolDefaultLayers 未指定层数时的默认值，与前端 LAYER_DECOMPOSITION_DEFAULT_LAYERS 对齐。
const doubaoPoolDefaultLayers = 3

// Dola 池视频模型键，与前端 user-session.ts 的 DOLA_VIDEO_MODEL_* 保持一致。
const (
	dolaPoolVideoModel10   = "dola-seedance-video-1.0"
	dolaPoolVideoModel25   = "dola-seedance-video-2.5"
	dolaPoolVideoModelFast = "dola-seedance-video-fast"
)

// IsDoubaoPoolChannel reports whether the task targets the built-in doubao account-pool channel.
func IsDoubaoPoolChannel(channelID string) bool {
	return strings.TrimSpace(channelID) == DoubaoPoolChannelID
}

// IsDolaPoolChannel reports whether the task targets the built-in Dola account-pool channel.
func IsDolaPoolChannel(channelID string) bool {
	return strings.TrimSpace(channelID) == DolaPoolChannelID
}

// IsAccountPoolChannel 涵盖豆包 / Dola 两个内置账号池渠道。两者凭据都在账号池服务里，
// 准入、能力校验、计费与并发的语义一致；差异只在取号站点与模型键前缀。
func IsAccountPoolChannel(channelID string) bool {
	return IsDoubaoPoolChannel(channelID) || IsDolaPoolChannel(channelID)
}

// accountPoolSite 返回渠道对应的取号站点；空表示按豆包优先跨站取号。
func accountPoolSite(channelID string) string {
	if IsDolaPoolChannel(channelID) {
		return DolaPoolSite
	}
	return ""
}

// accountPoolModelMismatch 校验模型键与账号池渠道是否配套，配套时返回空字符串。
// 取号站点由渠道决定（accountPoolSite），而模型名会按站点再映射一次（accountPoolVideoVariant），
// 所以把 Dola 站模型键配到豆包池（或反过来）不会报错，只会静默跑成另一个站点的另一个模型。
// 目录读路径按渠道分组登记模型键，正常前端不会配错；这里是任务准入写路径的强校验。
func accountPoolModelMismatch(channelID string, modelKey string) string {
	key := strings.ToLower(strings.TrimSpace(modelKey))
	if IsDolaPoolChannel(channelID) && !strings.HasPrefix(key, "dola-") {
		return "Dola 账号池只支持 dola- 前缀的模型：" + modelKey
	}
	if IsDoubaoPoolChannel(channelID) && strings.HasPrefix(key, "dola-") {
		return "Dola 站模型必须选择 Dola 账号池渠道：" + modelKey
	}
	return ""
}

// accountPoolVideoVariant 按渠道把前端模型键翻译成站点会话里的模型描述。
// generateVideoOnce 内部的 mapAbilityModel 会再做一次归一：
// "Seedance 2.5" → seedance_v2.5，"Seedance 2.0 Fast" → seedance_v2.0_fast，"Seedance 1.0" → seedance_v1.0。
func accountPoolVideoVariant(channelID, modelKey string) string {
	if IsDolaPoolChannel(channelID) {
		key := strings.ToLower(modelKey)
		// 2.5 判断必须在 fast 之前：模型键不含 fast，但顺序上保持显式优先。
		if strings.Contains(key, "2.5") {
			return "Seedance 2.5"
		}
		if strings.Contains(key, "fast") {
			return "Seedance 2.0 Fast"
		}
		return "Seedance 1.0"
	}
	return doubaoPoolVideoVariant(modelKey)
}

func isDoubaoPoolInterface(interfaceType string) bool {
	return strings.TrimSpace(interfaceType) == DoubaoPoolInterfaceType
}

// isDoubaoPoolLayerModel 判断模型键是否是「AI 图层拆分」模型。
// 命名保持 layer-decomposition 语义，前端据此识别专用接口（单请求模式）。
func isDoubaoPoolLayerModel(modelKey string) bool {
	key := strings.ToLower(strings.TrimSpace(modelKey))
	if key == "" {
		return false
	}
	return key == doubaoPoolLayerModel || strings.Contains(key, "layer-decomposition") || strings.Contains(key, "layerize")
}

// doubaoPoolModelCapability 把账号池模型键映射到生成能力；第二返回值表示键是否受支持。
// doubao-seedance-video 是历史默认键，继续按 Mini 语义接受；dola- 前缀键属于 Dola 池。
func doubaoPoolModelCapability(modelKey string) (string, bool) {
	key := strings.TrimSpace(modelKey)
	if isDoubaoPoolLayerModel(key) || key == doubaoPoolImageModel {
		return "image", true
	}
	if key == "doubao-seedance-video" || strings.HasPrefix(key, "doubao-seedance-video-") {
		return "video", true
	}
	if key == dolaPoolVideoModel10 || key == dolaPoolVideoModel25 || key == dolaPoolVideoModelFast {
		return "video", true
	}
	return "", false
}

// doubaoPoolVideoVariant 把前端模型键翻译成豆包会话里的模型描述。
// 与参考实现 mapAbilityModel 对齐：fast/mini 都落到 seedance_v2.0_fast 档，
// 未带后缀的历史键按 Mini 处理。
func doubaoPoolVideoVariant(modelKey string) string {
	key := strings.ToLower(strings.TrimSpace(modelKey))
	if strings.Contains(key, "fast") {
		return "Seedance 2.0 Fast"
	}
	return "Seedance 2.0 Mini"
}

// doubaoPoolMaxRefImages 图生视频参考图上限：与账号池 Seedance 能力表（多参考图）对齐，超出的忽略。
const doubaoPoolMaxRefImages = 4

// runDoubaoPoolVideoTask 文生/图生视频：池内取号生成，产物下载后以 dataUrl 回填任务结果，
// 与其他视频协议的任务结果形状保持一致。ReferenceImages 非空时走图生视频
// （参考图随提交上传到豆包，上传失败自动降级为纯文生视频）。
func (s *Service) runDoubaoPoolVideoTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	duration := 0
	if seconds := strings.TrimSpace(input.Config.VideoSeconds); seconds != "" {
		if parsed, err := parsePositiveInt(seconds); err == nil {
			duration = parsed
		}
	}
	refImages := s.doubaoPoolRefImages(input, doubaoPoolMaxRefImages)
	result, err := s.DoubaoGenerateVideo(ctx, DoubaoGenerateVideoRequest{
		Prompt:    input.Prompt,
		Model:     accountPoolVideoVariant(input.Config.ChannelID, input.Config.Model),
		Duration:  duration,
		Ratio:     strings.TrimSpace(input.Config.Size),
		RefImages: refImages,
		// Dola 渠道锁定 Dola 站取号；豆包渠道留空按豆包优先跨站。
		Site: accountPoolSite(input.Config.ChannelID),
		// 执行期间实时登记当前使用的账号（含会话直链），任务面板据此展示
		// 「正在用哪个账号生成」并提供打开生成对话的入口。
		OnLive: func(info doubao.LiveInfo) {
			if taskID := strings.TrimSpace(taskExecutionID(ctx)); taskID != "" {
				s.setProviderLiveAccount(taskID, info)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	data, mimeType, err := s.downloadDoubaoPoolMedia(ctx, input.Config, result.URLs, "视频")
	if err != nil {
		return nil, err
	}
	// 无水印保证在生成层完成（doubao/fallback.go 母片通道，实测是真无水印原片）。
	// 不做 delogo 遮盖：用户要求高清原片，宁要原样水印也不要打码画质；
	// 母片通道失败时 URLs[0] 仍是干净度最高的候选（preferWatermarkFree 已滤过）。
	return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
}

// runDoubaoPoolImageTask 文生图 / 图生图：池内取号生成，全部产物转 dataUrl。
// 带参考图时走图生图（图片编辑、文字编辑、标注编辑、图层拆分都依赖这条）；
// 图层拆分模型键由 runDoubaoPoolLayerTask 在同一任务里按层各生成一张。
func (s *Service) runDoubaoPoolImageTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	refImages := s.doubaoPoolRefImages(input, doubaoPoolMaxRefImages)
	if isDoubaoPoolLayerModel(input.Config.Model) {
		return s.runDoubaoPoolLayerTask(ctx, input, refImages)
	}
	result, err := s.DoubaoGenerateImage(ctx, DoubaoGenerateImageRequest{
		Prompt:    input.Prompt,
		Ratio:     strings.TrimSpace(input.Config.Size),
		RefImages: refImages,
	})
	if err != nil {
		return nil, err
	}
	images := make([]map[string]string, 0, len(result.URLs))
	for _, rawURL := range result.URLs {
		data, mimeType, err := s.downloadDoubaoPoolMedia(ctx, input.Config, []string{rawURL}, "图片")
		if err != nil {
			return nil, err
		}
		images = append(images, map[string]string{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("豆包账号池未返回图片：%s", strings.TrimSpace(result.Text))
	}
	return map[string]interface{}{"mode": "image", "images": images}, nil
}

// runDoubaoPoolLayerTask 用账号池履约「AI 图层拆分」：单次任务内按层各发一次图生图请求。
// 单层失败只损失该层（返回的 images 少于请求层数，前端会按空位把对应节点标为失败），
// 选号、失败切号、代理绑定全部复用账号池机制，不另写一套调度。
func (s *Service) runDoubaoPoolLayerTask(ctx context.Context, input canvasGenerationInput, refImages []doubao.RefImage) (map[string]interface{}, error) {
	layers := doubaoPoolLayerCount(input)
	if len(refImages) == 0 {
		// 没有源图就不是图层拆分，而是文生图；此项必须显式失败，避免拿一张“新图”冒充图层。
		return nil, errors.New("图层拆分需要源图：请先选择或生成一张图片")
	}
	prompts := doubao.LayerPrompts(input.Prompt, layers, doubaoPoolLayerRegions(input))
	result, err := s.DoubaoGenerateLayers(ctx, DoubaoGenerateLayersRequest{
		Prompts:   prompts,
		Ratio:     strings.TrimSpace(input.Config.Size),
		RefImages: refImages,
		OnLive: func(info doubao.LiveInfo) {
			if taskID := strings.TrimSpace(taskExecutionID(ctx)); taskID != "" {
				s.setProviderLiveAccount(taskID, info)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	images := make([]map[string]string, 0, len(result.URLs))
	for index, rawURL := range result.URLs {
		if strings.TrimSpace(rawURL) == "" {
			continue
		}
		data, mimeType, downloadErr := s.downloadDoubaoPoolMedia(ctx, input.Config, []string{rawURL}, fmt.Sprintf("图层 %d", index+1))
		if downloadErr != nil {
			log.Printf("[doubao-pool] 图层 %d 下载失败（跳过）：%v", index+1, downloadErr)
			continue
		}
		images = append(images, map[string]string{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType})
	}
	if len(images) == 0 {
		message := "豆包账号池没有返回任何图层"
		if len(result.Failures) > 0 {
			message += "：" + result.Failures[0].Message
		}
		return nil, errors.New(message)
	}
	return map[string]interface{}{"mode": "image", "images": images}, nil
}

// doubaoPoolRefImages 把任务参考图转成账号池的参考图结构（单张读取失败只跳过该张）。
// max 是单次参考图上限：视频侧与图片侧的上游限制不同，由调用方决定。
func (s *Service) doubaoPoolRefImages(input canvasGenerationInput, max int) []doubao.RefImage {
	refImages := make([]doubao.RefImage, 0, len(input.ReferenceImages))
	for index, image := range input.ReferenceImages {
		if max > 0 && len(refImages) >= max {
			break
		}
		raw, mimeType, err := mediaBytes(image)
		if err != nil {
			log.Printf("[doubao-pool] 参考图 %d 读取失败（跳过）：%v", index+1, err)
			continue
		}
		ext := "jpg"
		switch {
		case strings.Contains(mimeType, "png"):
			ext = "png"
		case strings.Contains(mimeType, "webp"):
			ext = "webp"
		}
		refImages = append(refImages, doubao.RefImage{
			Filename: fmt.Sprintf("reference-%d.%s", index+1, ext),
			Data:     raw,
		})
	}
	return refImages
}

// doubaoPoolLayerCount 读取本次拆分的层数：优先 metadata.layerCount（前端按框选与上限收敛后的值），
// 其次 config.count，最后回落到默认值；一律收敛到 [1, doubaoPoolMaxLayers]。
func doubaoPoolLayerCount(input canvasGenerationInput) int {
	count := 0
	if raw, ok := input.Metadata["layerCount"].(float64); ok {
		count = int(raw)
	}
	if count <= 0 {
		if parsed, err := strconv.Atoi(strings.TrimSpace(stringValue(input.Config.Count))); err == nil {
			count = parsed
		}
	}
	if count <= 0 {
		count = doubaoPoolDefaultLayers
	}
	if count > doubaoPoolMaxLayers {
		count = doubaoPoolMaxLayers
	}
	return count
}

// doubaoPoolLayerRegions 读取前端传来的图层选区（图像 0-1000 坐标系）。
// 兼容 metadata.layerRegions 与 metadata.regions；非法项直接跳过而不是整批放弃。
func doubaoPoolLayerRegions(input canvasGenerationInput) []doubao.LayerRegion {
	raw, ok := input.Metadata["layerRegions"]
	if !ok {
		raw = input.Metadata["regions"]
	}
	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return nil
	}
	regions := make([]doubao.LayerRegion, 0, len(items))
	for _, item := range items {
		values, ok := item.([]interface{})
		if !ok || len(values) != 4 {
			continue
		}
		nums := make([]int, 0, 4)
		for _, value := range values {
			number, ok := value.(float64)
			if !ok {
				nums = nil
				break
			}
			nums = append(nums, clampDoubaoLayerCoordinate(number))
		}
		if len(nums) != 4 {
			continue
		}
		regions = append(regions, doubao.LayerRegion{X1: nums[0], Y1: nums[1], X2: nums[2], Y2: nums[3]})
	}
	return regions
}

// clampDoubaoLayerCoordinate 把选区坐标收敛到 0-1000 的归一化坐标系。
func clampDoubaoLayerCoordinate(value float64) int {
	if value < 0 {
		return 0
	}
	if value > 1000 {
		return 1000
	}
	return int(value + 0.5)
}

func (s *Service) downloadDoubaoPoolMedia(ctx context.Context, config providerConfig, urls []string, label string) ([]byte, string, error) {
	var lastErr error
	for _, rawURL := range urls {
		trimmed := strings.TrimSpace(rawURL)
		if trimmed == "" {
			continue
		}
		downloadCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		data, mimeType, err := getProviderExternalBinary(withProviderRequestKind(downloadCtx, "download"), config, trimmed)
		cancel()
		if err == nil && len(data) > 0 {
			return data, normalizedMediaMimeType(mimeType, data), nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, "", fmt.Errorf("豆包%s下载失败：%w", label, lastErr)
	}
	return nil, "", fmt.Errorf("豆包账号池未返回%s地址", label)
}

func parsePositiveInt(value string) (int, error) {
	var parsed int
	_, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("无效的正整数：%s", value)
	}
	return parsed, nil
}

// resolveDoubaoPoolModelSelection 是账号池渠道（豆包 / Dola）的任务准入：无渠道表、无价格档，
// 只校验模型键与任务能力匹配后回填 provider 路由字段。渠道 ID 原样保留，
// 执行层按它锁定取号站点（豆包池跨站豆包优先，Dola 池锁定 Dola 站）。
func (s *Service) resolveDoubaoPoolModelSelection(config map[string]any, taskType string) (map[string]any, error) {
	channelID := strings.TrimSpace(stringValue(config["channelId"]))
	if !IsAccountPoolChannel(channelID) {
		channelID = DoubaoPoolChannelID
	}
	modelKey := strings.TrimPrefix(strings.TrimSpace(stringValue(config["model"])), "models/")
	capability, ok := doubaoPoolModelCapability(modelKey)
	if !ok {
		return nil, InvalidModelSelection("账号池渠道不支持模型：" + modelKey)
	}
	// 模型键与渠道必须配套：配错会在另一个站点上再映射一次模型名，把用户的请求静默跑成别的模型。
	if reason := accountPoolModelMismatch(channelID, modelKey); reason != "" {
		return nil, InvalidModelSelection(reason)
	}
	// 任务类型是 canvas_image / canvas_video / video_* 这类形态，所以能力映射必须用全仓统一的
	// capabilityFromTaskType；只认裸 "image"/"video" 的本地判断在生产调用里永远不命中，
	// 等于让能力校验形同虚设：视频模型键能进图片任务，执行层只按模型键判断是否拆层，会静默出一张普通图。
	if want := capabilityFromTaskType(taskType); want != "" && want != capability {
		return nil, ModelCapabilityNotSupported("所选模型与任务能力不匹配")
	}
	nextConfig := make(map[string]any, len(config)+2)
	for key, value := range config {
		switch key {
		case "channelId", "channelModelKey", "priceTierId", "providerModelKey", "apiFormat", "interfaceType", "baseUrl", "apiKey", "secretKey", "headers", "model", "capabilityConfig":
			continue
		default:
			nextConfig[key] = value
		}
	}
	nextConfig["channelId"] = channelID
	nextConfig["model"] = modelKey
	nextConfig["channelModelKey"] = modelKey
	nextConfig["providerModelKey"] = modelKey
	nextConfig["interfaceType"] = DoubaoPoolInterfaceType
	nextConfig["apiFormat"] = "openai"
	nextConfig["priceTierId"] = ""
	return nextConfig, nil
}

// ---- 账号池渠道的公开目录读模型 ----

// accountPoolModelSpec 描述账号池渠道里的一个模型条目。
type accountPoolModelSpec struct {
	key         string
	displayName string
	description string
	capability  string
}

// accountPoolChannelSpec 描述一个内置账号池渠道。
type accountPoolChannelSpec struct {
	channelID   string
	displayName string
	sortOrder   int
	// site 是统计可用性时的站点：豆包渠道看豆包站，Dola 渠道锁定 Dola 站。
	site   string
	models []accountPoolModelSpec
}

// accountPoolChannelSpecs 是账号池渠道的登记表。
// 这里只列真实可履约的模型键（doubaoPoolModelCapability 认得的那些）：
// Dola 站不出图，所以 dola-pool 只登记视频键；模型键与渠道 ID 必须配套，
// 因为取号站点由渠道决定（accountPoolSite）。
func accountPoolChannelSpecs() []accountPoolChannelSpec {
	return []accountPoolChannelSpec{
		{
			channelID:   DoubaoPoolChannelID,
			displayName: "豆包账号池（网页版）",
			sortOrder:   900,
			site:        doubao.SiteDoubao,
			models: []accountPoolModelSpec{
				{key: doubaoPoolImageModel, displayName: "豆包 Seedream 图片", description: "账号池网页版文生图 / 图生图，不扣平台积分。", capability: "image"},
				{key: doubaoPoolLayerModel, displayName: "豆包 Seedream 图层拆分", description: "按层逐张生成（单次请求拆层是官方 API 能力，网页账号做不到），层数上限 6，不输出透明 PNG。", capability: "image"},
				{key: "doubao-seedance-video", displayName: "豆包 Seedance 2.0 Mini", description: "账号池文生 / 图生视频，豆包站优先、可跨站取号。", capability: "video"},
				{key: "doubao-seedance-video-fast", displayName: "豆包 Seedance 2.0 Fast", description: "账号池快速档视频，豆包站优先、可跨站取号。", capability: "video"},
			},
		},
		{
			channelID:   DolaPoolChannelID,
			displayName: "Dola 账号池（视频）",
			sortOrder:   901,
			site:        doubao.SiteDola,
			models: []accountPoolModelSpec{
				{key: dolaPoolVideoModel25, displayName: "Dola Seedance 2.5", description: "Dola 站视频，锁定 Dola 账号取号。", capability: "video"},
				{key: dolaPoolVideoModelFast, displayName: "Dola Seedance 2.0 Fast", description: "Dola 站快速档视频，锁定 Dola 账号取号。", capability: "video"},
				{key: dolaPoolVideoModel10, displayName: "Dola Seedance 1.0", description: "Dola 站基础档视频，锁定 Dola 账号取号。", capability: "video"},
			},
		},
	}
}

// accountPoolChannelCatalog 把内置账号池渠道合成为创作端目录里的可选渠道。
// 账号池凭据来自池服务、刻意不落 system_channels（见文件头注释），所以目录读模型必须在这里显式合成。
// 这些渠道带 optional + defaultEnabled=true：默认就出现在用户的候选列表里（账号池模型对用户暴露），
// 用户不想用时在设置里自己关掉；具体能不能用则看 availability。
// defaultEnabled 由后端发布，是「默认暴露 / 默认不暴露」的唯一开关，改这里不需要前端发版。
// 这是纯读路径：池状态读取失败只降级为 state=error，绝不能让整个模型目录失败。
func (s *Service) accountPoolChannelCatalog(intent *ModelRequestIntent) []PublicChannelCatalog {
	statuses := map[string]*doubao.PoolStatus{}
	errors := map[string]error{}
	for _, site := range []string{doubao.SiteDoubao, doubao.SiteDola} {
		status, err := s.DoubaoPoolStatus(site)
		statuses[site], errors[site] = status, err
		if err != nil {
			log.Printf("[doubao-pool] 读取 %s 站账号池状态失败，目录降级发布：%v", site, err)
		}
	}
	specs := accountPoolChannelSpecs()
	// 豆包渠道的可用数只统计豆包站（图片只认豆包站账号），Dola 站的可选账号数写进 detail。
	dolaReady, _ := accountPoolStatusCounts(statuses[doubao.SiteDola])
	result := make([]PublicChannelCatalog, 0, len(specs))
	for _, spec := range specs {
		models := accountPoolCatalogModels(spec.models, intent)
		if len(models) == 0 {
			continue
		}
		availabilityDetail := ""
		if spec.site == doubao.SiteDoubao && dolaReady > 0 {
			availabilityDetail = fmt.Sprintf("Dola 站另有 %d 个账号可用于视频", dolaReady)
		}
		result = append(result, PublicChannelCatalog{
			ID:             spec.channelID,
			Name:           spec.displayName,
			DisplayName:    spec.displayName,
			SortOrder:      spec.sortOrder,
			Optional:       true,
			DefaultEnabled: true,
			Availability:   accountPoolSiteAvailability(statuses[spec.site], errors[spec.site], availabilityDetail),
			Models:         models,
		})
	}
	return result
}

// accountPoolSiteAvailability 把站点池状态转成公开可用性快照。
// 读路径降级：状态读取失败标 state=error 并给出可读文案，不让用户把「读不到」误读成「0 个账号」。
func accountPoolSiteAvailability(status *doubao.PoolStatus, statusErr error, detail string) *PublicChannelAvailability {
	ready, total := accountPoolStatusCounts(status)
	state := "empty"
	switch {
	case statusErr != nil:
		state = "error"
	case ready > 0:
		state = "ready"
	}
	if statusErr != nil {
		detail = "账号池状态读取失败"
	}
	return &PublicChannelAvailability{State: state, ReadyAccounts: ready, TotalAccounts: total, Detail: detail}
}

// accountPoolStatusCounts 汇总池状态里的可用 / 账号总数；status 为空时按 0 处理。
func accountPoolStatusCounts(status *doubao.PoolStatus) (int, int) {
	if status == nil {
		return 0, 0
	}
	return status.AvailableCount, status.AccountCount
}

// accountPoolCatalogModels 生成账号池渠道的公开模型条目。
// 账号池模型没有渠道模型行，所以 ID 直接用模型键（稳定且可读）；不发布价格档：
// pricingMode=pool + priceLabel 说明这里不按平台积分数计费，前端也不会把它当成 0 积分模型。
// intent 非空时按能力过滤，避免图片选择器里混进视频模型。
func accountPoolCatalogModels(specs []accountPoolModelSpec, intent *ModelRequestIntent) []PublicChannelModel {
	wanted := ""
	if intent != nil {
		wanted = normalizeCapability(intent.Capability)
	}
	models := make([]PublicChannelModel, 0, len(specs))
	for _, spec := range specs {
		if wanted != "" && normalizeCapability(spec.capability) != wanted {
			continue
		}
		models = append(models, PublicChannelModel{
			ID:          spec.key,
			ModelKey:    spec.key,
			DisplayName: spec.displayName,
			Description: spec.description,
			Capability:  spec.capability,
			Protocol:    model.ChannelInterfaceType(DoubaoPoolInterfaceType),
			PriceTiers:  []PublicChannelModelPriceTier{},
			PricingMode: "pool",
			PriceLabel:  "账号池 · 不扣费",
			Available:   true,
		})
	}
	return models
}
