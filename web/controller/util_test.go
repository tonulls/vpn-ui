package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

func TestGetRemoteIpTrustsConfiguredProxyAndRejectsDirectSpoofing(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "client-ip.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	setting := &model.Setting{Key: "webTrustedProxies", Value: "127.0.0.1/32, 10.0.0.0/8"}
	if err := database.GetDB().Create(setting).Error; err != nil {
		t.Fatalf("create trusted proxy setting: %v", err)
	}

	trustedRequest := httptest.NewRequest("GET", "http://panel.test/", nil)
	trustedRequest.RemoteAddr = "127.0.0.1:8080"
	trustedRequest.Header.Set("X-Forwarded-For", "198.51.100.20, 10.1.2.3")
	if got, want := remoteIPForTest(trustedRequest), "198.51.100.20"; got != want {
		t.Fatalf("trusted proxy resolved client IP to %q, want %q", got, want)
	}

	untrustedRequest := httptest.NewRequest("GET", "http://panel.test/", nil)
	untrustedRequest.RemoteAddr = "203.0.113.9:54321"
	untrustedRequest.Header.Set("X-Forwarded-For", "198.51.100.99")
	untrustedRequest.Header.Set("X-Real-IP", "198.51.100.98")
	if got, want := remoteIPForTest(untrustedRequest), "203.0.113.9"; got != want {
		t.Fatalf("untrusted client IP resolved to %q, want direct peer %q", got, want)
	}
}

func remoteIPForTest(req *http.Request) string {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return getRemoteIp(c)
}
