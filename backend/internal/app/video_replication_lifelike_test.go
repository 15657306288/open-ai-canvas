package app

import (
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

// 只换商品时人物不在替换目标里，原片编辑提示词以前对人物只有「与原片一致」，成片人脸像蜡像、动作僵硬。
// 真人规则要在每段都出现，并且排在「保持不变」之前，提示词被按模型字数截断时也保留。
func TestVideoReplicationPromptsKeepPeopleLifelike(t *testing.T) {
	references := []map[string]string{{"category": model.VideoReplicationAssetProductImage}}
	storyboard := VideoReplicationStoryboard{ProductStructure: strings.Repeat("瓶身结构细节。", 20)}
	full := videoReplicationEditPrompt("product", VideoReplicationParameters{PromptMaxChars: 5000}, VideoReplicationShot{}, storyboard, "", references)
	if !strings.Contains(full, videoReplicationLifelikeRule) {
		t.Fatalf("edit prompt misses lifelike rule:\n%s", full)
	}
	if strings.Index(full, videoReplicationLifelikeRule) > strings.Index(full, "保持不变：") {
		t.Fatalf("lifelike rule should come before keep list:\n%s", full)
	}
	// 超出预算时规则让位到末尾，替换细节优先保留。
	size := len([]rune(full))
	tight := videoReplicationEditPrompt("product", VideoReplicationParameters{PromptMaxChars: size - 5}, VideoReplicationShot{}, storyboard, "", references)
	if !strings.Contains(tight, "去掉原视频画面里烧录的字幕") || len([]rune(tight)) > size-5 {
		t.Fatalf("tight edit prompt should keep replacement details first:\n%s", tight)
	}

	// 锁死只锁镜头；人物动作照原片但要流畅，段首段尾不能从静止开始或定格。
	for _, want := range []string{"像真人一样流畅自然", "第一帧起就照原片自然在动"} {
		if !strings.Contains(full, want) {
			t.Fatalf("edit prompt misses %q:\n%s", want, full)
		}
	}
	if !strings.Contains(videoReplicationEditClipFilter(0), "flags=lanczos") {
		t.Fatal("reference clip should scale with lanczos")
	}

	shot := videoReplicationShotPrompt("product", VideoReplicationParameters{}, VideoReplicationShot{Prompt: "女生手持商品对镜头介绍"}, "", references)
	if !strings.Contains(shot, videoReplicationLifelikeRule) {
		t.Fatalf("shot prompt misses lifelike rule:\n%s", shot)
	}

	for _, id := range []string{VideoReplicationKnowledgeSeedance, VideoReplicationKnowledgeKling} {
		negative := videoReplicationKnowledgeNegative(id)
		for _, want := range []string{"表情僵硬", "蜡像脸", "动作僵硬机械"} {
			if !strings.Contains(negative, want) {
				t.Fatalf("negative %s misses %q: %s", id, want, negative)
			}
		}
	}
}

// 复现场景：Seedance 2.5、换商品+人物、替换音频、23 秒长台词、后台提示词上限 8000 字。
// 以前台词拼接写死 3900 字节截断，换人物的表情要求、去字幕规则和反提示词全被截掉。
func TestVideoReplicationLongVoiceKeepsPersonRulesWithinAdminBudget(t *testing.T) {
	references := []map[string]string{{"category": model.VideoReplicationAssetProductImage}, {"category": model.VideoReplicationAssetModelImage}}
	storyboard := VideoReplicationStoryboard{
		SourceLook: "细长圆柱形金属管身，玫瑰金与银色抛光金属光泽，浅粉色唇膏体。", NewSubject: "亮黑方管金色内壁的口红本体，旋出鲜红色斜切膏体。",
		ReferenceProps: "置于右侧下方的黑色方形口红顶盖", ProductStructure: "经典黑金方管口红，包含亮黑方形底座管身、香槟金色旋转内柱、鲜红色斜切水滴形膏体及配套独立黑色方盖。",
		KnowledgeID: VideoReplicationKnowledgeSeedance,
	}
	params := VideoReplicationParameters{ReplaceMode: "combination", AudioMode: "replace", PromptMaxChars: 8000, MaxReferenceVideos: 10, ReferenceMode: "reference"}
	lines := strings.Repeat("真的很想跟你们分享一下，我今天这个显白正红嘴巴，一涂上嘴就很高级浓郁。", 4)
	sound := videoReplicationSound{Edit: true, SpeechSegment: true, Stems: true, Subtitled: true}
	compose := func(params VideoReplicationParameters) string {
		negative := videoReplicationKnowledgeNegative(storyboard.KnowledgeID)
		budget := videoReplicationPromptBudgetFor(params).without(negative)
		edit := budget.clip(videoReplicationEditPrompt("product,model", params, VideoReplicationShot{ProductPresence: "hand"}, storyboard, "", references))
		return videoReplicationWithVoicePrompt(edit, lines, 1, references, sound, budget) + "\n" + negative
	}
	prompt := compose(params)
	for _, want := range []string{"不采用图中的表情、姿势", "跟着 @视频1 中的原人物自然变化", videoReplicationLifelikeRule, "去掉原视频画面里烧录的字幕", "台词：人物逐字说", "不要表情僵硬"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt misses %q:\n%s", want, prompt)
		}
	}
	// 预算紧时只截画面描述，台词与反提示词保留，总长不超过后台上限。
	params.PromptMaxChars = 900
	tight := compose(params)
	if n := len([]rune(tight)); n > 900 || !strings.Contains(tight, "台词：人物逐字说") || !strings.Contains(tight, "不要表情僵硬") {
		t.Fatalf("tight prompt (%d runes) lost voice or negative:\n%s", n, tight)
	}
}

// 用户上传音色后成片像机器人念台词：以前要求「语调、语速与参考音频完全一致」，且原片说话人语气（轻柔欢快、亲切种草）没写进提示词。
// 现在参考音频只借音色，语气按用户语气指导 > 原片语气，情绪跟着营销节奏起伏。
func TestVideoReplicationVoicePromptCarriesEmotion(t *testing.T) {
	project := &model.VideoReplicationProject{AnalysisJSON: `{"marketing":"开门见山种草-上唇实测-色号解析-成分卖点-场景号召","voice":{"speakers":[{"id":"S1","label":"出镜女主播","tone":"轻柔欢快、亲切种草","onCamera":true}]}}`}
	sound := videoReplicationSound{Edit: true, SpeechSegment: true}
	sound.Delivery, sound.Selling = videoReplicationDeliveryNote(project)
	references := []map[string]string{{"category": model.VideoReplicationAssetModelImage}}
	prompt := videoReplicationWithVoicePrompt("画面", "这个正红色真的超显白", 1, references, sound, videoReplicationPromptBudgetFor(VideoReplicationParameters{}))
	for _, want := range []string{"只提供音色，语气和情绪不照搬", "语气轻柔欢快、亲切种草", "开门见山种草", "不要平铺直叙、一字一顿、播音腔或机器人朗读"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("voice prompt misses %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "语调、语速与它完全一致") || strings.Contains(prompt, "音量平稳") {
		t.Fatalf("voice prompt still forces flat delivery:\n%s", prompt)
	}
	// 用户在音色绑定里写的语气指导优先于识别结果。
	project.VoiceCastJSON = `{"roles":[{"speakerId":"S1","instructions":"兴奋夸张、语速偏快"}]}`
	if delivery, _ := videoReplicationDeliveryNote(project); delivery != "兴奋夸张、语速偏快" {
		t.Fatalf("delivery = %q", delivery)
	}
}

// 原片有人对镜说话时，用户没勾「是否口播视频」也按口播处理：换人 / 换声音后口型要对上台词。无人说话的原片不受影响。
func TestVideoReplicationDetectsTalkingFromAnalysis(t *testing.T) {
	onCamera := &VideoReplicationAnalysis{Voice: &VideoReplicationVoice{Type: "on_camera"}}
	if !videoReplicationWithDetectedTalking(VideoReplicationParameters{}, onCamera).Talking {
		t.Fatal("on-camera speech should enable talking")
	}
	if !videoReplicationWithDetectedTalking(VideoReplicationParameters{}, &VideoReplicationAnalysis{Voice: &VideoReplicationVoice{Type: "dialogue"}}).Talking {
		t.Fatal("dialogue should enable talking")
	}
	for _, kind := range []string{"music", "narration", "silent", ""} {
		if videoReplicationWithDetectedTalking(VideoReplicationParameters{}, &VideoReplicationAnalysis{Voice: &VideoReplicationVoice{Type: kind}}).Talking {
			t.Fatalf("%q must not become talking", kind)
		}
	}
	if videoReplicationWithDetectedTalking(VideoReplicationParameters{}, nil).Talking {
		t.Fatal("missing analysis must not become talking")
	}
	// 换人 + 保留原声：按口播处理后，原片编辑和口型提示词都要求嘴型对上原台词。
	params := videoReplicationWithDetectedTalking(VideoReplicationParameters{AudioMode: "keep", PromptMaxChars: 5000}, onCamera)
	references := []map[string]string{{"category": model.VideoReplicationAssetModelImage}}
	edit := videoReplicationEditPrompt("model", params, VideoReplicationShot{}, VideoReplicationStoryboard{}, "", references)
	if !strings.Contains(edit, "口型与 @视频1 一致") {
		t.Fatalf("edit prompt misses lip sync:\n%s", edit)
	}
	lip := videoReplicationWithLipSyncPrompt(edit, "这个口红真的超显白", videoReplicationPromptBudgetFor(params))
	if !strings.Contains(lip, "人物口型与原台词「这个口红真的超显白」同步") {
		t.Fatalf("lip sync prompt:\n%s", lip)
	}
	if !strings.Contains(videoReplicationLifelikeRule, "咀嚼吞咽") {
		t.Fatal("lifelike rule should cover eating")
	}
}
