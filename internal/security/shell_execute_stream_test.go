package security

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// streamCollector stands in for the SDK stream: it records what the shell writes and signals
// when the stream ends, so these tests assert the process behaviour (which is the security
// concern) without importing the agent framework's types.

type streamCollector struct {
	mu      sync.Mutex
	events  []ShellEvent
	sink    chan ShellEvent
	closed  chan struct{}
	once    sync.Once
	started time.Time
}

func newStreamCollector() *streamCollector {
	return &streamCollector{sink: make(chan ShellEvent, 64), closed: make(chan struct{})}
}

func (c *streamCollector) Send(event ShellEvent) bool {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
	select {
	case c.sink <- event:
	default:
	}
	return false
}

func (c *streamCollector) Close() {
	c.once.Do(func() { close(c.closed) })
}

func (c *streamCollector) recorded() []ShellEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ShellEvent(nil), c.events...)
}

func (c *streamCollector) text() string {
	var b strings.Builder
	for _, event := range c.recorded() {
		b.WriteString(event.Output)
	}
	return b.String()
}

// waitClosed blocks until the shell has finished writing, and reports how long that took.
func (c *streamCollector) waitClosed(t *testing.T) time.Duration {
	t.Helper()
	select {
	case <-c.closed:
		return time.Since(c.started)
	case <-time.After(20 * time.Second):
		t.Fatalf("the shell never closed its stream; output so far: %q", c.text())
		return 0
	}
}

// next returns the first event that arrives, or fails the test if none arrives in time. It
// is how the early-stderr assertions work: the point of the implementation is that stderr
// shows up while stdout is still blocked, so waiting for the *end* of the stream would test
// nothing.
func (c *streamCollector) next(t *testing.T, within time.Duration) (ShellEvent, bool) {
	t.Helper()
	select {
	case event := <-c.sink:
		return event, true
	case <-c.closed:
		return ShellEvent{}, false
	case <-time.After(within):
		return ShellEvent{}, false
	}
}

func runShell(t *testing.T, ctx context.Context, command string, background bool) *streamCollector {
	t.Helper()
	collector := newStreamCollector()
	collector.started = time.Now()
	RunShellStreaming(ctx, command, background, collector)
	return collector
}

func exitCodeOf(events []ShellEvent) *int {
	var found *int
	for _, event := range events {
		if event.ExitCode != nil {
			value := *event.ExitCode
			found = &value
		}
	}
	return found
}

func TestStreamingShell_StreamsStderrBeforeStdoutEOF(t *testing.T) {
	command := PrepareNonInteractiveShellCommand("echo err-only >&2; exit 1")
	collector := runShell(t, context.Background(), command, false)

	if took := collector.waitClosed(t); took > 3*time.Second {
		t.Fatalf("expected fast completion, took %v", took)
	}
	if !strings.Contains(collector.text(), "err-only") {
		t.Fatalf("expected stderr in output, got: %q", collector.text())
	}
}

func TestStreamingShell_SudoFailsFast(t *testing.T) {
	// Exercise stderr delivery and failure propagation without relying on the
	// host's sudo policy: CI runners may allow passwordless sudo, even as root.
	// A shell function also prevents this test from invoking the real sudo.
	command := PrepareNonInteractiveShellCommand(`
sudo() {
    printf '%s\n' 'sudo: a password is required' >&2
    return 1
}
sudo whoami && printf '%s\n' 'unexpected-command-success'
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collector := runShell(t, ctx, command, false)
	took := collector.waitClosed(t)
	if ctx.Err() != nil || took > 5*time.Second {
		t.Fatalf("sudo should fail quickly, took %v output=%q", took, collector.text())
	}
	out := collector.text()
	if strings.Contains(out, "command exited with non-zero code") {
		t.Fatalf("legacy exit line present: %q", out)
	}
	if !strings.Contains(out, "sudo: a password is required") {
		t.Fatalf("expected sudo error text, got: %q", out)
	}
	if strings.Contains(out, "unexpected-command-success") {
		t.Fatalf("command after failed sudo unexpectedly ran: %q", out)
	}
	exitCode := exitCodeOf(collector.recorded())
	if exitCode == nil || *exitCode != 1 {
		t.Fatalf("expected exit code 1, got: %v", exitCode)
	}
}

func TestStreamingShell_StderrWhileStdoutBlocks(t *testing.T) {
	// 模拟 sudo：stderr 先有输出，stdout 侧进程仍挂起；旧 eino local 在首包 stderr 前不会向流写任何内容。
	command := PrepareNonInteractiveShellCommand(`echo "password prompt" >&2; sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	collector := runShell(t, ctx, command, false)
	start := time.Now()
	event, ok := collector.next(t, 1500*time.Millisecond)
	if !ok {
		t.Fatalf("expected early stderr, got: %q", collector.text())
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("expected stderr promptly, took %v output=%q", took, collector.text())
	}
	if !strings.Contains(event.Output, "password prompt") {
		t.Fatalf("expected early stderr, got: %q", collector.text())
	}
}

// TestStreamingShell_BackgroundJobDoesNotHoldPipe 模拟 cmd & 后继续前台逻辑：重定向后应快速结束。
func TestStreamingShell_BackgroundJobDoesNotHoldPipe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping shell integration in -short")
	}
	command := `(sh -c 'printf x; sleep 120') & echo started; sleep 0`
	collector := runShell(t, context.Background(), command, false)

	if took := collector.waitClosed(t); took > 3*time.Second {
		t.Fatalf("expected fast completion, took %v output=%q", took, collector.text())
	}
	if !strings.Contains(collector.text(), "started") {
		t.Fatalf("expected foreground echo, got: %q", collector.text())
	}
}

func TestRunShellStreaming_RejectsEmptyCommand(t *testing.T) {
	collector := newStreamCollector()
	collector.started = time.Now()
	RunShellStreaming(context.Background(), "   ", false, collector)
	collector.waitClosed(t)
	events := collector.recorded()
	if len(events) != 1 || events[0].Err == nil {
		t.Fatalf("events = %+v, want a single error for an empty command", events)
	}
	if !strings.Contains(events[0].Err.Error(), "command is required") {
		t.Fatalf("error = %v, want the command-required message", events[0].Err)
	}
}
