package app

import (
	"time"

	"yingce/backend/internal/model"
)

// 电商 Agent 商品视频：粘贴视频分享链接，由 internal/videoparser 多平台解析服务识别平台、解析、下载，
// 再转存为资源并登记为参考视频；导入流程见 video_link_import.go。本文件保留链接输入结构与项目级的删除 / 后台查看接口。

type VideoReplicationLinkInput struct {
	URL string `json:"url"`
}

// DeleteVideoReplicationProject 删除用户自己的复刻项目；分析、生成或合成进行中时拒绝，避免任务回写到已删除的项目。
func (s *Service) DeleteVideoReplicationProject(userID, projectID string) error {
	if err := s.requireVideoReplication(); err != nil {
		return err
	}
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return videoReplicationNotFound(err)
	}
	switch project.Status {
	case model.VideoReplicationStatusAnalyzing, model.VideoReplicationStatusGenerating, model.VideoReplicationStatusComposing:
		return BadAuthRequest("视频正在生成中，请等待完成或取消后再删除")
	}
	return videoReplicationNotFound(s.repo.DeleteVideoReplicationProject(userID, projectID))
}

// AdminVideoReplicationOverview 是后台「运行总览」：days 天内的项目、任务、成片与失败原因。
func (s *Service) AdminVideoReplicationOverview(actor *model.User, days int) (*VideoReplicationOverview, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	if days <= 0 || days > 365 {
		days = 7
	}
	return s.repo.VideoReplicationOverview(time.Now().AddDate(0, 0, -days))
}

// AdminVideoReplicationProject 后台查看任意用户项目的完整详情（素材、子任务、成片）。
func (s *Service) AdminVideoReplicationProject(actor *model.User, id string) (*VideoReplicationProjectView, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	owner, err := s.repo.VideoReplicationProjectOwner(id)
	if err != nil {
		return nil, videoReplicationNotFound(err)
	}
	return s.VideoReplicationProject(owner, id)
}

// videoReplicationKindDisabled 判断后台是否关闭了该替换类型。
func (s *Service) videoReplicationKindDisabled(params VideoReplicationParameters) error {
	setting, err := s.videoReplicationSetting()
	if err != nil {
		return err
	}
	kind := params.ReplaceMode
	if kind != "" && containsString(setting.DisabledKinds, kind) {
		return BadAuthRequest("该视频功能暂未开放")
	}
	return nil
}
