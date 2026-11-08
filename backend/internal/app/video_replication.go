package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

// 商品视频复刻（影棚视频的一种创作模式）的项目、素材与 @ 引用。
// 工作流：素材检查 → ffmpeg 解析参考视频 → 识图模型理解镜头 → 商品/模特档案 → 策划模型重构分镜
// → 用户确认/编辑分镜 → 逐镜头提交工作台视频任务 → 时间线渲染任务合成成片。

const (
	VideoReplicationTypeProduct = "product"
	VideoReplicationTypeModel   = "model"
	VideoReplicationTypeApparel = "apparel"
	VideoReplicationTypeFace    = "face"
	// VideoReplicationTypeBackground 换背景：保留人物、商品与动作，只把场景换成背景参考图。
	VideoReplicationTypeBackground = "background"

	videoReplicationMaxVideos   = 6
	videoReplicationMaxImages   = 9
	videoReplicationMaxPrompt   = 4000
	videoReplicationMaxShotsOut = 30
)

var videoReplicationMentionPattern = regexp.MustCompile(`@\[node:([A-Za-z0-9_-]{1,64})\]`)

var videoReplicationShotIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var videoReplicationCategoryLabels = map[string]string{
	model.VideoReplicationAssetReferenceVideo:     "参考视频",
	model.VideoReplicationAssetProductImage:       "商品图片",
	model.VideoReplicationAssetModelImage:         "模特图片",
	model.VideoReplicationAssetApparelImage:       "服饰图片",
	model.VideoReplicationAssetFaceImage:          "人脸照片",
	model.VideoReplicationAssetBackgroundImage:    "背景图片",
	model.VideoReplicationAssetVoiceAudio:         "参考音色",
	model.VideoReplicationAssetProductDetailImage: "商品细节图",
	model.VideoReplicationAssetPackagingImage:     "包装图",
}

type VideoReplicationParameters struct {
	Model          string `json:"model,omitempty"`
	LogicalModelID string `json:"logicalModelId,omitempty"`
	ChannelID      string `json:"channelId,omitempty"`
	ModelLabel     string `json:"modelLabel,omitempty"`
	Ratio          string `json:"ratio,omitempty"`
	Resolution     string `json:"resolution,omitempty"`
	// TargetSeconds 是成片目标总时长；ShotSeconds 是所选模型允许的单镜头时长，分镜按它取整。
	TargetSeconds      int    `json:"targetSeconds,omitempty"`
	ShotSeconds        []int  `json:"shotSeconds,omitempty"`
	GenerateAudio      bool   `json:"generateAudio"`
	Watermark          bool   `json:"watermark"`
	Camera             string `json:"camera,omitempty"`
	Rhythm             string `json:"rhythm,omitempty"`
	Lighting           string `json:"lighting,omitempty"`
	Mood               string `json:"mood,omitempty"`
	ReferenceMode      string `json:"referenceMode,omitempty"`
	MaxReferenceImages int    `json:"maxReferenceImages,omitempty"`
	Transition         string `json:"transition,omitempty"`
	Subtitles          bool   `json:"subtitles"`
	// FaceConsent 是用户对人脸素材肖像授权的确认；人脸类型没有确认时拒绝生成。
	FaceConsent bool `json:"faceConsent"`
	// KeepOriginalAudio 为 true 时成片使用参考视频原音轨（音乐与节奏），适合舞蹈、卡点、BGM 视频。
	KeepOriginalAudio bool `json:"keepOriginalAudio"`
	// Auto / SegmentSeconds / AutoEstimate 由「一键生成」在服务端写入：分析完成后自动分段生成并合成，前端保存参数时不能改写。
	Auto           bool    `json:"auto,omitempty"`
	SegmentSeconds int     `json:"segmentSeconds,omitempty"`
	AutoEstimate   float64 `json:"autoEstimate,omitempty"`
	// MaxVideoTasks 是用户本次一键生成确认的付费视频任务数上限。
	MaxVideoTasks int `json:"maxVideoTasks,omitempty"`
	// ConfirmFirst 为 true 时是「先分析、再确认生成」：分析完成后停在复刻方案，用户确认费用后才提交付费任务。
	ConfirmFirst bool `json:"confirmFirst,omitempty"`
	// RewriteVoice（重做口播）：按用户的商品改写参考视频的口播台词；关闭时沿用原台词。
	RewriteVoice bool `json:"rewriteVoice"`
	// ReplaceMode 是电商 Agent 商品视频选择的替换方式（model / clothing / face / background / product /
	// precise-product / precise-person / combination），决定逐段画面描述里的一致性要求。
	ReplaceMode string `json:"replaceMode,omitempty"`
	// AudioMode：replace 按参考视频台词重新配音（模型原生音频）/ keep 保留原声 / mute 静音。为空时沿用旧字段。
	AudioMode string `json:"audioMode,omitempty"`
	// SubjectHint 是商品替换时用户指定的原视频商品主体（例如「上衣」），最多 10 字。
	SubjectHint string `json:"subjectHint,omitempty"`
	// ProductName 是新商品名称，与原视频里的商品主体分开。
	ProductName string `json:"productName,omitempty"`
	// MaxReferenceAudios 是所选模型可接收的参考音频数（来自模型能力配置），0 表示不发送参考音色。
	MaxReferenceAudios int `json:"maxReferenceAudios,omitempty"`
	// MaxReferenceVideos / MaxReferenceVideoSeconds 来自所选模型能力：能接收参考视频时走「原片编辑」，
	// 每段把对应的原视频片段作为参考视频发送，只替换目标，场景、动作与运镜沿用原片。
	MaxReferenceVideos       int `json:"maxReferenceVideos,omitempty"`
	MaxReferenceVideoSeconds int `json:"maxReferenceVideoSeconds,omitempty"`
	// MaxReferenceAudioSeconds / PromptMaxChars 同样来自后台渠道给所选模型配的能力（参考音频时长、提示词字数），
	// 开始生成时由服务端按模型配置重写（applyVideoReplicationModelLimits），不在代码里写死。
	MaxReferenceAudioSeconds int `json:"maxReferenceAudioSeconds,omitempty"`
	PromptMaxChars           int `json:"promptMaxChars,omitempty"`
	// TrimStartMs / TrimEndMs 是「裁剪视频」选定的参考视频区间（相对原视频，毫秒）；TrimEndMs 为 0 表示不裁剪。
	TrimStartMs int64 `json:"trimStartMs,omitempty"`
	TrimEndMs   int64 `json:"trimEndMs,omitempty"`
	// Regions 是用户在原视频 RegionFrameMs 处画面上框出的替换目标（归一化坐标），框几件换几件。
	Regions       []VideoReplicationRegion `json:"regions,omitempty"`
	RegionFrameMs int64                    `json:"regionFrameMs,omitempty"`
	// Talking 标记口播视频：识别原台词，成片人物口型与台词同步（保留原声时对原声、换音色时说原台词）。
	Talking bool `json:"talking,omitempty"`
	// RegionTarget 是分析时识图模型对框选目标的描述，只在服务端生成提示词时临时填入，不随参数保存。
	RegionTarget string `json:"-"`
	// Keyframe* 是给元素参考图出图用的系统图片模型（前端按模型能力选好传来）：补充要求换画面元素时，每个新元素先出一张参考图。
	KeyframeModel          string                       `json:"keyframeModel,omitempty"`
	KeyframeLogicalModelID string                       `json:"keyframeLogicalModelId,omitempty"`
	KeyframeChannelID      string                       `json:"keyframeChannelId,omitempty"`
	KeyframeMaxImages      int                          `json:"keyframeMaxImages,omitempty"`
	KeyframeParameters     *VideoReplicationImageParams `json:"keyframeParameters,omitempty"`
}

// VideoReplicationImageParams 是元素参考图出图参数（与工作台图片任务同名字段）。
type VideoReplicationImageParams struct {
	Size            string `json:"size"`
	Quality         string `json:"quality,omitempty"`
	ImageResolution string `json:"imageResolution,omitempty"`
	Count           int    `json:"count"`
}

type VideoReplicationSourceShot struct {
	Index              int    `json:"index"`
	StartMs            int64  `json:"startMs"`
	EndMs              int64  `json:"endMs"`
	KeyframeResourceID string `json:"keyframeResourceId,omitempty"`
	ShotSize           string `json:"shotSize,omitempty"`
	Camera             string `json:"camera,omitempty"`
	Scene              string `json:"scene,omitempty"`
	Action             string `json:"action,omitempty"`
	PersonAction       string `json:"personAction,omitempty"`
	ProductDisplay     string `json:"productDisplay,omitempty"`
	Lighting           string `json:"lighting,omitempty"`
	Composition        string `json:"composition,omitempty"`
	Subtitle           string `json:"subtitle,omitempty"`
	Voiceover          string `json:"voiceover,omitempty"`
	SpeechDelivery     string `json:"speechDelivery,omitempty"`
}

type VideoReplicationVideoAnalysis struct {
	AssetID         string                       `json:"assetId"`
	Name            string                       `json:"name"`
	Primary         bool                         `json:"primary"`
	DurationMs      int64                        `json:"durationMs"`
	Width           int                          `json:"width"`
	Height          int                          `json:"height"`
	FPS             float64                      `json:"fps"`
	HasAudio        bool                         `json:"hasAudio"`
	AudioResourceID string                       `json:"audioResourceId,omitempty"`
	CutCount        int                          `json:"cutCount"`
	Shots           []VideoReplicationSourceShot `json:"shots"`
}

type VideoReplicationSummary struct {
	Delivery    *VideoReplicationSpeechDelivery `json:"delivery,omitempty"`
	Structure   string                          `json:"structure,omitempty"`
	Rhythm      string                          `json:"rhythm,omitempty"`
	EditingPace string                          `json:"editingPace,omitempty"`
	Style       string                          `json:"style,omitempty"`
	Mood        string                          `json:"mood,omitempty"`
	Hook        string                          `json:"hook,omitempty"`
	Subtitles   string                          `json:"subtitles,omitempty"`
	Voiceover   string                          `json:"voiceover,omitempty"`
}

// 表达节奏来自原视频音轨与动作，不作为新商品优惠事实。
type VideoReplicationSpeechDelivery struct {
	Mode        string `json:"mode"`
	Pace        string `json:"pace,omitempty"`
	Pauses      string `json:"pauses,omitempty"`
	Emphasis    string `json:"emphasis,omitempty"`
	Interaction string `json:"interaction,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
}

type VideoReplicationRecommendation struct {
	Camera   string `json:"camera,omitempty"`
	Rhythm   string `json:"rhythm,omitempty"`
	Lighting string `json:"lighting,omitempty"`
	Mood     string `json:"mood,omitempty"`
	Ratio    string `json:"ratio,omitempty"`
	Seconds  int    `json:"seconds,omitempty"`
}

// VideoReplicationVoice 是参考视频的声音分析：类型由音轨探测、ASR 结果与画面理解综合判断。
// Type：on_camera 人物对镜口播 / narration 画外旁白 / dialogue 多人对话 / music 纯背景音乐 / silent 无声 / unknown。
type VideoReplicationVoice struct {
	Type          string                         `json:"type"`
	TypeLabel     string                         `json:"typeLabel"`
	HasAudio      bool                           `json:"hasAudio"`
	ASRAvailable  bool                           `json:"asrAvailable"`
	ASRError      string                         `json:"asrError,omitempty"`
	Language      string                         `json:"language,omitempty"`
	SpeechMs      int64                          `json:"speechMs"`
	Segments      []timelineTranscriptionSegment `json:"segments,omitempty"`
	Diarization   bool                           `json:"diarization"`
	Music         string                         `json:"music,omitempty"`
	SoundEffects  string                         `json:"soundEffects,omitempty"`
	SpeakerCount  string                         `json:"speakerCount,omitempty"`
	CharsPerSec   float64                        `json:"charsPerSecond,omitempty"`
	LipSyncNeeded bool                           `json:"lipSyncNeeded"`
	Speakers      []VideoReplicationSpeaker      `json:"speakers,omitempty"`
}

// VideoReplicationSpeaker 是画面理解识别出的一个说话人（不识别真实身份）。
type VideoReplicationSpeaker struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Gender   string `json:"gender,omitempty"`
	Age      string `json:"age,omitempty"`
	Tone     string `json:"tone,omitempty"`
	OnCamera bool   `json:"onCamera"`
}

// VideoReplicationCounts 是画面理解得到的整体统计。
type VideoReplicationCounts struct {
	Shots    int    `json:"shots"`
	Scenes   string `json:"scenes,omitempty"`
	People   string `json:"people,omitempty"`
	Products string `json:"products,omitempty"`
}

type VideoReplicationAnalysis struct {
	// voiceCast 是分析结束时 Agent 推荐的说话人音色，单独写入项目，不随分析 JSON 保存。
	voiceCast       VideoReplicationVoiceCast
	Voice           *VideoReplicationVoice          `json:"voice,omitempty"`
	Counts          VideoReplicationCounts          `json:"counts"`
	Marketing       string                          `json:"marketing,omitempty"`
	Videos          []VideoReplicationVideoAnalysis `json:"videos"`
	Summary         VideoReplicationSummary         `json:"summary"`
	Product         map[string]any                  `json:"product,omitempty"`
	Model           map[string]any                  `json:"model,omitempty"`
	Recommendation  VideoReplicationRecommendation  `json:"recommendation"`
	VisionModel     string                          `json:"visionModel,omitempty"`
	PlanningModel   string                          `json:"planningModel,omitempty"`
	FFmpegSceneMode string                          `json:"ffmpegSceneMode,omitempty"`
	AnalyzedAt      time.Time                       `json:"analyzedAt"`
	// MediaDigest 覆盖参考视频与商品/模特素材；不变时复用已保存的视频理解结果，不重复调用识图模型。
	// PlanDigest 再加上替换目标、要求、单段时长与比例；也不变时分析与分段方案整份复用。
	MediaDigest string `json:"mediaDigest,omitempty"`
	PlanDigest  string `json:"planDigest,omitempty"`
	// RegionTarget 是识图模型对用户框选目标的描述；RegionFrameResourceID 是画好红框的那一帧。
	RegionTarget          string `json:"regionTarget,omitempty"`
	RegionFrameResourceID string `json:"regionFrameResourceId,omitempty"`
	// Structure 是新旧商品的结构对齐结果（逐段动作改写），在分段规划时生成。
	Structure *VideoReplicationStructure `json:"structure,omitempty"`
}

type VideoReplicationShot struct {
	ID           string `json:"id"`
	Index        int    `json:"index"`
	DurationSec  int    `json:"durationSec"`
	Title        string `json:"title"`
	ShotSize     string `json:"shotSize,omitempty"`
	Camera       string `json:"camera,omitempty"`
	Scene        string `json:"scene,omitempty"`
	Action       string `json:"action,omitempty"`
	ProductFocus string `json:"productFocus,omitempty"`
	Lighting     string `json:"lighting,omitempty"`
	Mood         string `json:"mood,omitempty"`
	Subtitle     string `json:"subtitle,omitempty"`
	Voiceover    string `json:"voiceover,omitempty"`
	Speaker      string `json:"speaker,omitempty"`
	SourceShot   string `json:"sourceShot,omitempty"`
	// SourceStartMs / SourceEndMs 是这一段对应的参考视频区间（裁剪后的时间轴）；原片编辑模式按它截取参考视频片段。
	SourceStartMs int64  `json:"sourceStartMs,omitempty"`
	SourceEndMs   int64  `json:"sourceEndMs,omitempty"`
	Prompt        string `json:"prompt"`
	// Adaptation 是结构对齐后这一段的动作：原片在拆装 / 展示原商品某个部件时，改成新商品上对应部件的做法。
	Adaptation string `json:"adaptation,omitempty"`
	// Packaging 表示这一段原片出现了包装盒，需要换成新商品的包装。
	Packaging bool `json:"packaging,omitempty"`
	// ProductPresence 是原商品在这一段的存在感（hero / hand / minor / none，来自结构对齐）；为空表示未知。
	ProductPresence string `json:"productPresence,omitempty"`
	// SceneEdits 是识图看本段关键帧后，按用户补充要求逐个点名的元素替换（「位置+原元素→新元素」，用「；」分隔）。
	// 只写一句「所有梨换成香蕉」时，视频模型只改最显眼的几个，没点名的梨原样保留。
	SceneEdits string `json:"sceneEdits,omitempty"`
}

// VideoReplicationElement 是一条元素替换：原片里的 From 全片统一换成 To；ResourceID 是 To 的元素参考图（可空，空时只用文字）。
type VideoReplicationElement struct {
	ID         string `json:"id"`
	From       string `json:"from"`
	To         string `json:"to"`
	ResourceID string `json:"resourceId,omitempty"`
	// Error 是参考图没生成成功的原因，出图成功后清空。
	Error string `json:"error,omitempty"`
}

type VideoReplicationStoryboard struct {
	Shots []VideoReplicationShot `json:"shots"`
	Notes string                 `json:"notes,omitempty"`
	// ProductStructure 是结构对齐时识图模型对新商品结构的概述（部件、按键、开合与拆装方式）。
	ProductStructure string `json:"productStructure,omitempty"`
	// ApparelStructure / Wearing / Styling 是结构对齐给出的新服装结构、正确穿法和整体搭配，替换服装时写进每段提示词。
	ApparelStructure string `json:"apparelStructure,omitempty"`
	Wearing          string `json:"wearing,omitempty"`
	Styling          string `json:"styling,omitempty"`
	// SourceLook / NewSubject / ReferenceProps 来自结构对齐：原商品外观、新商品图里的商品本体、图里不能搬进视频的道具与布景。
	SourceLook     string `json:"sourceLook,omitempty"`
	NewSubject     string `json:"newSubject,omitempty"`
	ReferenceProps string `json:"referenceProps,omitempty"`
	// Changes 是分段规划从用户补充要求里整理出的画面改动清单（任何品类：换主题元素、配色风格、场景、光线、
	// 人物动作表情、镜头、增删物体等，如「梨→苹果；秋日暖黄→清新红绿」）。不为空时逐段提示词不再把原片场景、
	// 道具与光线列为保持不变，只锁镜头数量、剪辑切点与节奏，其余按清单改。
	Changes string `json:"changes,omitempty"`
	// Banned 是按用户补充要求全片都不能再出现的原元素（如「梨、枇杷、梨形小车」）。
	Banned string `json:"banned,omitempty"`
	// Elements 是全片统一的元素替换清单（「梨→香蕉」「梨形小车→香蕉形小车」），来自结构对齐。原片编辑时每个新元素
	// 先自动出一张元素参考图，视频模型以原片为底按「把 @视频1 中的 A 替换成 @图片N 中的 B」逐项替换，镜头构图不动。
	Elements []VideoReplicationElement `json:"elements,omitempty"`
	// Knowledge 是规划这份分镜时调用的模型提示词知识库（Seedance / 可灵）；没有调用时为空。
	Knowledge   string `json:"knowledge,omitempty"`
	KnowledgeID string `json:"knowledgeId,omitempty"`
	// VoiceAdapted：口播已按用户商品与补充说明改写（台词、字幕同步更新）。
	VoiceAdapted bool `json:"voiceAdapted,omitempty"`
	// SubtitlesAdapted：原片字幕已按新商品改写，包含没有人声的纯字幕片段。
	SubtitlesAdapted bool `json:"subtitlesAdapted,omitempty"`
	// Subtitles：参考视频带字幕，成片按新口播生成字幕轨（不让视频模型画字，避免乱码）。
	Subtitles bool `json:"subtitles,omitempty"`
}

type VideoReplicationAssetView struct {
	model.VideoReplicationAsset
	Label   string         `json:"label"`
	Profile map[string]any `json:"profile,omitempty"`
}

type VideoReplicationMention struct {
	AssetID  string `json:"assetId"`
	Category string `json:"category"`
	Label    string `json:"label"`
}

type VideoReplicationProjectView struct {
	model.VideoReplicationProject
	Parameters VideoReplicationParameters      `json:"parameters"`
	Analysis   *VideoReplicationAnalysis       `json:"analysis,omitempty"`
	Storyboard *VideoReplicationStoryboard     `json:"storyboard,omitempty"`
	Assets     []VideoReplicationAssetView     `json:"assets"`
	Mentions   []VideoReplicationMention       `json:"mentions"`
	Runs       []model.VideoReplicationShotRun `json:"runs"`
	Outputs    []VideoReplicationOutputView    `json:"outputs"`
	Plan       *VideoReplicationPlan           `json:"plan,omitempty"`
	PlanStale  bool                            `json:"planStale"`
	Spent      float64                         `json:"spentCredits"`
	VoiceCast  VideoReplicationVoiceCast       `json:"voiceCast"`
	// AnalysisFresh 表示已保存的分析与分段方案仍对应当前素材和要求，可以直接确认生成，无需再次分析。
	AnalysisFresh bool `json:"analysisFresh"`
}

type VideoReplicationProjectInput struct {
	Title      *string                     `json:"title,omitempty"`
	VideoType  *string                     `json:"videoType,omitempty"`
	Prompt     *string                     `json:"prompt,omitempty"`
	Parameters *VideoReplicationParameters `json:"parameters,omitempty"`
}

type VideoReplicationAssetInput struct {
	Category   string `json:"category"`
	ResourceID string `json:"resourceId"`
	Name       string `json:"name"`
}

type VideoReplicationReorderInput struct {
	Category  string   `json:"category"`
	IDs       []string `json:"ids"`
	PrimaryID string   `json:"primaryId,omitempty"`
}

// videoReplicationTargetOrder 是替换目标的固定顺序；每个目标对应一类参考图。
var videoReplicationTargetOrder = []string{VideoReplicationTypeProduct, VideoReplicationTypeModel, VideoReplicationTypeApparel, VideoReplicationTypeFace, VideoReplicationTypeBackground}

var videoReplicationTargetCategory = map[string]string{
	VideoReplicationTypeProduct:    model.VideoReplicationAssetProductImage,
	VideoReplicationTypeModel:      model.VideoReplicationAssetModelImage,
	VideoReplicationTypeApparel:    model.VideoReplicationAssetApparelImage,
	VideoReplicationTypeFace:       model.VideoReplicationAssetFaceImage,
	VideoReplicationTypeBackground: model.VideoReplicationAssetBackgroundImage,
}

// videoReplicationTargets 解析逗号分隔的替换目标；为空表示不替换商品/人物，只按创作描述复刻。
func videoReplicationTargets(value string) []string {
	selected := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		selected[strings.TrimSpace(item)] = true
	}
	targets := []string{}
	for _, target := range videoReplicationTargetOrder {
		if selected[target] {
			targets = append(targets, target)
		}
	}
	return targets
}

func videoReplicationHasTarget(value, target string) bool {
	return containsString(videoReplicationTargets(value), target)
}

func validVideoReplicationType(value string) bool {
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" && videoReplicationTargetCategory[item] == "" {
			return false
		}
	}
	return true
}

func (s *Service) requireVideoReplication() error {
	return s.RequireFeature(FeatureVideoWorkbench)
}

func (s *Service) CreateVideoReplicationProject(userID string, input VideoReplicationProjectInput) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project := &model.VideoReplicationProject{ID: newID(), UserID: userID, Title: "商品视频复刻", VideoType: "", Status: model.VideoReplicationStatusDraft, ParametersJSON: "{}"}
	if err := applyVideoReplicationInput(project, input); err != nil {
		return nil, err
	}
	if err := s.repo.CreateVideoReplicationProject(project); err != nil {
		return nil, err
	}
	return s.VideoReplicationProject(userID, project.ID)
}

func applyVideoReplicationInput(project *model.VideoReplicationProject, input VideoReplicationProjectInput) error {
	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if title == "" || utf8.RuneCountInString(title) > 60 {
			return BadAuthRequest("项目名称需为 1-60 个字")
		}
		project.Title = title
	}
	if input.VideoType != nil {
		if !validVideoReplicationType(*input.VideoType) {
			return BadAuthRequest("替换目标无效")
		}
		project.VideoType = strings.Join(videoReplicationTargets(*input.VideoType), ",")
	}
	if input.Prompt != nil {
		if utf8.RuneCountInString(*input.Prompt) > videoReplicationMaxPrompt {
			return BadAuthRequest(fmt.Sprintf("创作描述最多 %d 字", videoReplicationMaxPrompt))
		}
		project.Prompt = *input.Prompt
	}
	if input.Parameters != nil {
		params := *input.Parameters
		if err := validateVideoReplicationParameters(&params); err != nil {
			return err
		}
		current := videoReplicationParameters(project)
		params.Auto, params.SegmentSeconds, params.AutoEstimate, params.ConfirmFirst = current.Auto, current.SegmentSeconds, current.AutoEstimate, current.ConfirmFirst
		params.MaxVideoTasks = current.MaxVideoTasks
		raw, _ := json.Marshal(params)
		project.ParametersJSON = string(raw)
	}
	return nil
}

func validateVideoReplicationParameters(params *VideoReplicationParameters) error {
	if params.KeyframeMaxImages < 0 || params.KeyframeMaxImages > 20 {
		return BadAuthRequest("元素参考图模型参数无效")
	}
	if p := params.KeyframeParameters; p != nil {
		p.Count = 1
		for _, value := range []*string{&p.Size, &p.Quality, &p.ImageResolution} {
			*value = strings.TrimSpace(*value)
			if utf8.RuneCountInString(*value) > 32 || strings.Contains(*value, "://") {
				return BadAuthRequest("元素参考图模型参数无效")
			}
		}
	}
	for _, value := range []*string{&params.Model, &params.LogicalModelID, &params.ChannelID, &params.ModelLabel, &params.Ratio, &params.Resolution, &params.Camera, &params.Rhythm, &params.Lighting, &params.Mood, &params.ReferenceMode, &params.Transition, &params.KeyframeModel, &params.KeyframeLogicalModelID, &params.KeyframeChannelID} {
		*value = strings.TrimSpace(*value)
		if utf8.RuneCountInString(*value) > 120 || strings.Contains(*value, "://") || strings.HasPrefix(strings.ToLower(*value), "data:") {
			return BadAuthRequest("视频参数格式无效")
		}
	}
	switch params.ReplaceMode {
	case "", "model", "clothing", "face", "background", "product", "precise-product", "precise-person", "combination":
	default:
		return BadAuthRequest("替换方式无效")
	}
	switch params.AudioMode {
	case "", "replace", "keep", "mute", "generate":
	default:
		return BadAuthRequest("声音选项无效")
	}
	params.SubjectHint = strings.TrimSpace(params.SubjectHint)
	if utf8.RuneCountInString(params.SubjectHint) > 10 || strings.Contains(params.SubjectHint, "://") {
		return BadAuthRequest("商品主体最多 10 个字")
	}
	params.ProductName = strings.TrimSpace(params.ProductName)
	if utf8.RuneCountInString(params.ProductName) > 60 || strings.ContainsFunc(params.ProductName, unicode.IsControl) {
		return BadAuthRequest("商品名称最多 60 个字，请填写单行名称")
	}
	switch params.ReferenceMode {
	case "", "reference", "first_frame", "none":
	default:
		return BadAuthRequest("参考图模式无效")
	}
	switch params.Transition {
	case "", "cut", "fade":
	default:
		return BadAuthRequest("转场方式无效")
	}
	if err := validateVideoReplicationClip(params); err != nil {
		return err
	}
	// 参考素材数量 / 时长 / 提示词字数以后台给模型配的能力为准（开始生成时服务端再按模型配置重写），
	// 这里只挡明显异常的数值，不再按固定的 10 个视频、3 段音频、20 张图截断管理员的配置。
	if params.MaxReferenceVideos < 0 || params.MaxReferenceVideos > videoReplicationParamSanityMax || params.MaxReferenceVideoSeconds < 0 || params.MaxReferenceVideoSeconds > 3600 {
		return BadAuthRequest("参考视频能力参数无效")
	}
	if params.MaxReferenceAudios < 0 || params.MaxReferenceAudios > videoReplicationParamSanityMax || params.MaxReferenceAudioSeconds < 0 || params.MaxReferenceAudioSeconds > 3600 {
		return BadAuthRequest("参考音频数量无效")
	}
	if params.PromptMaxChars < 0 || params.PromptMaxChars > 200000 {
		return BadAuthRequest("提示词上限参数无效")
	}
	maxSeconds := 600
	if params.TargetSeconds < 0 || params.TargetSeconds > maxSeconds || params.MaxReferenceImages < 0 || params.MaxReferenceImages > videoReplicationParamSanityMax || len(params.ShotSeconds) > 64 {
		return BadAuthRequest("视频参数数值无效")
	}
	for _, value := range params.ShotSeconds {
		if value < 1 || value > 60 {
			return BadAuthRequest("视频参数数值无效")
		}
	}
	return nil
}

func (s *Service) UpdateVideoReplicationProject(userID, id string, input VideoReplicationProjectInput) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, id)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if err := applyVideoReplicationInput(project, input); err != nil {
		return nil, err
	}
	values := map[string]any{"title": project.Title, "video_type": project.VideoType, "prompt": project.Prompt, "parameters_json": project.ParametersJSON}
	if input.Prompt != nil {
		assets, err := s.repo.VideoReplicationAssets(userID, id)
		if err != nil {
			return nil, err
		}
		if _, err := videoReplicationMentions(project.Prompt, assets); err != nil {
			return nil, err
		}
	}
	if err := s.repo.UpdateVideoReplicationProject(userID, id, values); err != nil {
		return nil, err
	}
	return s.VideoReplicationProject(userID, id)
}

func videoReplicationNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NotFound("商品视频项目不存在")
	}
	return err
}

func (s *Service) VideoReplicationProjects(userID string, limit int) ([]model.VideoReplicationProject, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	return s.repo.VideoReplicationProjects(userID, limit)
}

// VideoReplicationProject 读取项目并同步镜头任务与合成任务的最新状态。
func (s *Service) VideoReplicationProject(userID, id string) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, id)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if project.Status == model.VideoReplicationStatusAnalyzing && (project.AnalysisLeaseUntil == nil || project.AnalysisLeaseUntil.Before(time.Now())) {
		if err := s.repo.ExpireVideoReplicationAnalysis(userID, id); err != nil {
			return nil, err
		}
		if project, err = s.repo.VideoReplicationProject(userID, id); err != nil {
			return nil, err
		}
	}
	if err := s.syncVideoReplication(context.Background(), project); err != nil {
		return nil, err
	}
	return s.videoReplicationView(project)
}

func (s *Service) videoReplicationView(project *model.VideoReplicationProject) (*VideoReplicationProjectView, error) {
	assets, err := s.repo.VideoReplicationAssets(project.UserID, project.ID)
	if err != nil {
		return nil, err
	}
	runs, err := s.repo.VideoReplicationRuns(project.UserID, project.ID)
	if err != nil {
		return nil, err
	}
	outputs, err := s.repo.VideoReplicationOutputs(project.UserID, project.ID)
	if err != nil {
		return nil, err
	}
	view := &VideoReplicationProjectView{VideoReplicationProject: *project, Assets: videoReplicationAssetViews(assets), Runs: runs, Outputs: make([]VideoReplicationOutputView, 0, len(outputs))}
	for _, output := range outputs {
		item := VideoReplicationOutputView{VideoReplicationOutput: output}
		if strings.TrimSpace(output.QualityJSON) != "" {
			item.Quality = &VideoReplicationQuality{}
			_ = json.Unmarshal([]byte(output.QualityJSON), item.Quality)
		}
		view.Outputs = append(view.Outputs, item)
	}
	if plan := videoReplicationPlan(project); plan != nil {
		view.Plan = plan
		view.PlanStale = plan.Digest != videoReplicationDigest(project) || plan.StoryboardRevision != project.StoryboardRevision
	}
	view.VoiceCast = videoReplicationVoiceCast(project)
	if spent, err := s.videoReplicationSpentCredits(project.UserID, runs); err == nil {
		view.Spent = spent
	}
	view.Parameters = videoReplicationParameters(project)
	if strings.TrimSpace(project.AnalysisJSON) != "" {
		var analysis VideoReplicationAnalysis
		if json.Unmarshal([]byte(project.AnalysisJSON), &analysis) == nil {
			view.Analysis = &analysis
			view.AnalysisFresh = project.Status != model.VideoReplicationStatusAnalyzing && analysis.PlanDigest != "" && analysis.PlanDigest == videoReplicationPlanDigest(project, assets)
		}
	}
	if storyboard := videoReplicationStoryboard(project); storyboard != nil {
		view.Storyboard = storyboard
	}
	view.Mentions, _ = videoReplicationMentions(project.Prompt, assets)
	if view.Mentions == nil {
		view.Mentions = []VideoReplicationMention{}
	}
	return view, nil
}

func videoReplicationParameters(project *model.VideoReplicationProject) VideoReplicationParameters {
	var params VideoReplicationParameters
	_ = json.Unmarshal([]byte(project.ParametersJSON), &params)
	return params
}

func videoReplicationStoryboard(project *model.VideoReplicationProject) *VideoReplicationStoryboard {
	if strings.TrimSpace(project.StoryboardJSON) == "" {
		return nil
	}
	var storyboard VideoReplicationStoryboard
	if json.Unmarshal([]byte(project.StoryboardJSON), &storyboard) != nil {
		return nil
	}
	return &storyboard
}

// videoReplicationAssetViews 按类别内顺序生成「参考视频01」这类展示名；引用关系始终以素材 ID 为准。
func videoReplicationAssetViews(assets []model.VideoReplicationAsset) []VideoReplicationAssetView {
	counters := map[string]int{}
	views := make([]VideoReplicationAssetView, 0, len(assets))
	for _, asset := range assets {
		counters[asset.Category]++
		view := VideoReplicationAssetView{VideoReplicationAsset: asset, Label: fmt.Sprintf("%s%02d", videoReplicationCategoryLabels[asset.Category], counters[asset.Category])}
		if strings.TrimSpace(asset.ProfileJSON) != "" {
			_ = json.Unmarshal([]byte(asset.ProfileJSON), &view.Profile)
		}
		views = append(views, view)
	}
	return views
}

// videoReplicationMentions 解析创作描述里的 @[node:<素材 ID>]；引用不属于本项目的素材视为非法请求。
func videoReplicationMentions(prompt string, assets []model.VideoReplicationAsset) ([]VideoReplicationMention, error) {
	views := videoReplicationAssetViews(assets)
	byID := map[string]VideoReplicationAssetView{}
	for _, view := range views {
		byID[view.ID] = view
	}
	seen := map[string]bool{}
	mentions := []VideoReplicationMention{}
	for _, match := range videoReplicationMentionPattern.FindAllStringSubmatch(prompt, -1) {
		view, ok := byID[match[1]]
		if !ok {
			return nil, BadAuthRequest("创作描述引用了不存在或已删除的素材，请重新选择")
		}
		if seen[view.ID] {
			continue
		}
		seen[view.ID] = true
		mentions = append(mentions, VideoReplicationMention{AssetID: view.ID, Category: view.Category, Label: view.Label})
	}
	return mentions, nil
}

// videoReplicationReadablePrompt 把 @[node:id] 换成「@商品图片01」，供模型阅读。
func videoReplicationReadablePrompt(prompt string, assets []model.VideoReplicationAsset) string {
	labels := map[string]string{}
	for _, view := range videoReplicationAssetViews(assets) {
		labels[view.ID] = view.Label
	}
	return videoReplicationMentionPattern.ReplaceAllStringFunc(prompt, func(token string) string {
		id := videoReplicationMentionPattern.FindStringSubmatch(token)[1]
		if label, ok := labels[id]; ok {
			return "@" + label
		}
		return ""
	})
}

func (s *Service) AddVideoReplicationAsset(userID, projectID string, input VideoReplicationAssetInput) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if project.Status == model.VideoReplicationStatusAnalyzing {
		return nil, BadAuthRequest("参考视频分析中，请等待分析完成后再调整素材")
	}
	category := strings.TrimSpace(input.Category)
	if _, ok := videoReplicationCategoryLabels[category]; !ok {
		return nil, BadAuthRequest("素材类别无效")
	}
	assets, err := s.repo.VideoReplicationAssets(userID, projectID)
	if err != nil {
		return nil, err
	}
	sameCategory := 0
	for _, asset := range assets {
		if asset.Category != category {
			continue
		}
		sameCategory++
		if asset.ResourceID == strings.TrimSpace(input.ResourceID) {
			return nil, BadAuthRequest("该素材已添加")
		}
	}
	limit := videoReplicationMaxImages
	if category == model.VideoReplicationAssetReferenceVideo {
		limit = videoReplicationMaxVideos
	}
	if category == model.VideoReplicationAssetVoiceAudio {
		limit = videoReplicationMaxVoiceAudios
	}
	if sameCategory >= limit {
		return nil, BadAuthRequest(fmt.Sprintf("%s最多 %d 个", videoReplicationCategoryLabels[category], limit))
	}
	resource, err := s.repo.ResourceForUser(userID, strings.TrimSpace(input.ResourceID))
	if err != nil || resource.Status != model.ResourceStatusReady {
		return nil, BadAuthRequest("素材不存在或尚未上传完成")
	}
	name := strings.TrimSpace(input.Name)
	if utf8.RuneCountInString(name) > 120 {
		name = string([]rune(name)[:120])
	}
	asset := &model.VideoReplicationAsset{ID: newID(), ProjectID: projectID, UserID: userID, Category: category, ResourceID: resource.ID, Name: name, SortOrder: sameCategory, IsPrimary: sameCategory == 0, MimeType: resource.MimeType, SizeBytes: resource.Size, Width: resource.Width, Height: resource.Height, DurationMs: resource.DurationMs}
	if category == model.VideoReplicationAssetReferenceVideo {
		if resource.Kind != "video" || !videoReplicationVideoMimes[strings.ToLower(resource.MimeType)] {
			// 扩展名是 MP4 / MOV 但内容识别不出视频：文件损坏或不完整，而不是格式不支持。
			if videoReplicationVideoFileName(name) {
				return nil, BadAuthRequest(videoReplicationBrokenVideoMessage)
			}
			return nil, BadAuthRequest("参考视频只支持 MP4、MOV")
		}
		if resource.Size > videoReplicationMaxVideoBytes {
			return nil, BadAuthRequest("参考视频不能超过 100MB")
		}
		// 以服务器 ffprobe 读取的完整文件为准，不使用浏览器上报的时长与尺寸。
		err := s.withVideoReplicationResourceFile(userID, resource.ID, videoReplicationMaxVideoBytes, func(_ *model.Resource, path string) error {
			probe, probeErr := probeVideoReplicationFile(context.Background(), path)
			if probeErr != nil {
				if errors.Is(probeErr, errMediaProbeUnavailable) {
					return &AppError{Status: 503, Code: 503, Message: "服务器缺少 ffprobe，暂时无法校验参考视频", Retryable: true}
				}
				return BadAuthRequest(videoReplicationBrokenVideoMessage + "（" + clipText(probeErr.Error(), 120) + "）")
			}
			// 超过后台时长上限的视频也可以导入，生成前用「裁剪视频」截出一段；裁剪后的时长在开始生成时校验。
			if probe.DurationMs < videoReplicationMinDurationMs-videoReplicationDurationSlackMs {
				return BadAuthRequest(fmt.Sprintf("参考视频时长 %.1f 秒，短于 4 秒", float64(probe.DurationMs)/1000))
			}
			if probe.DurationMs > videoReplicationMaxSourceMs+videoReplicationDurationSlackMs {
				return BadAuthRequest(fmt.Sprintf("参考视频时长 %.1f 秒，超过 %d 分钟，请先在本地剪短", float64(probe.DurationMs)/1000, videoReplicationMaxSourceMs/60000))
			}
			asset.DurationMs, asset.Width, asset.Height, asset.FPS, asset.HasAudio = probe.DurationMs, probe.Width, probe.Height, probe.FPS, probe.HasAudio
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if category == model.VideoReplicationAssetVoiceAudio {
		if resource.Kind != "audio" {
			return nil, BadAuthRequest("参考音色只能是音频")
		}
		if resource.Size > 20<<20 {
			return nil, BadAuthRequest("参考音色不能超过 20MB")
		}
		if resource.DurationMs > videoReplicationMaxVoiceMs+500 {
			return nil, BadAuthRequest(fmt.Sprintf("参考音色时长 %.1f 秒，最长 %d 秒；请剪成一段清晰的单人说话片段", float64(resource.DurationMs)/1000, videoReplicationMaxVoiceMs/1000))
		}
		// 多段参考音色一起作为参考音频发送，模型按合计时长限制（Seedance 2.0 合计 ≤15 秒）。
		var voiceMs int64
		for _, existing := range assets {
			if existing.Category == model.VideoReplicationAssetVoiceAudio {
				voiceMs += existing.DurationMs
			}
		}
		if total := voiceMs + resource.DurationMs; sameCategory > 0 && total > videoReplicationMaxVoiceMs+500 {
			return nil, BadAuthRequest(fmt.Sprintf("参考音色合计 %.1f 秒，多段加起来最长 %d 秒；请换短一点的片段", float64(total)/1000, videoReplicationMaxVoiceMs/1000))
		}
	} else {
		if resource.Kind != "image" {
			return nil, BadAuthRequest(videoReplicationCategoryLabels[category] + "只能是图片")
		}
		if resource.Size > videoReplicationMaxImageBytes {
			return nil, BadAuthRequest("图片不能超过 30MB")
		}
		// 浏览器上传不一定带尺寸：用 ffprobe 读取真实宽高，供预览与宽高比展示；探测失败不阻止登记。
		if asset.Width == 0 || asset.Height == 0 {
			_ = s.withVideoReplicationResourceFile(userID, resource.ID, videoReplicationMaxImageBytes, func(_ *model.Resource, path string) error {
				if info, err := probeMediaFile(context.Background(), path); err == nil && info.Width > 0 {
					asset.Width, asset.Height = info.Width, info.Height
				}
				return nil
			})
		}
	}
	if asset.Name == "" {
		asset.Name = fmt.Sprintf("%s%02d", videoReplicationCategoryLabels[category], sameCategory+1)
	}
	if err := s.repo.CreateVideoReplicationAsset(asset); err != nil {
		return nil, err
	}
	return s.VideoReplicationProject(userID, projectID)
}

// DeleteVideoReplicationAsset 删除素材，并把创作描述里指向它的 @ 引用一并移除。素材对应的资源保留在素材库。
func (s *Service) DeleteVideoReplicationAsset(userID, projectID, assetID string) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if project.Status == model.VideoReplicationStatusAnalyzing {
		return nil, BadAuthRequest("参考视频分析中，请等待分析完成后再调整素材")
	}
	if _, err := s.repo.DeleteVideoReplicationAsset(userID, projectID, assetID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("素材不存在")
		}
		return nil, err
	}
	token := "@[node:" + assetID + "]"
	if strings.Contains(project.Prompt, token) {
		if err := s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"prompt": strings.ReplaceAll(project.Prompt, token, "")}); err != nil {
			return nil, err
		}
	}
	return s.VideoReplicationProject(userID, projectID)
}

func (s *Service) ReorderVideoReplicationAssets(userID, projectID string, input VideoReplicationReorderInput) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	if _, ok := videoReplicationCategoryLabels[input.Category]; !ok {
		return nil, BadAuthRequest("素材类别无效")
	}
	if _, err := s.repo.VideoReplicationProject(userID, projectID); err != nil {
		return nil, videoReplicationNotFound(err)
	}
	if input.PrimaryID != "" && !containsString(input.IDs, input.PrimaryID) {
		return nil, BadAuthRequest("主素材必须属于同一类别")
	}
	if err := s.repo.ReorderVideoReplicationAssets(userID, projectID, input.Category, input.IDs, input.PrimaryID); err != nil {
		return nil, BadAuthRequest(err.Error())
	}
	return s.VideoReplicationProject(userID, projectID)
}

// UpdateVideoReplicationStoryboard 保存用户编辑后的分镜；修订号不一致说明分镜已被重新分析或在别处修改。
func (s *Service) UpdateVideoReplicationStoryboard(userID, projectID string, revision int, storyboard VideoReplicationStoryboard) (*VideoReplicationProjectView, error) {
	if err := s.requireVideoReplication(); err != nil {
		return nil, err
	}
	if _, err := s.repo.VideoReplicationProject(userID, projectID); err != nil {
		return nil, videoReplicationNotFound(err)
	}
	normalized, err := normalizeVideoReplicationStoryboard(storyboard, nil)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(normalized)
	updated, err := s.repo.UpdateVideoReplicationStoryboard(userID, projectID, revision, string(raw))
	if err != nil {
		return nil, err
	}
	if !updated {
		return nil, &AppError{Status: 409, Code: 409, Message: "分镜已更新或正在重新分析，请刷新后再编辑"}
	}
	return s.VideoReplicationProject(userID, projectID)
}

// normalizeVideoReplicationStoryboard 修剪字段长度、补齐分镜 ID，并把镜头时长取整到模型允许的值。
func normalizeVideoReplicationStoryboard(storyboard VideoReplicationStoryboard, allowedSeconds []int) (VideoReplicationStoryboard, error) {
	if len(storyboard.Shots) == 0 {
		return storyboard, BadAuthRequest("分镜至少需要 1 个镜头")
	}
	if len(storyboard.Shots) > videoReplicationMaxShotsOut {
		return storyboard, BadAuthRequest(fmt.Sprintf("分镜最多 %d 个镜头", videoReplicationMaxShotsOut))
	}
	seen := map[string]bool{}
	clip := func(value string, max int) string {
		value = strings.TrimSpace(value)
		if utf8.RuneCountInString(value) > max {
			return string([]rune(value)[:max])
		}
		return value
	}
	for index := range storyboard.Shots {
		shot := &storyboard.Shots[index]
		shot.ID = strings.TrimSpace(shot.ID)
		if shot.ID == "" || len(shot.ID) > 64 || seen[shot.ID] || !videoReplicationShotIDPattern.MatchString(shot.ID) {
			shot.ID = fmt.Sprintf("shot_%s", strings.ReplaceAll(newID(), "-", "")[:12])
		}
		seen[shot.ID] = true
		shot.Index = index
		shot.Title = clip(shot.Title, 60)
		for _, field := range []*string{&shot.ShotSize, &shot.Camera, &shot.Scene, &shot.Action, &shot.ProductFocus, &shot.Lighting, &shot.Mood, &shot.SourceShot} {
			*field = clip(*field, 300)
		}
		if storyboard.VoiceAdapted || storyboard.SubtitlesAdapted {
			shot.Voiceover = strings.TrimSpace(shot.Voiceover)
			shot.Subtitle = strings.TrimSpace(shot.Subtitle)
			if utf8.RuneCountInString(shot.Voiceover) > 2000 || utf8.RuneCountInString(shot.Subtitle) > 2000 {
				return storyboard, BadAuthRequest(fmt.Sprintf("镜头 %d 的改写口播或字幕过长，请缩短后再保存", index+1))
			}
			// 商品改写稿已经按段长校验，不能再截断台词或把字幕截成与声音不同的半句。
			if shot.Voiceover != "" {
				shot.Subtitle = shot.Voiceover
			}
		} else {
			shot.Subtitle = clip(shot.Subtitle, 80)
			shot.Voiceover = clip(shot.Voiceover, 200)
		}
		shot.Speaker = clip(shot.Speaker, 24)
		shot.Prompt = clip(shot.Prompt, 2000)
		shot.Adaptation = clip(shot.Adaptation, 300)
		shot.SceneEdits = clip(shot.SceneEdits, 300)
		shot.ProductPresence = normalizeVideoReplicationPresence(shot.ProductPresence)
		if shot.Prompt == "" {
			return storyboard, BadAuthRequest(fmt.Sprintf("镜头 %d 缺少画面描述", index+1))
		}
		shot.DurationSec = snapVideoReplicationSeconds(shot.DurationSec, allowedSeconds)
	}
	storyboard.Notes = clip(storyboard.Notes, 1000)
	storyboard.ProductStructure = clip(storyboard.ProductStructure, 400)
	storyboard.ApparelStructure = clip(storyboard.ApparelStructure, 300)
	storyboard.Wearing = clip(storyboard.Wearing, 200)
	storyboard.Styling = clip(storyboard.Styling, 240)
	storyboard.SourceLook = clip(storyboard.SourceLook, 80)
	storyboard.NewSubject = clip(storyboard.NewSubject, 80)
	storyboard.ReferenceProps = clip(storyboard.ReferenceProps, 120)
	storyboard.Changes = clip(storyboard.Changes, 300)
	storyboard.Banned = clip(storyboard.Banned, 80)
	storyboard.Elements = normalizeVideoReplicationElements(storyboard.Elements)
	storyboard.Knowledge = clip(storyboard.Knowledge, 40)
	if _, ok := videoReplicationKnowledgeSpecByID(storyboard.KnowledgeID); !ok {
		storyboard.KnowledgeID = ""
	}
	return storyboard, nil
}

// videoReplicationMinSegmentSeconds 是单段视频的最短时长：多数视频接口要求不少于 4 秒。
const videoReplicationMinSegmentSeconds = 4

// snapVideoReplicationSeconds 把时长取到模型允许的档位；模型档位里有 ≥4 秒的就不取更短的档位。
func snapVideoReplicationSeconds(value int, allowed []int) int {
	if value < videoReplicationMinSegmentSeconds {
		value = videoReplicationMinSegmentSeconds
	}
	if value > 60 {
		value = 60
	}
	usable := videoReplicationSegmentOptions(allowed)
	if len(usable) == 0 {
		return value
	}
	best := usable[0]
	for _, candidate := range usable {
		if absInt(candidate-value) < absInt(best-value) || (absInt(candidate-value) == absInt(best-value) && candidate > best) {
			best = candidate
		}
	}
	return best
}

// videoReplicationSegmentOptions 返回模型允许且不短于 4 秒的单段时长档位；模型只声明了更短档位时原样返回。
func videoReplicationSegmentOptions(allowed []int) []int {
	usable := []int{}
	for _, value := range allowed {
		if value >= videoReplicationMinSegmentSeconds {
			usable = append(usable, value)
		}
	}
	if len(usable) == 0 {
		return allowed
	}
	return usable
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

type VideoReplicationOutputView struct {
	model.VideoReplicationOutput
	Quality *VideoReplicationQuality `json:"quality,omitempty"`
}
