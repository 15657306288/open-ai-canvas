package app

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// 电商视频 Agent 的「模型提示词知识库」：按用户所选视频模型的系列（Seedance / 可灵）把对应的提示词规范交给分镜策划模型，
// 让逐段提示词按该模型最擅长的写法组织。后台可分别关闭；关闭后 Agent 不调用该知识库，按通用写法规划。

const (
	VideoReplicationKnowledgeSeedance = "seedance"
	VideoReplicationKnowledgeKling    = "kling"
	videoReplicationKnowledgeMaxRunes = 30000
)

//go:embed video_replication_knowledge/seedance-2.5.md
var videoReplicationKnowledgeSeedanceDefault string

//go:embed video_replication_knowledge/kling-4.0.md
var videoReplicationKnowledgeKlingDefault string

// VideoReplicationKnowledgeBase 是后台保存的一个知识库。Content 为空表示使用内置默认内容。
type VideoReplicationKnowledgeBase struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Content string `json:"content,omitempty"`
}

// VideoReplicationKnowledgeView 是后台看到的知识库：始终带上生效内容与是否改过默认内容。
type VideoReplicationKnowledgeView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Models      []string `json:"models"`
	Enabled     bool     `json:"enabled"`
	Content     string   `json:"content"`
	Customized  bool     `json:"customized"`
	Default     string   `json:"defaultContent"`
}

type videoReplicationKnowledgeSpec struct {
	id, name, description string
	models                []string
	keywords              []string
	content               string
}

var videoReplicationKnowledgeSpecs = []videoReplicationKnowledgeSpec{
	{id: VideoReplicationKnowledgeSeedance, name: "Seedance 2.5 支持库", description: "用户选择 Seedance 系列模型（2.0 mini、2.0 等）时调用：电商带货多镜头提示词规范、产品文字保护锁与五层反提示词。", models: []string{"Seedance 2.0 mini", "Seedance 2.0", "Seedance 2.5"}, keywords: []string{"seedance", "doubao-seedance", "即梦"}, content: videoReplicationKnowledgeText(videoReplicationKnowledgeSeedanceDefault)},
	{id: VideoReplicationKnowledgeKling, name: "可灵 4.0 支持库", description: "用户选择可灵系列模型时调用：主体 + 运动 + 场景 + 镜头语言公式、多镜头时间轴、对白口型同步写法与短语式负向提示词。", models: []string{"可灵 4.0", "可灵 3.x", "可灵 2.x"}, keywords: []string{"kling", "可灵", "keling"}, content: videoReplicationKnowledgeText(videoReplicationKnowledgeKlingDefault)},
}

// videoReplicationKnowledgeText 统一换行：Windows 检出的内置文件是 CRLF，后台提交的是 LF，比较前要一致。
func videoReplicationKnowledgeText(value string) string {
	return strings.ReplaceAll(value, "\r\n", "\n")
}

func videoReplicationKnowledgeSpecByID(id string) (videoReplicationKnowledgeSpec, bool) {
	for _, spec := range videoReplicationKnowledgeSpecs {
		if spec.id == id {
			return spec, true
		}
	}
	return videoReplicationKnowledgeSpec{}, false
}

// videoReplicationModelFamily 按模型键与显示名判断所选视频模型属于哪个知识库；都不匹配返回空。
func videoReplicationModelFamily(params VideoReplicationParameters) string {
	name := strings.ToLower(params.Model + " " + params.ModelLabel)
	for _, spec := range videoReplicationKnowledgeSpecs {
		for _, keyword := range spec.keywords {
			if strings.Contains(name, keyword) {
				return spec.id
			}
		}
	}
	return ""
}

// knowledge 返回某个知识库的生效状态：没保存过的知识库默认开启、使用内置内容。
func (setting VideoReplicationSetting) knowledge(id string) (enabled bool, content string) {
	spec, ok := videoReplicationKnowledgeSpecByID(id)
	if !ok {
		return false, ""
	}
	enabled, content = true, spec.content
	for _, item := range setting.Knowledge {
		if item.ID != id {
			continue
		}
		enabled = item.Enabled
		if strings.TrimSpace(item.Content) != "" {
			content = item.Content
		}
	}
	return enabled, content
}

func (setting VideoReplicationSetting) knowledgeViews() []VideoReplicationKnowledgeView {
	views := make([]VideoReplicationKnowledgeView, 0, len(videoReplicationKnowledgeSpecs))
	for _, spec := range videoReplicationKnowledgeSpecs {
		enabled, content := setting.knowledge(spec.id)
		views = append(views, VideoReplicationKnowledgeView{ID: spec.id, Name: spec.name, Description: spec.description, Models: spec.models, Enabled: enabled, Content: content, Customized: content != spec.content, Default: spec.content})
	}
	return views
}

// normalizeVideoReplicationKnowledge 校验后台提交的知识库：只接受内置的两个 ID；内容与默认一致时不重复保存。
func normalizeVideoReplicationKnowledge(input []VideoReplicationKnowledgeBase) ([]VideoReplicationKnowledgeBase, error) {
	result := []VideoReplicationKnowledgeBase{}
	for _, item := range input {
		spec, ok := videoReplicationKnowledgeSpecByID(strings.TrimSpace(item.ID))
		if !ok {
			return nil, BadAuthRequest("知识库无效：" + item.ID)
		}
		for _, existing := range result {
			if existing.ID == spec.id {
				return nil, BadAuthRequest("知识库重复：" + spec.name)
			}
		}
		content := strings.ToValidUTF8(strings.ReplaceAll(item.Content, "\r\n", "\n"), "")
		if utf8.RuneCountInString(content) > videoReplicationKnowledgeMaxRunes {
			return nil, BadAuthRequest(spec.name + "内容过长，最多 30000 字")
		}
		if strings.TrimSpace(content) == strings.TrimSpace(spec.content) {
			content = ""
		}
		result = append(result, VideoReplicationKnowledgeBase{ID: spec.id, Enabled: item.Enabled, Content: content})
	}
	return result, nil
}

// videoReplicationActiveKnowledge 是本次规划实际调用的知识库；所选模型不属于任何知识库或知识库已关闭时返回 nil。
type videoReplicationActiveKnowledge struct {
	ID, Name, Content string
}

func (s *Service) videoReplicationKnowledgeFor(params VideoReplicationParameters) *videoReplicationActiveKnowledge {
	family := videoReplicationModelFamily(params)
	if family == "" {
		return nil
	}
	setting, err := s.videoReplicationSetting()
	if err != nil {
		return nil
	}
	enabled, content := setting.knowledge(family)
	if !enabled || strings.TrimSpace(content) == "" {
		return nil
	}
	spec, _ := videoReplicationKnowledgeSpecByID(family)
	return &videoReplicationActiveKnowledge{ID: family, Name: spec.name, Content: content}
}

// videoReplicationKnowledgeKey 进入分段方案摘要：开关或内容变化后，素材不变也会重新规划。
func (s *Service) videoReplicationKnowledgeKey(params VideoReplicationParameters) string {
	knowledge := s.videoReplicationKnowledgeFor(params)
	if knowledge == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(knowledge.ID + "|" + knowledge.Content))
	return hex.EncodeToString(sum[:6])
}

// videoReplicationKnowledgeSystem 把知识库接到分镜策划的系统提示词后面，并说明在「复刻」任务里怎么用：
// 吸收镜头语言、公式、一致性锁、音频与口型写法和反提示词，但分段、时长与 JSON 结构仍按复刻要求。
func videoReplicationKnowledgeSystem(knowledge *videoReplicationActiveKnowledge) string {
	if knowledge == nil {
		return ""
	}
	return "\n\n【目标视频模型提示词知识库：" + knowledge.Name + "】\n本次视频由该模型生成，下面是该模型的电商带货提示词规范。使用方式：\n" +
		"1. 本任务是复刻参考视频，不是从零创作：分段数量、每段时长、画面内容和节奏仍以参考视频为准，输出仍是要求里的 JSON。\n" +
		"2. 每段 prompt 按知识库的核心公式与词表组织（景别、运镜、光线、色彩、动作幅度、声音写法），产品一致性锁与产品文字保护写进去，末尾附上与本段相关的简短反提示词。\n" +
		"3. 不要输出知识库里的整套模板段落（素材职责、产品档案图、人物四视图、场景四宫格等），也不要出现「--- 开始 ---」之类的分隔行。\n" +
		"4. 知识库与复刻要求冲突时，以复刻要求和用户补充要求为准。\n" +
		"----- 知识库开始 -----\n" + knowledge.Content + "\n----- 知识库结束 -----"
}

// videoReplicationKnowledgeNegative 是附在每段视频提示词末尾的反提示词，按模型写法区分（可灵用短语列表）。
func videoReplicationKnowledgeNegative(id string) string {
	switch id {
	case VideoReplicationKnowledgeKling:
		return "负向：主体漂移，产品变形，肢体畸形，多余手指，文字乱码，编造产品参数，新增字幕，水印，口型与台词错位，不说话时嘴部乱动，声音与说话人不符，表情僵硬，假笑，蜡像脸，磨皮塑料感，动作僵硬机械，动作卡顿，木偶感，播音腔，机器人念稿。"
	case VideoReplicationKnowledgeSeedance:
		return "反提示词：不要主体漂移，不要产品变形，不要肢体畸形，不要多余手指，不要文字乱码、编造产品参数或新增字幕，不要水印，不要人物口型与台词错位，不要不说话时嘴部乱动，不要表情僵硬或假笑，不要蜡像脸和磨皮塑料感，不要动作僵硬机械、卡顿或像木偶，不要播音腔或机器人念稿。"
	}
	return ""
}
