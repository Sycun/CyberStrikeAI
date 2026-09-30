package handler

import (
	"context"
	"net/http"
	"sync"
	"time"

	"cyberstrike-ai/internal/sse"

	"github.com/gin-gonic/gin"
)

// sseInterval is how often we write on long SSE streams. Shorter intervals help NATs and
// some proxies that treat connections as idle; 10s is a reasonable balance with traffic.
const sseKeepaliveInterval = 10 * time.Second

// runSSEKeepalive starts periodic SSE heartbeats in a background goroutine.
// The returned stop function must be deferred (or called) before the handler returns so the
// goroutine exits before Gin finalizes the ResponseWriter (avoids "Write called after Handler finished").
//
// writeMu must be the same mutex used by the handler's event writes for this request: concurrent
// writes to http.ResponseWriter break chunked transfer encoding (browser: net::ERR_INVALID_CHUNKED_ENCODING).
func runSSEKeepalive(c *gin.Context, writeMu *sync.Mutex) func() {
	if writeMu == nil {
		return func() {}
	}
	// The heartbeat shares the stream's mutex and the stream's writer: two goroutines
	// writing one ResponseWriter is what produced the chunked-encoding corruption the
	// comment above describes.
	writer := sse.NewWriterWithOptions(agentSSE, c.Writer, sse.Options{Lock: writeMu})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sseKeepaliveLoop(c, writer, stop)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			wg.Wait()
		})
	}
}

// sseKeepaliveLoop sends periodic SSE traffic so proxies (e.g. nginx proxy_read_timeout), NATs,
// and load balancers do not close long-running streams. Some intermediaries ignore comment-only
// lines, so we send both a comment and a minimal data frame (type heartbeat) per tick.
func sseKeepaliveLoop(c *gin.Context, writer *sse.Writer, stop <-chan struct{}) {
	ticker := time.NewTicker(sseKeepaliveInterval)
	defer ticker.Stop()
	ctx := c.Request.Context()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if sseShuttingDown(stop, ctx) {
				return
			}
			// A comment line plus a frame per tick: some intermediaries ignore
			// comments alone, so the frame is what actually resets an idle timer.
			// The frame carries an empty `message`, which is the one byte-level change
			// from the hand-written version; clients switch on `type`, and monitor.js
			// already handles `heartbeat` without reading a message.
			if err := writer.Comment("keepalive"); err != nil {
				return
			}
			if err := writer.Send("heartbeat", "", nil); err != nil {
				return
			}
			if flusher, ok := c.Writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
}

func sseShuttingDown(stop <-chan struct{}, ctx context.Context) bool {
	select {
	case <-stop:
		return true
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
