package app

import "strings"

// videoReplicationModelProfile 读取所选渠道模型真实的视频能力（时长档位、参考素材与提示词上限）；
// 读不到时返回 nil，由任务准入兜底。参考素材/提示词上限一律取后台渠道模型的能力配置，不在代码里写死。
func (s *Service) videoReplicationModelProfile(cfg providerConfig) *VideoCapabilityConfig {
	item, err := s.repo.ChannelModelByKey(cfg.ChannelID, providerChannelModelKey(cfg))
	if err != nil {
		return nil
	}
	profile, err := DecodeModelCapabilityConfig(item.CapabilityConfigJSON)
	if err != nil || profile == nil || profile.Video == nil {
		return nil
	}
	normalized, err := NormalizeModelCapabilityConfigForModel("video", string(item.Protocol), firstNonEmpty(item.ProviderModelKey, item.ModelKey), profile)
	if err != nil || normalized == nil || normalized.Video == nil {
		return nil
	}
	return normalized.Video
}

// videoReplicationChannelProxy 返回系统渠道配置的出站代理（后台渠道里填写），没有则为空。
func (s *Service) videoReplicationChannelProxy(channelID string) string {
	if s == nil || strings.TrimSpace(channelID) == "" {
		return ""
	}
	if ch, err := s.repo.SystemChannel(channelID); err == nil {
		return strings.TrimSpace(ch.ProxyURL)
	}
	return ""
}
