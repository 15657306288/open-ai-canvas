package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newConfirmationHarness 提供 internal + web 两套确认路由的内存库测试环境。
func newConfirmationHarness(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv(internalTokenEnv, testInternalToken)

	dbPath := filepath.Join(t.TempDir(), "confirmation.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?_txlock=immediate&_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.CreditAccount{}, &model.BillingOrder{},
		&model.CreditLedgerEntry{}, &model.SystemSetting{}, &model.ChannelModel{},
		&model.AgentConfirmation{}, &model.AuthSession{},
	); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	svc := service.New(repo, t.TempDir())

	router := gin.New()
	api := router.Group("/api")
	RegisterInternalRoutes(api, svc)
	RegisterAgentConfirmationRoutes(api, svc)
	return router, db
}

func seedReservedOrder(t *testing.T, db *gorm.DB, userID string, id string, amount int64) {
	t.Helper()
	order := model.BillingOrder{
		ID:                     id,
		UserID:                 userID,
		IdempotencyKey:         "idem-" + id,
		Scene:                  "mcp",
		Status:                 model.BillingStatusReserved,
		AmountMicrocredits:     amount,
		ReservedAmountMicrocredits: amount,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
}

func TestInternalConfirmationCreateAndPoll(t *testing.T) {
	router, db := newConfirmationHarness(t)
	internalUser(t, db, "u-conf", model.UserStatusActive, 1_000_000)
	seedReservedOrder(t, db, "u-conf", "ord-conf-1", 20_000)

	body := map[string]any{
		"userId": "u-conf", "orderId": "ord-conf-1", "tool": "canvas_generate_image",
		"modelKey": "nano-banana-2", "amountMicrocredits": 20_000,
		"promptSummary": "一只猫", "idempotencyKey": "idem-conf-1",
	}
	status, env := internalDo(t, router, http.MethodPost, "/api/internal/confirmations", body, testInternalToken)
	if status != 200 || env.Code != 0 {
		t.Fatalf("create: status=%d env=%+v", status, env)
	}
	m := rawJSON(t, env)
	id := m["id"].(string)
	if id == "" || m["status"] != "pending" || m["orderId"] != "ord-conf-1" || m["amountMicrocredits"].(float64) != 20_000 {
		t.Fatalf("bad create view: %+v", m)
	}

	// 幂等：同 user+idempotencyKey 返回同一记录
	status2, env2 := internalDo(t, router, http.MethodPost, "/api/internal/confirmations", body, testInternalToken)
	if status2 != 200 || rawJSON(t, env2)["id"].(string) != id {
		t.Fatalf("idempotency: status=%d env=%+v", status2, env2)
	}

	// 轮询：pending
	status3, env3 := internalDo(t, router, http.MethodGet, "/api/internal/confirmations/"+id, nil, testInternalToken)
	if status3 != 200 || rawJSON(t, env3)["status"] != "pending" {
		t.Fatalf("poll: status=%d env=%+v", status3, env3)
	}
}

func TestInternalConfirmationRejectsForeignOrder(t *testing.T) {
	router, db := newConfirmationHarness(t)
	internalUser(t, db, "u-a", model.UserStatusActive, 1_000_000)
	seedReservedOrder(t, db, "u-b", "ord-other", 20_000)

	body := map[string]any{
		"userId": "u-a", "orderId": "ord-other", "tool": "canvas_generate_video",
		"amountMicrocredits": 20_000, "idempotencyKey": "idem-x",
	}
	status, _ := internalDo(t, router, http.MethodPost, "/api/internal/confirmations", body, testInternalToken)
	if status != http.StatusNotFound {
		t.Fatalf("status=%d want 404（订单不属于该用户）", status)
	}
}

func TestConfirmationWebApproveReject(t *testing.T) {
	router, db := newConfirmationHarness(t)
	internalUser(t, db, "u-web-a", model.UserStatusActive, 1_000_000)
	internalUser(t, db, "u-web-b", model.UserStatusActive, 1_000_000)
	seedReservedOrder(t, db, "u-web-a", "ord-web-1", 20_000)
	cookieA := loginCookie(t, db, "u-web-a")
	cookieB := loginCookie(t, db, "u-web-b")

	// internal 创建
	body := map[string]any{
		"userId": "u-web-a", "orderId": "ord-web-1", "tool": "canvas_generate_image",
		"amountMicrocredits": 20_000, "idempotencyKey": "idem-web-1",
	}
	_, env := internalDo(t, router, http.MethodPost, "/api/internal/confirmations", body, testInternalToken)
	id := rawJSON(t, env)["id"].(string)

	// pending 列表只有 A 能看到
	status, envList := doWithCookie(t, router, http.MethodGet, "/api/agent-confirmations", nil, cookieA)
	if status != 200 {
		t.Fatalf("list: status=%d env=%+v", status, envList)
	}
	items := rawJSON(t, envList)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"].(string) != id {
		t.Fatalf("A pending list 异常: %+v", items)
	}
	_, envListB := doWithCookie(t, router, http.MethodGet, "/api/agent-confirmations", nil, cookieB)
	if len(rawJSON(t, envListB)["items"].([]any)) != 0 {
		t.Fatalf("B 不应看到 A 的确认: %+v", envListB)
	}

	// B 不能批准 A 的确认
	statusB, _ := doWithCookie(t, router, http.MethodPost, "/api/agent-confirmations/"+id+"/approve", nil, cookieB)
	if statusB != http.StatusNotFound {
		t.Fatalf("B approve: status=%d want 404", statusB)
	}

	// A 批准成功
	statusA, envApprove := doWithCookie(t, router, http.MethodPost, "/api/agent-confirmations/"+id+"/approve", nil, cookieA)
	if statusA != 200 || rawJSON(t, envApprove)["status"] != "approved" {
		t.Fatalf("A approve: status=%d env=%+v", statusA, envApprove)
	}

	// 重复批准幂等
	statusA2, envApprove2 := doWithCookie(t, router, http.MethodPost, "/api/agent-confirmations/"+id+"/approve", nil, cookieA)
	if statusA2 != 200 || rawJSON(t, envApprove2)["status"] != "approved" {
		t.Fatalf("A approve twice: status=%d env=%+v", statusA2, envApprove2)
	}

	// 已批准后不能拒绝（409）
	statusRej, _ := doWithCookie(t, router, http.MethodPost, "/api/agent-confirmations/"+id+"/reject", nil, cookieA)
	if statusRej != http.StatusConflict {
		t.Fatalf("reject after approve: status=%d want 409", statusRej)
	}

	// 网关轮询看到 approved
	statusPoll, envPoll := internalDo(t, router, http.MethodGet, "/api/internal/confirmations/"+id, nil, testInternalToken)
	if statusPoll != 200 || rawJSON(t, envPoll)["status"] != "approved" {
		t.Fatalf("poll after approve: status=%d env=%+v", statusPoll, envPoll)
	}
}

func TestConfirmationWebReject(t *testing.T) {
	router, db := newConfirmationHarness(t)
	internalUser(t, db, "u-rej", model.UserStatusActive, 1_000_000)
	seedReservedOrder(t, db, "u-rej", "ord-rej-1", 20_000)
	cookieA := loginCookie(t, db, "u-rej")

	body := map[string]any{
		"userId": "u-rej", "orderId": "ord-rej-1", "tool": "canvas_generate_video",
		"amountMicrocredits": 20_000, "idempotencyKey": "idem-rej-1",
	}
	_, env := internalDo(t, router, http.MethodPost, "/api/internal/confirmations", body, testInternalToken)
	id := rawJSON(t, env)["id"].(string)

	status, envRej := doWithCookie(t, router, http.MethodPost, "/api/agent-confirmations/"+id+"/reject", nil, cookieA)
	if status != 200 || rawJSON(t, envRej)["status"] != "rejected" {
		t.Fatalf("reject: status=%d env=%+v", status, envRej)
	}
	_, envPoll := internalDo(t, router, http.MethodGet, "/api/internal/confirmations/"+id, nil, testInternalToken)
	if rawJSON(t, envPoll)["status"] != "rejected" {
		t.Fatalf("poll after reject: %+v", envPoll)
	}
}

// --- 测试辅助 ---

func loginCookie(t *testing.T, db *gorm.DB, userID string) string {
	t.Helper()
	token := "test-token-" + userID
	sum := sha256.Sum256([]byte(token))
	session := model.AuthSession{
		ID:        "sess-" + userID,
		UserID:    userID,
		TokenHash: hex.EncodeToString(sum[:]),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	return session.ID + "." + token
}

func doWithCookie(t *testing.T, router *gin.Engine, method, path string, body any, cookie string) (int, internalEnvelope) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Cookie", service.SessionCookieName+"="+cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var env internalEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("bad envelope: %v body=%s", err, w.Body.String())
	}
	return w.Code, env
}
