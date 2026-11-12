package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"yingce/backend/internal/model"
	"yingce/backend/internal/workbench"
)

// 元素替换：原片编辑时不重绘画面。识图给出全片统一的替换清单（梨→苹果、梨形小车→香蕉形小车），每个新元素先用图片模型
// 出一张单独的元素参考图，视频模型仍以原视频片段为底，按「把 @视频1 中的梨替换成 @图片N 中的苹果」逐项替换：
// Seedance 原片编辑能可靠换掉有参考图的目标，只靠文字的元素改动常被原样保留；而按改好的整帧重绘又会改掉原片的镜头和人物。
// 这样镜头、构图、人物、动作都留在原片上，只有清单里的元素被换掉，全片同一个原元素换成同一张图里的新元素。

const (
	// videoReplicationElementCategory 是元素参考图在参考图列表里的类别。
	videoReplicationElementCategory = "element_ref"
	// videoReplicationElementMax 是读不到模型参考图上限时元素替换清单的条数；读得到时按模型名额（videoReplicationElementLimit）。
	videoReplicationElementMax = 6
	// videoReplicationElementSanityMax 只挡异常数据（存储时的绝对上限）。
	videoReplicationElementSanityMax = 30
	// videoReplicationElementTimeout 是等元素参考图出图的最长时间；超时的元素只用文字替换。
	videoReplicationElementTimeout = 12 * time.Minute
	videoReplicationElementPoll    = 5 * time.Second
)

// normalizeVideoReplicationElements 修剪字段、去掉空条目和重复的原元素、补齐 ID。
func normalizeVideoReplicationElements(elements []VideoReplicationElement) []VideoReplicationElement {
	out := []VideoReplicationElement{}
	seen := map[string]bool{}
	ids := map[string]bool{}
	for _, element := range elements {
		element.From = videoReplicationClipRunes(strings.TrimSpace(element.From), 30)
		element.To = videoReplicationClipRunes(strings.TrimSpace(element.To), 40)
		element.ResourceID = videoReplicationClipRunes(strings.TrimSpace(element.ResourceID), 64)
		element.Error = videoReplicationClipRunes(strings.TrimSpace(element.Error), 160)
		element.ID = strings.TrimSpace(element.ID)
		if element.From == "" || element.To == "" || element.From == element.To || seen[element.From] || len(out) >= videoReplicationElementSanityMax {
			continue
		}
		if element.ID == "" || len(element.ID) > 32 || ids[element.ID] || !videoReplicationShotIDPattern.MatchString(element.ID) {
			element.ID = fmt.Sprintf("e%d", len(out)+1)
			for ids[element.ID] {
				element.ID += "x"
			}
		}
		seen[element.From], ids[element.ID] = true, true
		out = append(out, element)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// videoReplicationElementsFromChanges 在结构对齐没给出替换清单时，从改动清单里取「A→B」形式的条目兜底。
func videoReplicationElementsFromChanges(changes string) []VideoReplicationElement {
	elements := []VideoReplicationElement{}
	for _, item := range strings.FieldsFunc(changes, func(r rune) bool { return strings.ContainsRune("；;\n", r) }) {
		parts := strings.SplitN(strings.ReplaceAll(item, "->", "→"), "→", 2)
		if len(parts) != 2 {
			continue
		}
		to := strings.TrimSpace(parts[1])
		for _, from := range strings.FieldsFunc(parts[0], func(r rune) bool { return strings.ContainsRune("、，,/", r) }) {
			// 「画面中的梨」→「梨」：去掉常见的位置前缀，留下元素本身。
			from = strings.TrimSpace(from)
			for _, prefix := range []string{"画面中的", "画面里的", "画面中", "画面里", "原片中的", "原片里的", "所有的", "所有"} {
				from = strings.TrimPrefix(from, prefix)
			}
			if from != "" && to != "" {
				elements = append(elements, VideoReplicationElement{From: from, To: to})
			}
		}
	}
	return normalizeVideoReplicationElements(elements)
}

// videoReplicationElementInShot：识图给出的本段逐项改动里提到了这个原元素。只看每项「→」左边的原元素；
// 「梨」不因为本段只有「梨形小车→香蕉形小车」就算出现（清单里有更具体的「梨形小车」时归它）。
func videoReplicationElementInShot(element VideoReplicationElement, shot VideoReplicationShot, elements []VideoReplicationElement) bool {
	for _, item := range strings.FieldsFunc(shot.SceneEdits, func(r rune) bool { return strings.ContainsRune("；;\n", r) }) {
		left := strings.SplitN(strings.ReplaceAll(item, "->", "→"), "→", 2)[0]
		if !strings.Contains(left, element.From) {
			continue
		}
		claimed := false
		for _, other := range elements {
			if len(other.From) > len(element.From) && strings.Contains(other.From, element.From) && strings.Contains(left, other.From) {
				// 去掉更具体的元素后再看是否还单独提到了它（「梨形小车和两颗梨」仍算有梨）。
				claimed = !strings.Contains(strings.ReplaceAll(left, other.From, ""), element.From)
				if claimed {
					break
				}
			}
		}
		if !claimed {
			return true
		}
	}
	return false
}

// videoReplicationElementSourceFrame 为元素参考图挑一张出现了原元素的原片关键帧，让新元素的大小视角、画风和光影贴合原片：
// 原片分析的镜头描述里提到它的优先，其次是逐项改动提到它的段，最后用第一张关键帧。
func videoReplicationElementSourceFrame(video VideoReplicationVideoAnalysis, storyboard VideoReplicationStoryboard, element VideoReplicationElement) string {
	for _, source := range video.Shots {
		text := source.Scene + source.Action + source.PersonAction + source.ProductDisplay + source.Composition
		if source.KeyframeResourceID != "" && strings.Contains(text, element.From) {
			return source.KeyframeResourceID
		}
	}
	for _, shot := range storyboard.Shots {
		if videoReplicationElementInShot(element, shot, storyboard.Elements) {
			if id := videoReplicationShotSourceFrame(video, shot); id != "" {
				return id
			}
		}
	}
	for _, source := range video.Shots {
		if source.KeyframeResourceID != "" {
			return source.KeyframeResourceID
		}
	}
	return ""
}

// videoReplicationElementPrompt 是元素参考图的出图提示词：图1 是原片里出现原元素的一帧（可能没有）。
func videoReplicationElementPrompt(element VideoReplicationElement, withFrame bool, note string) string {
	var b strings.Builder
	if note = strings.TrimSpace(note); note != "" {
		b.WriteString("用户修改意见（优先执行）：" + videoReplicationClipRunes(note, 200) + "\n")
	}
	b.WriteString("画一张「" + element.To + "」的元素参考图，它会在视频里替换原来的「" + element.From + "」。\n")
	if withFrame {
		b.WriteString("图1 是原视频里的一帧，其中有「" + element.From + "」：新元素的大小比例、观看角度、材质质感、画风（写实 / 三维 / 卡通按图1）、色调和光影与图1里的「" + element.From + "」及整个画面一致，放回原画面时毫不突兀。\n")
	}
	b.WriteString("造型要一眼就能认出是「" + element.To + "」；原元素如果是带功能结构的造型物（车、椅子、房子、吊舱、容器等），保留它的功能结构，只把造型和配色改成新元素。\n")
	b.WriteString("画面里只有这一个元素（同类多个的只画一个），完整不裁切，居中，干净的浅灰纯色背景；不要人物、手、文字、Logo、水印和其他物体。")
	return strings.ToValidUTF8(clipText(b.String(), 1800), "")
}

// videoReplicationNoImageModelError 是没有可用系统图片模型时写给元素的原因。
const videoReplicationNoImageModelError = "没有可用的图片模型，无法生成参考图（需管理员开放能收参考图的系统图片模型）"

// prepareVideoReplicationElements 为替换清单里还没有参考图的元素（以及 notes 指定重做的元素）出图，写回分镜。
// 出图失败的元素记下原因（Error），这一次只用文字替换，不阻止生成；下次生成时自动再试。
func (s *Service) prepareVideoReplicationElements(userID, projectID string, params VideoReplicationParameters, notes map[string]string) {
	project, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil {
		return
	}
	storyboard := videoReplicationStoryboard(project)
	analysis := videoReplicationSavedAnalysis(project)
	if storyboard == nil || len(storyboard.Elements) == 0 {
		return
	}
	if strings.TrimSpace(params.KeyframeModel) == "" || (params.KeyframeLogicalModelID == "" && params.KeyframeChannelID == "") {
		// 没有可用的系统图片模型：标出原因，元素只用文字替换（管理员开放图片模型后下次生成会补做）。
		changed := false
		for index := range storyboard.Elements {
			if storyboard.Elements[index].ResourceID == "" {
				storyboard.Elements[index].Error = videoReplicationNoImageModelError
				changed = true
			}
		}
		if changed {
			if raw, err := json.Marshal(storyboard); err == nil {
				_, _ = s.repo.UpdateVideoReplicationStoryboard(userID, projectID, project.StoryboardRevision, string(raw))
			}
		}
		return
	}
	var video VideoReplicationVideoAnalysis
	if analysis != nil && len(analysis.Videos) > 0 {
		video = analysis.Videos[0]
	}
	parameters := videoReplicationKeyframeParameters(params)
	indexes := []int{}
	requests := []WorkbenchTaskInput{}
	for index, element := range storyboard.Elements {
		note, forced := notes[element.ID]
		if element.ResourceID != "" && !forced {
			continue
		}
		references := []workbench.TaskReference{}
		if frame := videoReplicationElementSourceFrame(video, *storyboard, element); frame != "" && params.KeyframeMaxImages > 0 {
			references = append(references, workbench.TaskReference{ResourceID: frame, Kind: "image", Role: "reference"})
		}
		request := WorkbenchTaskInput{Kind: WorkbenchKindImage, Prompt: videoReplicationElementPrompt(element, len(references) > 0, note), Model: params.KeyframeModel, Parameters: parameters, References: references}
		if params.KeyframeLogicalModelID != "" {
			request.LogicalModelID = params.KeyframeLogicalModelID
		} else {
			request.ChannelID = params.KeyframeChannelID
		}
		indexes = append(indexes, index)
		requests = append(requests, request)
	}
	if len(requests) == 0 {
		return
	}
	results, failures := s.runVideoReplicationImageTasks(userID, projectID, requests, "正在生成替换元素的参考图")
	// 分镜可能在等待期间被重新分析：修订号变了就放弃（新方案会重新出图）。
	latest, err := s.repo.VideoReplicationProject(userID, projectID)
	if err != nil || latest.StoryboardRevision != project.StoryboardRevision {
		return
	}
	for position, index := range indexes {
		if id := results[position]; id != "" {
			storyboard.Elements[index].ResourceID, storyboard.Elements[index].Error = id, ""
		} else if storyboard.Elements[index].ResourceID == "" {
			// 重做失败时保留旧图，不标失败；本来就没有图的才记下原因。
			storyboard.Elements[index].Error = failures[position]
			if storyboard.Elements[index].Error == "" {
				storyboard.Elements[index].Error = "出图失败"
			}
		}
	}
	raw, err := json.Marshal(storyboard)
	if err != nil {
		return
	}
	if _, err := s.repo.UpdateVideoReplicationStoryboard(userID, projectID, project.StoryboardRevision, string(raw)); err != nil {
		slog.Warn("video replication element save failed", "projectId", projectID, "error", err)
	}
}

// videoReplicationWithElements 保留每类目标至少一张及全部用户辅助资料，仅为本段元素让出同类的可选角度。
// 其他段的元素最后使用空位；放不下的元素仍用文字替换，提示词不会引用未发送的图。
func videoReplicationWithElements(references []map[string]string, elements []VideoReplicationElement, shot VideoReplicationShot, limit int) []map[string]string {
	ordered := []VideoReplicationElement{}
	inShot := 0
	for pass := 0; pass < 2; pass++ {
		for _, element := range elements {
			if element.ResourceID != "" && videoReplicationElementInShot(element, shot, elements) == (pass == 0) {
				ordered = append(ordered, element)
				if pass == 0 {
					inShot++
				}
			}
		}
	}
	if len(ordered) == 0 {
		return references
	}
	out := append([]map[string]string{}, references...)
	if limit <= 0 {
		limit = len(out) + len(ordered)
	}
	counts := map[string]int{}
	for _, reference := range out {
		counts[reference["category"]]++
	}
	targetCategories := map[string]bool{}
	for _, category := range videoReplicationTargetCategory {
		targetCategories[category] = true
	}
	for i := len(out) - 1; i >= 0 && len(out)+inShot > limit; i-- {
		category := out[i]["category"]
		if targetCategories[category] && counts[category] > 1 {
			out = append(out[:i], out[i+1:]...)
			counts[category]--
		}
	}
	ordered = ordered[:min(len(ordered), max(limit-len(out), 0))]
	role := "reference"
	if len(out) > 0 {
		role = out[0]["role"]
	}
	for _, element := range ordered {
		out = append(out, map[string]string{"resourceId": element.ResourceID, "category": videoReplicationElementCategory, "label": element.To, "role": role, "elementId": element.ID})
	}
	return out
}

// videoReplicationElementClauses 写出元素替换句：有参考图的「把 @视频1 中的梨替换成 @图片3 中的苹果」，没带上图的只写文字；
// 本段逐项改动没提到的元素写成「如果出现」，识图每段只看一两张关键帧，可能漏看。识图一段都没给出逐项改动时全部按出现处理。
func videoReplicationElementClauses(elements []VideoReplicationElement, shot VideoReplicationShot, storyboard VideoReplicationStoryboard, references []map[string]string, token, source string) []string {
	tokens := map[string]string{}
	for index, reference := range references {
		if reference["category"] == videoReplicationElementCategory {
			tokens[reference["elementId"]] = fmt.Sprintf(token, index+1)
		}
	}
	anyEdits := false
	for _, other := range storyboard.Shots {
		anyEdits = anyEdits || strings.TrimSpace(other.SceneEdits) != ""
	}
	clauses := []string{}
	for _, element := range elements {
		to := element.To
		if ref := tokens[element.ID]; ref != "" {
			to = ref + " 中的" + element.To
		}
		if !anyEdits || videoReplicationElementInShot(element, shot, elements) {
			clauses = append(clauses, "把 "+source+" 中的"+element.From+"替换成 "+to)
		} else {
			clauses = append(clauses, source+" 中如果出现"+element.From+"，替换成 "+to)
		}
	}
	return clauses
}

// videoReplicationKeyframeParameters 是参考帧 / 元素图的出图参数（数量 1，尺寸质量沿用前端选的图片模型参数）。
func videoReplicationKeyframeParameters(params VideoReplicationParameters) map[string]any {
	parameters := map[string]any{"count": 1}
	if p := params.KeyframeParameters; p != nil {
		for key, value := range map[string]string{"size": p.Size, "quality": p.Quality, "imageResolution": p.ImageResolution} {
			if value != "" {
				parameters[key] = value
			}
		}
	}
	return parameters
}

// videoReplicationElementsMissing：清单里还有元素没有参考图（含上次出图失败的，再试一次）。
func videoReplicationElementsMissing(elements []VideoReplicationElement) bool {
	for _, element := range elements {
		if element.ResourceID == "" {
			return true
		}
	}
	return false
}

// videoReplicationShotSourceFrame 返回本段开头所在原片镜头的关键帧（没有时取与本段重叠最长的镜头）。
func videoReplicationShotSourceFrame(video VideoReplicationVideoAnalysis, shot VideoReplicationShot) string {
	probe := shot.SourceStartMs + 500
	best, overlapBest := "", int64(0)
	for _, source := range video.Shots {
		if source.KeyframeResourceID == "" {
			continue
		}
		if source.StartMs <= probe && probe < source.EndMs {
			return source.KeyframeResourceID
		}
		overlap := min(source.EndMs, shot.SourceEndMs) - max(source.StartMs, shot.SourceStartMs)
		if overlap > overlapBest {
			best, overlapBest = source.KeyframeResourceID, overlap
		}
	}
	return best
}

// videoReplicationImageConcurrency 是一个项目同时在跑的出图任务数。一次并发五张时，经代理的图片渠道曾在同一秒
// 全部 connection reset（单张都能成功），所以排队逐批提交。
const videoReplicationImageConcurrency = 2

// videoReplicationImageAttempts 是单张图遇到网络 / 上游临时错误时的最多尝试次数（含第一次）。
const videoReplicationImageAttempts = 3

// videoReplicationTransientImageError：连接被重置、提前关闭、超时、上游 5xx 等临时错误，重新提交通常就能成功；
// 审核拒绝、积分不足、参数错误等重试也没用。
func videoReplicationTransientImageError(message string) bool {
	text := strings.ToLower(message)
	for _, key := range []string{"connection reset", "connection refused", "eof", "timeout", "timed out", "deadline exceeded", "broken pipe", "tls handshake", "no such host", "temporarily", "网络异常", "暂时不可用", "连接提前关闭", "http 500", "http 502", "http 503", "http 504", "429", "繁忙", "超时"} {
		if strings.Contains(text, key) {
			return true
		}
	}
	return false
}

// videoReplicationImageFailureText 把出图失败原因压成给用户看的一句话。
func videoReplicationImageFailureText(message string) string {
	message = strings.TrimSpace(message)
	switch {
	case message == "":
		return "出图失败"
	case videoReplicationTransientImageError(message):
		return "图片渠道网络异常，已自动重试 " + fmt.Sprint(videoReplicationImageAttempts-1) + " 次仍失败"
	default:
		return clipText(message, 120)
	}
}

// runVideoReplicationImageTasks 排队提交出图任务并等待结果，返回 排队序号 → 图片资源 ID 和 排队序号 → 失败原因。
// 同时最多 videoReplicationImageConcurrency 张；网络等临时错误自动重新提交（最多 videoReplicationImageAttempts 次）。
// 用户同时进行的任务满了就等已有任务完成再继续提交，不把超出的直接丢掉；最多等 videoReplicationElementTimeout。
func (s *Service) runVideoReplicationImageTasks(userID, projectID string, requests []WorkbenchTaskInput, stage string) (map[int]string, map[int]string) {
	total := len(requests)
	queue := make([]int, 0, total)
	for index := range requests {
		queue = append(queue, index)
	}
	attempts := map[int]int{}
	tasks := map[int]string{}
	results := map[int]string{}
	failures := map[int]string{}
	fail := func(position int, message string) bool {
		attempts[position]++
		if videoReplicationTransientImageError(message) && attempts[position] < videoReplicationImageAttempts {
			slog.Warn("video replication image retry", "projectId", projectID, "item", position+1, "attempt", attempts[position], "error", message)
			queue = append(queue, position)
			return true
		}
		slog.Warn("video replication image failed", "projectId", projectID, "item", position+1, "error", message)
		failures[position] = videoReplicationImageFailureText(message)
		return false
	}
	deadline := time.Now().Add(videoReplicationElementTimeout)
	for (len(queue) > 0 || len(tasks) > 0) && time.Now().Before(deadline) {
		for len(queue) > 0 && len(tasks) < videoReplicationImageConcurrency {
			position := queue[0]
			task, err := s.CreateWorkbenchTask(userID, requests[position], "", "vr-element-"+newID())
			if err != nil {
				if videoReplicationActiveLimit(err) {
					break
				}
				queue = queue[1:]
				if !fail(position, err.Error()) && !videoReplicationTransientImageError(err.Error()) {
					// 积分不足、模型不可用时后面每张都会同样失败：直接放弃，记下同样的原因。
					for _, rest := range queue {
						failures[rest] = videoReplicationImageFailureText(err.Error())
					}
					queue = nil
				}
				continue
			}
			queue = queue[1:]
			tasks[position] = task.ID
		}
		_ = s.repo.UpdateVideoReplicationProject(userID, projectID, map[string]any{"stage": fmt.Sprintf("%s：%d/%d", stage, len(results)+len(failures), total)})
		time.Sleep(videoReplicationElementPoll)
		for position, taskID := range tasks {
			task, err := s.repo.TaskForUser(userID, taskID)
			if err != nil {
				delete(tasks, position)
				failures[position] = "出图任务丢失"
				continue
			}
			switch task.Status {
			case model.TaskStatusSucceeded:
				delete(tasks, position)
				if id := dramaTaskResource(task); id != "" {
					results[position] = id
				} else {
					fail(position, "模型没有返回图片")
				}
			case model.TaskStatusFailed, model.TaskStatusCancelled:
				delete(tasks, position)
				fail(position, task.Error)
			}
		}
	}
	for _, position := range queue {
		if _, done := failures[position]; !done {
			failures[position] = "等待出图超时"
		}
	}
	for position := range tasks {
		failures[position] = "等待出图超时"
	}
	return results, failures
}

// videoReplicationActiveLimit：同时进行的任务数已满（任务未创建，可以稍后重试）。
func videoReplicationActiveLimit(err error) bool {
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Details == nil {
		return false
	}
	scope, _ := appErr.Details["scope"].(string)
	return scope == "active_tasks"
}
