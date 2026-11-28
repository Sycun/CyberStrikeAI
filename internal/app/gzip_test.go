package app

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGzipMiddlewareCompressesJSONAndSkipsSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gzipMiddleware())

	payload := strings.Repeat(`{"toolName":"nmap","result":"open port 80/tcp http"}`, 200)
	r.GET("/api/conversations/1", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(payload))
	})
	r.POST("/api/agent/stream", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write([]byte("data: {\"type\":\"done\"}\n\n"))
		c.Writer.Flush()
	})
	r.GET("/api/empty", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	// 1. JSON with Accept-Encoding: gzip should be compressed and round-trip cleanly.
	req := httptest.NewRequest(http.MethodGet, "/api/conversations/1", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", w.Header().Get("Vary"))
	}
	if w.Body.Len() >= len(payload)/4 {
		t.Fatalf("compressed len = %d, expected much smaller than %d", w.Body.Len(), len(payload))
	}
	gr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	decoded, err := io.ReadAll(gr)
	_ = gr.Close()
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if string(decoded) != payload {
		t.Fatalf("decoded payload mismatch: len=%d want=%d", len(decoded), len(payload))
	}

	// 2. SSE stream endpoint must NOT be gzipped.
	sseReq := httptest.NewRequest(http.MethodPost, "/api/agent/stream", nil)
	sseReq.Header.Set("Accept-Encoding", "gzip")
	sseW := httptest.NewRecorder()
	r.ServeHTTP(sseW, sseReq)
	if got := sseW.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("SSE Content-Encoding = %q, want empty", got)
	}
	if !strings.Contains(sseW.Body.String(), `"done"`) {
		t.Fatalf("unexpected SSE body: %q", sseW.Body.String())
	}

	// 3. 204 No Content must NOT set Content-Encoding.
	emptyReq := httptest.NewRequest(http.MethodGet, "/api/empty", nil)
	emptyReq.Header.Set("Accept-Encoding", "gzip")
	emptyW := httptest.NewRecorder()
	r.ServeHTTP(emptyW, emptyReq)
	if emptyW.Code != http.StatusNoContent {
		t.Fatalf("204 status = %d", emptyW.Code)
	}
	if got := emptyW.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("204 Content-Encoding = %q, want empty", got)
	}
}
