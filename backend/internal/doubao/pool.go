// Package doubao 实现豆包账号池与网页协议（samantha）生成客户端。
//
// 账号池语义移植自豆包创作工作台 src/account-store.js，调度策略对齐
// doubao2API（github.com/HongYan789/doubao2API）的账号池方案：
//   - 文生图 / 文生视频必须从账号池取号；
//   - 取号按「最少并发(inflight)优先 + 最久未用(LRU)打散」，单账号并发上限锁定，
//     同账号两次取号强制最小请求间隔 + 随机抖动（防风控）；
//   - 限流失败按指数退避冷却（600s 起步、连续失败翻倍、封顶 3600s），成功清零；
//   - 单账号额度耗尽 / 被风控时自动冷却并切换到下一个账号；
//   - 登录态失效（Cookie 过期）区别于风控限流，不进冷却，需重新登录；
//   - 只接受「扫码登录」或「手动粘贴 Cookie」来源，禁止 CDP 同步。
package doubao

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"infinite-canvas/backend/internal/netproxy"
	"log"
	gorand "math/rand"
	"strings"
	"sync"
	"time"
	"unicode"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

const (
	// CooldownDefaultMs 普通失败（风控/限流）的默认冷却时长（仅作兜底，
	// 限流实际走 rateLimitBackoffMs 指数退避）。
	CooldownDefaultMs int64 = 60 * 1000
	// CooldownQuotaMs 额度耗尽的冷却时长（30 分钟）。
	CooldownQuotaMs int64 = 30 * 60 * 1000
)

// doubao2API 式调度参数（策略对齐 HongYan789/doubao2API 的 AccountPool）。
const (
	// MaxInflightPerAccount 单账号最大并发生成数。豆包池并发跑同一账号会取到
	// 同一上游对象导致结果串扰，锁 1：并发任务在池内排队等空闲账号。
	MaxInflightPerAccount = 1
	// AccountMinIntervalMs 同账号两次取号的最小间隔（毫秒）。
	AccountMinIntervalMs int64 = 1200
	// AcquireJitterMinMs/AcquireJitterMaxMs 取号随机抖动区间（毫秒，防风控）。
	AcquireJitterMinMs int64 = 120
	AcquireJitterMaxMs int64 = 360
	// RateLimitBaseCooldownMs 限流指数退避基础冷却（10 分钟）。
	RateLimitBaseCooldownMs int64 = 600 * 1000
	// RateLimitMaxCooldownMs 限流冷却上限（1 小时）。
	RateLimitMaxCooldownMs int64 = 3600 * 1000
	// inflightPollInterval 全部账号并发占满时的轮询间隔。
	inflightPollInterval = 5 * time.Second
)

// 失败分类（与参考实现 detectBlock 的 kind 对齐）。
const (
	FailKindRateLimited    = "rate_limited"
	FailKindQuotaExhausted = "quota_exhausted"
	FailKindSessionExpired = "session_expired"
)

// 站点标识：同一张账号池表承载多站点账号（豆包 / Dola / 即梦）。
const (
	SiteDoubao = "doubao"
	SiteDola   = "dola"
	SiteJimeng = "jimeng"
)

// NormalizeSite 归一化站点标识；未知值回落到豆包（兼容历史数据）。
func NormalizeSite(site string) string {
	switch strings.ToLower(strings.TrimSpace(site)) {
	case SiteDola:
		return SiteDola
	case SiteJimeng:
		return SiteJimeng
	default:
		return SiteDoubao
	}
}

// siteDisplayName 站点显示名（默认账号命名用）。
func siteDisplayName(site string) string {
	switch site {
	case SiteDola:
		return "Dola"
	case SiteJimeng:
		return "即梦"
	default:
		return "豆包"
	}
}

// poolMetaID 活跃账号指针按站点分键（历史豆包池的 "pool" 由迁移复制为 "pool:doubao"）。
func poolMetaID(site string) string { return "pool:" + site }

// MarkFailedOptions 生成上游的三类失败，供取号方决定冷却策略。
type MarkFailedOptions struct {
	Kind       string // rate_limited | quota_exhausted | session_expired
	Message    string
	CooldownMs int64 // 覆盖默认冷却时长
}

// NormalizeSessionID 从整段 Cookie / 粘贴文本中提取真正的 sessionid。
func NormalizeSessionID(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	if len(text) >= 16 && len(text) <= 64 && isHex(text) {
		return text
	}
	lower := strings.ToLower(text)
	for _, key := range []string{"sessionid=", "sessionid_ss="} {
		if idx := strings.Index(lower, key); idx >= 0 {
			rest := text[idx+len(key):]
			if end := strings.IndexFunc(rest, func(r rune) bool { return r == ';' || unicode.IsSpace(r) }); end >= 0 {
				rest = rest[:end]
			}
			return strings.TrimSpace(rest)
		}
	}
	return text
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// BuildCookieHeader 把粘贴串整理成请求头。
func BuildCookieHeader(input string) string {
	text := strings.TrimSpace(input)
	if text == "" {
		return ""
	}
	if strings.Contains(text, "=") && (strings.Contains(text, ";") || strings.Contains(strings.ToLower(text), "sessionid=")) {
		return text
	}
	sid := NormalizeSessionID(text)
	if sid == "" {
		return ""
	}
	return fmt.Sprintf("sessionid=%s; sessionid_ss=%s", sid, sid)
}

// HasRichCookieHeader 是否具备较完整的浏览器 Cookie（仅 sessionid 极易被风控）。
func HasRichCookieHeader(cookieHeader string) bool {
	if cookieHeader == "" {
		return false
	}
	names := map[string]bool{}
	for _, part := range strings.Split(cookieHeader, ";") {
		part = strings.TrimSpace(part)
		if idx := strings.Index(part, "="); idx > 0 {
			names[strings.ToLower(part[:idx])] = true
		}
	}
	return len(names) >= 4 ||
		names["ttwid"] ||
		names["passport_csrf_token"] ||
		names["sid_guard"] ||
		names["odin_tt"]
}

// MaskSession 脱敏展示。
func MaskSession(sessionID string) string {
	id := NormalizeSessionID(sessionID)
	if id == "" || len(id) < 12 {
		return "(empty)"
	}
	return id[:6] + "..." + id[len(id)-4:]
}

// NormalizeTags 标签归一化：去空、去重、限长（最多 8 个、每个 16 字符）。
func NormalizeTags(input []string) []string {
	out := make([]string, 0, len(input))
	for _, raw := range input {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		if runes := []rune(t); len(runes) > 16 {
			t = string(runes[:16])
		}
		dup := false
		for _, existing := range out {
			if existing == t {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, t)
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func randomID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func formatDuration(ms int64) string {
	sec := ms / 1000
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%d小时%d分%02d秒", h, m, s)
	case m > 0:
		return fmt.Sprintf("%d分%02d秒", m, s)
	default:
		return fmt.Sprintf("%d秒", s)
	}
}

func splitTags(joined string) []string {
	if joined == "" {
		return []string{}
	}
	return NormalizeTags(strings.Split(joined, ","))
}

func joinTags(tags []string) string {
	return strings.Join(NormalizeTags(tags), ",")
}

// AccountView 账号的脱敏视图（列表/接口返回，绝不携带 Cookie）。
type AccountView struct {
	ID                    string     `json:"id"`
	Site                  string     `json:"site"`
	Label                 string     `json:"label"`
	Masked                string     `json:"masked"`
	Active                bool       `json:"active"`
	State                 string     `json:"state"` // ready | cooling | expired | disabled
	StatusText            string     `json:"statusText"`
	CooldownUntil         *time.Time `json:"cooldownUntil"`
	CooldownRemainingText string     `json:"cooldownRemainingText"`
	Enabled               bool       `json:"enabled"`
	LoginExpired          bool       `json:"loginExpired"`
	LastError             string     `json:"lastError"`
	HasFullCookie         bool       `json:"hasFullCookie"`
	SuccessCount          int        `json:"successCount"`
	QuotaExhaustedAt      *time.Time `json:"quotaExhaustedAt"`
	// 视频额度：videoQuotaText 为剩余额度文案（"剩 3 条" / "剩 21 秒" / "不限"）。
	VideoQuotaText   string     `json:"videoQuotaText"`
	VideoCountUsed   int        `json:"videoCountUsed"`
	VideoSecondsUsed int        `json:"videoSecondsUsed"`
	Source           string     `json:"source"`
	ProxyID          string     `json:"proxyId"`
	Tags             []string   `json:"tags"`
	Note             string     `json:"note"`
	UseCount         int        `json:"useCount"`
	FailCount        int        `json:"failCount"`
	LastUsedAt       *time.Time `json:"lastUsedAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// PoolStatus 池子总览。
type PoolStatus struct {
	Site           string        `json:"site"`
	AccountCount   int           `json:"accountCount"`
	AvailableCount int           `json:"availableCount"`
	CoolingCount   int           `json:"coolingCount"`
	ExpiredCount   int           `json:"expiredCount"`
	DisabledCount  int           `json:"disabledCount"`
	TotalSuccess   int           `json:"totalSuccess"`
	TotalFail      int           `json:"totalFail"`
	Tags           []string      `json:"tags"`
	Accounts       []AccountView `json:"accounts"`
	CheckedAt      string        `json:"checkedAt"`
}

// ActiveCredential 取号结果：生成客户端只需要这些。
type ActiveCredential struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Site         string `json:"site"`
	SessionID    string `json:"sessionId"`
	CookieHeader string `json:"cookieHeader"`
	// ProxyURL 账号绑定的出网代理地址（取号时解析，空 = 直连）。
	ProxyURL string `json:"proxyUrl"`
}

// Service 豆包账号池。所有写操作在互斥锁 + 事务内完成，
// 避免并发任务互相覆盖冷却标记（对应参考实现的 createLock）。
type Service struct {
	db *gorm.DB
	mu sync.Mutex
	// inflight / nextAvail 是进程内调度状态（doubao2API 语义）：
	// inflight 记录各账号正在执行的生成任务数；nextAvail 记录同账号
	// 下一次允许取号的最早时间（最小间隔 + 抖动）。重启后自然归零。
	inflight  map[string]int
	nextAvail map[string]time.Time
}

// NewService 创建账号池服务。
func NewService(db *gorm.DB) *Service {
	return &Service{db: db, inflight: map[string]int{}, nextAvail: map[string]time.Time{}}
}

func (s *Service) usable(a *model.DoubaoAccount, now time.Time) bool {
	// 视频额度用完的账号不再参与取号（含被自动停用的账号）。
	if !a.Enabled || a.LoginExpired || videoQuotaExhausted(a) {
		return false
	}
	return !(a.CooldownUntil != nil && a.CooldownUntil.After(now))
}

func accountState(a *model.DoubaoAccount, now time.Time) string {
	if !a.Enabled {
		return "disabled"
	}
	if a.LoginExpired {
		return "expired"
	}
	if a.CooldownUntil != nil && a.CooldownUntil.After(now) {
		return "cooling"
	}
	return "ready"
}

func (s *Service) activeIDTx(tx *gorm.DB, site string) (string, error) {
	var meta model.DoubaoPoolMeta
	if err := tx.Where("id = ?", poolMetaID(site)).First(&meta).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	return meta.ActiveID, nil
}

func (s *Service) setActiveIDTx(tx *gorm.DB, site string, id string) error {
	var meta model.DoubaoPoolMeta
	err := tx.Where("id = ?", poolMetaID(site)).First(&meta).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&model.DoubaoPoolMeta{ID: poolMetaID(site), ActiveID: id, UpdatedAt: time.Now()}).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&meta).Updates(map[string]any{"active_id": id, "updated_at": time.Now()}).Error
}

func (s *Service) view(a *model.DoubaoAccount, active string, now time.Time) AccountView {
	state := accountState(a, now)
	remaining := ""
	status := "可用"
	switch state {
	case "disabled":
		// 被额度扣完自动停用的账号要和手动停用区分开，提示如何恢复。
		if a.QuotaExhaustedAt != nil {
			status = "视频额度已用完 · 已自动停用"
		} else {
			status = "已停用"
		}
	case "expired":
		status = "登录态失效 · 请重新登录"
	case "cooling":
		remaining = formatDuration(a.CooldownUntil.Sub(now).Milliseconds())
		status = "冷却中 · 剩余 " + remaining
	}
	return AccountView{
		ID:                    a.ID,
		Site:                  NormalizeSite(a.Site),
		Label:                 a.Label,
		Masked:                MaskSession(a.SessionID),
		Active:                a.ID == active,
		State:                 state,
		StatusText:            status,
		CooldownUntil:         a.CooldownUntil,
		CooldownRemainingText: remaining,
		Enabled:               a.Enabled,
		LoginExpired:          a.LoginExpired,
		LastError:             a.LastError,
		HasFullCookie:         HasRichCookieHeader(a.CookieHeader),
		ProxyID:               a.ProxyID,
		SuccessCount:          a.SuccessCount,
		QuotaExhaustedAt:      a.QuotaExhaustedAt,
		VideoQuotaText:        videoQuotaRemainingText(a),
		VideoCountUsed:        a.VideoCountUsed,
		VideoSecondsUsed:      a.VideoSecondsUsed,
		Source:                a.Source,
		Tags:                  splitTags(a.Tags),
		Note:                  a.Note,
		UseCount:              a.UseCount,
		FailCount:             a.FailCount,
		LastUsedAt:            a.LastUsedAt,
		UpdatedAt:             a.UpdatedAt,
	}
}

// sweepExpiredCooldowns 清除已到期冷却。返回被清理的账号数。
func (s *Service) sweepExpiredCooldowns() (int64, error) {
	now := time.Now()
	res := s.db.Model(&model.DoubaoAccount{}).
		Where("cooldown_until IS NOT NULL AND cooldown_until <= ?", now).
		Updates(map[string]any{"cooldown_until": nil, "last_error": "", "quota_exhausted_at": nil})
	return res.RowsAffected, res.Error
}

// sweepDolaDailyQuota Dola 站点 seedance 额度按自然日重置（每日 2 条）：
// 额度日期不是今天的账号清零用量；其中「因额度用完自动停用」的账号跨天自动恢复
// （手动停用 enabled=false 且无 quota_exhausted_at 的账号保持停用，不越权启用）。
// 豆包 / 即梦站点的额度语义不变（累计制，管理员重置才恢复）。调用方需持有 s.mu。
func (s *Service) sweepDolaDailyQuota() (int64, error) {
	today := time.Now().Format("2006-01-02")
	stale := s.db.Model(&model.DoubaoAccount{}).
		Where("site = ? AND (video_quota_date IS NULL OR video_quota_date = '' OR video_quota_date <> ?)", SiteDola, today)
	// 先恢复被额度停用的账号（只恢复自动停用的那批），再统一清零用量并刷新日期。
	if err := stale.Where("enabled = ? AND quota_exhausted_at IS NOT NULL", false).
		Updates(map[string]any{"enabled": true, "last_error": ""}).Error; err != nil {
		return 0, err
	}
	res := s.db.Model(&model.DoubaoAccount{}).
		Where("site = ? AND (video_quota_date IS NULL OR video_quota_date = '' OR video_quota_date <> ?)", SiteDola, today).
		Updates(map[string]any{
			"video_count_used":   0,
			"video_seconds_used": 0,
			"quota_exhausted_at": nil,
			"video_quota_date":   today,
		})
	return res.RowsAffected, res.Error
}

func (s *Service) loadAccounts(site string) ([]model.DoubaoAccount, string, error) {
	var accounts []model.DoubaoAccount
	if err := s.db.Where("site = ?", site).Order("created_at ASC").Find(&accounts).Error; err != nil {
		return nil, "", err
	}
	active, err := s.activeIDTx(s.db, site)
	if err != nil {
		return nil, "", err
	}
	found := false
	for _, a := range accounts {
		if a.ID == active {
			found = true
			break
		}
	}
	if !found && len(accounts) > 0 {
		active = accounts[0].ID
	}
	if len(accounts) == 0 {
		active = ""
	}
	return accounts, active, nil
}

// Status 池子总览（列表 + 统计，按站点过滤），先清理到期冷却。
func (s *Service) Status(site string) (*PoolStatus, error) {
	site = NormalizeSite(site)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.sweepExpiredCooldowns(); err != nil {
		return nil, err
	}
	// Dola 额度按自然日发放：展示前先做跨天重置，避免「剩 0 条」的过期文案。
	if _, err := s.sweepDolaDailyQuota(); err != nil {
		return nil, err
	}
	accounts, active, err := s.loadAccounts(site)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	status := &PoolStatus{Site: site, AccountCount: len(accounts), Tags: []string{}, Accounts: []AccountView{}, CheckedAt: now.Format(time.RFC3339)}
	tagSet := map[string]bool{}
	for i := range accounts {
		a := &accounts[i]
		view := s.view(a, active, now)
		status.Accounts = append(status.Accounts, view)
		switch view.State {
		case "ready":
			status.AvailableCount++
		case "cooling":
			status.CoolingCount++
		case "expired":
			status.ExpiredCount++
		case "disabled":
			status.DisabledCount++
		}
		status.TotalSuccess += a.SuccessCount
		status.TotalFail += a.FailCount
		for _, t := range view.Tags {
			tagSet[t] = true
		}
	}
	for t := range tagSet {
		status.Tags = append(status.Tags, t)
	}
	return status, nil
}

// UpsertInput 新增/更新账号的入参。
type UpsertInput struct {
	Cookie    string // 纯 sessionid 或整段 Cookie
	Site      string // doubao | dola | jimeng（空按豆包处理）
	Label     string
	Source    string // manual | qr
	Tags      []string
	Note      string
	SetActive bool
}

// Upsert 新增或更新账号（同一 sessionid 视为更新）。
// 重新登录/刷新 Cookie 后清除冷却与失效标记。
func (s *Service) Upsert(input UpsertInput) (*AccountView, error) {
	source := input.Source
	if source == "" {
		source = "manual"
	}
	if source == "cdp" {
		return nil, errors.New("禁止使用调试浏览器（CDP）中已登录的账号，请改用扫码登录或手动粘贴 Cookie")
	}
	raw := strings.TrimSpace(input.Cookie)
	if raw == "" {
		return nil, errors.New("sessionId 无效")
	}
	sessionID := NormalizeSessionID(raw)
	if sessionID == "" {
		return nil, errors.New("sessionId 无效")
	}
	cookieHeader := BuildCookieHeader(raw)
	site := NormalizeSite(input.Site)

	s.mu.Lock()
	defer s.mu.Unlock()
	var view *AccountView
	err := s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		var existing model.DoubaoAccount
		err := tx.Where("site = ? AND session_id = ?", site, sessionID).First(&existing).Error
		switch {
		case err == nil:
			existing.CookieHeader = cookieHeader
			existing.Source = source
			existing.UpdatedAt = now
			if input.Label != "" {
				existing.Label = input.Label
			}
			existing.CooldownUntil = nil
			existing.LastError = ""
			existing.LoginExpired = false
			existing.ConsecutiveFailures = 0
			existing.Enabled = true
			// 重新登录/重新导入视为额度已恢复：清零视频额度用量。
			resetQuotaFieldsTx(&existing)
			if len(input.Tags) > 0 {
				existing.Tags = joinTags(input.Tags)
			}
			if input.Note != "" {
				existing.Note = input.Note
			}
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
			if input.SetActive {
				if err := s.setActiveIDTx(tx, site, existing.ID); err != nil {
					return err
				}
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			count := int64(0)
			if err := tx.Model(&model.DoubaoAccount{}).Where("site = ?", site).Count(&count).Error; err != nil {
				return err
			}
			existing = model.DoubaoAccount{
				ID:           randomID(),
				Site:         site,
				Label:        input.Label,
				SessionID:    sessionID,
				CookieHeader: cookieHeader,
				Source:       source,
				Enabled:      true,
				Tags:         joinTags(input.Tags),
				Note:         input.Note,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if existing.Label == "" {
				existing.Label = fmt.Sprintf("%s账号 %d", siteDisplayName(site), count+1)
			}
			if err := tx.Create(&existing).Error; err != nil {
				return err
			}
			if input.SetActive || count == 0 {
				if err := s.setActiveIDTx(tx, site, existing.ID); err != nil {
					return err
				}
			}
		default:
			return err
		}
		active, err := s.activeIDTx(tx, site)
		if err != nil {
			return err
		}
		v := s.view(&existing, active, now)
		view = &v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// BulkImportInput 批量导入入参：多行文本，`#` 注释，`|` 追加标签与备注。
type BulkImportInput struct {
	Text      string   `json:"text"`
	Site      string   `json:"site"`
	Tags      []string `json:"tags"`
	SetActive bool     `json:"setActive"`
}

// BulkImportResult 批量导入结果。
type BulkImportResult struct {
	Added   int               `json:"added"`
	Updated int               `json:"updated"`
	Failed  []BulkImportError `json:"failed"`
	Total   int               `json:"total"`
}

// BulkImportError 单行导入失败。
type BulkImportError struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// BulkImport 批量导入账号。
func (s *Service) BulkImport(input BulkImportInput) (*BulkImportResult, error) {
	site := NormalizeSite(input.Site)
	baseTags := NormalizeTags(input.Tags)
	result := &BulkImportResult{Failed: []BulkImportError{}}
	for i, line := range strings.Split(strings.ReplaceAll(input.Text, "\r\n", "\n"), "\n") {
		lineNo := i + 1
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		cred := strings.TrimSpace(parts[0])
		tagPart, notePart := "", ""
		if len(parts) > 1 {
			tagPart = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			notePart = strings.TrimSpace(parts[2])
		}
		lower := strings.ToLower(cred)
		// 豆包认 sessionid；Dola/即梦等站点允许没有 sessionid 的整段 Cookie。
		looksLikeSession := strings.Contains(lower, "sessionid") && strings.Contains(lower, "=") ||
			(len(cred) >= 16 && len(cred) <= 128 && isHex(cred)) ||
			(strings.Contains(cred, "=") && strings.Contains(cred, ";"))
		if !looksLikeSession {
			result.Failed = append(result.Failed, BulkImportError{Line: lineNo, Reason: "未识别到凭据（需含 sessionid=，或形如 k=v; k2=v2 的整段 Cookie）"})
			continue
		}
		sid := NormalizeSessionID(cred)
		if sid == "" {
			result.Failed = append(result.Failed, BulkImportError{Line: lineNo, Reason: "未识别到 sessionid"})
			continue
		}
		tags := append(append([]string{}, baseTags...), NormalizeTags(splitTagText(tagPart))...)
		existed := s.existsSession(site, sid)
		if _, err := s.Upsert(UpsertInput{Cookie: cred, Site: site, Source: "manual", Tags: tags, Note: notePart, SetActive: input.SetActive}); err != nil {
			result.Failed = append(result.Failed, BulkImportError{Line: lineNo, Reason: err.Error()})
			continue
		}
		if existed {
			result.Updated++
		} else {
			result.Added++
		}
	}
	result.Total = result.Added + result.Updated
	return result, nil
}

func splitTagText(text string) []string {
	if text == "" {
		return nil
	}
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == '，' || r == ' ' || r == '\t'
	})
	return fields
}

func (s *Service) existsSession(site, sessionID string) bool {
	var count int64
	s.db.Model(&model.DoubaoAccount{}).Where("site = ? AND session_id = ?", site, sessionID).Count(&count)
	return count > 0
}

// UpdatePatch 编辑账号的可管理字段。
type UpdatePatch struct {
	Label   *string   `json:"label"`
	Note    *string   `json:"note"`
	Tags    *[]string `json:"tags"`
	Enabled *bool     `json:"enabled"`
}

// Update 编辑账号字段。
func (s *Service) Update(id string, patch UpdatePatch) (*AccountView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var view *AccountView
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var a model.DoubaoAccount
		if err := tx.Where("id = ?", id).First(&a).Error; err != nil {
			return errors.New("账号不存在")
		}
		if patch.Label != nil {
			label := strings.TrimSpace(*patch.Label)
			if label != "" {
				a.Label = label
			}
		}
		if patch.Note != nil {
			note := *patch.Note
			if runes := []rune(note); len(runes) > 120 {
				note = string(runes[:120])
			}
			a.Note = note
		}
		if patch.Tags != nil {
			a.Tags = joinTags(*patch.Tags)
		}
		if patch.Enabled != nil {
			a.Enabled = *patch.Enabled
			// 启用即恢复额度（与批量启用语义一致）。
			if a.Enabled {
				a.CooldownUntil = nil
				a.LastError = ""
				a.LoginExpired = false
				resetQuotaFieldsTx(&a)
			}
		}
		a.UpdatedAt = time.Now()
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		active, err := s.activeIDTx(tx, NormalizeSite(a.Site))
		if err != nil {
			return err
		}
		v := s.view(&a, active, time.Now())
		view = &v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// Remove 删除账号（活跃指针后继在同站点内顺延）。
func (s *Service) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var victim model.DoubaoAccount
		if err := tx.Where("id = ?", id).First(&victim).Error; err != nil {
			return errors.New("账号不存在")
		}
		site := NormalizeSite(victim.Site)
		if err := tx.Where("id = ?", id).Delete(&model.DoubaoAccount{}).Error; err != nil {
			return err
		}
		delete(s.inflight, id)
		delete(s.nextAvail, id)
		active, err := s.activeIDTx(tx, site)
		if err != nil {
			return err
		}
		if active == id {
			var next model.DoubaoAccount
			if err := tx.Where("site = ?", site).Order("created_at ASC").First(&next).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return s.setActiveIDTx(tx, site, "")
				}
				return err
			}
			return s.setActiveIDTx(tx, site, next.ID)
		}
		return nil
	})
}

// BatchOp 批量操作：activate | remove | clear-cooldown | enable | disable | tag。
func (s *Service) BatchOp(action string, site string, ids []string, tags []string) (int, error) {
	if len(ids) == 0 {
		return 0, errors.New("未选择任何账号")
	}
	site = NormalizeSite(site)
	hit := map[string]bool{}
	for _, id := range ids {
		hit[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	affected := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var accounts []model.DoubaoAccount
		if err := tx.Where("site = ?", site).Order("created_at ASC").Find(&accounts).Error; err != nil {
			return err
		}
		now := time.Now()
		switch action {
		case "activate":
			for i := range accounts {
				if hit[accounts[i].ID] {
					a := &accounts[i]
					a.CooldownUntil = nil
					a.LastError = ""
					a.LoginExpired = false
					a.Enabled = true
					resetQuotaFieldsTx(a)
					a.UpdatedAt = now
					if err := tx.Save(a).Error; err != nil {
						return err
					}
					if err := s.setActiveIDTx(tx, site, a.ID); err != nil {
						return err
					}
					affected = 1
					break
				}
			}
			if affected == 0 {
				return errors.New("账号不存在")
			}
		case "remove":
			for i := range accounts {
				if hit[accounts[i].ID] {
					if err := tx.Delete(&accounts[i]).Error; err != nil {
						return err
					}
					delete(s.inflight, accounts[i].ID)
					delete(s.nextAvail, accounts[i].ID)
					affected++
				}
			}
			active, err := s.activeIDTx(tx, site)
			if err != nil {
				return err
			}
			if active != "" && !hit[active] {
				return nil
			}
			var next model.DoubaoAccount
			if err := tx.Where("site = ?", site).Order("created_at ASC").First(&next).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return s.setActiveIDTx(tx, site, "")
				}
				return err
			}
			return s.setActiveIDTx(tx, site, next.ID)
		case "clear-cooldown":
			for i := range accounts {
				if !hit[accounts[i].ID] {
					continue
				}
				a := &accounts[i]
				a.CooldownUntil = nil
				a.LastError = ""
				a.LoginExpired = false
				a.ConsecutiveFailures = 0
				resetQuotaFieldsTx(a)
				a.UpdatedAt = now
				if err := tx.Save(a).Error; err != nil {
					return err
				}
				affected++
			}
		case "enable", "disable", "reset-quota":
			// reset-quota = 启用 + 清零视频额度（额度用完被自动停用的账号由此恢复）。
			for i := range accounts {
				if !hit[accounts[i].ID] {
					continue
				}
				a := &accounts[i]
				switch action {
				case "enable", "reset-quota":
					// 启用即恢复额度：否则额度用完的账号启用后仍不可取号，令人困惑。
					a.Enabled = true
					a.CooldownUntil = nil
					a.LastError = ""
					a.LoginExpired = false
					a.ConsecutiveFailures = 0
					resetQuotaFieldsTx(a)
				default:
					a.Enabled = false
				}
				a.UpdatedAt = now
				if err := tx.Save(a).Error; err != nil {
					return err
				}
				affected++
			}
		case "tag":
			merged := NormalizeTags(tags)
			if len(merged) == 0 {
				return errors.New("未提供标签")
			}
			for i := range accounts {
				if !hit[accounts[i].ID] {
					continue
				}
				a := &accounts[i]
				a.Tags = joinTags(append(splitTags(a.Tags), merged...))
				a.UpdatedAt = now
				if err := tx.Save(a).Error; err != nil {
					return err
				}
				affected++
			}
		default:
			return fmt.Errorf("不支持的操作：%s", action)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// generateSites 生成链路取号的站点顺序：优先豆包，豆包不可用自动落到 Dola
// （两站点共用同一套 samantha 协议，仅域名不同；即梦协议不同不参与）。
var generateSites = []string{SiteDoubao, SiteDola}

// Pick 取一个生成可用账号：按 generateSites 顺序跨站点取号。
// 豆包账号停用/失效时自动改用 Dola 账号生成。
func (s *Service) Pick(preferID string) (*ActiveCredential, error) {
	var errs []string
	for _, site := range generateSites {
		cred, err := s.PickSite(site, preferID)
		if err == nil {
			return cred, nil
		}
		errs = append(errs, siteDisplayName(site)+"："+err.Error())
	}
	return nil, errors.New(strings.Join(errs, "；"))
}

// PickSite 取指定站点的可用账号：优先指定 prefer、其次当前活跃、再次任意可用。
// 成功取号会记录 useCount/lastUsedAt 并设为该站点的活跃账号。
func (s *Service) PickSite(site string, preferID string) (*ActiveCredential, error) {
	site = NormalizeSite(site)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.sweepExpiredCooldowns(); err != nil {
		return nil, err
	}
	// Dola 额度按自然日发放：取号前先做跨天重置，昨天的用量不计入今天。
	if _, err := s.sweepDolaDailyQuota(); err != nil {
		return nil, err
	}
	accounts, active, err := s.loadAccounts(site)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var chosen *model.DoubaoAccount
	if preferID != "" {
		for i := range accounts {
			if accounts[i].ID == preferID && s.usable(&accounts[i], now) {
				chosen = &accounts[i]
				break
			}
		}
	}
	if chosen == nil {
		for i := range accounts {
			if accounts[i].ID == active && s.usable(&accounts[i], now) {
				chosen = &accounts[i]
				break
			}
		}
	}
	if chosen == nil {
		for i := range accounts {
			if s.usable(&accounts[i], now) {
				chosen = &accounts[i]
				break
			}
		}
	}
	if chosen == nil {
		return nil, s.pickUnavailableError(accounts, now)
	}
	now = time.Now()
	chosen.UseCount++
	chosen.LastUsedAt = &now
	if err := s.db.Save(chosen).Error; err != nil {
		return nil, err
	}
	if chosen.ID != active {
		if err := s.db.Transaction(func(tx *gorm.DB) error { return s.setActiveIDTx(tx, site, chosen.ID) }); err != nil {
			return nil, err
		}
	}
	return &ActiveCredential{ID: chosen.ID, Label: chosen.Label, Site: chosen.Site, SessionID: chosen.SessionID, CookieHeader: chosen.CookieHeader, ProxyURL: netproxy.LookupURL(s.db, chosen.ProxyID)}, nil
}

// acquireJitter 取号抖动时长（防同刻并发请求，doubao2API 的 REQUEST_JITTER）。
func acquireJitter() time.Duration {
	span := AcquireJitterMaxMs - AcquireJitterMinMs + 1
	return time.Duration(AcquireJitterMinMs+gorand.Int63n(span)) * time.Millisecond
}

// rateLimitBackoffMs 限流指数退避：600s 起步、连续失败翻倍、封顶 3600s
// （doubao2API 的 RATE_LIMIT_BASE_COOLDOWN / RATE_LIMIT_MAX_COOLDOWN）。
func rateLimitBackoffMs(consecutive int) int64 {
	ms := RateLimitBaseCooldownMs
	for i := 1; i < consecutive && ms < RateLimitMaxCooldownMs; i++ {
		ms *= 2
	}
	if ms > RateLimitMaxCooldownMs {
		ms = RateLimitMaxCooldownMs
	}
	return ms
}

// Acquire 以 doubao2API 的池化策略取号：单账号并发上限内、最少并发优先、
// 最久未用(LRU)打散、同账号最小请求间隔 + 随机抖动。全部账号占满或间隔未到时
// 等待重试（尊重 ctx 取消）。取到的账号必须配对 Release 释放并发名额。
func (s *Service) Acquire(ctx context.Context, site, preferID string) (*ActiveCredential, error) {
	site = NormalizeSite(site)
	for {
		cred, wait, err := s.acquireOnce(site, preferID)
		if err != nil {
			return nil, err
		}
		if wait <= 0 {
			return cred, nil
		}
		log.Printf("[doubao] 账号池全忙或同账号间隔未到，%s 后重试取号（site=%s）", wait.Round(time.Second), site)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// acquireOnce 单轮取号：返回 wait>0 表示本轮无可立即取用的账号，需等待后重试。
func (s *Service) acquireOnce(site, preferID string) (*ActiveCredential, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.sweepExpiredCooldowns(); err != nil {
		return nil, 0, err
	}
	// Dola 额度按自然日发放：取号前先做跨天重置，昨天的用量不计入今天。
	if _, err := s.sweepDolaDailyQuota(); err != nil {
		return nil, 0, err
	}
	accounts, active, err := s.loadAccounts(site)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now()
	var ready, spaced []*model.DoubaoAccount
	var minWait time.Duration
	for i := range accounts {
		a := &accounts[i]
		if !s.usable(a, now) {
			continue
		}
		if s.inflight[a.ID] >= MaxInflightPerAccount {
			continue // 并发名额占满：等任务结束释放
		}
		wait := time.Duration(0)
		if until := s.nextAvail[a.ID]; until.After(now) {
			wait = until.Sub(now)
		}
		if wait > 0 {
			spaced = append(spaced, a)
			if minWait == 0 || wait < minWait {
				minWait = wait
			}
			continue
		}
		ready = append(ready, a)
	}
	if len(ready) == 0 {
		// 间隔未到：等最小间隔 + 抖动后重试（抖动避免并发取号惊群）。
		if len(spaced) > 0 {
			return nil, minWait + acquireJitter(), nil
		}
		// 有账号可用但并发名额被占满：短轮询等名额释放。
		for i := range accounts {
			if s.usable(&accounts[i], now) {
				return nil, inflightPollInterval, nil
			}
		}
		return nil, 0, s.pickUnavailableError(accounts, now)
	}
	chosen := chooseReadyAccount(ready, active, preferID)
	now = time.Now()
	chosen.UseCount++
	chosen.LastUsedAt = &now
	if err := s.db.Save(chosen).Error; err != nil {
		return nil, 0, err
	}
	if chosen.ID != active {
		if err := s.db.Transaction(func(tx *gorm.DB) error { return s.setActiveIDTx(tx, site, chosen.ID) }); err != nil {
			return nil, 0, err
		}
	}
	s.inflight[chosen.ID]++
	s.nextAvail[chosen.ID] = now.Add(time.Duration(AccountMinIntervalMs)*time.Millisecond + acquireJitter())
	return &ActiveCredential{ID: chosen.ID, Label: chosen.Label, Site: chosen.Site, SessionID: chosen.SessionID, CookieHeader: chosen.CookieHeader, ProxyURL: netproxy.LookupURL(s.db, chosen.ProxyID)}, 0, nil
}

// chooseReadyAccount 就绪账号选择：显式指定优先 → 当前活跃 → LRU（最久未用优先，
// 打散使用，贴近 doubao2API 的「inflight 最少优先」轮询语义）。
func chooseReadyAccount(ready []*model.DoubaoAccount, active, preferID string) *model.DoubaoAccount {
	for _, a := range ready {
		if preferID != "" && a.ID == preferID {
			return a
		}
	}
	for _, a := range ready {
		if a.ID == active {
			return a
		}
	}
	var chosen *model.DoubaoAccount
	for _, a := range ready {
		if chosen == nil || lastUsedBefore(a, chosen) {
			chosen = a
		}
	}
	return chosen
}

func lastUsedBefore(a, b *model.DoubaoAccount) bool {
	switch {
	case a.LastUsedAt == nil && b.LastUsedAt == nil:
		return a.CreatedAt.Before(b.CreatedAt)
	case a.LastUsedAt == nil:
		return true
	case b.LastUsedAt == nil:
		return false
	default:
		return a.LastUsedAt.Before(*b.LastUsedAt)
	}
}

// Release 释放账号的并发生成名额（与 Acquire 配对；重复调用安全）。
func (s *Service) Release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[id] > 0 {
		s.inflight[id]--
	}
}

// Inflight 查询账号当前并发任务数（测试/观测用）。
func (s *Service) Inflight(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight[id]
}

// pickUnavailableError 区分「池子为空」与「有账号但全部不可用」，
// 避免账号明明在池里却提示用户去添加账号。
func (s *Service) pickUnavailableError(accounts []model.DoubaoAccount, now time.Time) error {
	if len(accounts) == 0 {
		return errors.New("账号池中没有可用账号：请先在账号池添加账号（扫码登录或粘贴 Cookie）")
	}
	disabled, expired, quotaDone := 0, 0, 0
	shortest := time.Duration(0)
	for i := range accounts {
		a := &accounts[i]
		switch {
		case !a.Enabled:
			disabled++
			if a.QuotaExhaustedAt != nil || videoQuotaExhausted(a) {
				quotaDone++
			}
		case a.LoginExpired:
			expired++
		case a.CooldownUntil != nil && a.CooldownUntil.After(now):
			remain := a.CooldownUntil.Sub(now)
			if shortest == 0 || remain < shortest {
				shortest = remain
			}
		}
	}
	if disabled == len(accounts) {
		if quotaDone == disabled {
			return errors.New("账号池中的账号视频额度均已用完（已自动停用），请在账号池「重置额度」或添加新账号")
		}
		return errors.New("账号池中的账号均已停用，请先启用账号")
	}
	if expired+disabled == len(accounts) {
		return errors.New("账号池中的账号登录态均已失效，请重新扫码登录")
	}
	if shortest > 0 {
		return fmt.Errorf("账号池中的账号均在冷却中（最快 %s 后恢复），可稍后重试或点「解除冷却」", formatDuration(shortest.Milliseconds()))
	}
	return errors.New("账号池中没有可用账号，请检查账号状态")
}

// ShortestCooldown 返回冷却中账号的最短剩余等待时长；没有账号在冷却时返回 false。
func (s *Service) ShortestCooldown() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var accounts []model.DoubaoAccount
	if err := s.db.Where("site IN ? AND enabled = ? AND login_expired = ? AND cooldown_until IS NOT NULL", generateSites, true, false).Find(&accounts).Error; err != nil {
		return 0, false
	}
	now := time.Now()
	shortest := time.Duration(0)
	for i := range accounts {
		a := &accounts[i]
		if a.CooldownUntil == nil || !a.CooldownUntil.After(now) {
			continue
		}
		remain := a.CooldownUntil.Sub(now)
		if shortest == 0 || remain < shortest {
			shortest = remain
		}
	}
	return shortest, shortest > 0
}

// MarkSuccess 记录一次成功，并清除冷却/失效标记。
func (s *Service) MarkSuccess(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var a model.DoubaoAccount
		if err := tx.Where("id = ?", id).First(&a).Error; err != nil {
			return errors.New("账号不存在")
		}
		a.SuccessCount++
		a.UseCount++
		a.CooldownUntil = nil
		a.LastError = ""
		a.QuotaExhaustedAt = nil
		a.LoginExpired = false
		a.ConsecutiveFailures = 0
		now := time.Now()
		a.LastUsedAt = &now
		a.UpdatedAt = now
		return tx.Save(&a).Error
	})
}

// MarkVideoSuccess 记录一次视频生成成功：清除冷却/失效标记，并按账号站点的
// 兜底限额扣减视频额度（条数或累计秒数）。失败路径不走这里，不扣额度。
// 扣减后额度用完时自动停用该账号，并把活跃指针切到同站点下一个可用账号。
// 返回 exhausted（额度是否用完）、switched（是否完成切换）与切换到的账号视图。
func (s *Service) MarkVideoSuccess(id string, modelName string, durationSeconds int) (exhausted, switched bool, next *AccountView, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var a model.DoubaoAccount
		if e := tx.Where("id = ?", id).First(&a).Error; e != nil {
			return errors.New("账号不存在")
		}
		now := time.Now()
		a.SuccessCount++
		a.UseCount++
		a.CooldownUntil = nil
		a.LastError = ""
		a.QuotaExhaustedAt = nil
		a.LoginExpired = false
		a.ConsecutiveFailures = 0
		a.LastUsedAt = &now
		a.UpdatedAt = now
		// 额度归属日期随本次成功刷新：Dola 站每日额度从这次成功起算今天。
		a.VideoQuotaDate = now.Format("2006-01-02")
		quota := AccountVideoQuota(a.Site, modelName)
		if quota.MaxCount > 0 {
			a.VideoCountUsed++
		}
		if quota.MaxSeconds > 0 {
			if durationSeconds <= 0 {
				durationSeconds = 1
			}
			a.VideoSecondsUsed += durationSeconds
		}
		if videoQuotaReached(quota, a.VideoCountUsed, a.VideoSecondsUsed) {
			exhausted = true
			a.Enabled = false
			a.LastError = "视频额度已用完，已自动停用"
			a.QuotaExhaustedAt = &now
		}
		if e := tx.Save(&a).Error; e != nil {
			return e
		}
		if !exhausted {
			return nil
		}
		// 额度用完：切换到同站点的下一个可用账号（沿用 MarkFailed 的顺位语义）。
		site := NormalizeSite(a.Site)
		var accounts []model.DoubaoAccount
		if e := tx.Where("site = ?", site).Order("created_at ASC").Find(&accounts).Error; e != nil {
			return e
		}
		for i := range accounts {
			cand := &accounts[i]
			if cand.ID != id && s.usable(cand, now) {
				if e := s.setActiveIDTx(tx, site, cand.ID); e != nil {
					return e
				}
				switched = true
				active, e := s.activeIDTx(tx, site)
				if e != nil {
					return e
				}
				v := s.view(cand, active, now)
				next = &v
				break
			}
		}
		return nil
	})
	return exhausted, switched, next, err
}

// resetQuotaFieldsTx 清零视频额度用量并解除额度停用标记（重新启用/重置额度时视为额度已恢复）。
func resetQuotaFieldsTx(a *model.DoubaoAccount) {
	a.VideoCountUsed = 0
	a.VideoSecondsUsed = 0
	a.QuotaExhaustedAt = nil
}

// MarkFailed 标记失败：风控进短冷却、额度耗尽进长冷却、登录失效只置位不冷却；
// 并自动切换到下一个可用账号。
func (s *Service) MarkFailed(id string, opts MarkFailedOptions) (switched bool, next *AccountView, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var a model.DoubaoAccount
		if e := tx.Where("id = ?", id).First(&a).Error; e != nil {
			return errors.New("账号不存在")
		}
		now := time.Now()
		a.LastError = truncate(opts.Message, 300)
		a.FailCount++
		a.LastUsedAt = &now
		a.UpdatedAt = now
		if opts.Kind == FailKindSessionExpired {
			a.LoginExpired = true
		} else {
			cooldownMs := CooldownDefaultMs
			if opts.Kind == FailKindQuotaExhausted {
				cooldownMs = CooldownQuotaMs
				a.QuotaExhaustedAt = &now
			}
			if opts.Kind == FailKindRateLimited {
				// 限流走指数退避：连续失败翻倍（600s→1200s→…封顶 3600s），
				// 成功即清零；避免 60s 短冷却后反复撞风控（doubao2API 策略）。
				a.ConsecutiveFailures++
				cooldownMs = rateLimitBackoffMs(a.ConsecutiveFailures)
			}
			if opts.CooldownMs > 0 {
				cooldownMs = opts.CooldownMs // 手动冷却保持显式时长
			}
			until := now.Add(time.Duration(cooldownMs) * time.Millisecond)
			a.CooldownUntil = &until
		}
		if e := tx.Save(&a).Error; e != nil {
			return e
		}
		// 自动切换到同站点的下一个可用账号
		site := NormalizeSite(a.Site)
		var accounts []model.DoubaoAccount
		if e := tx.Where("site = ?", site).Order("created_at ASC").Find(&accounts).Error; e != nil {
			return e
		}
		for i := range accounts {
			cand := &accounts[i]
			if cand.ID != id && s.usable(cand, now) {
				if e := s.setActiveIDTx(tx, site, cand.ID); e != nil {
					return e
				}
				switched = true
				active, e := s.activeIDTx(tx, site)
				if e != nil {
					return e
				}
				v := s.view(cand, active, now)
				next = &v
				break
			}
		}
		return nil
	})
	return switched, next, err
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// RefreshCookieHeader 回存账号的最新 Cookie。Dola 浏览器会话在真实浏览器里使用后
// 上游会轮换 sessionid（sessionid_ss 机制），浏览器侧收割到的最新 Cookie 必须
// 回写账号池，否则下个任务拿着旧值会被上游降级成「游客模式」。
func (s *Service) RefreshCookieHeader(id, cookieHeader string) error {
	header := strings.TrimSpace(cookieHeader)
	if header == "" {
		return nil
	}
	sessionID := NormalizeSessionID(header)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var a model.DoubaoAccount
		if err := tx.Where("id = ?", id).First(&a).Error; err != nil {
			return errors.New("账号不存在")
		}
		updates := map[string]any{
			"cookie_header": header,
			"login_expired": false,
			"last_error":    "",
		}
		if sessionID != "" {
			updates["session_id"] = sessionID
		}
		return tx.Model(&model.DoubaoAccount{}).Where("id = ?", id).Updates(updates).Error
	})
}

// ClearAllCooldowns 清除全部账号的冷却/失效标记。
func (s *Service) ClearAllCooldowns() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var accounts []model.DoubaoAccount
	if err := s.db.Where("cooldown_until IS NOT NULL OR login_expired = ? OR last_error <> ''", true).Find(&accounts).Error; err != nil {
		return 0, err
	}
	n := 0
	now := time.Now()
	for i := range accounts {
		a := &accounts[i]
		a.CooldownUntil = nil
		a.LastError = ""
		resetQuotaFieldsTx(a)
		a.LoginExpired = false
		a.ConsecutiveFailures = 0
		a.UpdatedAt = now
		if err := s.db.Save(a).Error; err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
