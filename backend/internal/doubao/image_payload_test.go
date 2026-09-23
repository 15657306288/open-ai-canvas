package doubao

import (
	"encoding/json"
	"strings"
	"testing"
)

// 图层拆分的上游指令必须带层序号、层总数与选区；选区坐标走 <bbox> 归一化文本。
func TestBuildLayerPromptCarriesIndexTotalAndRegion(t *testing.T) {
	region := LayerRegion{X1: 100, Y1: 200, X2: 300, Y2: 400}
	prompt := BuildLayerPrompt("拆分为主体与背景", 2, 3, &region)

	if !strings.Contains(prompt, "拆分为主体与背景") {
		t.Fatalf("基础提示词丢失：%q", prompt)
	}
	if !strings.Contains(prompt, "第 2 个图层（共 3 个图层）") {
		t.Fatalf("缺少层序号/总数：%q", prompt)
	}
	if !strings.Contains(prompt, "<bbox>100 200 300 400</bbox>") {
		t.Fatalf("缺少选区 bbox：%q", prompt)
	}
	if !strings.Contains(prompt, "只输出这一张图层图片") {
		t.Fatalf("缺少单层约束：%q", prompt)
	}

	withoutRegion := BuildLayerPrompt("拆分为主体与背景", 1, 3, nil)
	if strings.Contains(withoutRegion, "<bbox>") {
		t.Fatalf("无选区时不应出现 bbox：%q", withoutRegion)
	}
}

// 层数展开：每层一条提示词，regions 不足的层按无选区处理，非法层数收敛到 1。
func TestLayerPromptsExpandPerLayer(t *testing.T) {
	regions := []LayerRegion{{X1: 1, Y1: 2, X2: 3, Y2: 4}, {X1: 5, Y1: 6, X2: 7, Y2: 8}}
	prompts := LayerPrompts("基础", 3, regions)

	if len(prompts) != 3 {
		t.Fatalf("层数 = %d, want 3", len(prompts))
	}
	if !strings.Contains(prompts[0], "<bbox>1 2 3 4</bbox>") || !strings.Contains(prompts[1], "<bbox>5 6 7 8</bbox>") {
		t.Fatalf("前两层选区映射错误：%q", prompts)
	}
	if strings.Contains(prompts[2], "<bbox>") {
		t.Fatalf("第三层没有选区却带上了 bbox：%q", prompts[2])
	}
	if got := LayerPrompts("基础", 0, nil); len(got) != 1 {
		t.Fatalf("层数 0 应对齐到 1 层，得到 %d", len(got))
	}
}

// 参考图必须写进 content_type=2009 消息的 attachments，且与 2020 视频消息同构。
func TestImageGenerationPayloadCarriesReferenceAttachments(t *testing.T) {
	payload := imageGenerationPayload("把背景换掉", "", "1824x1024", "", []string{"tos-cn-i-abc/ref-1.png", "  ", "tos-cn-i-abc/ref-2.jpeg"})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Messages []struct {
			Content     string           `json:"content"`
			ContentType int              `json:"content_type"`
			Attachments []map[string]any `json:"attachments"`
			Skill       map[string]any   `json:"skill"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(decoded.Messages))
	}
	message := decoded.Messages[0]
	if message.ContentType != 2009 {
		t.Fatalf("content_type = %d, want 2009", message.ContentType)
	}
	if len(message.Attachments) != 2 {
		t.Fatalf("空白 key 应被丢弃，attachments = %#v", message.Attachments)
	}
	for _, attachment := range message.Attachments {
		if attachment["type"] != "image" {
			t.Fatalf("attachment type = %#v", attachment["type"])
		}
		if key, _ := attachment["key"].(string); !strings.HasPrefix(key, "tos-cn-i-abc/") {
			t.Fatalf("attachment key = %#v", attachment["key"])
		}
	}
	// 正文里必须同时带比例方位词（像素尺寸 → 标准比例），这是豆包唯一认的写法。
	if !strings.Contains(message.Content, "横屏 16:9") {
		t.Fatalf("content 未归一化比例：%s", message.Content)
	}
	if model, _ := message.Skill["skill_type"].(float64); model != 3 {
		t.Fatalf("skill_type = %#v, want 3", message.Skill["skill_type"])
	}
}

// 无参考图时 attachments 必须是空数组（保持原有文生图形态不变）。
func TestImageGenerationPayloadWithoutRefsKeepsEmptyAttachments(t *testing.T) {
	payload := imageGenerationPayload("画一只猫", "Seedream 4.5", "1:1", "", nil)
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", payload["messages"])
	}
	message, _ := messages[0].(map[string]any)
	attachments, ok := message["attachments"].([]any)
	if !ok || len(attachments) != 0 {
		t.Fatalf("attachments = %#v, want empty slice", message["attachments"])
	}
}
