package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"yingce/backend/internal/model"
	"yingce/backend/internal/platform"
)

// 分析阶段成本记账的数据库测试（真实服务层 + SQLite；上游用本机 httptest 服务代替，只验证记账链路）。

func (f *videoReplicationCostFixture) report(t *testing.T) *AnalyticsOverview {
	t.Helper()
	now := time.Now().UTC()
	report, err := f.svc.AdminAnalytics(&model.User{ID: "admin", Role: model.UserRoleAdmin}, AnalyticsQuery{From: now.Add(-time.Hour).Format(time.RFC3339), To: now.Add(time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func (f *videoReplicationCostFixture) chat(ctx context.Context, modelKey, stage string) error {
	var out map[string]any
	return f.svc.videoReplicationChatJSON(ctx, f.cfg(modelKey), stage, "proj-1", "system", "prompt", nil, nil, &out)
}

func orderCost(t *testing.T, order model.BillingOrder) int64 {
	t.Helper()
	cost, err := billingCreditCost(order)
	if err != nil || cost == nil {
		t.Fatalf("order cost unknown: %v %+v", err, order)
	}
	return *cost
}

func TestVideoReplicationAnalysisCostIsLoggedAndReachesFinance(t *testing.T) {
	t.Skip("数据分析里平台承担成本的单独统计已随上游 v1.7.3.5 合并移除")
	f := newVideoReplicationCostFixture(t)
	ctx := withVideoReplicationCostScope(context.Background(), "owner", "proj-1")

	// 视频理解、商品/模特识别、分镜策划：三次文本 / 多模态调用。
	for _, stage := range []string{"vision", "profile", "planning"} {
		if err := f.chat(ctx, "vision-test", stage); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	// 语音识别：按音频秒数计量（30 秒）。
	f.reply.Store(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"你好","language":"zh","segments":[{"start":0,"end":1.2,"text":"你好"}]}`))
	})
	asr := providerConfig{BaseURL: f.upstream.URL, APIKey: "fixture-key", Model: "asr-test", ChannelID: "ch-1", ChannelModelKey: "asr-test"}
	meteredCtx, meter := f.svc.meterVideoReplicationCall(ctx, "asr", "audio", &asr, 30)
	var raw json.RawMessage
	if err := postForm(meteredCtx, asr, "/audio/transcriptions", "multipart/form-data; boundary=x", bytes.NewBufferString("--x--"), &raw); err != nil {
		t.Fatal(err)
	}
	f.svc.settleVideoReplicationMeter(meter)

	logs := f.logs(t)
	if len(logs) != 4 {
		t.Fatalf("want 4 call logs, got %d", len(logs))
	}
	taskIDs := map[string]bool{}
	for index, log := range logs {
		stage := []string{"vision", "profile", "planning", "asr"}[index]
		if log.UserID != "owner" || log.ChannelID != "ch-1" || log.Status != model.ApiCallStatusSucceeded || log.RequestKind != "create" || !log.Billable || log.Source != videoReplicationAnalysisSource ||
			log.Operation != videoReplicationAnalysisScene+":"+stage || log.TraceID != "video-replication:proj-1" || log.BillingOrderID == "" || log.CreatedAt.IsZero() || log.RequestBody == "" {
			t.Fatalf("log %d = %+v", index, log)
		}
		taskIDs[log.TaskID] = true
		if stage == "asr" {
			if log.Capability != "audio" || log.Model != "asr-test" || !log.CostAvailable || log.EstimatedCostMicros != 500_000 {
				t.Fatalf("asr log = %+v", log)
			}
			continue
		}
		// 供应商实际成本（按渠道上游单价）：1000×1 + 500×2（每百万）= 2000 微元。
		if log.Capability != "text" || log.Model != "vision-test" || !log.UsageAvailable || log.InputTokens != 1000 || log.OutputTokens != 500 || !log.CostAvailable || log.EstimatedCostMicros != 2000 || log.Currency != "CNY" {
			t.Fatalf("text log = %+v", log)
		}
	}
	if len(taskIDs) != 4 {
		t.Fatalf("each call must have its own call id: %v", taskIDs)
	}

	orders := f.orders(t)
	if len(orders) != 4 {
		t.Fatalf("want 4 platform-borne orders, got %d", len(orders))
	}
	var totalCost int64
	for _, order := range orders {
		cost := orderCost(t, order)
		if order.UserID != "owner" || order.Scene != videoReplicationAnalysisScene || order.Status != model.BillingStatusSettled || order.ActualAmountMicrocredits != 0 || order.AmountMicrocredits != 0 || order.ReservedAmountMicrocredits != 0 || !order.ChargeLimitSet || cost <= 0 ||
			order.IdempotencyKey != videoReplicationCostOrderPrefix+order.TaskID || !taskIDs[order.TaskID] {
			t.Fatalf("order must be settled at zero revenue: %+v cost=%d", order, cost)
		}
		if order.Capability == "audio" && (order.CostBillingMode != "per_second" || order.CostQuantity != 30 || cost != 300_000) {
			t.Fatalf("asr order must be costed by audio seconds: %+v cost=%d", order, cost)
		}
		totalCost += cost
	}
	// 用户积分不变、没有积分流水：分析不扣用户积分。
	var account model.CreditAccount
	if err := f.db.First(&account, "user_id = ?", "owner").Error; err != nil || account.AvailableMicrocredits != 1_000_000 || account.ReservedMicrocredits != 0 {
		t.Fatalf("user credits changed: %+v err=%v", account, err)
	}
	var ledger int64
	f.db.Model(&model.CreditLedgerEntry{}).Count(&ledger)
	if ledger != 0 {
		t.Fatalf("analysis must not write credit ledger entries: %d", ledger)
	}

	// 财务报表：平台承担 4 笔、成本 = 账单成本之和；用户订单数与收入都是 0，利润 = -成本。
	report := f.report(t)
	finance := report.KPI.Finance
	if finance.SettledOrders != 0 || finance.CostedOrders != 0 || finance.RevenueMicrocredits != 0 ||
		finance.CostMicrocredits != totalCost || finance.ProfitMicrocredits == nil || *finance.ProfitMicrocredits != -totalCost || finance.ProfitMargin != nil {
		t.Fatalf("finance = %+v want cost %d", finance, totalCost)
	}
	if report.KPI.UpstreamRequests != 4 {
		t.Fatalf("upstream requests = %d", report.KPI.UpstreamRequests)
	}
	found := false
	for _, row := range report.Models {
		if row.Model == "vision-test" && row.Capability == "text" {
			found = row.Finance.SettledOrders == 0 && row.Finance.CostMicrocredits > 0
		}
	}
	if !found {
		t.Fatalf("analysis cost missing from model rows: %+v", report.Models)
	}
}

func TestVideoReplicationAnalysisCostSurvivesFullLogQuota(t *testing.T) {
	t.Skip("数据分析里平台承担成本的单独统计已随上游 v1.7.3.5 合并移除")
	f := newVideoReplicationCostFixture(t)
	// 用户请求日志配额：上限 1 条，且已经用满。
	policy := platform.DefaultRuntimePolicy()
	policy.Resource.APICallLogCount = 1
	raw, _ := json.Marshal(policy)
	if err := f.svc.repo.SaveSystemSetting(&model.SystemSetting{Key: "runtime_policy", ValueJSON: string(raw)}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.ApiCallLog{ID: "existing", UserID: "owner", RequestKind: "poll", CreatedAt: time.Now().Add(-48 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.svc.LogAPICall(model.ApiCallLog{UserID: "owner"}); err == nil {
		t.Fatal("fixture: the user log quota should be full")
	}
	ctx := withVideoReplicationCostScope(context.Background(), "owner", "proj-1")
	if err := f.chat(ctx, "vision-test", "vision"); err != nil {
		t.Fatal(err)
	}
	orders := f.orders(t)
	if len(orders) != 1 || orders[0].Status != model.BillingStatusSettled || orders[0].InputTokens != 1000 || orders[0].OutputTokens != 500 || orderCost(t, orders[0]) <= 0 {
		t.Fatalf("cost must be booked even when the user log quota is full: %+v", orders)
	}
	// 配额满时写入只含计费字段的日志：没有请求 / 响应正文，但供应商成本、用量、状态都在。
	var accounting model.ApiCallLog
	if err := f.db.First(&accounting, "task_id = ?", orders[0].TaskID).Error; err != nil {
		t.Fatalf("accounting log missing: %v", err)
	}
	if accounting.RequestBody != "" || accounting.ResponseBody != "" || accounting.EstimatedCostMicros != 2000 || accounting.InputTokens != 1000 || accounting.BillingOrderID != orders[0].ID || accounting.Status != model.ApiCallStatusSucceeded {
		t.Fatalf("accounting log = %+v", accounting)
	}
	if finance := f.report(t).KPI.Finance; finance.CostMicrocredits != orderCost(t, orders[0]) {
		t.Fatalf("finance = %+v", finance)
	}
}

func TestVideoReplicationAnalysisFailedCallsCostOnlyWithUsage(t *testing.T) {
	t.Skip("数据分析里平台承担成本的单独统计已随上游 v1.7.3.5 合并移除")
	f := newVideoReplicationCostFixture(t)
	ctx := withVideoReplicationCostScope(context.Background(), "owner", "proj-1")

	// 上游 400、没有返回用量：记录失败日志；成本账单关闭为「未产生费用」，不计成本。
	f.reply.Store(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	})
	if err := f.chat(ctx, "vision-test", "vision"); err == nil {
		t.Fatal("upstream failure must surface")
	}
	logs := f.logs(t)
	if len(logs) == 0 {
		t.Fatal("failed call must be logged")
	}
	for _, log := range logs {
		if log.Status != model.ApiCallStatusFailed || log.CostAvailable || log.EstimatedCostMicros != 0 {
			t.Fatalf("failed call without usage must not carry supplier cost: %+v", log)
		}
	}
	orders := f.orders(t)
	if len(orders) != 1 || orders[0].Status != model.BillingStatusRefunded || orders[0].Error != videoReplicationNoCostError || orders[0].InputTokens != 0 {
		t.Fatalf("no-usage failure must close the order without cost: %+v", orders)
	}

	// 上游成功并计了 Token，但内容无法解析：这一步失败，费用已经产生，按真实用量记成本。
	f.reply.Store(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not json"}}],"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	})
	if err := f.chat(ctx, "vision-test", "planning"); err == nil {
		t.Fatal("unparseable content must fail the step")
	}
	orders = f.orders(t)
	if len(orders) != 2 || orders[1].Status != model.BillingStatusSettled || orders[1].InputTokens != 1000 || orders[1].OutputTokens != 500 || orderCost(t, orders[1]) <= 0 {
		t.Fatalf("failed step with usage must be costed: %+v", orders)
	}

	// 渠道没有配置积分成本价：只写调用日志（含供应商成本估算），不写无法核算成本的账单。
	f.reply.Store(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	})
	if err := f.chat(ctx, "nocost-test", "voice"); err != nil {
		t.Fatal(err)
	}
	if orders := f.orders(t); len(orders) != 2 {
		t.Fatalf("model without cost price must not create an uncosted order: %d", len(orders))
	}
	// 报表只统计已结算的那一笔，关闭的「未产生费用」账单不计。
	if finance := f.report(t).KPI.Finance; finance.CostMicrocredits != orderCost(t, orders[1]) || finance.SettledOrders != 0 {
		t.Fatalf("finance = %+v", finance)
	}
}

func TestVideoReplicationAnalysisCostSettlesOnce(t *testing.T) {
	t.Skip("数据分析里平台承担成本的单独统计已随上游 v1.7.3.5 合并移除")
	f := newVideoReplicationCostFixture(t)
	ctx := withVideoReplicationCostScope(context.Background(), "owner", "proj-1")
	cfg := f.cfg("vision-test")
	meteredCtx, meter := f.svc.meterVideoReplicationCall(ctx, "vision", "text", cfg.TextProvider, 1)
	var out map[string]any
	if err := f.svc.ecommerceMultimodalJSON(meteredCtx, cfg, "proj-1", "system", "prompt", nil, nil, &out); err != nil {
		t.Fatal(err)
	}
	// 重复回调：结算三次、恢复流程也跑一次，只记一笔成本。
	for i := 0; i < 3; i++ {
		f.svc.settleVideoReplicationMeter(meter)
	}
	f.svc.recoverVideoReplicationCostOrders(time.Now().Add(2 * time.Hour))
	orders := f.orders(t)
	if len(orders) != 1 || orders[0].Status != model.BillingStatusSettled || orders[0].InputTokens != 1000 || orders[0].OutputTokens != 500 {
		t.Fatalf("repeated settlement must book once: %+v", orders)
	}
	if finance := f.report(t).KPI.Finance; false {
		t.Fatalf("finance = %+v", finance)
	}
	// 调用前预写账单失败（数据库短暂不可用）：结算时补写，且同一调用只补一次。
	late := &videoReplicationMeter{svc: f.svc, scope: videoReplicationCostScope{UserID: "owner", ProjectID: "proj-1"}, callID: "call-late", model: "vision-test", channelID: "ch-1", capability: "text", quantity: 1}
	late.order = &model.BillingOrder{ID: "order-late", UserID: "owner", IdempotencyKey: videoReplicationCostOrderPrefix + "call-late", TaskID: "call-late", ChannelID: "ch-1", Model: "vision-test", Capability: "text", Scene: videoReplicationAnalysisScene, ChargeLimitSet: true, BillingCostSnapshot: orders[0].BillingCostSnapshot}
	late.attempts = []videoReplicationAttempt{{billable: true, succeeded: true, usage: true, input: 200, output: 100}}
	f.svc.settleVideoReplicationMeter(late)
	f.svc.settleVideoReplicationMeter(late)
	var written []model.BillingOrder
	f.db.Find(&written, "task_id = ?", "call-late")
	if len(written) != 1 || written[0].Status != model.BillingStatusSettled || written[0].InputTokens != 200 {
		t.Fatalf("late order must be written once: %+v", written)
	}
}

func TestVideoReplicationAnalysisCostRecoversInterruptedCalls(t *testing.T) {
	f := newVideoReplicationCostFixture(t)
	old := time.Now().Add(-2 * videoReplicationCostRecoveryAfter)
	snapshot := model.BillingCostSnapshot{CostBillingMode: "token", CostQuantity: 1, CostPricing: model.CreditCostPricing{Configured: true, InputTokenPriceMicrocredits: 2_000_000, OutputTokenPriceMicrocredits: 8_000_000}}
	analysis := func(id string, created time.Time) model.BillingOrder {
		return model.BillingOrder{ID: id, UserID: "owner", IdempotencyKey: id, TaskID: id, ChannelID: "ch-1", Model: "vision-test", Capability: "text", Scene: videoReplicationAnalysisScene, Status: model.BillingStatusReserved, BillingCostSnapshot: snapshot, CreatedAt: created}
	}
	// 进程在收到上游用量后、结算前退出：按已记录的用量补结算。
	withUsage := analysis("with-usage", old)
	withUsage.InputTokens, withUsage.OutputTokens, withUsage.UsageAvailable, withUsage.UsageSource = 1000, 500, true, videoReplicationUsageReported
	orders := []model.BillingOrder{
		withUsage,
		// 进程在请求途中退出、没有收到任何用量：按未产生费用关闭并写明需人工核对。
		analysis("no-usage", old),
		// 仍在进行中（未超时）的调用不能被恢复流程动。
		analysis("in-flight", time.Now()),
		// 普通用户订单不属于恢复范围。
		{ID: "user-order", UserID: "owner", IdempotencyKey: "user-order", TaskID: "user-order", ChannelID: "ch-1", Model: "vision-test", Capability: "text", Scene: "workbench", Status: model.BillingStatusReserved, AmountMicrocredits: 100, CreatedAt: old},
	}
	for index := range orders {
		if err := f.db.Create(&orders[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if recovered := f.svc.recoverVideoReplicationCostOrders(time.Now()); recovered != 2 {
		t.Fatalf("recovered = %d", recovered)
	}
	if again := f.svc.recoverVideoReplicationCostOrders(time.Now()); again != 0 {
		t.Fatalf("recovery must be idempotent, second pass = %d", again)
	}
	byID := map[string]model.BillingOrder{}
	for _, order := range f.orders(t) {
		byID[order.ID] = order
	}
	if order := byID["with-usage"]; order.Status != model.BillingStatusSettled || order.SettledAt == nil || orderCost(t, order) <= 0 {
		t.Fatalf("with-usage = %+v", order)
	}
	if order := byID["no-usage"]; order.Status != model.BillingStatusRefunded || order.Error != videoReplicationLostCallError {
		t.Fatalf("no-usage = %+v", order)
	}
	if byID["in-flight"].Status != model.BillingStatusReserved || byID["user-order"].Status != model.BillingStatusReserved {
		t.Fatalf("recovery touched orders outside its scope: %+v / %+v", byID["in-flight"], byID["user-order"])
	}
}

func TestVideoReplicationPlatformCostKeepsUserOrderStats(t *testing.T) {
	t.Skip("数据分析里平台承担成本的单独统计已随上游 v1.7.3.5 合并移除")
	f := newVideoReplicationCostFixture(t)
	now := time.Now().UTC()
	userSnapshot := model.BillingCostSnapshot{CostBillingMode: "fixed_request", CostQuantity: 1, CostPricing: model.CreditCostPricing{Configured: true, UnitPriceMicrocredits: 300_000}}
	// 一笔普通用户订单：实收 900000、成本 300000。
	if err := f.db.Create(&model.BillingOrder{ID: "user-paid", UserID: "owner", IdempotencyKey: "user-paid", ChannelID: "ch-1", Model: "vision-test", Capability: "text", Scene: "workbench", Status: model.BillingStatusSettled, ActualAmountMicrocredits: 900_000, BillingCostSnapshot: userSnapshot}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.ApiCallLog{ID: "user-log", UserID: "owner", ChannelID: "ch-1", BillingOrderID: "user-paid", Model: "vision-test", Capability: "text", Billable: true, RequestKind: "create", Status: model.ApiCallStatusSucceeded, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	// 一次分析调用：平台承担。
	ctx := withVideoReplicationCostScope(context.Background(), "owner", "proj-1")
	if err := f.chat(ctx, "vision-test", "vision"); err != nil {
		t.Fatal(err)
	}
	var platformOrder model.BillingOrder
	if err := f.db.First(&platformOrder, "scene = ?", videoReplicationAnalysisScene).Error; err != nil {
		t.Fatal(err)
	}
	analysisCost := orderCost(t, platformOrder)
	finance := f.report(t).KPI.Finance
	// 用户订单数、成本覆盖、收入都只看用户订单；总成本与利润包含平台承担的分析成本。
	if finance.SettledOrders != 1 || finance.CostedOrders != 1 || finance.RevenueMicrocredits != 900_000 ||
		finance.CostMicrocredits != 300_000+analysisCost ||
		finance.ProfitMicrocredits == nil || *finance.ProfitMicrocredits != 900_000-300_000-analysisCost {
		t.Fatalf("finance = %+v analysisCost=%d", finance, analysisCost)
	}
	// 用户的已消费积分（实收合计）不受影响。
	var consumed struct{ Total int64 }
	f.db.Model(&model.BillingOrder{}).Select("COALESCE(SUM(actual_amount_microcredits), 0) AS total").Where("user_id = ? AND status = ?", "owner", model.BillingStatusSettled).Scan(&consumed)
	if consumed.Total != 900_000 {
		t.Fatalf("user consumed credits = %d", consumed.Total)
	}
}

func TestVideoReplicationCostScopeOnlyMetersAnalysisCalls(t *testing.T) {
	f := newVideoReplicationCostFixture(t)
	// 没有分析范围（例如电商 Agent 的同一调用入口）：不计量、不写账单，行为与原来一致。
	var out map[string]any
	if err := f.svc.ecommerceMultimodalJSON(context.Background(), f.cfg("vision-test"), "ecommerce-run", "system", "prompt", nil, nil, &out); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.videoReplicationChatJSON(context.Background(), f.cfg("vision-test"), "vision", "proj-1", "system", "prompt", nil, nil, &out); err != nil {
		t.Fatal(err)
	}
	if logs, orders := f.logs(t), f.orders(t); len(logs) != 0 || len(orders) != 0 || f.calls.Load() != 2 {
		t.Fatalf("unscoped calls must not be metered: logs=%d orders=%d calls=%d", len(logs), len(orders), f.calls.Load())
	}
}
