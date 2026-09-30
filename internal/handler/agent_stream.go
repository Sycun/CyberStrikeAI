package handler

import (
	"context"
	"net/http"
	"sync"

	"cyberstrike-ai/internal/sse"

	"github.com/gin-gonic/gin"
)

// agentStream is one conversational SSE connection: the single writer from
// internal/sse plus the three guards every agent endpoint used to re-implement in its
// own closure - the cancellation suppression, the replay-bus mirror, and the
// "stop writing a dead connection" flag.
//
// Keeping the guards here instead of in four copies is what makes the registry the
// real gate: there is no longer a hand-built frame that could slip an undeclared name
// onto the wire.
type agentStream struct {
	writer *sse.Writer
	bus    *TaskEventBus
	ctx    context.Context

	// conversationID returns the run to mirror frames for. It is a function because
	// the id is only known after the session is prepared, while the stream exists
	// from the first byte.
	conversationID func() string

	// skip suppresses a frame by policy, notably the error an Eino run reports when
	// the user pressed stop - the UI already shows the cancelled notice and would
	// otherwise render two replies.
	skip func(eventType string) bool

	mu           sync.Mutex
	disconnected bool
}

func (h *AgentHandler) newAgentStream(c *gin.Context, writeMu *sync.Mutex) *agentStream {
	stream := &agentStream{bus: h.taskEventBus, ctx: c.Request.Context()}
	stream.writer = sse.NewWriterWithOptions(agentSSE, c.Writer, sse.Options{
		// The keepalive goroutine writes on the same ResponseWriter; concurrent writes
		// break chunked transfer encoding, so the mutex is shared rather than owned.
		Lock: writeMu,
		Flush: func() {
			if flusher, ok := c.Writer.(http.Flusher); ok {
				flusher.Flush()
				return
			}
			c.Writer.Flush()
		},
		OnError: func(error) { stream.markDisconnected() },
	})
	return stream
}

func (s *agentStream) markDisconnected() {
	s.mu.Lock()
	s.disconnected = true
	s.mu.Unlock()
}

func (s *agentStream) isDisconnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.disconnected
}

// send writes one frame, mirroring it to the replay bus first so a client that
// reconnects mid-run sees the frame whether or not this connection is still alive.
func (s *agentStream) send(eventType, message string, data interface{}) {
	if s == nil {
		return
	}
	if s.skip != nil && s.skip(eventType) {
		return
	}
	conversationID := ""
	if s.conversationID != nil {
		conversationID = s.conversationID()
	}
	if conversationID != "" && s.bus != nil {
		if line, ok := mirrorLine(eventType, message, data); ok {
			s.bus.Publish(conversationID, line)
		}
	}
	if s.isDisconnected() {
		return
	}
	select {
	case <-s.ctx.Done():
		s.markDisconnected()
		return
	default:
	}
	_ = s.writer.Send(eventType, message, data)
}

// close stops this stream from writing after the handler returns.
func (s *agentStream) close() {
	if s == nil {
		return
	}
	s.writer.Close()
}
