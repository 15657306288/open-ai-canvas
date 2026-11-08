package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

func videoReplicationVoiceBudgetReply(w http.ResponseWriter, value any) {
	content, _ := json.Marshal(value)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}},
		"usage":   map[string]int{"prompt_tokens": 1000, "completion_tokens": 500},
	})
}

func videoReplicationVoiceBudgetRequest(r *http.Request, out any) error {
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return err
	}
	for _, message := range body.Messages {
		if message.Role == "user" {
			return json.Unmarshal([]byte(message.Content), out)
		}
	}
	return fmt.Errorf("request has no user message")
}

func videoReplicationVoiceBudgetAnalysis() *VideoReplicationAnalysis {
	return &VideoReplicationAnalysis{
		Product: map[string]any{"name": "桂花曲奇", "deliveryTone": "自然分享"},
		Summary: VideoReplicationSummary{Subtitles: "有商品介绍字幕"},
		Voice: &VideoReplicationVoice{
			Type: "on_camera", HasAudio: true, ASRAvailable: true, CharsPerSec: 4.5,
			Segments: []timelineTranscriptionSegment{
				{StartMs: 0, EndMs: 22000, Text: "看看这包香辣大鸡腿。"},
				{StartMs: 22000, EndMs: 38000, Text: "这只鸡腿拿起来就能吃。"},
			},
		},
		Videos: []VideoReplicationVideoAnalysis{{DurationMs: 38000, HasAudio: true, Shots: []VideoReplicationSourceShot{
			{StartMs: 0, EndMs: 22000, Subtitle: "香辣大鸡腿"},
			{StartMs: 22000, EndMs: 38000, Subtitle: "开袋就能吃"},
		}}},
	}
}

func TestVideoReplicationVoiceBudgetPlannerRepairsSpeechAndKeepsTiming(t *testing.T) {
	f := newVideoReplicationCostFixture(t)
	const repaired = "看看这块桂花曲奇，拿在手里给大家看一看。"
	const kept = "试试看这块桂花曲奇。"
	f.reply.Store(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("planning must only call the text model: %s", r.URL.Path)
		}
		if f.calls.Load() == 1 {
			videoReplicationVoiceBudgetReply(w, VideoReplicationStoryboard{Shots: []VideoReplicationShot{
				{Prompt: "手持曲奇特写", Voiceover: strings.Repeat("曲奇", 50) + "。", Subtitle: "旧字幕"},
				{Prompt: "展示曲奇包装", Voiceover: kept},
			}})
			return
		}
		var request struct {
			Context struct {
				ProductName string         `json:"productName"`
				Product     map[string]any `json:"product"`
			} `json:"context"`
			Lines []videoReplicationVoiceRepairLine `json:"lines"`
		}
		if err := videoReplicationVoiceBudgetRequest(r, &request); err != nil {
			t.Errorf("decode repair request: %v", err)
		}
		if len(request.Lines) != 1 || request.Lines[0].Index != 1 || request.Lines[0].MaxChars != 99 || request.Lines[0].TargetChars >= 99 {
			t.Errorf("repair must only target the overlong 99-character segment: %+v", request.Lines)
		}
		if request.Context.ProductName != "桂花曲奇" || request.Context.Product["name"] != "桂花曲奇" {
			t.Errorf("repair lost the new product facts: %+v", request.Context)
		}
		videoReplicationVoiceBudgetReply(w, map[string]any{"lines": []any{map[string]any{"index": 1, "text": repaired}}})
	})
	params := VideoReplicationParameters{GenerateAudio: true, AudioMode: "keep", KeepOriginalAudio: true, SegmentSeconds: 22, ShotSeconds: []int{16, 22}, ProductName: "桂花曲奇", ReplaceMode: "combination", MaxVideoTasks: 2}
	ctx := withVideoReplicationCostScope(context.Background(), "owner", "proj-1")
	board, err := f.svc.planVideoReplicationAutoStoryboard(ctx, f.cfg("vision-test"), &model.VideoReplicationProject{ID: "proj-1", UserID: "owner", VideoType: "model,product,background"}, params, nil, videoReplicationVoiceBudgetAnalysis())
	if err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 2 || len(board.Shots) != 2 || !board.VoiceAdapted || !board.SubtitlesAdapted {
		t.Fatalf("expected one planning call and one repair: calls=%d board=%+v", f.calls.Load(), board)
	}
	for index, want := range []struct {
		text, prompt string
		seconds      int
		start, end   int64
	}{{repaired, "手持曲奇特写", 22, 0, 22000}, {kept, "展示曲奇包装", 16, 22000, 38000}} {
		shot := board.Shots[index]
		if shot.Voiceover != want.text || shot.Subtitle != want.text || shot.Prompt != want.prompt || shot.DurationSec != want.seconds || shot.SourceStartMs != want.start || shot.SourceEndMs != want.end {
			t.Fatalf("repair changed timing, visuals or final subtitles: %+v", shot)
		}
		if strings.Contains(shot.Voiceover, "鸡腿") {
			t.Fatal("old product speech returned")
		}
	}
	logs := f.logs(t)
	if len(logs) != 2 || logs[1].Operation != videoReplicationAnalysisScene+":voice-repair" || logs[1].Status != model.ApiCallStatusSucceeded {
		t.Fatalf("repair must use the existing metered text path: %+v", logs)
	}
	var account model.CreditAccount
	if err := f.db.First(&account, "user_id = ?", "owner").Error; err != nil || account.AvailableMicrocredits != 1_000_000 || account.ReservedMicrocredits != 0 {
		t.Fatalf("planning changed user credits: %+v, %v", account, err)
	}
	var tasks int64
	if err := f.db.Model(&model.Task{}).Count(&tasks).Error; err != nil || tasks != 0 {
		t.Fatalf("repair must not submit video tasks: %d, %v", tasks, err)
	}
}

func TestVideoReplicationVoiceBudgetValidBoundaryNeedsNoModelCall(t *testing.T) {
	exact := strings.Repeat("曲奇", 49) + "。"
	if utf8.RuneCountInString(exact) != 99 {
		t.Fatal("invalid boundary fixture")
	}
	segments := []videoReplicationAutoSegment{{Lines: "原台词", MaxChars: 99}, {MaxChars: 99}}
	shots := []VideoReplicationShot{{Voiceover: exact}, {Voiceover: "没有原台词时不得擅自加旁白"}}
	if err := (&Service{}).repairVideoReplicationAdaptedLines(context.Background(), ecommerceProviderConfig{}, "project", []byte(`{}`), segments, shots); err != nil {
		t.Fatal(err)
	}
	if shots[0].Voiceover != exact || shots[1].Voiceover != "没有原台词时不得擅自加旁白" {
		t.Fatal("valid and silent source segments should not be rewritten")
	}
}

func TestVideoReplicationVoiceBudgetRepairIsBoundedAndRejectsWrongSegments(t *testing.T) {
	const good = `{"lines":[{"index":1,"text":"看看桂花曲奇。"}]}`
	long, _ := json.Marshal(map[string]any{"lines": []any{map[string]any{"index": 1, "text": strings.Repeat("曲奇", 60)}}})
	for _, test := range []struct {
		name    string
		replies []string
		status  int
		wantErr bool
	}{
		{name: "missing draft is completed", replies: []string{good}},
		{name: "empty repair retries", replies: []string{`{"lines":[{"index":1,"text":"（微笑）"}]}`, good}},
		{name: "long repair retries", replies: []string{string(long), good}},
		{name: "two long repairs stop", replies: []string{string(long), string(long)}, wantErr: true},
		{name: "missing response segment stops", replies: []string{`{"lines":[]}`}, wantErr: true},
		{name: "unknown index stops", replies: []string{`{"lines":[{"index":2,"text":"看看桂花曲奇。"}]}`}, wantErr: true},
		{name: "duplicate index stops", replies: []string{`{"lines":[{"index":1,"text":"曲奇。"},{"index":1,"text":"曲奇。"}]}`}, wantErr: true},
		{name: "upstream error stops", replies: []string{`{"error":{"message":"invalid request"}}`}, status: http.StatusBadRequest, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newVideoReplicationCostFixture(t)
			f.reply.Store(func(w http.ResponseWriter, _ *http.Request) {
				index := int(f.calls.Load()) - 1
				if index >= len(test.replies) {
					t.Errorf("unexpected additional model request %d", index+1)
					index = len(test.replies) - 1
				}
				if test.status != 0 {
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(test.replies[index]))
					return
				}
				videoReplicationVoiceBudgetReply(w, json.RawMessage(test.replies[index]))
			})
			shots := []VideoReplicationShot{{Prompt: "曲奇展示"}}
			err := f.svc.repairVideoReplicationAdaptedLines(context.Background(), f.cfg("vision-test"), "proj-1", []byte(`{"productName":"桂花曲奇"}`), []videoReplicationAutoSegment{{Lines: "原商品台词", MaxChars: 99}}, shots)
			if (err != nil) != test.wantErr || f.calls.Load() != int32(len(test.replies)) {
				t.Fatalf("repair result: calls=%d shots=%+v err=%v", f.calls.Load(), shots, err)
			}
			if test.wantErr {
				if !strings.Contains(err.Error(), "未提交视频生成") {
					t.Fatalf("failure must keep the planning-stage outcome: %v", err)
				}
			} else if shots[0].Voiceover != "看看桂花曲奇。" || shots[0].Prompt != "曲奇展示" {
				t.Fatalf("repair did not preserve the complete new-product line: %+v", shots)
			}
		})
	}
}

func TestVideoReplicationVoiceBudgetRetriesOnlyRemainingInvalidLines(t *testing.T) {
	f := newVideoReplicationCostFixture(t)
	const first = "看看这块曲奇。"
	const second = "曲奇拿起来给大家看看。"
	const kept = "这块桂花曲奇。"
	long := strings.Repeat("曲奇", 60)
	f.reply.Store(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Lines []videoReplicationVoiceRepairLine `json:"lines"`
		}
		if err := videoReplicationVoiceBudgetRequest(r, &request); err != nil {
			t.Errorf("decode repair: %v", err)
		}
		if f.calls.Load() == 1 {
			if len(request.Lines) != 2 || request.Lines[0].Index != 1 || request.Lines[1].Index != 2 {
				t.Errorf("must batch only invalid segments: %+v", request.Lines)
			}
			videoReplicationVoiceBudgetReply(w, map[string]any{"lines": []any{map[string]any{"index": 1, "text": first}, map[string]any{"index": 2, "text": long}}})
			return
		}
		if len(request.Lines) != 1 || request.Lines[0].Index != 2 || request.Lines[0].Draft != long {
			t.Errorf("retry must retain the valid first repair: %+v", request.Lines)
		}
		videoReplicationVoiceBudgetReply(w, map[string]any{"lines": []any{map[string]any{"index": 2, "text": second}}})
	})
	segments := []videoReplicationAutoSegment{{Lines: "旧商品一", MaxChars: 99}, {Lines: "旧商品二", MaxChars: 99}, {Lines: "旧商品三", MaxChars: 99}}
	shots := []VideoReplicationShot{{Voiceover: long}, {Voiceover: long}, {Voiceover: kept}}
	if err := f.svc.repairVideoReplicationAdaptedLines(context.Background(), f.cfg("vision-test"), "proj-1", []byte(`{"productName":"桂花曲奇"}`), segments, shots); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 2 || shots[0].Voiceover != first || shots[1].Voiceover != second || shots[2].Voiceover != kept {
		t.Fatalf("valid lines were lost or reprocessed: calls=%d shots=%+v", f.calls.Load(), shots)
	}
}

func TestVideoReplicationVoiceBudgetPlannerRepairsMutedSubtitlesWithoutNarration(t *testing.T) {
	f := newVideoReplicationCostFixture(t)
	f.reply.Store(func(w http.ResponseWriter, _ *http.Request) {
		if f.calls.Load() == 1 {
			videoReplicationVoiceBudgetReply(w, VideoReplicationStoryboard{Shots: []VideoReplicationShot{{Prompt: "曲奇包装特写", Subtitle: strings.Repeat("桂花曲奇", 10), Voiceover: "模型擅自加的旁白"}}})
			return
		}
		videoReplicationVoiceBudgetReply(w, map[string]any{"lines": []any{map[string]any{"index": 1, "text": "看看桂花曲奇。"}}})
	})
	analysis := &VideoReplicationAnalysis{Product: map[string]any{"name": "桂花曲奇"}, Videos: []VideoReplicationVideoAnalysis{{DurationMs: 4000, Shots: []VideoReplicationSourceShot{{StartMs: 0, EndMs: 4000, Subtitle: "香辣大鸡腿"}}}}}
	board, err := f.svc.planVideoReplicationAutoStoryboard(context.Background(), f.cfg("vision-test"), &model.VideoReplicationProject{ID: "proj-1", UserID: "owner", VideoType: "product"}, VideoReplicationParameters{AudioMode: "mute", SegmentSeconds: 4, ProductName: "桂花曲奇"}, nil, analysis)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 2 || board.VoiceAdapted || !board.SubtitlesAdapted || len(board.Shots) != 1 || board.Shots[0].Voiceover != "" || board.Shots[0].Subtitle != "看看桂花曲奇。" || board.Shots[0].DurationSec != 4 {
		t.Fatalf("subtitle repair changed mute or introduced narration: %+v", board)
	}
}
