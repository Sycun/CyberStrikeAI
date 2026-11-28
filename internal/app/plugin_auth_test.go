package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Stub the post-authorization endpoint handler. This verifies the actual
// platform middleware and route permissions without starting terminal or C2 tasks.
func TestPluginPortModePreservesChatWebShellAndC2StreamingAuthentication(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "auth.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth := security.NewAuthManager(1)
	password, err := auth.AttachRBACStore(db)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.Authenticate("admin", password)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	api := r.Group("/api", pluginAwarePlatformAuth(auth, config.PluginProxyConfig{}), security.RBACMiddleware(db))
	ok := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	api.POST("/agent/stream", ok)
	api.POST("/multi-agent/stream", ok)
	api.POST("/webshell/exec", ok)
	api.POST("/terminal/run/stream", ok)
	api.GET("/terminal/ws", ok)
	api.GET("/c2/events/stream", ok)
	for _, tt := range []struct {
		name, method, path, transport string
	}{
		{"Chat SSE", "POST", "/api/agent/stream", "bearer"},
		{"Multi-agent SSE", "POST", "/api/multi-agent/stream", "bearer"},
		{"WebShell", "POST", "/api/webshell/exec", "bearer"},
		{"Terminal SSE", "POST", "/api/terminal/run/stream", "bearer"},
		{"Terminal WebSocket", "GET", "/api/terminal/ws", "ws"},
		{"C2 event SSE", "GET", "/api/c2/events/stream", "sse"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("primary cookie authenticates on port mode: %d", w.Code)
			}
			switch tt.transport {
			case "bearer":
				req.Header.Set("Authorization", "Bearer "+token)
			case "ws":
				req.URL.RawQuery = "token=" + token
				req.Header.Set("Upgrade", "websocket")
			case "sse":
				req.URL.RawQuery = "token=" + token
				req.Header.Set("Accept", "text/event-stream")
			}
			w = httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusNoContent {
				t.Fatalf("authorized client rejected: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
