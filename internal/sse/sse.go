// Package sse is the single writer for server-sent events.
//
// The defect it removes: four separate closures built event frames by hand, plus
// a fifth short-key format in the terminal stream, so the frontend had to
// understand several wire shapes and nothing recorded which event names exist.
// Emission now goes through one function, and the name list it validates against
// is the same list the generated frontend enum is built from.
package sse

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Frame is the agent-stream envelope: `{type, message, data}`. The key is `type`
// because that is what every client already reads; `at` is only filled in when a
// stream opts into timestamps, so switching an emitter to this writer does not
// change a byte for anyone.
type Frame struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	At      string `json:"at,omitempty"`
}

// Options configure one emitter.
type Options struct {
	// Flush pushes the partial response to the client after each frame.
	Flush func()
	// OnLine receives the exact bytes written, so a mirror (the task event bus that
	// lets a reconnecting client subscribe to a run in progress) replays what the
	// original stream sent rather than a re-encoding of it.
	OnLine func(line []byte)
	// OnError sees the write error. Handlers use it to stop writing to a connection
	// that has gone away instead of failing the whole run.
	OnError func(err error)
	// Skip suppresses a frame by policy (for example an error that only exists
	// because the user pressed stop). It stays a hook because the policy belongs to
	// the caller, not to the transport.
	Skip func(name string) bool
	// StampTime adds the `at` field. Off by default for byte-compatibility.
	StampTime bool
	// Lock serialises writes with something outside this writer. A heartbeat
	// goroutine and the run's own emitter share one http.ResponseWriter, and
	// concurrent writes to it break chunked transfer encoding
	// (net::ERR_INVALID_CHUNKED_ENCODING), so they have to share a mutex too.
	Lock sync.Locker
}

// Registry is the set of event kinds this server can emit.
type Registry struct {
	mu      sync.RWMutex
	kinds   map[string]Kind
	writers map[*Writer]bool
}

// Kind documents one event name and the payload it carries.
type Kind struct {
	Name        string
	Description string
	Payload     string
	// Terminal marks the legacy short-key frames so the frontend generator can
	// tell which names still need migrating to the unified envelope.
	Terminal bool
}

// ErrUnregisteredKind is returned instead of emitting an event nobody declared.
var ErrUnregisteredKind = fmt.Errorf("sse: event kind is not registered")

func NewRegistry() *Registry {
	return &Registry{kinds: map[string]Kind{}, writers: map[*Writer]bool{}}
}

// Register declares an event kind. Declaring the same name twice is a bug in the
// emitter, not something to absorb silently.
func (r *Registry) Register(kind Kind) error {
	if strings.TrimSpace(kind.Name) == "" {
		return fmt.Errorf("sse: event kind name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.kinds[kind.Name]; exists {
		return fmt.Errorf("sse: event kind %q is already registered", kind.Name)
	}
	r.kinds[kind.Name] = kind
	return nil
}

// MustRegister panics on a duplicate, for use in package initialization.
func (r *Registry) MustRegister(kind Kind) {
	if err := r.Register(kind); err != nil {
		panic(err)
	}
}

// Kinds returns the declared set sorted by name, for code generation and tests.
func (r *Registry) Kinds() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, 0, len(r.kinds))
	for _, kind := range r.kinds {
		out = append(out, kind)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Has reports whether a name is declared.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.kinds[name]
	return ok
}

// TrackWriter keeps a live connection so a broadcast (heartbeat, shutdown notice)
// can reach every stream without the callers coordinating.
func (r *Registry) TrackWriter(w *Writer) {
	r.mu.Lock()
	r.writers[w] = true
	r.mu.Unlock()
}

// ReleaseWriter forgets a closed connection.
func (r *Registry) ReleaseWriter(w *Writer) {
	r.mu.Lock()
	delete(r.writers, w)
	r.mu.Unlock()
}

// Writer emits frames on one connection. It is not safe for concurrent use; the
// handlers that already serialize their own stream own a single goroutine.
type Writer struct {
	registry *Registry
	out      io.Writer
	opts     Options
	mu       sync.Mutex
	closed   bool
	seq      int
}

// lock returns the mutex this writer serialises with: the caller's, when one was
// supplied, otherwise its own.
func (w *Writer) lock() sync.Locker {
	if w.opts.Lock != nil {
		return w.opts.Lock
	}
	return &w.mu
}

func (w *Writer) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func (w *Writer) markClosed() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
}

// NewWriter binds a registry to an HTTP response writer.
func NewWriter(registry *Registry, out io.Writer, flush func()) *Writer {
	return NewWriterWithOptions(registry, out, Options{Flush: flush})
}

// NewWriterWithOptions binds a registry to any sink with the hooks a handler needs
// to keep its own behaviour while giving up the frame encoding.
func NewWriterWithOptions(registry *Registry, out io.Writer, opts Options) *Writer {
	w := &Writer{registry: registry, out: out, opts: opts}
	registry.TrackWriter(w)
	return w
}

// Send writes one registered event. An unregistered name is a programming error
// and fails loudly rather than reaching a client with an unknown frame.
func (w *Writer) Send(name, message string, data any) error {
	return w.sendAt(time.Now(), name, message, data)
}

func (w *Writer) sendAt(now time.Time, name, message string, data any) error {
	if !w.registry.Has(name) {
		return fmt.Errorf("%w: %s", ErrUnregisteredKind, name)
	}
	if w.opts.Skip != nil && w.opts.Skip(name) {
		return nil
	}
	frame := Frame{Type: name, Message: message, Data: data}
	if w.opts.StampTime {
		frame.At = now.UTC().Format(time.RFC3339Nano)
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("sse: encode %s: %w", name, err)
	}
	line := frameLine(body)
	return w.writeLine(line, func() { w.seq++ })
}

// writeLine is the one place that touches the response, so no caller can write a
// frame without the registry and the serialisation that go with it.
func (w *Writer) writeLine(line []byte, before func()) error {
	lock := w.lock()
	if w.isClosed() {
		return io.ErrClosedPipe
	}
	lock.Lock()
	if w.closed {
		lock.Unlock()
		return io.ErrClosedPipe
	}
	if before != nil {
		before()
	}
	_, err := w.out.Write(line)
	lock.Unlock()
	if err != nil {
		if w.opts.OnError != nil {
			w.opts.OnError(err)
		}
		return err
	}
	if w.opts.OnLine != nil {
		w.opts.OnLine(line)
	}
	if w.opts.Flush != nil {
		w.opts.Flush()
	}
	return nil
}

// Encode renders one frame without writing it, for callers that must hand the exact
// bytes to something else (a replay buffer, a test fixture).
func Encode(name, message string, data any) ([]byte, error) {
	body, err := json.Marshal(Frame{Type: name, Message: message, Data: data})
	if err != nil {
		return nil, fmt.Errorf("sse: encode %s: %w", name, err)
	}
	return frameLine(body), nil
}

// frameLine is the whole wire format: one `data:` line, the JSON body, a blank
// line. It lives here so that no emitter can invent another one.
func frameLine(body []byte) []byte {
	line := make([]byte, 0, len(body)+8)
	line = append(line, []byte("data: ")...)
	line = append(line, body...)
	return append(line, '\n', '\n')
}

// SendLegacy writes a pre-encoded payload for the terminal stream, whose short-key
// shape the frontend still parses. It stays inside the writer so no emitter can
// invent a sixth format.
func (w *Writer) SendLegacy(name string, raw []byte) error {
	if !w.registry.Has(name) {
		return fmt.Errorf("%w: %s", ErrUnregisteredKind, name)
	}
	return w.writeLine(frameLine(raw), nil)
}

// Comment writes an SSE comment line. Proxies and NATs use downstream bytes to decide
// whether a stream is idle, and some intermediaries ignore comments alone, so the
// heartbeat pairs this with a frame - both of them produced here rather than
// assembled by hand in a handler.
func (w *Writer) Comment(text string) error {
	return w.writeLine(append(append([]byte(": "), text...), '\n', '\n'), nil)
}

// Heartbeat is emitted on the registry cadence so proxies do not close idle streams.
func (w *Writer) Heartbeat() error {
	return w.Send(KindHeartbeat, "", nil)
}

// Close stops the writer from emitting after the request context is done.
func (w *Writer) Close() {
	w.markClosed()
	w.registry.ReleaseWriter(w)
}

// KindHeartbeat is always registered because the transport, not a feature, owns it.
const KindHeartbeat = "heartbeat"

// Recorder turns registered events into frame bytes without writing them anywhere.
// The task event bus keeps exactly these bytes so a client that reconnects mid-run
// replays what the live stream sent; validating here too means a bus can never carry
// a frame the live stream would have refused.
type Recorder struct {
	registry *Registry
}

// NewRecorder binds an encoder to a registry.
func NewRecorder(registry *Registry) *Recorder {
	return &Recorder{registry: registry}
}

// Line encodes one frame, or refuses an unregistered name.
func (r *Recorder) Line(name, message string, data any) ([]byte, error) {
	if r == nil || r.registry == nil {
		return nil, fmt.Errorf("sse: recorder requires a registry")
	}
	if !r.registry.Has(name) {
		return nil, fmt.Errorf("%w: %s", ErrUnregisteredKind, name)
	}
	body, err := json.Marshal(Frame{Type: name, Message: message, Data: data})
	if err != nil {
		return nil, fmt.Errorf("sse: encode %s: %w", name, err)
	}
	return frameLine(body), nil
}
