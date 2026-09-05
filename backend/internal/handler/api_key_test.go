package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newApiKeyHarness 提供网站 API Key 管理 + internal verify 的内存库测试环境。
func newApiKeyHarness(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv(internalTokenEnv, testInternalToken)

	dbPath := filepath.Join(t.TempDir(), "apikey.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?_txlock=immediate&_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.ApiKey{}, &model.AuthSession{},
		&model.CreditAccount{}, &model.BillingOrder{}, &model.CreditLedgerEntry{},
		&model.SystemSetting{}, &model.ChannelModel{},
	); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	svc := service.New(repo, t.TempDir())

	router := gin.New()
	api := router.Group("/api")
	RegisterApiKeyRoutes(api, svc)
	RegisterInternalApiKeyRoutes(api, svc)
	return router, db
}

// 复用 agent_confirmation_test.go 的 loginCookie：构造 session cookie。

func TestApiKeyCreateAndVerify(t *testing.T) {
	router, db := newApiKeyHarness(t)
	internalUser(t, db, "u-key", model.UserStatusActive, 1_000_000)
	cookie := loginCookie(t, db, "u-key")

	// 用户签发 key
	req := httptest.NewRequest(http.MethodPost, "/api/api-keys", strings.NewReader(`{"name":"测试Key"}`))
	req.Header.Set("Cookie", "open_ai_canvas_session="+cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var create struct {
		Code int `json:"code"`
		Data struct {
			ID     string `json:"id"`
			Key    string `json:"key"`
			Prefix string `json:"prefix"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &create); err != nil {
		t.Fatal(err)
	}
	if create.Code != 0 || !strings.HasPrefix(create.Data.Key, "ak_") || create.Data.ID == "" {
		t.Fatalf("bad create: %+v", create)
	}

	// internal verify：明文 key → 拿到真实 userId
	status, env := internalDo(t, router, http.MethodPost, "/api/internal/api-keys/verify", map[string]string{"key": create.Data.Key}, testInternalToken)
	if status != 200 || env.Code != 0 {
		t.Fatalf("verify: status=%d env=%+v", status, env)
	}
	m := rawJSON(t, env)
	if m["userId"] != "u-key" || m["enabled"] != true || m["keyId"] != create.Data.ID {
		t.Fatalf("bad verify result: %+v", m)
	}

	// 列表（不含明文）
	req2 := httptest.NewRequest(http.MethodGet, "/api/api-keys", nil)
	req2.Header.Set("Cookie", "open_ai_canvas_session="+cookie)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != 200 || !strings.Contains(w2.Body.String(), create.Data.ID) || strings.Contains(w2.Body.String(), create.Data.Key) {
		t.Fatalf("list should show id not plaintext: %d %s", w2.Code, w2.Body.String())
	}
}

func TestApiKeyVerifyRejectsUnknownAndDeleted(t *testing.T) {
	router, db := newApiKeyHarness(t)
	internalUser(t, db, "u-key2", model.UserStatusActive, 1_000_000)
	cookie := loginCookie(t, db, "u-key2")

	// 未知 key → 401
	status, env := internalDo(t, router, http.MethodPost, "/api/internal/api-keys/verify", map[string]string{"key": "ak_0123456789abcdef0123456789abcdef"}, testInternalToken)
	if status != 401 {
		t.Fatalf("unknown key should 401, got %d", status)
	}
	if env.Code == 0 {
		t.Fatalf("unknown key must not pass")
	}

	// 删除后 → 401
	req := httptest.NewRequest(http.MethodPost, "/api/api-keys", strings.NewReader(`{"name":"待删"}`))
	req.Header.Set("Cookie", "open_ai_canvas_session="+cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created struct {
		Data struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)

	del := httptest.NewRequest(http.MethodDelete, "/api/api-keys/"+created.Data.ID, nil)
	del.Header.Set("Cookie", "open_ai_canvas_session="+cookie)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, del)
	if w2.Code != 200 {
		t.Fatalf("delete status=%d", w2.Code)
	}

	status2, env2 := internalDo(t, router, http.MethodPost, "/api/internal/api-keys/verify", map[string]string{"key": created.Data.Key}, testInternalToken)
	if status2 != 401 {
		t.Fatalf("deleted key should 401, got %d", status2)
	}
	if env2.Code == 0 {
		t.Fatalf("deleted key must not pass")
	}
}

func TestApiKeyScopedToOwner(t *testing.T) {
	router, db := newApiKeyHarness(t)
	internalUser(t, db, "u-owner", model.UserStatusActive, 1_000_000)
	internalUser(t, db, "u-other", model.UserStatusActive, 1_000_000)
	cookieOwner := loginCookie(t, db, "u-owner")
	cookieOther := loginCookie(t, db, "u-other")

	// owner 签发
	req := httptest.NewRequest(http.MethodPost, "/api/api-keys", strings.NewReader(`{"name":"owner-key"}`))
	req.Header.Set("Cookie", "open_ai_canvas_session="+cookieOwner)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)

	// 他人删除 owner 的 key → 404，且 key 仍有效
	del := httptest.NewRequest(http.MethodDelete, "/api/api-keys/"+created.Data.ID, nil)
	del.Header.Set("Cookie", "open_ai_canvas_session="+cookieOther)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, del)
	if w2.Code != 404 {
		t.Fatalf("other user delete should 404, got %d", w2.Code)
	}
	// owner 列表仍有该 key
	lst := httptest.NewRequest(http.MethodGet, "/api/api-keys", nil)
	lst.Header.Set("Cookie", "open_ai_canvas_session="+cookieOwner)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, lst)
	if w3.Code != 200 || !strings.Contains(w3.Body.String(), created.Data.ID) {
		t.Fatalf("owner list should still contain key: %s", w3.Body.String())
	}
}


