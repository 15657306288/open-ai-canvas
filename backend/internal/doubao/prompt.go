package doubao

// 图层拆分的提示词构造：与前端 web/src/lib/canvas/canvas-layer-decomposition.ts 的
// buildLayerDecompositionPrompt 保持同一语义（层序号、层总数、选区 bbox），
// 使「一次请求拆 N 层」的后端编排与前端逐层模式给到上游的是同一套指令。

import (
	"fmt"
	"strings"
)

// LayerRegion 图层选区，使用图像 0-1000 归一化坐标系（与画布框选一致）。
type LayerRegion struct {
	X1 int `json:"x1"`
	Y1 int `json:"y1"`
	X2 int `json:"x2"`
	Y2 int `json:"y2"`
}

func (r LayerRegion) String() string {
	return fmt.Sprintf("%d %d %d %d", r.X1, r.Y1, r.X2, r.Y2)
}

// BuildLayerPrompt 生成第 index 层（1 起）的单层提示词。
func BuildLayerPrompt(base string, index, total int, region *LayerRegion) string {
	if total < 1 {
		total = 1
	}
	if index < 1 {
		index = 1
	}
	lines := make([]string, 0, 4)
	if trimmed := strings.TrimSpace(base); trimmed != "" {
		lines = append(lines, trimmed)
	}
	lines = append(lines, fmt.Sprintf("本次只输出第 %d 个图层（共 %d 个图层），其余图层的内容不要出现在这张图片里。", index, total))
	if region != nil {
		lines = append(lines, fmt.Sprintf("第 %d 个图层对应图中框选区域（图像 0-1000 坐标系）：<bbox>%s</bbox>，以该区域中的主体作为这一层的内容，保持原始边缘细节。", index, region.String()))
	}
	lines = append(lines, "只输出这一张图层图片，不要输出拼图或多图层合成图，不要添加新的元素。")
	return strings.Join(lines, "\n")
}

// LayerPrompts 按层数展开提示词；regions 少于层数时，后续层按无选区处理。
func LayerPrompts(base string, count int, regions []LayerRegion) []string {
	if count < 1 {
		count = 1
	}
	prompts := make([]string, 0, count)
	for index := 0; index < count; index++ {
		var region *LayerRegion
		if index < len(regions) {
			region = &regions[index]
		}
		prompts = append(prompts, BuildLayerPrompt(base, index+1, count, region))
	}
	return prompts
}
