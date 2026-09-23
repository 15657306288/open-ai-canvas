package doubao

// 账号池视频额度：按「站点 + 模型」的兜底限额（与前端 model-capabilities.ts 的
// accountPoolVideoLimits 对齐，同源于账号池限额表）：
//   - 豆包池账号：seedance 系列累计上限 5 条；
//   - Dola 池账号：seedance 系列每日上限 2 条（按自然日发放，跨天由
//     sweepDolaDailyQuota 清零重置并恢复因额度停用的账号）；
//   - 即梦池账号：seedance1.0 Fast 累计上限 33 秒（seedance1.5 Pro 不限条数）；
//   - 其余模型不限。
//
// 语义：生成成功才扣额度，失败不扣；额度用完自动停用该账号并切换到下一个可用账号。
// 管理员重新启用 / 重置额度 / 重新登录时会清零用量（视为额度已恢复）。

import (
	"fmt"
	"strings"

	"infinite-canvas/backend/internal/model"
)

// VideoQuota 账号的视频生成额度：MaxCount 按条数封顶，MaxSeconds 按累计秒数封顶；0 表示该维度不限。
type VideoQuota struct {
	MaxCount   int
	MaxSeconds int
}

// AccountVideoQuota 指定站点与模型下账号的兜底视频额度。
func AccountVideoQuota(site, model string) VideoQuota {
	value := strings.ToLower(model)
	switch NormalizeSite(site) {
	case SiteDoubao:
		if strings.Contains(value, "seedance") {
			return VideoQuota{MaxCount: 5}
		}
	case SiteDola:
		if strings.Contains(value, "seedance") {
			return VideoQuota{MaxCount: 2}
		}
	case SiteJimeng:
		if strings.Contains(value, "seedance1") && strings.Contains(value, "fast") {
			return VideoQuota{MaxSeconds: 33}
		}
	}
	return VideoQuota{}
}

// poolVideoQuota 取号时的池级额度（取号时尚无模型语境，按该站点对 seedance 的兜底限额判断）。
// 即梦的秒数限额只约束 Fast 档，但即梦不参与生成取号，池级按 33 秒判断即可。
func poolVideoQuota(site string) VideoQuota {
	switch NormalizeSite(site) {
	case SiteDoubao:
		return VideoQuota{MaxCount: 5}
	case SiteDola:
		return VideoQuota{MaxCount: 2}
	case SiteJimeng:
		return VideoQuota{MaxSeconds: 33}
	}
	return VideoQuota{}
}

// videoQuotaReached 额度是否已用完。
func videoQuotaReached(q VideoQuota, countUsed, secondsUsed int) bool {
	return (q.MaxCount > 0 && countUsed >= q.MaxCount) ||
		(q.MaxSeconds > 0 && secondsUsed >= q.MaxSeconds)
}

// videoQuotaExhausted 账号的池级视频额度是否已用完（取号资格判断）。
func videoQuotaExhausted(a *model.DoubaoAccount) bool {
	q := poolVideoQuota(a.Site)
	return videoQuotaReached(q, a.VideoCountUsed, a.VideoSecondsUsed)
}

// videoQuotaRemainingText 剩余额度展示文案（"剩 3 条" / "剩 21 秒" / "不限"）。
func videoQuotaRemainingText(a *model.DoubaoAccount) string {
	q := poolVideoQuota(a.Site)
	switch {
	case q.MaxCount > 0:
		remain := q.MaxCount - a.VideoCountUsed
		if remain < 0 {
			remain = 0
		}
		return fmt.Sprintf("剩 %d 条", remain)
	case q.MaxSeconds > 0:
		remain := q.MaxSeconds - a.VideoSecondsUsed
		if remain < 0 {
			remain = 0
		}
		return fmt.Sprintf("剩 %d 秒", remain)
	default:
		return "不限"
	}
}
