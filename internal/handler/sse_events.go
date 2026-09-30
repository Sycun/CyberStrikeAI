package handler

import (
	"encoding/json"
	"net/http"
	"sync"

	"cyberstrike-ai/internal/sse"

	"github.com/gin-gonic/gin"
)

// The terminal stream is the one emitter whose frame shape differs from the
// StreamEvent envelope the rest of the product uses. It now goes through the
// shared writer so no further format can appear, while its bytes stay identical
// for the client: the payload is still the short-key {t,d,c} object the terminal
// pane parses.
var terminalSSE = sse.NewRegistry()

func init() {
	for _, kind := range []sse.Kind{
		{Name: sse.KindHeartbeat, Description: "keep the stream alive through proxies"},
		{Name: "out", Description: "stdout chunk from the running command", Terminal: true},
		{Name: "err", Description: "stderr chunk from the running command", Terminal: true},
		{Name: "exit", Description: "process exit code; the stream ends after it", Terminal: true},
	} {
		terminalSSE.MustRegister(kind)
	}
}

// TerminalSSEKinds is the declared terminal event set, exposed for the drift test.
func TerminalSSEKinds() []sse.Kind { return terminalSSE.Kinds() }

type terminalEmitter struct {
	writer *sse.Writer
	mu     sync.Mutex
}

// newTerminalEmitter opens the SSE response and returns the single writer for it.
func newTerminalEmitter(c *gin.Context) (*terminalEmitter, bool) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, false
	}
	return &terminalEmitter{writer: sse.NewWriter(terminalSSE, c.Writer, flusher.Flush)}, true
}

func (e *terminalEmitter) send(ev streamEvent) {
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// A failure here means the client is gone; the stream loop ends on its own.
	_ = e.writer.SendLegacy(ev.T, body)
}
