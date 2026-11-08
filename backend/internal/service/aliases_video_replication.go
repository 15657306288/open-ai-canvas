package service

import "yingce/backend/internal/app"

type (
	VideoReplicationProjectInput      = app.VideoReplicationProjectInput
	VideoReplicationAssetInput        = app.VideoReplicationAssetInput
	VideoReplicationReorderInput      = app.VideoReplicationReorderInput
	VideoReplicationStoryboard        = app.VideoReplicationStoryboard
	VideoReplicationGenerateInput     = app.VideoReplicationGenerateInput
	VideoReplicationComposeInput      = app.VideoReplicationComposeInput
	VideoReplicationPlanInput         = app.VideoReplicationPlanInput
	VideoReplicationStartInput        = app.VideoReplicationStartInput
	VideoReplicationSetting           = app.VideoReplicationSetting
	VideoReplicationVoiceCast         = app.VideoReplicationVoiceCast
	VideoReplicationVoicePreviewInput = app.VideoReplicationVoicePreviewInput
	VideoReplicationLinkInput         = app.VideoReplicationLinkInput
	VideoLinkEvent                    = app.VideoLinkEvent
	VideoLinkImportResult             = app.VideoLinkImportResult
	VideoLinkImportError              = app.VideoLinkImportError
)

// VideoLinkErrorView 把链接导入错误转成 SSE 错误事件，内部错误不透出原文。
var VideoLinkErrorView = app.VideoLinkErrorView
