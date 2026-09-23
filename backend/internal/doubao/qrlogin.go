package doubao

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// 扫码登录：驱动本机浏览器（Chrome/Edge）打开豆包，用户在窗口内扫码或账密登录，
// 后端轮询 cookie，捕获 sessionid 后连同全量 Cookie 一起写入账号池。
// 与参考实现（Playwright 弹窗登录）语义一致，只是浏览器内核换成 go-rod。

const (
	qrPollInterval = 1500 * time.Millisecond
	qrTimeout      = 5 * time.Minute
	cookieSettle   = 2500 * time.Millisecond // 等待 ttwid / csrf 等 cookie 落盘
	dolaSettle     = 20 * time.Second        // Dola 风控敏感：检测到登录态后给用户交互（发消息/过滑块）的时间
)

type QRLoginState string

const (
	QRIdle     QRLoginState = "idle"
	QRWaiting  QRLoginState = "waiting" // 浏览器已打开，等待扫码/登录
	QRSuccess  QRLoginState = "success"
	QRExpired  QRLoginState = "expired" // 超时未登录
	QRCanceled QRLoginState = "canceled"
	QRFailed   QRLoginState = "failed" // 启动浏览器等硬错误
)

// QRLoginView 登录会话的对外只读快照。
type QRLoginView struct {
	State       string `json:"state"`
	Message     string `json:"message"`
	AccountID   string `json:"accountId,omitempty"`
	Masked      string `json:"masked,omitempty"`
	HasBrowser  bool   `json:"hasBrowser"`
	ElapsedText string `json:"elapsedText"`
}

type qrSession struct {
	mu         sync.Mutex
	state      QRLoginState
	message    string
	accountID  string
	masked     string
	startedAt  time.Time
	hasBrowser bool
	cancel     context.CancelFunc
}

func (s *qrSession) view() QRLoginView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return QRLoginView{
		State:       string(s.state),
		Message:     s.message,
		AccountID:   s.accountID,
		Masked:      s.masked,
		HasBrowser:  s.hasBrowser,
		ElapsedText: formatElapsed(time.Since(s.startedAt)),
	}
}

func (s *qrSession) update(state QRLoginState, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
	if message != "" {
		s.message = message
	}
}

func formatElapsed(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%d 秒", seconds)
	}
	return fmt.Sprintf("%d 分 %d 秒", seconds/60, seconds%60)
}

// 站点登录配置：登录入口 URL 与 Cookie 作用域（即梦网页版同样以 sessionid 为登录态）。
type siteLoginConfig struct {
	loginURL string
	label    string
}

func siteLogin(site string) siteLoginConfig {
	switch NormalizeSite(site) {
	case SiteJimeng:
		return siteLoginConfig{loginURL: "https://jimeng.jianying.com", label: siteDisplayName(SiteJimeng)}
	case SiteDola:
		return siteLoginConfig{loginURL: "https://www.dola.com", label: siteDisplayName(SiteDola)}
	default:
		return siteLoginConfig{loginURL: "https://www.doubao.com", label: siteDisplayName(SiteDoubao)}
	}
}

// QRLoginManager 单站点单会话登录管理器：同一站点同一时间只允许一个登录窗口。
type QRLoginManager struct {
	mu      sync.Mutex
	site    string
	cfg     siteLoginConfig
	session *qrSession
	pool    *Service
}

func NewQRLoginManager(pool *Service, site string) *QRLoginManager {
	site = NormalizeSite(site)
	return &QRLoginManager{pool: pool, site: site, cfg: siteLogin(site)}
}

// Start 启动登录会话；已有进行中的会话时返回当前快照（前端据此复用轮询）。
func (m *QRLoginManager) Start() (QRLoginView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil && m.alive(m.session) {
		return m.session.view(), false
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &qrSession{state: QRWaiting, message: "正在启动浏览器…", startedAt: time.Now(), cancel: cancel}
	m.session = session
	go m.run(ctx, session)
	return session.view(), true
}

func (m *QRLoginManager) alive(session *qrSession) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	switch session.state {
	case QRSuccess, QRExpired, QRCanceled, QRFailed:
		return false
	}
	return true
}

// Status 返回当前会话快照；无会话时返回 idle。
func (m *QRLoginManager) Status() QRLoginView {
	m.mu.Lock()
	session := m.session
	m.mu.Unlock()
	if session == nil {
		return QRLoginView{State: string(QRIdle)}
	}
	return session.view()
}

// Cancel 取消进行中的登录并关闭浏览器窗口。
func (m *QRLoginManager) Cancel() error {
	m.mu.Lock()
	session := m.session
	m.mu.Unlock()
	if session == nil || !m.alive(session) {
		return fmt.Errorf("没有进行中的登录")
	}
	session.update(QRCanceled, "已取消登录")
	session.cancel()
	return nil
}

// browserCandidates 返回本机可能的浏览器可执行文件，按优先级排序。
func browserCandidates() []string {
	var candidates []string
	if custom := strings.TrimSpace(os.Getenv("DOUBAO_LOGIN_BROWSER")); custom != "" {
		candidates = append(candidates, custom)
	}
	programFiles := os.Getenv("ProgramFiles")
	programFilesX86 := os.Getenv("ProgramFiles(x86)")
	localAppData := os.Getenv("LOCALAPPDATA")
	for _, base := range []string{programFiles, programFilesX86} {
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
		)
	}
	// agent-browser 等工具下载的 Chrome：~/.agent-browser/browsers/chrome-*/chrome-win64/chrome.exe
	if localAppData != "" {
		pattern := filepath.Join(localAppData, ".agent-browser", "browsers", "chrome-*", "chrome-win64", "chrome.exe")
		if matches, err := filepath.Glob(pattern); err == nil {
			sort.Sort(sort.Reverse(sort.StringSlice(matches))) // 多版本取最新
			candidates = append(candidates, matches...)
		}
	}
	var existing []string
	seen := map[string]bool{}
	for _, path := range candidates {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			existing = append(existing, path)
		}
	}
	return existing
}

func (m *QRLoginManager) run(ctx context.Context, session *qrSession) {
	defer func() {
		if recovered := recover(); recovered != nil {
			session.update(QRFailed, fmt.Sprintf("登录流程异常退出：%v", recovered))
		}
	}()

	bin := ""
	if candidates := browserCandidates(); len(candidates) > 0 {
		bin = candidates[0]
	}
	if bin == "" {
		session.update(QRFailed, "本机未找到 Chrome / Edge，无法打开登录窗口。可安装 Chrome，或用「粘贴 Cookie」方式添加账号")
		return
	}

	// 每次登录用独立 profile，避免旧登录态干扰（与参考实现 switchAccount 语义一致）。
	profileDir := filepath.Join(os.TempDir(), fmt.Sprintf("yingce-%s-login", m.site), fmt.Sprintf("acct-%d", time.Now().UnixMilli()))

	launchOpts := launcher.New().
		Bin(bin).
		Headless(false).
		UserDataDir(profileDir).
		Set("--disable-blink-features", "AutomationControlled").
		Set("--window-size", "1280,900")

	ctx2, cancel := context.WithTimeout(ctx, qrTimeout+30*time.Second)
	defer cancel()

	controlURL, err := launchOpts.Launch()
	if err != nil {
		session.update(QRFailed, "启动浏览器失败："+err.Error())
		return
	}
	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		session.update(QRFailed, "连接浏览器失败："+err.Error())
		return
	}
	session.mu.Lock()
	session.hasBrowser = true
	session.mu.Unlock()
	defer func() {
		_ = browser.Close()
	}()

	go func() {
		<-ctx2.Done()
		_ = browser.Close()
	}()

	page, err := browser.Page(proto.TargetCreateTarget{URL: m.cfg.loginURL})
	if err != nil {
		session.update(QRFailed, "打开页面失败："+err.Error())
		return
	}
	if err := page.Navigate(m.cfg.loginURL); err != nil {
		session.update(QRFailed, fmt.Sprintf("打开%s首页失败：", m.cfg.label)+err.Error())
		return
	}
	// Dola：池内富 Cookie（含 ttwid 等指纹 Cookie）预注入后打开即是登录态，
	// 收割完整 Cookie 时会就地更新同账号；sessionid-only 薄 Cookie 不预注入——
	// 上游会把它当游客态（「游客模式暂不支持生成」），反而干扰用户在窗口里重新登录。
	if NormalizeSite(m.site) == SiteDola {
		if cred, err := m.pool.PickSite(SiteDola, ""); err == nil && cred != nil && HasRichCookieHeader(cred.CookieHeader) {
			params := make([]*proto.NetworkCookieParam, 0, 24)
			for _, part := range strings.Split(cred.CookieHeader, ";") {
				part = strings.TrimSpace(part)
				idx := strings.Index(part, "=")
				if idx <= 0 {
					continue
				}
				params = append(params, &proto.NetworkCookieParam{
					Name: part[:idx], Value: part[idx+1:], Domain: ".dola.com", Path: "/",
				})
			}
			if len(params) > 0 {
				_ = page.SetCookies(params)
				_ = page.Reload()
			}
		}
	}
	session.update(QRWaiting, fmt.Sprintf("请在弹出的浏览器窗口中扫码或登录%s，登录后会自动保存到账号池", m.cfg.label))

	deadline := time.Now().Add(qrTimeout)
	lastSID := ""
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return // canceled / 父级取消，状态已更新
		case <-time.After(qrPollInterval):
		}

		cookies, err := page.Cookies([]string{m.cfg.loginURL})
		if err != nil {
			continue
		}
		sid := ""
		for _, cookie := range cookies {
			if cookie.Name == "sessionid" && cookie.Value != "" {
				sid = cookie.Value
				break
			}
		}
		if sid == "" {
			continue
		}
		if sid == lastSID {
			continue
		}
		lastSID = sid
		// Dola 风控敏感：给用户留出发消息/过滑块的交互时间，让 ttwid 等指纹 Cookie 真正落盘。
		if NormalizeSite(m.site) == SiteDola {
			session.update(QRWaiting, "已检测到登录态，请在弹出的窗口中随意发一条消息（如遇滑块验证请完成），约 20 秒后自动保存完整 Cookie…")
			time.Sleep(dolaSettle)
		} else {
			session.update(QRWaiting, "已检测到登录态，正在收集完整 Cookie…")
		}
		time.Sleep(cookieSettle)

		full, err := page.Cookies([]string{m.cfg.loginURL})
		if err != nil || len(full) == 0 {
			full = cookies
		}
		cookieHeader := buildCookieHeaderFromRod(full)
		account, err := m.pool.Upsert(UpsertInput{
			Cookie:    cookieHeader,
			Site:      m.site,
			Source:    "qr",
			SetActive: true,
		})
		if err != nil {
			session.update(QRFailed, "登录成功但写入账号池失败："+err.Error())
			return
		}
		session.mu.Lock()
		session.state = QRSuccess
		session.message = fmt.Sprintf("登录成功，已写入账号池：%s（Cookie %d 条）", account.Masked, len(full))
		session.accountID = account.ID
		session.masked = account.Masked
		session.mu.Unlock()
		return
	}

	session.update(QRExpired, "登录超时：5 分钟内未检测到登录态，请重试")
}

// buildCookieHeaderFromRod 把 rod 返回的 cookie 列表拼成请求可用的 Cookie 头。
func buildCookieHeaderFromRod(cookies []*proto.NetworkCookie) string {
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie.Name == "" || cookie.Value == "" {
			continue
		}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}
