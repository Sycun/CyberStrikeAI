package handler

import (
	"io"

	"cyberstrike-ai/internal/sse"
)

// c2EventSSE declares the categories the C2 event stream can carry.
//
// That stream is the third wire shape in the product: the frame body is a bare
// `c2.Event` (no envelope), and the page switches on its `category` field. The
// category therefore plays the role the event `type` plays on the agent streams, so it
// goes through the same registry and the same writer - one place decides what a frame
// looks like, and a category nobody declared cannot reach a client.
var c2EventSSE = func() *sse.Registry {
	registry := sse.NewRegistry()
	for _, kind := range c2EventSSEKinds() {
		registry.MustRegister(kind)
	}
	return registry
}()

func c2EventSSEKinds() []sse.Kind {
	return []sse.Kind{
		{Name: "listener", Description: "a listener was created, started, stopped or failed", Payload: "c2.Event{level,sessionId,taskId,message,data}"},
		{Name: "session", Description: "a implant session checked in, came online, or went offline", Payload: "c2.Event{level,sessionId,message,data}"},
		{Name: "task", Description: "a task was queued, approved, rejected, cancelled, or finished", Payload: "c2.Event{level,sessionId,taskId,message,data}"},
	}
}

// C2EventSSEKinds exposes the declared categories for the drift gate.
func C2EventSSEKinds() []sse.Kind { return c2EventSSE.Kinds() }

// streamSink redirects writes to the writer gin hands the stream callback. gin flushes
// that writer once the callback returns, so frames must go there rather than to the
// response directly; the indirection lets one sse.Writer serve every callback of one
// connection without rebuilding it per event.
type streamSink struct {
	current io.Writer
}

func (s *streamSink) Write(p []byte) (int, error) { return s.current.Write(p) }

// use points the sink at the connection's current writer. gin invokes the stream
// callback serially, so no lock is needed.
func (s *streamSink) use(w io.Writer) { s.current = w }

// newC2EventStream binds the C2 category registry to a stream sink.
func newC2EventStream() (*sse.Writer, *streamSink) {
	sink := &streamSink{}
	return sse.NewWriter(c2EventSSE, sink, nil), sink
}
