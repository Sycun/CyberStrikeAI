package sse

import (
	"bytes"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func testRegistry() *Registry {
	r := NewRegistry()
	r.MustRegister(Kind{Name: KindHeartbeat})
	r.MustRegister(Kind{Name: "out", Terminal: true})
	r.MustRegister(Kind{Name: "err", Terminal: true})
	r.MustRegister(Kind{Name: "exit", Terminal: true})
	r.MustRegister(Kind{Name: "tick"})
	return r
}

func TestWriterRefusesUndeclaredKind(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := NewWriter(testRegistry(), recorder.Body, recorder.Flush)

	if err := writer.Send("undeclared_event", "hi", nil); err == nil {
		t.Fatal("an undeclared event kind was emitted")
	} else if !errors.Is(err, ErrUnregisteredKind) {
		t.Fatalf("wrong error: %v", err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("bytes reached the client for a refused event: %q", recorder.Body.String())
	}
}

// TestLegacyTerminalFrameIsByteCompatible guards the migration of the odd-format
// emitter: the client must receive exactly what it received before.
func TestLegacyTerminalFrameIsByteCompatible(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := NewWriter(testRegistry(), recorder.Body, recorder.Flush)

	payload := []byte(`{"t":"out","d":"hello"}`)
	if err := writer.SendLegacy("out", payload); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Body.String(); got != "data: "+string(payload)+"\n\n" {
		t.Fatalf("frame changed shape: %q", got)
	}
	if err := writer.SendLegacy("exit", []byte(`{"t":"exit","c":0}`)); err != nil {
		t.Fatalf("declared kind refused: %v", err)
	}
}

// The bytes are the contract: handlers were migrated off their own frame builders,
// so this envelope has to stay what the clients already parse - `type`, always
// present `message`, optional `data`, and no extra field unless a stream asks for it.
func TestUnifiedFrameCarriesKindMessageAndPayload(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := NewWriter(testRegistry(), recorder.Body, recorder.Flush)
	if err := writer.Send("tick", "counting", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if got, want := recorder.Body.String(), `data: {"type":"tick","message":"counting","data":{"n":1}}`+"\n\n"; got != want {
		t.Fatalf("frame = %q, want %q", got, want)
	}
	bare := httptest.NewRecorder()
	if err := NewWriter(testRegistry(), bare.Body, nil).Send("tick", "", nil); err != nil {
		t.Fatal(err)
	}
	if got, want := bare.Body.String(), `data: {"type":"tick","message":""}`+"\n\n"; got != want {
		t.Fatalf("empty-payload frame = %q, want %q", got, want)
	}
}

func TestStampTimeIsOptIn(t *testing.T) {
	plain := httptest.NewRecorder()
	if err := NewWriter(testRegistry(), plain.Body, nil).Send("tick", "", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Body.String(), `"at":`) {
		t.Fatalf("timestamps leaked into a stream that did not ask: %s", plain.Body.String())
	}

	stamped := httptest.NewRecorder()
	writer := NewWriterWithOptions(testRegistry(), stamped.Body, Options{StampTime: true})
	if err := writer.Send("tick", "", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stamped.Body.String(), `"at":"`) {
		t.Fatalf("stamp requested but no `at` field: %s", stamped.Body.String())
	}
}

// A mirror must replay the exact bytes the stream sent, not a re-encoding: the
// reconnecting client and the live client have to see the same frame.
func TestOnLineSeesTheExactBytes(t *testing.T) {
	recorder := httptest.NewRecorder()
	var mirrored []string
	writer := NewWriterWithOptions(testRegistry(), recorder.Body, Options{
		OnLine: func(line []byte) { mirrored = append(mirrored, string(line)) },
	})
	if err := writer.Send("tick", "counting", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if len(mirrored) != 1 || mirrored[0] != recorder.Body.String() {
		t.Fatalf("mirror got %q, stream sent %q", mirrored, recorder.Body.String())
	}
}

func TestSkipSuppressesAPolicyRejectedFrame(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := NewWriterWithOptions(testRegistry(), recorder.Body, Options{
		Skip: func(name string) bool { return name == "tick" },
	})
	if err := writer.Send("tick", "should not reach the client", nil); err != nil {
		t.Fatalf("suppressed frame returned an error: %v", err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("suppressed frame was written: %q", recorder.Body.String())
	}
}

// A dead connection is reported to the handler that owns the run, and later sends
// stop trying instead of writing into a closed stream.
func TestOnErrorReportsWriteFailures(t *testing.T) {
	var reported []error
	writer := NewWriterWithOptions(testRegistry(), failingWriter{}, Options{
		OnError: func(err error) { reported = append(reported, err) },
	})
	if err := writer.Send("tick", "", nil); err == nil {
		t.Fatal("a failing sink should surface the error")
	}
	if len(reported) != 1 {
		t.Fatalf("OnError calls = %d, want 1", len(reported))
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCloseStopsEmissionAndReleasesTracking(t *testing.T) {
	registry := testRegistry()
	recorder := httptest.NewRecorder()
	writer := NewWriter(registry, recorder.Body, recorder.Flush)
	before := len(registry.writers)
	writer.Close()
	if err := writer.Send("tick", "", nil); err == nil {
		t.Fatal("a closed writer kept emitting")
	}
	if len(registry.writers) != before-1 {
		t.Fatal("closed writer was not released from the registry")
	}
}

func TestConcurrentSendSerializesFrames(t *testing.T) {
	recorder := httptest.NewRecorder()
	var mu sync.Mutex
	buffered := &lockedBuffer{w: recorder.Body, mu: &mu}
	writer := NewWriter(testRegistry(), buffered, func() {})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := writer.Send("tick", "x", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	// Every frame must be a complete line pair, never interleaved mid-write.
	if got := strings.Count(buffered.String(), "data: "); got != 20 {
		t.Fatalf("frames = %d, want 20", got)
	}
}

type lockedBuffer struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.w.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.w.String()
}

func TestDuplicateRegistrationIsRejected(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(Kind{Name: "dup"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Kind{Name: "dup"}); err == nil {
		t.Fatal("a duplicate event kind was accepted")
	}
	if err := registry.Register(Kind{Name: " "}); err == nil {
		t.Fatal("a blank event kind was accepted")
	}
}
