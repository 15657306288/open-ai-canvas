package model

import (
	"encoding/json"
	"strings"
)

// channelModelNameEntry 是旧导入工具写入 models_json 时的对象形态。
// 早期版本把渠道模型写成 [{"model":"gpt-image-2.5","caps":["image"]}]，
// 当前后台保存的是 ["gpt-image-2.5"]，两种数据在同一张表里长期并存。
type channelModelNameEntry struct {
	Model string `json:"model"`
}

// ParseChannelModelNames 从渠道 models_json 中提取模型名，兼容字符串数组与对象数组两种形态。
// 解析失败或格式未知时返回空切片，调用方按“该渠道未登记任何模型”处理。
func ParseChannelModelNames(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	var names []string
	if err := json.Unmarshal([]byte(trimmed), &names); err != nil {
		var entries []channelModelNameEntry
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return nil
		}
		names = make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Model)
		}
	}

	result := make([]string, 0, len(names))
	for _, name := range names {
		if value := strings.TrimSpace(name); value != "" {
			result = append(result, value)
		}
	}
	return result
}
