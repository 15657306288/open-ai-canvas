package model

import "time"

// 影棚视频「视频复刻」：分析参考视频的镜头结构，结合商品/模特素材重构原创分镜，
// 再逐镜头提交工作台视频任务并用 ffmpeg 合成成片。逐镜头生成与合成都复用通用 Task，
// 计费、退款、超时与重启恢复走任务系统；这里只保存创作项目与镜头版本的关联。

const (
	VideoReplicationStatusDraft      = "draft"
	VideoReplicationStatusAnalyzing  = "analyzing"
	VideoReplicationStatusAnalyzed   = "analyzed"
	VideoReplicationStatusGenerating = "generating"
	VideoReplicationStatusComposing  = "composing"
	VideoReplicationStatusCompleted  = "completed"
	VideoReplicationStatusFailed     = "failed"

	VideoReplicationAssetReferenceVideo = "reference_video"
	VideoReplicationAssetProductImage   = "product_image"
	VideoReplicationAssetModelImage     = "model_image"
	VideoReplicationAssetApparelImage   = "apparel_image"
	VideoReplicationAssetFaceImage      = "face_image"
	// 电商 Agent 商品视频「换背景」的背景参考图。
	VideoReplicationAssetBackgroundImage = "background_image"
	// 商品细节 / 拆解图：新商品的局部、可拆部件与拆装方式，供结构对齐与原片编辑参考，不是独立的替换目标。
	VideoReplicationAssetProductDetailImage = "product_detail_image"
	// 包装图：原视频出现包装盒时换成这张图里的包装。
	VideoReplicationAssetPackagingImage = "packaging_image"
	// 服饰替换的参考音色（音频）：替换音频时随每段视频任务作为参考音频发送。
	VideoReplicationAssetVoiceAudio = "voice_audio"

	VideoReplicationRunSubmitting = "submitting"
	VideoReplicationRunQueued     = "queued"
	VideoReplicationRunRunning    = "running"
	VideoReplicationRunSucceeded  = "succeeded"
	VideoReplicationRunFailed     = "failed"
	VideoReplicationRunCancelled  = "cancelled"
)

type VideoReplicationProject struct {
	ID     string `json:"id" gorm:"primaryKey;size:36"`
	UserID string `json:"-" gorm:"size:36;index:idx_video_replication_user_updated,priority:1"`
	Title  string `json:"title" gorm:"size:200"`
	// VideoType 是逗号分隔的替换目标（product,model,apparel,face,background），可以为空：只按创作描述复刻。
	VideoType string `json:"videoType" gorm:"size:64"`
	// Prompt 保存原始创作描述，@ 引用以 @[node:<素材 ID>] 形式内嵌，提交时由后端解析为素材关系。
	Prompt         string `json:"prompt" gorm:"type:text"`
	ParametersJSON string `json:"-" gorm:"type:text"`
	Status         string `json:"status" gorm:"size:24;index"`
	Stage          string `json:"stage" gorm:"size:200"`
	Progress       int    `json:"progress"`
	Error          string `json:"error" gorm:"type:text"`
	AnalysisJSON   string `json:"-" gorm:"type:text"`
	StoryboardJSON string `json:"-" gorm:"type:text"`
	// VoiceCastJSON 是说话人与音色、模特图的绑定（Agent 推荐，用户可改）。
	VoiceCastJSON string `json:"-" gorm:"type:text"`
	// StoryboardRevision 防止分析重跑与用户编辑互相覆盖。
	StoryboardRevision int `json:"storyboardRevision"`
	// AnalysisLeaseUntil 是分析协程的租约；进程重启后租约过期，读取时把项目标为可重新分析。
	AnalysisLeaseUntil *time.Time `json:"-"`
	// PlanJSON 是用户确认过的生成方案（原生直出 / 智能分段、任务数、预算）；没有确认方案不允许调用付费模型。
	PlanJSON        string     `json:"-" gorm:"type:text"`
	PlanConfirmedAt *time.Time `json:"planConfirmedAt,omitempty"`
	// Paused 表示智能分段因预算或用户操作暂停自动提交；进行中的任务不受影响。
	Paused      bool      `json:"paused"`
	PauseReason string    `json:"pauseReason" gorm:"size:300"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt" gorm:"index:idx_video_replication_user_updated,priority:2"`
}

// VideoReplicationAsset 是项目内三类独立素材之一；ID 独立于资源 ID，@ 引用与任务都按它关联。
type VideoReplicationAsset struct {
	ID          string  `json:"id" gorm:"primaryKey;size:36"`
	ProjectID   string  `json:"projectId" gorm:"size:36;index"`
	UserID      string  `json:"-" gorm:"size:36;index"`
	Category    string  `json:"category" gorm:"size:32"`
	ResourceID  string  `json:"resourceId" gorm:"size:36"`
	Name        string  `json:"name" gorm:"size:200"`
	SortOrder   int     `json:"sortOrder"`
	IsPrimary   bool    `json:"isPrimary"`
	MimeType    string  `json:"mimeType" gorm:"size:120"`
	SizeBytes   int64   `json:"sizeBytes"`
	DurationMs  int64   `json:"durationMs"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	FPS         float64 `json:"fps"`
	HasAudio    bool    `json:"hasAudio"`
	ProfileJSON string  `json:"-" gorm:"type:text"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// VideoReplicationShotRun 是某个分镜的一次生成（一个版本）。重新生成新建一行，旧版本保留。
type VideoReplicationShotRun struct {
	ID        string `json:"id" gorm:"primaryKey;size:36"`
	ProjectID string `json:"projectId" gorm:"size:36;index;uniqueIndex:idx_video_replication_run_key,priority:1"`
	UserID    string `json:"-" gorm:"size:36;index"`
	ShotID    string `json:"shotId" gorm:"size:64;index"`
	// Kind：video 为画面生成任务，voice 为该镜头口播的 TTS 配音任务。
	Kind    string `json:"kind" gorm:"size:16;default:video"`
	Version int    `json:"version"`
	// IdempotencyKey 是「提交批次键:分镜 ID」；同一键重复提交返回已有版本，不重复建任务和扣费。
	IdempotencyKey   string    `json:"-" gorm:"size:160;uniqueIndex:idx_video_replication_run_key,priority:2"`
	TaskID           string    `json:"taskId" gorm:"size:36;index"`
	Status           string    `json:"status" gorm:"size:24;index"`
	OutputResourceID string    `json:"outputResourceId" gorm:"size:36"`
	Prompt           string    `json:"prompt" gorm:"type:text"`
	ParametersJSON   string    `json:"-" gorm:"type:text"`
	Error            string    `json:"error" gorm:"type:text"`
	Selected         bool      `json:"selected"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// VideoReplicationOutput 是一次成片合成（时间线渲染任务），保留历史版本。
type VideoReplicationOutput struct {
	ID             string `json:"id" gorm:"primaryKey;size:36"`
	ProjectID      string `json:"projectId" gorm:"size:36;index;uniqueIndex:idx_video_replication_output_key,priority:1"`
	UserID         string `json:"-" gorm:"size:36;index"`
	IdempotencyKey string `json:"-" gorm:"size:160;uniqueIndex:idx_video_replication_output_key,priority:2"`
	// Mode：native 表示模型一次任务直接输出的完整视频（未经剪辑拼接）；segmented 表示多段生成后 FFmpeg 合成。
	Mode        string    `json:"mode" gorm:"size:16"`
	QualityJSON string    `json:"-" gorm:"type:text"`
	TaskID      string    `json:"taskId" gorm:"size:36"`
	Status      string    `json:"status" gorm:"size:24"`
	ResourceID  string    `json:"resourceId" gorm:"size:36"`
	SubtitleSRT string    `json:"subtitleSrt" gorm:"type:text"`
	RunIDsJSON  string    `json:"-" gorm:"type:text"`
	DurationMs  int64     `json:"durationMs"`
	Error       string    `json:"error" gorm:"type:text"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
