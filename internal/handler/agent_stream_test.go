package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/sse"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The conversational streams now share one emitter, so the wire contract lives in one
// place too. These tests pin what the page actually receives - byte for byte - and
// what it must not receive.

func newStreamTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/agent/stream", nil)
	return c, recorder
}

func TestAgentStreamWritesTheDeclaredFrameShape(t *testing.T) {
	c, recorder := newStreamTestContext(t)
	var writeMu sync.Mutex
	stream := (&AgentHandler{}).newAgentStream(c, &writeMu)

	if err := stream.writer.Send("conversation", "会话已创建", map[string]interface{}{"conversationId": "c1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	want := `data: {"type":"conversation","message":"会话已创建","data":{"conversationId":"c1"}}` + "\n\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("frame = %q, want %q", got, want)
	}
}

// An event nobody declared cannot reach a client: this is the failure the registry
// exists to make impossible, and it is the reason a new emitter must add a kind.
func TestAgentStreamRefusesAnUndeclaredEvent(t *testing.T) {
	c, recorder := newStreamTestContext(t)
	var writeMu sync.Mutex
	stream := (&AgentHandler{}).newAgentStream(c, &writeMu)

	err := stream.writer.Send("event_nobody_declared", "", nil)
	if err == nil {
		t.Fatal("an undeclared event was written")
	}
	if !errors.Is(err, sse.ErrUnregisteredKind) || !strings.Contains(err.Error(), "event_nobody_declared") {
		t.Fatalf("error should be the registry refusal naming the event: %v", err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("refused event still reached the wire: %q", recorder.Body.String())
	}
	if _, err := sseBusRecorder.Line("event_nobody_declared", "", nil); !strings.Contains(err.Error(), "event_nobody_declared") {
		t.Fatalf("the replay bus accepted the same undeclared event: %v", err)
	}
}

// A reconnecting client subscribes to the bus, so the mirrored bytes must be the ones
// the live stream sent - not a second encoding that could drift.
func TestAgentStreamMirrorsIdenticalBytesToTheBus(t *testing.T) {
	c, recorder := newStreamTestContext(t)
	var writeMu sync.Mutex
	bus := NewTaskEventBus()
	handler := &AgentHandler{taskEventBus: bus}
	stream := handler.newAgentStream(c, &writeMu)
	stream.conversationID = func() string { return "c1" }

	sub, ch := bus.Subscribe("c1")
	defer bus.Unsubscribe("c1", sub)
	stream.send("progress", "working", map[string]interface{}{"step": 1})

	var mirrored []byte
	select {
	case line := <-ch:
		mirrored = line
	case <-time.After(2 * time.Second):
		t.Fatal("the bus never saw the frame")
	}
	if got, want := string(mirrored), recorder.Body.String(); got != want {
		t.Fatalf("bus mirrored %q, stream sent %q", got, want)
	}
}

func TestAgentStreamStopsWritingAfterTheClientGoesAway(t *testing.T) {
	c, recorder := newStreamTestContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = c.Request.WithContext(ctx)
	var writeMu sync.Mutex
	stream := (&AgentHandler{}).newAgentStream(c, &writeMu)

	stream.send("progress", "before", nil)
	cancel()
	stream.send("progress", "after", nil)

	body := recorder.Body.String()
	if !strings.Contains(body, `"message":"before"`) {
		t.Fatalf("the first frame never reached the wire: %q", body)
	}
	if strings.Contains(body, `"message":"after"`) {
		t.Fatalf("a frame was written after the client disconnected: %q", body)
	}
}

// The cancellation guard is what the four duplicated closures each re-implemented; it
// now lives once, next to the writer it protects.
func TestAgentStreamSkipSuppressesCancelledErrors(t *testing.T) {
	c, recorder := newStreamTestContext(t)
	runCtx, cancelWithCause := context.WithCancelCause(context.Background())
	cancelWithCause(ErrTaskCancelled)

	var writeMu sync.Mutex
	stream := (&AgentHandler{}).newAgentStream(c, &writeMu)
	stream.skip = func(eventType string) bool {
		if eventType != "error" {
			return false
		}
		return context.Cause(runCtx) == ErrTaskCancelled
	}
	stream.send("error", "context canceled", nil)
	stream.send("cancelled", "已停止", nil)

	body := recorder.Body.String()
	if strings.Contains(body, `"message":"context canceled"`) {
		t.Fatalf("a cancellation error frame reached the UI: %q", body)
	}
	if !strings.Contains(body, `"type":"cancelled"`) {
		t.Fatalf("the cancelled frame was suppressed too: %q", body)
	}
}

// Byte-compatibility for the heartbeat: the one deliberate difference from the
// hand-written version is the empty `message`, which clients do not read.
func TestKeepaliveFramesFromTheSingleWriter(t *testing.T) {
	c, recorder := newStreamTestContext(t)
	var writeMu sync.Mutex
	stream := (&AgentHandler{}).newAgentStream(c, &writeMu)

	if err := stream.writer.Comment("keepalive"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if err := stream.writer.Send("heartbeat", "", nil); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	want := ": keepalive\n\ndata: {\"type\":\"heartbeat\",\"message\":\"\"}\n\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("keepalive bytes = %q, want %q", got, want)
	}
}

// End-to-end for a migrated endpoint: a rejected request must still be answered with
// the same two frames the page handled before the refactor.
func TestEinoSingleAgentStreamAnswersABadRequestWithFrames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &AgentHandler{config: &config.Config{}, logger: zap.NewNop()}
	router := gin.New()
	router.POST("/api/agent/eino/stream", handler.EinoSingleAgentLoopStream)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/eino/stream", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	body := recorder.Body.String()
	if ct := recorder.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q, want an event stream", ct)
	}
	var frames []StreamEvent
	for _, line := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("frame lost its prefix: %q", line)
		}
		var frame StreamEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatalf("frame is not the envelope the client parses: %v\n%q", err, line)
		}
		frames = append(frames, frame)
	}
	if len(frames) != 2 || frames[0].Type != "error" || frames[1].Type != "done" {
		t.Fatalf("frames = %+v, want an error followed by done", frames)
	}
}

// The multi-agent endpoint's "not enabled" answer is the other early-exit path that
// used to build its own frames.
func TestMultiAgentStreamReportsDisabledOverFrames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &AgentHandler{config: &config.Config{}, logger: zap.NewNop()}
	router := gin.New()
	router.POST("/api/agent/multi/stream", handler.MultiAgentLoopStream)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/multi/stream", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	body := recorder.Body.String()
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `"type":"done"`) {
		t.Fatalf("disabled multi-agent did not answer with an error and a done frame: %q", body)
	}
	if !strings.Contains(body, "multi_agent.enabled") {
		t.Fatalf("the error lost its instruction: %q", body)
	}
}

// TestConduitEventsReachTheWire is the behaviour proof for the progress-callback half of
// the inventory. These names are spelled out at callback *call* sites outside the emitter
// package (`progressCallback(ctx, "tool_call", detail)` in the runtime, the workflow engine
// and the Eino observers), so a registry built from the emitter package alone refused them
// at runtime and silently dropped tool output from the stream. Each one must now produce
// bytes, in the envelope the page parses.
func TestConduitEventsReachTheWire(t *testing.T) {
	inv := scanInventory(t)
	names := conduitOnlyNames(inv)
	if len(names) < 40 {
		t.Fatalf("the inventory marks only %d names as conduit-only; the extractor is broken", len(names))
	}
	t.Logf("conduit-only agent events: %d", len(names))

	for _, name := range names {
		c, recorder := newStreamTestContext(t)
		var writeMu sync.Mutex
		stream := (&AgentHandler{}).newAgentStream(c, &writeMu)
		stream.send(name, "", map[string]interface{}{"eventType": name})

		body := recorder.Body.String()
		want := `data: {"type":"` + name + `","message":"","data":{"eventType":"` + name + `"}}` + "\n\n"
		if body != want {
			t.Fatalf("conduit event %q reached the wire as %q, want %q", name, body, want)
		}
		line, err := sseBusRecorder.Line(name, "", map[string]interface{}{"eventType": name})
		if err != nil {
			t.Fatalf("the replay bus refused conduit event %q: %v", name, err)
		}
		if string(line) != body {
			t.Fatalf("bus frame for %q is %q, stream frame is %q", name, line, body)
		}
	}
}

// conduitOnlyNames are the agent events the progress conduit proves reachable but the
// emitter package's own value graph does not see - the ones a registry built only from
// internal/handler would have dropped.
func conduitOnlyNames(inv sse.Inventory) []string {
	emitterSeen := map[string]bool{}
	conduitSeen := map[string]bool{}
	for _, s := range inv.Sources {
		if s.Stream != sse.StreamAgent {
			continue
		}
		switch s.Mode {
		case "conduit", "handled":
			conduitSeen[s.Name] = true
		default:
			emitterSeen[s.Name] = true
		}
	}
	out := []string{}
	for name := range conduitSeen {
		if !emitterSeen[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
