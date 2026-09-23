package model

import "time"

// DoubaoAccount 豆包账号池账号。
// Cookie 等敏感字段不序列化到 JSON，对外只暴露脱敏视图（见 doubao 域的 AccountView）。
type DoubaoAccount struct {
	ID string `gorm:"size:64;primaryKey" json:"id"`
	// Site 站点标识：doubao | dola | jimeng（同一账号池表承载多站点账号）。
	Site  string `gorm:"size:16;not null;default:'doubao';index:idx_doubao_accounts_site_session,priority:1" json:"site"`
	Label string `gorm:"size:120;not null;default:''" json:"label"`
	// SessionID 从 Cookie 提取的会话标识；唯一性按 (site, session_id) 组合保证
	// （历史全局唯一索引由 V22 迁移重建）。
	SessionID        string     `gorm:"size:512;not null;index:idx_doubao_accounts_site_session,priority:2,unique" json:"-"`
	CookieHeader     string     `gorm:"type:text" json:"-"`
	Source           string     `gorm:"size:32;not null;default:'manual'" json:"source"`
	Enabled          bool       `gorm:"not null;default:true" json:"enabled"`
	LoginExpired     bool       `gorm:"not null;default:false" json:"loginExpired"`
	CooldownUntil    *time.Time `json:"cooldownUntil"`
	QuotaExhaustedAt *time.Time `json:"quotaExhaustedAt"`
	LastError        string     `gorm:"size:512;not null;default:''" json:"lastError"`
	SuccessCount     int        `gorm:"not null;default:0" json:"successCount"`
	// ConsecutiveFailures 连续限流失败次数（指数退避冷却用，成功即清零，
	// 策略对齐 doubao2API 的 RATE_LIMIT_BASE_COOLDOWN 指数退避）。
	ConsecutiveFailures int `gorm:"not null;default:0" json:"consecutiveFailures"`
	// 视频额度用量：按账号池限额（豆包 5 条 / Dola 2 条 / 即梦 Fast 累计 33 秒）
	// 在生成成功后扣减，失败不扣；额度用完自动停用账号并切换下一个（见 doubao 包）。
	VideoCountUsed   int `gorm:"not null;default:0" json:"videoCountUsed"`
	VideoSecondsUsed int `gorm:"not null;default:0" json:"videoSecondsUsed"`
	// VideoQuotaDate 视频额度所属日期（本地时区 YYYY-MM-DD）。Dola 站点的 seedance
	// 额度按自然日发放（每日 2 条）：跨天后扫描清零用量并恢复「因额度用完自动停用」
	// 的账号（见 doubao.sweepDolaDailyQuota）；空表示历史数据，首次扫描即按今天重置。
	VideoQuotaDate string     `gorm:"size:10;not null;default:''" json:"videoQuotaDate"`
	UseCount       int        `gorm:"not null;default:0" json:"useCount"`
	FailCount      int        `gorm:"not null;default:0" json:"failCount"`
	LastUsedAt     *time.Time `json:"lastUsedAt"`
	// Tags 以逗号连接存储（单账号最多 8 个、每个 16 字符，见 doubao.NormalizeTags）。
	Tags string `gorm:"size:256;not null;default:''" json:"tags"`
	Note string `gorm:"size:256;not null;default:''" json:"note"`
	// ProxyID 绑定的出网代理（network_proxies.id，空 = 直连）。
	ProxyID   string    `gorm:"size:64;not null;default:''" json:"proxyId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (DoubaoAccount) TableName() string { return "doubao_accounts" }
