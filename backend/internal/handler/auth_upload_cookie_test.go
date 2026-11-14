package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"yingce/backend/internal/service"
)

func TestUploadSessionCookieIsSetForDirectUploadDomain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CANVAS_UPLOAD_COOKIE_DOMAIN", "yingce.cc.cd")

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "https://yingce.cc.cd/api/auth/login", nil)
	context.Request.Header.Set("X-Forwarded-Proto", "https")

	setSessionCookie(context, "session-token", 3600)

	response := &http.Response{Header: context.Writer.Header()}
	var mainCookie, uploadCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		switch cookie.Name {
		case service.SessionCookieName:
			mainCookie = cookie
		case uploadSessionCookieName:
			uploadCookie = cookie
		}
	}
	if mainCookie == nil || mainCookie.Value != "session-token" || mainCookie.Path != "/" || mainCookie.Domain != "" {
		t.Fatalf("main session cookie = %+v", mainCookie)
	}
	if uploadCookie == nil || uploadCookie.Value != "session-token" || uploadCookie.Domain != "yingce.cc.cd" || uploadCookie.Path != "/api/resources" || !uploadCookie.HttpOnly || !uploadCookie.Secure || uploadCookie.MaxAge != 3600 {
		t.Fatalf("upload session cookie = %+v", uploadCookie)
	}
}

func TestSessionCookieFallsBackToUploadCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "https://upload.yingce.cc.cd/api/resources", nil)
	context.Request.AddCookie(&http.Cookie{Name: uploadSessionCookieName, Value: "upload-token"})

	if got := sessionCookie(context); got != "upload-token" {
		t.Fatalf("sessionCookie() = %q; want upload-token", got)
	}
}
