package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testInternalToken = "test-internal-token"

type internalEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
	Msg  string          `json:"msg"`
}

func newInternalHarness(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv(internalTokenEnv, testInternalToken)

	dbPath := filepath.Join(t.TempDir(), "internal.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?_txlock=immediate&_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.CreditAccount{}, &model.BillingOrder{},
		&model.CreditLedgerEntry{}, &model.SystemSetting{},
	); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	svc := service.New(repo, t.TempDir())

	router := gin.New()
	api := router.Group("/api")
	RegisterInternalRoutes(api, svc)
	return router, db
}

func internalUser(t *testing.T, db *gorm.DB, id string, status model.UserStatus, available int64) {
	t.Helper()
	if err := db.Create(&model.User{ID: id, Username: id, Role: model.UserRoleUser, Status: status}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CreditAccount{UserID: id, AvailableMicrocredits: available}).Error; err != nil {
		t.Fatal(err)
	}
}

func internalDo(t *testing.T, router *gin.Engine, method, path string, body any, token string) (int, internalEnvelope) {
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
	if token != "" {
		req.Header.Set(internalTokenHeader, token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var env internalEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, env
}

func rawJSON(t *testing.T, env internalEnvelope) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(env.Data, &m); err != nil {
		t.Fatalf("data not object: %s", string(env.Data))
	}
	return m
}

func TestInternalTokenAuthFailClosed(t *testing.T) {
	router, _ := newInternalHarness(t)
	cases := []struct{ name, token string }{
		{"missing", ""},
		{"wrong", "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, env := internalDo(t, router, http.MethodGet, "/api/internal/accounts/u1", nil, tc.token)
			if status != http.StatusUnauthorized || env.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d code=%d, want 401", status, env.Code)
			}
		})
	}
}

func TestInternalTokenUnconfiguredRejects(t *testing.T) {
	router, _ := newInternalHarness(t)
	t.Setenv(internalTokenEnv, "") // 服务端未配置 → 整体不可用
	status, _ := internalDo(t, router, http.MethodGet, "/api/internal/accounts/u1", nil, testInternalToken)
	if status != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", status)
	}
}

func TestInternalAccountView(t *testing.T) {
	router, db := newInternalHarness(t)
	internalUser(t, db, "u-active", model.UserStatusActive, 1_000_000)
	internalUser(t, db, "u-disabled", model.UserStatusDisabled, 1_000_000)

	status, env := internalDo(t, router, http.MethodGet, "/api/internal/accounts/u-active", nil, testInternalToken)
	if status != 200 || env.Code != 0 {
		t.Fatalf("active: status=%d env=%+v", status, env)
	}
	m := rawJSON(t, env)
	if m["userId"] != "u-active" || m["availableMicrocredits"].(float64) != 1_000_000 {
		t.Fatalf("bad account view: %+v", m)
	}
	for _, id := range []string{"u-disabled", "u-missing"} {
		status, _ = internalDo(t, router, http.MethodGet, "/api/internal/accounts/"+id, nil, testInternalToken)
		if status != http.StatusNotFound {
			t.Fatalf("%s status=%d want 404", id, status)
		}
	}
}

func reserveBody(amount int64, tool, idem string) map[string]any {
	return map[string]any{"amountMicrocredits": amount, "tool": tool, "scene": "mcp", "idempotencyKey": idem}
}

func TestInternalReserveValidationAndBalance(t *testing.T) {
	router, db := newInternalHarness(t)
	internalUser(t, db, "u1", model.UserStatusActive, 5000)

	bad := []struct {
		name string
		body any
	}{
		{"zero amount", reserveBody(0, "canvas_get_context", "k0")},
		{"negative amount", reserveBody(-1, "canvas_get_context", "k0")},
		{"float amount", map[string]any{"amountMicrocredits": 100.5, "tool": "t", "idempotencyKey": "k0"}},
		{"missing tool", map[string]any{"amountMicrocredits": 100, "idempotencyKey": "k0"}},
		{"missing idem", map[string]any{"amountMicrocredits": 100, "tool": "t"}},
		{"unknown field", map[string]any{"amountMicrocredits": 100, "tool": "t", "idempotencyKey": "k0", "x": 1}},
		{"empty body", nil},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := internalDo(t, router, http.MethodPost, "/api/internal/accounts/u1/reservations", tc.body, testInternalToken)
			if status != http.StatusBadRequest {
				t.Fatalf("%s status=%d want 400", tc.name, status)
			}
		})
	}

	// 余额不足 → 402
	status, _ := internalDo(t, router, http.MethodPost, "/api/internal/accounts/u1/reservations", reserveBody(99999, "canvas_get_context", "kpoor"), testInternalToken)
	if status != http.StatusPaymentRequired {
		t.Fatalf("insufficient: status=%d want 402", status)
	}

	// 正常冻结
	status, env := internalDo(t, router, http.MethodPost, "/api/internal/accounts/u1/reservations", reserveBody(1000, "canvas_get_context", "k1"), testInternalToken)
	if status != 200 || env.Code != 0 {
		t.Fatalf("reserve status=%d env=%+v", status, env)
	}
	view := rawJSON(t, env)
	if view["status"] != "reserved" || view["amountMicrocredits"].(float64) != 1000 {
		t.Fatalf("bad reserve view: %+v", view)
	}
}

func TestInternalReserveIdempotency(t *testing.T) {
	router, db := newInternalHarness(t)
	internalUser(t, db, "u1", model.UserStatusActive, 1_000_000)
	_, first := internalDo(t, router, http.MethodPost, "/api/internal/accounts/u1/reservations", reserveBody(1000, "canvas_get_context", "idem-1"), testInternalToken)
	orderID := rawJSON(t, first)["orderId"].(string)

	// 同参数重复 → 返回同一订单
	_, again := internalDo(t, router, http.MethodPost, "/api/internal/accounts/u1/reservations", reserveBody(1000, "canvas_get_context", "idem-1"), testInternalToken)
	if rawJSON(t, again)["orderId"] != orderID {
		t.Fatal("idempotent reserve should return same order")
	}
	// 同幂等键不同金额 → 409
	status, _ := internalDo(t, router, http.MethodPost, "/api/internal/accounts/u1/reservations", reserveBody(2000, "canvas_get_context", "idem-1"), testInternalToken)
	if status != http.StatusConflict {
		t.Fatalf("conflict status=%d want 409", status)
	}
}

func TestInternalSettleRefundLifecycle(t *testing.T) {
	router, db := newInternalHarness(t)
	internalUser(t, db, "u1", model.UserStatusActive, 1_000_000)

	reserve := func(idem string) string {
		t.Helper()
		_, env := internalDo(t, router, http.MethodPost, "/api/internal/accounts/u1/reservations", reserveBody(1000, "canvas_get_context", idem), testInternalToken)
		return rawJSON(t, env)["orderId"].(string)
	}
	settle := func(orderID string, idem string) (int, map[string]any) {
		s, e := internalDo(t, router, http.MethodPost, "/api/internal/accounts/u1/reservations/"+orderID+"/settle", map[string]any{"idempotencyKey": idem}, testInternalToken)
		var v map[string]any
		if len(e.Data) > 0 {
			v = rawJSON(t, e)
		}
		return s, v
	}
	refund := func(user, orderID string, body map[string]any) int {
		s, _ := internalDo(t, router, http.MethodPost, "/api/internal/accounts/"+user+"/reservations/"+orderID+"/refund", body, testInternalToken)
		return s
	}

	// reserve → settle → settled；重复 settle 幂等
	orderA := reserve("life-a")
	if s, v := settle(orderA, "life-a"); s != 200 || v["status"] != "settled" {
		t.Fatalf("settle s=%d v=%+v", s, v)
	}
	if s, v := settle(orderA, "life-a"); s != 200 || v["status"] != "settled" {
		t.Fatalf("idempotent settle s=%d v=%+v", s, v)
	}
	// settled 后 refund → 409
	if s := refund("u1", orderA, map[string]any{"idempotencyKey": "life-a", "error": "x"}); s != http.StatusConflict {
		t.Fatalf("settled→refund s=%d want 409", s)
	}

	// reserve → refund → refunded；refunded 后 settle → 409
	orderB := reserve("life-b")
	if s := refund("u1", orderB, map[string]any{"idempotencyKey": "life-b", "error": "failed"}); s != 200 {
		t.Fatalf("refund s=%d want 200", s)
	}
	if s, _ := settle(orderB, "life-b"); s != http.StatusConflict {
		t.Fatalf("refunded→settle s=%d want 409", s)
	}

	// 订单归属不符 → 404；幂等键不匹配 → 409
	orderC := reserve("life-c")
	if s := refund("u-other", orderC, map[string]any{"idempotencyKey": "life-c"}); s != http.StatusNotFound {
		t.Fatalf("ownership s=%d want 404", s)
	}
	if s := refund("u1", orderC, map[string]any{"idempotencyKey": "wrong"}); s != http.StatusConflict {
		t.Fatalf("idem mismatch s=%d want 409", s)
	}
}
