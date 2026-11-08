package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

// 商品视频复刻分析阶段的模型成本：用户不扣积分（平台承担），但每次真实模型调用都要记成本。三个金额分开记录：
//   - 供应商实际成本：api_call_logs.estimated_cost_micros，按渠道上游单价（model_pricings）估算，每次上游请求一条。
//   - 魅夜AI 内部成本价：billing_orders 的成本快照（cost_*），按渠道模型价格档的积分成本价与真实用量计算，财务报表据此统计。
//   - 用户积分收入：同一笔账单的实收金额，固定为 0，不扣用户积分、不写积分流水。
//
// 记账与用户日志解耦：调用前先写一笔「进行中」账单（status=reserved，调用 ID 唯一），Provider HTTP 层把每次请求的
// 用量直接交给计量器，计量器先把用量累加到账单上，再写调用日志；用户日志配额满时改写只含计费字段的日志，记账不受影响。
// 调用结束后把账单结算为 settled（产生了费用）或 refunded（没有用量的失败，不计成本），都是带状态条件的更新，重复回调只生效一次。
// 进程在调用中途退出时账单停在 reserved，由 recoverVideoReplicationCostOrders 按已记录的用量补结算。

const (
	videoReplicationAnalysisScene = "video_replication_analysis"
	// 分析调用按普通生成请求（create）记录：调用统计和模型维度的财务行只按 create 汇总。
	// 调用 ID 不对应任何任务，日志写入时对任务状态的更新不会命中记录。
	videoReplicationAnalysisRequestKind = "create"
	videoReplicationAnalysisSource      = "video-replication-analysis"
	videoReplicationCostOrderPrefix     = "video-replication-analysis:"
	// videoReplicationCostRecoveryAfter 必须大于单次分析的最长时间，避免把仍在进行的调用当成中断。
	videoReplicationCostRecoveryAfter = videoReplicationAnalysisTimeout + 10*time.Minute

	// 账单 usage_source：调用期间记录是否已产生费用，供中断恢复使用。
	videoReplicationUsageReported = "provider"          // 上游返回了用量
	videoReplicationUsageMissing  = "provider_no_usage" // 上游成功但没有返回用量
	videoReplicationNoCostError   = "上游请求失败且没有返回用量，未产生费用"
	videoReplicationLostCallError = "分析调用中断且没有收到上游用量：按未产生费用处理，如供应商账单有扣费请人工核对"
)

// billingOrderPlatformBorne 判断账单是否是平台承担成本、用户实收为 0 的内部成本账单。
func billingOrderPlatformBorne(order model.BillingOrder) bool {
	return order.Scene == videoReplicationAnalysisScene
}

type videoReplicationCostScopeKey struct{}

type videoReplicationCostScope struct {
	UserID    string
	ProjectID string
}

// withVideoReplicationCostScope 标记这次分析属于哪个用户和项目；只有带这个标记的调用才会计量。
func withVideoReplicationCostScope(ctx context.Context, userID, projectID string) context.Context {
	return context.WithValue(ctx, videoReplicationCostScopeKey{}, videoReplicationCostScope{UserID: userID, ProjectID: projectID})
}

type videoReplicationAttempt struct {
	billable, succeeded, usage bool
	input, output, cached      int64
	providerRequestID          string
}

func (a videoReplicationAttempt) incurred() bool {
	return a.billable && (a.succeeded || a.usage)
}

type videoReplicationMeter struct {
	svc        *Service
	scope      videoReplicationCostScope
	stage      string
	capability string
	channelID  string
	model      string
	// callID 是这次真实模型调用的唯一 ID：写在调用日志的 task_id 和账单的幂等键上。
	callID string
	// quantity 是按次 / 按秒计费时的计量单位数（ASR 为音频秒数），Token 计费以上游返回的用量为准。
	quantity int64
	// order 是这次调用的成本账单；渠道没有配置积分成本价时为空，只写调用日志（含供应商成本估算）。
	order *model.BillingOrder

	mu       sync.Mutex
	attempts []videoReplicationAttempt
}

// meterVideoReplicationCall 为一次分析调用挂上计量：先写「进行中」成本账单，再把调用日志与用量的记录接管过来。
// 没有计量范围或没有渠道时原样返回，不影响调用本身。
func (s *Service) meterVideoReplicationCall(ctx context.Context, stage, capability string, provider *providerConfig, quantity int64) (context.Context, *videoReplicationMeter) {
	scope, ok := ctx.Value(videoReplicationCostScopeKey{}).(videoReplicationCostScope)
	// 没有协调器时 Provider HTTP 层无法计量（只有手工构造的服务会这样），直接调用。
	if s == nil || s.coordinator == nil || !ok || provider == nil || strings.TrimSpace(provider.ChannelID) == "" {
		return ctx, nil
	}
	meter := &videoReplicationMeter{svc: s, scope: scope, stage: stage, capability: capability, channelID: provider.ChannelID, model: firstNonEmpty(provider.ChannelModelKey, provider.Model), callID: newID(), quantity: max(1, quantity)}
	meter.order = s.newVideoReplicationCostOrder(meter)
	metadata := providerAnalyticsContext{
		Service: s, UserID: scope.UserID, TaskID: meter.callID, TraceID: "video-replication:" + scope.ProjectID, RequestID: scope.ProjectID,
		Capability: capability, Operation: videoReplicationAnalysisScene + ":" + stage, ChannelID: meter.channelID, Model: meter.model,
		RequestKind: videoReplicationAnalysisRequestKind, CostRecorder: meter.record, ProxyURL: s.videoReplicationChannelProxy(provider.ChannelID),
	}
	if meter.order != nil {
		metadata.BillingOrderID = meter.order.ID
	}
	return context.WithValue(ctx, providerAnalyticsKey{}, metadata), meter
}

// newVideoReplicationCostOrder 在调用前写入「进行中」的成本账单。写入失败时仍返回账单模板，结算时补写；
// 渠道模型没有配置积分成本价时返回 nil：不写无法核算成本的账单，避免财务报表利润变成未知。
func (s *Service) newVideoReplicationCostOrder(meter *videoReplicationMeter) *model.BillingOrder {
	item, err := s.repo.ChannelModelByKeyIncludingDisabled(meter.channelID, meter.model)
	if err != nil {
		slog.Warn("video replication analysis cost: channel model not found, call log only", "callId", meter.callID, "channelId", meter.channelID, "model", meter.model, "error", err)
		return nil
	}
	tier, err := s.channelModelPriceTierForBilling(*item, "", meter.capability)
	if err != nil || tier == nil || !tier.CostPricing.Configured {
		slog.Info("video replication analysis cost: no credit cost price configured, call log only", "callId", meter.callID, "model", meter.model)
		return nil
	}
	now := time.Now()
	order := &model.BillingOrder{
		ID: newID(), UserID: meter.scope.UserID, IdempotencyKey: videoReplicationCostOrderPrefix + meter.callID, TaskID: meter.callID,
		ChannelID: meter.channelID, ChannelModelID: item.ID, PriceTierID: tier.ID, PriceTierVersion: tier.PriceVersion, PriceSelectorJSON: tier.SelectorJSON,
		Model: meter.model, Capability: meter.capability, Scene: videoReplicationAnalysisScene, BillingMode: tier.BillingMode, PriceVersion: item.PriceVersion,
		MultiplierBasisPoints: 10000, Quantity: meter.quantity,
		// 平台承担：用户实收、预留都是 0，并用「已授权的 0 上限」标明这是有意的零价订单。
		ChargeLimitSet: true, Status: model.BillingStatusReserved, StartedAt: &now,
	}
	snapshotCreditCost(order, tier, meter.quantity, tokenBillingEstimate{})
	if err := s.repo.Create(order); err != nil {
		slog.Warn("video replication analysis cost: pre-create billing order failed, will retry at settlement", "callId", meter.callID, "error", err)
	}
	return order
}

// record 由 Provider HTTP 层在每次上游请求结束时同步调用：先记账，再写日志。
func (meter *videoReplicationMeter) record(callLog model.ApiCallLog) {
	attempt := videoReplicationAttempt{
		billable: callLog.Billable, succeeded: callLog.Status == model.ApiCallStatusSucceeded, usage: callLog.UsageAvailable,
		input: callLog.InputTokens, output: callLog.OutputTokens, cached: callLog.CachedTokens, providerRequestID: callLog.ProviderRequestID,
	}
	meter.mu.Lock()
	meter.attempts = append(meter.attempts, attempt)
	meter.mu.Unlock()
	if meter.order != nil && attempt.incurred() {
		source := videoReplicationUsageMissing
		if attempt.usage {
			source = videoReplicationUsageReported
		}
		// 立即把用量写到进行中的账单上：进程随后退出时，恢复流程据此补结算。
		if err := meter.svc.repo.AccumulateVideoReplicationCostUsage(meter.order.ID, attempt.input, attempt.output, attempt.cached, attempt.usage, source, attempt.providerRequestID); err != nil {
			slog.Warn("video replication analysis cost: record usage failed, settlement will retry", "callId", meter.callID, "error", err)
		}
	}
	callLog.Source = videoReplicationAnalysisSource
	meter.svc.persistVideoReplicationCallLog(callLog)
}

// persistVideoReplicationCallLog 先按普通规则（含用户日志存储配额）写调用日志；被拒绝时去掉请求与响应正文，
// 只保留计费字段直接写入，保证供应商成本和调用状态始终可查。
func (s *Service) persistVideoReplicationCallLog(callLog model.ApiCallLog) {
	if callLog.ID == "" {
		callLog.ID = newID()
	}
	if callLog.CreatedAt.IsZero() {
		callLog.CreatedAt = time.Now()
	}
	err := s.LogAPICall(callLog)
	if err == nil {
		return
	}
	slog.Warn("video replication analysis: call log rejected, writing accounting-only log", "callId", callLog.TaskID, "error", err)
	if callLog.StartedAt.IsZero() {
		callLog.StartedAt = callLog.CreatedAt.Add(-time.Duration(callLog.DurationMs) * time.Millisecond)
	}
	s.estimateCallCost(&callLog)
	callLog.RequestBody, callLog.ResponseBody = "", ""
	if fallbackErr := s.repo.Create(&callLog); fallbackErr != nil {
		slog.Error("video replication analysis: accounting-only call log failed", "callId", callLog.TaskID, "error", fallbackErr)
	}
}

// settleVideoReplicationMeter 在调用结束后结算这次调用的成本账单；重复调用只生效一次。
func (s *Service) settleVideoReplicationMeter(meter *videoReplicationMeter) {
	if meter == nil || meter.order == nil {
		return
	}
	meter.mu.Lock()
	attempts := append([]videoReplicationAttempt(nil), meter.attempts...)
	meter.mu.Unlock()
	incurred, usage := false, false
	var input, output, cached int64
	providerRequestID := ""
	for _, attempt := range attempts {
		if !attempt.incurred() {
			continue
		}
		// 重试中每次真正被上游计费的请求都是真实成本，按次累加；没有用量的失败请求不计。
		incurred = true
		providerRequestID = firstNonEmpty(attempt.providerRequestID, providerRequestID)
		if attempt.usage {
			usage = true
			input, output, cached = input+attempt.input, output+attempt.output, cached+attempt.cached
		}
	}
	now := time.Now()
	var updates map[string]any
	if incurred {
		source := videoReplicationUsageMissing
		if usage {
			source = videoReplicationUsageReported
		}
		updates = map[string]any{"status": model.BillingStatusSettled, "input_tokens": input, "output_tokens": output, "cached_tokens": cached, "usage_available": usage, "usage_source": source, "provider_request_id": providerRequestID, "settled_at": now, "error": "", "updated_at": now}
	} else {
		updates = map[string]any{"status": model.BillingStatusRefunded, "error": videoReplicationNoCostError, "refunded_at": now, "updated_at": now}
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		err := s.finishVideoReplicationCostOrder(meter.order, updates)
		if err == nil {
			return
		}
		slog.Warn("video replication analysis cost: settlement failed", "callId", meter.callID, "attempt", attempt+1, "error", err)
	}
	// 三次都失败：账单停在进行中，由恢复流程按已累加的用量补结算。
}

// finishVideoReplicationCostOrder 把进行中的账单转到终态；账单不存在（调用前写入失败）时直接写终态账单。
func (s *Service) finishVideoReplicationCostOrder(template *model.BillingOrder, updates map[string]any) error {
	changed, err := s.repo.TransitionVideoReplicationCostOrder(template.ID, model.BillingStatusReserved, updates)
	if err != nil || changed {
		return err
	}
	existing, err := s.repo.BillingOrder(template.ID)
	if err == nil {
		// 已经结算过（重复回调、或恢复流程先到）：不再改动，保证只记一次。
		if existing.Status != model.BillingStatusReserved {
			return nil
		}
		return errors.New("billing order is still reserved after transition")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if updates["status"] != model.BillingStatusSettled {
		return nil
	}
	order := *template
	order.Status = model.BillingStatusSettled
	order.InputTokens, _ = updates["input_tokens"].(int64)
	order.OutputTokens, _ = updates["output_tokens"].(int64)
	order.CachedTokens, _ = updates["cached_tokens"].(int64)
	order.UsageAvailable, _ = updates["usage_available"].(bool)
	order.UsageSource, _ = updates["usage_source"].(string)
	order.ProviderRequestID, _ = updates["provider_request_id"].(string)
	settledAt := time.Now()
	order.SettledAt = &settledAt
	if err := s.repo.Create(&order); err != nil {
		// 幂等键 (user_id, idempotency_key) 唯一：并发补写时只有一笔成功。
		if again, lookupErr := s.repo.BillingOrder(template.ID); lookupErr == nil && again.Status != model.BillingStatusReserved {
			return nil
		}
		return err
	}
	return nil
}

// recoverVideoReplicationCostOrders 处理调用中途进程退出留下的进行中账单：已经记录到用量的按用量结算，
// 没有收到任何用量的按未产生费用关闭并写明原因，便于对照供应商账单核对。返回处理的笔数。
func (s *Service) recoverVideoReplicationCostOrders(now time.Time) int {
	orders, err := s.repo.StaleVideoReplicationCostOrders(videoReplicationAnalysisScene, now.Add(-videoReplicationCostRecoveryAfter), 200)
	if err != nil {
		slog.Warn("video replication analysis cost: recovery query failed", "error", err)
		return 0
	}
	recovered := 0
	for _, order := range orders {
		updates := map[string]any{"status": model.BillingStatusRefunded, "error": videoReplicationLostCallError, "refunded_at": now, "updated_at": now}
		if order.UsageSource != "" {
			updates = map[string]any{"status": model.BillingStatusSettled, "settled_at": now, "updated_at": now, "error": ""}
		}
		changed, err := s.repo.TransitionVideoReplicationCostOrder(order.ID, model.BillingStatusReserved, updates)
		if err != nil {
			slog.Warn("video replication analysis cost: recovery failed", "orderId", order.ID, "error", err)
			continue
		}
		if changed {
			recovered++
		}
	}
	if recovered > 0 {
		slog.Info("video replication analysis cost: recovered interrupted calls", "count", recovered)
	}
	return recovered
}

// startVideoReplicationCostRecovery 启动时与之后每小时处理一次中断调用留下的成本账单（只在运行后台任务的实例上）。
func (s *Service) startVideoReplicationCostRecovery(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			s.recoverVideoReplicationCostOrders(time.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// videoReplicationChatJSON 是分析阶段调用文本 / 多模态模型的唯一入口：计量后再调用。
func (s *Service) videoReplicationChatJSON(ctx context.Context, cfg ecommerceProviderConfig, stage, projectID, system, prompt string, images, videos []string, out any) error {
	ctx, meter := s.meterVideoReplicationCall(ctx, stage, "text", cfg.TextProvider, 1)
	err := s.ecommerceMultimodalJSON(ctx, cfg, projectID, system, prompt, images, videos, out)
	s.settleVideoReplicationMeter(meter)
	return err
}

// logSelfHostedVideoReplicationASR 记录服务器自建 whisper.cpp 的识别调用：没有供应商费用（不可计费），只记调用、时长与状态。
func (s *Service) logSelfHostedVideoReplicationASR(ctx context.Context, started time.Time, err error) {
	scope, ok := ctx.Value(videoReplicationCostScopeKey{}).(videoReplicationCostScope)
	if s == nil || !ok {
		return
	}
	status, message := model.ApiCallStatusSucceeded, ""
	if err != nil {
		status, message = model.ApiCallStatusFailed, clipText(err.Error(), 400)
	}
	s.persistVideoReplicationCallLog(model.ApiCallLog{
		UserID: scope.UserID, TaskID: newID(), TraceID: "video-replication:" + scope.ProjectID, RequestID: scope.ProjectID,
		Source: videoReplicationAnalysisSource, Capability: "audio", Operation: videoReplicationAnalysisScene + ":asr", RequestKind: videoReplicationAnalysisRequestKind,
		Billable: false, Method: "POST", Path: "/inference", Model: "whisper.cpp（自建）", Status: status, DurationMs: time.Since(started).Milliseconds(),
		Error: message, StartedAt: started,
	})
}
