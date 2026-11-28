package app

import (
	"bufio"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

var gzipWriterPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return w
	},
}

// gzipMiddleware 对可压缩的 JSON/文本/静态资源响应启用 Gzip 压缩，
// 自动跳过 SSE、WebSocket、二进制压缩格式及客户端未声明 gzip 的请求。
func gzipMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !shouldTryGzipRequest(c.Request) {
			c.Next()
			return
		}

		origWriter := c.Writer
		gw := &gzipResponseWriter{
			ResponseWriter: origWriter,
		}
		c.Writer = gw
		defer func() {
			gw.close()
			c.Writer = origWriter
		}()

		c.Next()
	}
}

func shouldTryGzipRequest(r *http.Request) bool {
	if r == nil || r.Method == http.MethodHead {
		return false
	}
	if !strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip") {
		return false
	}
	if r.Header.Get("Upgrade") != "" {
		return false
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
		return false
	}
	path := strings.ToLower(r.URL.Path)
	if strings.HasSuffix(path, "/stream") ||
		strings.HasSuffix(path, "/task-events") ||
		strings.HasSuffix(path, "/events") ||
		strings.Contains(path, "/ws") ||
		strings.Contains(path, "/terminal") {
		return false
	}
	return true
}

func shouldCompressResponse(status int, header http.Header) bool {
	if status == http.StatusNoContent || status == http.StatusNotModified || (status >= 100 && status < 200) {
		return false
	}
	if header.Get("Content-Encoding") != "" {
		return false
	}
	ct := strings.ToLower(header.Get("Content-Type"))
	if strings.Contains(ct, "text/event-stream") {
		return false
	}
	if strings.HasPrefix(ct, "image/") && !strings.Contains(ct, "svg") {
		return false
	}
	if strings.HasPrefix(ct, "video/") ||
		strings.HasPrefix(ct, "audio/") ||
		strings.HasPrefix(ct, "font/") ||
		strings.Contains(ct, "application/gzip") ||
		strings.Contains(ct, "application/zip") ||
		strings.Contains(ct, "application/octet-stream") {
		return false
	}
	return true
}

type gzipResponseWriter struct {
	gin.ResponseWriter
	gz          *gzip.Writer
	decided     bool
	compressing bool
	statusCode  int
}

func (w *gzipResponseWriter) decide(status int) {
	if w.decided {
		return
	}
	w.decided = true
	if status == 0 {
		status = http.StatusOK
	}
	w.statusCode = status
	if !shouldCompressResponse(status, w.Header()) {
		return
	}
	w.compressing = true
	h := w.Header()
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length")
	vary := h.Get("Vary")
	if vary == "" {
		h.Set("Vary", "Accept-Encoding")
	} else if !strings.Contains(strings.ToLower(vary), "accept-encoding") {
		h.Set("Vary", vary+", Accept-Encoding")
	}
	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(w.ResponseWriter)
	w.gz = gz
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if w.Written() {
		return
	}
	w.decide(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) WriteHeaderNow() {
	if !w.decided {
		w.decide(w.Status())
	}
	w.ResponseWriter.WriteHeaderNow()
}

func (w *gzipResponseWriter) Write(data []byte) (int, error) {
	if !w.decided {
		if w.Header().Get("Content-Type") == "" && len(data) > 0 {
			w.Header().Set("Content-Type", http.DetectContentType(data))
		}
		status := w.statusCode
		if status == 0 {
			status = w.ResponseWriter.Status()
		}
		if status == 0 {
			status = http.StatusOK
		}
		w.decide(status)
	}
	if w.compressing && w.gz != nil {
		return w.gz.Write(data)
	}
	return w.ResponseWriter.Write(data)
}

func (w *gzipResponseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *gzipResponseWriter) Flush() {
	if w.compressing && w.gz != nil {
		_ = w.gz.Flush()
	}
	w.ResponseWriter.Flush()
}

func (w *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.close()
	return w.ResponseWriter.Hijack()
}

func (w *gzipResponseWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
		w.gz.Reset(io.Discard)
		gzipWriterPool.Put(w.gz)
		w.gz = nil
	}
}

// staticCacheControlMiddleware 为带内容哈希的 Vite 前端构建产物设置长期不可变缓存，
// 对语言包设置适度缓存，对 index.html 等入口文件保持 no-cache。
func staticCacheControlMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		switch {
		case strings.HasPrefix(path, "/static/app/assets/"):
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		case strings.HasPrefix(path, "/static/i18n/"):
			c.Header("Cache-Control", "public, max-age=3600")
		case path == "/static/app/" || path == "/static/app/index.html":
			c.Header("Cache-Control", "no-cache")
		}
		c.Next()
	}
}
