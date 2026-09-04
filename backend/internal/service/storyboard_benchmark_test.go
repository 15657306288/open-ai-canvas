package service

import (
	"context"
	"testing"

	"infinite-canvas/backend/internal/model"
)

type BenchmarkCase struct {
	Name        string
	Category    string
	Description string
	RawShots    []agentStoryboardShot
}

func getBenchmarkFixtures() []BenchmarkCase {
	return []BenchmarkCase{
		{
			Name:        "武戏对决 (Action Combat)",
			Category:    "beat_coverage",
			Description: "雨夜窄巷刀客决斗，动作节拍快速推进",
			RawShots: []agentStoryboardShot{
				{Title: "", Description: "雨滴打在青石板上", Duration: 0, ShotSize: "", Motion: ""},
				{Title: "拔刀出鞘", Description: "刀光一闪，刀身带起雨水", Duration: 3, ShotSize: "特写", Motion: "快速推近"},
				{Title: "刀刃相撞", Description: "火星四溅，两人错身而过", Duration: 2, ShotSize: "中景", Motion: "侧移跟随"},
				{Title: "收刀入鞘", Description: "胜负已分，一人倒地", Duration: 4, ShotSize: "全景", Motion: "固定机位"},
			},
		},
		{
			Name:        "悬疑揭秘 (Suspense Revelation)",
			Category:    "shot_diversity",
			Description: "侦探在书房密室发现被涂改的遗嘱",
			RawShots: []agentStoryboardShot{
				{Title: "书房全景", Description: "雷雨夜，凌乱的书房", Duration: 4, ShotSize: "全景", Motion: "缓慢推近"},
				{Title: "翻找抽屉", Description: "抽屉被拉开，露出暗格", Duration: 3, ShotSize: "中景", Motion: "俯拍固定"},
				{Title: "取出遗嘱", Description: "手颤抖地展开信纸", Duration: 3, ShotSize: "近景", Motion: "缓慢推进"},
				{Title: "签名特写", Description: "签名处的墨迹有刮擦痕迹", Duration: 3, ShotSize: "特写", Motion: "微距固定"},
			},
		},
		{
			Name:        "双人对峙 (Romance / Tension)",
			Category:    "camera_discipline",
			Description: "昔日挚友在会议室反目成仇，严格遵守轴线",
			RawShots: []agentStoryboardShot{
				{Title: "两人对视", Description: "长桌两端，两人隔桌对立", Duration: 4, ShotSize: "全景", Camera: "平视", Motion: "固定机位"},
				{Title: "正打提问", Description: "主角眼神冰冷提出质问", Duration: 3, ShotSize: "中景", Camera: "过肩平视", Motion: "微推"},
				{Title: "反打回应", Description: "对方冷笑并甩出证据", Duration: 3, ShotSize: "中景", Camera: "过肩平视", Motion: "微推"},
				{Title: "情绪爆发", Description: "主角眼眶泛红咬牙不语", Duration: 4, ShotSize: "特写", Camera: "平视", Motion: "缓慢推近"},
			},
		},
		{
			Name:        "都市日常微短剧 (Pacing / Rhythm)",
			Category:    "pacing_rhythm",
			Description: "打工人早高峰地铁赶卡，节奏明快紧凑",
			RawShots: []agentStoryboardShot{
				{Title: "地铁出站", Description: "人潮涌出闸机，主角飞奔", Duration: -2, ShotSize: "", Motion: ""},
				{Title: "看手表", Description: "指针指向 8:58", Duration: 2, ShotSize: "特写", Motion: "固定机位"},
				{Title: "冲进大厦", Description: "闸机刷卡成功，绿灯亮起", Duration: 3, ShotSize: "中景", Motion: "跟拍"},
				{Title: "电梯合拢", Description: "主角在电梯门合上前一秒闪入", Duration: 3, ShotSize: "近景", Motion: "推近"},
			},
		},
		{
			Name:        "宏大史诗开场 (Epic Establishing)",
			Category:    "tool_discipline",
			Description: "废土纪元未来巨型要塞的建立镜头",
			RawShots: []agentStoryboardShot{
				{Title: "废土苍穹", Description: "沙暴席卷黄沙漫天的荒原", Duration: 5, ShotSize: "大远景", Motion: "缓慢下摇"},
				{Title: "要塞巨壁", Description: "耸入云霄的钢铁城墙", Duration: 4, ShotSize: "远景", Motion: "仰拍推近"},
				{Title: "岗哨卫兵", Description: "防毒面具下的卫兵凝视地平线", Duration: 4, ShotSize: "中景", Motion: "缓慢环绕"},
			},
		},
	}
}

func TestStoryboardDirectorBenchmarkSuite(t *testing.T) {
	cases := getBenchmarkFixtures()
	if len(cases) != 5 {
		t.Fatalf("expected 5 benchmark cases, got %d", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			// 1. 基准测试：未经过 Critic/Repair 处理的 baseline 检查
			baselineIssues := 0
			for _, s := range tc.RawShots {
				if s.Duration <= 0 || s.Duration > 60 || s.Title == "" || s.ShotSize == "" || s.Motion == "" {
					baselineIssues++
				}
			}

			// 2. 执行 Director Critic & Repair 机制
			repaired := criticAndRepairStoryboardShots(tc.RawShots)
			if len(repaired) != len(tc.RawShots) {
				t.Fatalf("[%s] shot count mismatch: expected %d, got %d", tc.Name, len(tc.RawShots), len(repaired))
			}

			// 3. 验证经过 Director 规范处理后的 5 项核心指标
			sizes := make(map[string]bool)
			for i, shot := range repaired {
				if shot.Duration < 1 || shot.Duration > 10 {
					t.Fatalf("[%s] shot %d duration invalid: %d", tc.Name, i+1, shot.Duration)
				}
				if shot.Title == "" {
					t.Fatalf("[%s] shot %d title empty", tc.Name, i+1)
				}
				if shot.Description == "" {
					t.Fatalf("[%s] shot %d description empty", tc.Name, i+1)
				}
				if shot.ShotSize == "" {
					t.Fatalf("[%s] shot %d shotSize missing", tc.Name, i+1)
				}
				if shot.Motion == "" {
					t.Fatalf("[%s] shot %d camera motion missing", tc.Name, i+1)
				}
				sizes[shot.ShotSize] = true
			}

			// 景别多样性必须 >= 2 种（杜绝单一景别）
			if len(sizes) < 2 {
				t.Fatalf("[%s] shot diversity too low: %d sizes detected (%v)", tc.Name, len(sizes), sizes)
			}
		})
	}
}

func TestStoryboardBenchmarkEndToEndSemanticPersistence(t *testing.T) {
	service, db := newProjectWorkflowV2TestService(t)
	project, unit := seedWorkflowProject(t, db)
	if err := db.AutoMigrate(&model.PromptTemplate{}, &model.UserPromptCustomization{}, &model.CanvasUnitLink{}); err != nil {
		t.Fatal(err)
	}
	canvas := model.CanvasProject{ID: "canvas-bm-1", UserID: "user-1", Title: "评测分镜画布"}
	if err := db.Create(&canvas).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CanvasUnitLink{ID: "link-bm-1", ProjectID: project.ID, CanvasID: canvas.ID, UnitID: unit.ID, Role: "storyboard"}).Error; err != nil {
		t.Fatal(err)
	}

	task := model.Task{ID: "task-bm-1", UserID: "user-1", ProjectID: canvas.ID, Operation: "storyboard"}
	cases := getBenchmarkFixtures()

	for _, tc := range cases {
		input := agentStoryboardInput{
			DomainProjectID: project.ID,
			ProjectStyle:    storyboardProjectStyle{Prompt: "胶片质感"},
		}
		plan := agentStoryboardPlan{
			Title:      tc.Name,
			StyleGuide: "自然电影光",
			Shots:      tc.RawShots,
		}

		persisted, err := service.persistAgentStoryboardShots(task, input, plan)
		if err != nil {
			t.Fatalf("[%s] semantic persistence failed: %v", tc.Name, err)
		}
		if len(persisted) != len(tc.RawShots) {
			t.Fatalf("[%s] persisted count mismatch: expected %d, got %d", tc.Name, len(tc.RawShots), len(persisted))
		}

		// 验证每个镜头都有独立稳定的全局 shotId
		for idx, p := range persisted {
			if p.ShotID == "" {
				t.Fatalf("[%s] shot %d shotId is empty", tc.Name, idx+1)
			}
			if p.DomainProjectID != project.ID || p.UnitID != unit.ID {
				t.Fatalf("[%s] shot %d domain/unit link mismatch", tc.Name, idx+1)
			}
		}
	}
	_ = context.Background()
}
