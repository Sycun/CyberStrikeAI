// Package pluginhost runs capability plugins out of process.
//
// The design is the one the research report selected: a versioned JSON-RPC ABI
// over stdio, one instance per trust domain, lazy start and restart on crash, no
// credentials in the child environment, and no direct network from the child -
// egress goes through a host-side CONNECT proxy whose allowlist is derived from
// the capability's declared grants. Crash isolation and capability enforcement
// therefore do not depend on which interpreter the plugin is written in.
package pluginhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion is the ABI this host speaks. A plugin must answer with the same
// major version or the instance is refused before any capability runs.
const ProtocolVersion = "csai-plugin/1"

// ErrProtocolMismatch is returned when a plugin cannot speak this ABI.
var ErrProtocolMismatch = errors.New("pluginhost: protocol version mismatch")

// ErrNotConfigured means a capability asked for plugin-host execution while no
// host is configured. Callers must treat it as a refusal, not a fallback to
// in-process execution.
var ErrNotConfigured = errors.New("pluginhost: no host configured for this trust domain")

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e.Data == "" {
		return fmt.Sprintf("plugin rpc error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("plugin rpc error %d: %s (%s)", e.Code, e.Message, e.Data)
}

// JSON-RPC error codes, with product-specific ones in the server-error range.
const (
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInternalError  = -32603
	// CodeGrantRefused is returned when a plugin asks for a mediated capability its
	// manifest never declared. It is a refusal, not a transport failure.
	CodeGrantRefused = -32001
	// CodeUnavailable marks a plugin that crashed and has not come back yet.
	CodeUnavailable = -32002
)

// Request is one JSON-RPC call. Params is kept raw so a method can decode it into
// its own shape without the transport knowing about capabilities.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is either a result or an error.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Methods the host calls on a plugin.
const (
	MethodInitialize = "initialize"
	MethodListCaps   = "capabilities/list"
	MethodInvoke     = "capabilities/invoke"
	MethodShutdown   = "shutdown"
)

// Methods a plugin may call back into the host. Anything else is refused: the
// callback surface is the only way a plugin reaches platform state, so it is
// enumerated here rather than discovered.
const (
	CallbackGrantCheck = "host/grant_check"
	CallbackLog        = "host/log"
	CallbackProgress   = "host/progress"
	CallbackFetch      = "host/http_request"
)

// allowedCallbacks is the whole callback set.
var allowedCallbacks = map[string]bool{
	CallbackGrantCheck: true,
	CallbackLog:        true,
	CallbackProgress:   true,
	CallbackFetch:      true,
}

// InitializeParams is the handshake. Capabilities the plugin may use are decided
// by the host's manifest, not by this message: a plugin cannot negotiate itself a
// wider grant set than the installed manifest declares.
type InitializeParams struct {
	Protocol    string            `json:"protocol"`
	PluginID    string            `json:"pluginId"`
	TrustDomain string            `json:"trustDomain"`
	RequestID   string            `json:"requestId"`
	Env         map[string]string `json:"env,omitempty"`
}

// InitializeResult is what a plugin reports back at handshake.
type InitializeResult struct {
	Protocol     string   `json:"protocol"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// InvokeParams carries one capability call.
type InvokeParams struct {
	CapabilityID string          `json:"capabilityId"`
	RequestID    string          `json:"requestId"`
	Params       json.RawMessage `json:"params,omitempty"`
	TimeoutMS    int64           `json:"timeoutMs,omitempty"`
}

// InvokeResult is a capability's output. ErrorText is for domain failures the
// plugin understood; transport-level failures use the JSON-RPC error object.
type InvokeResult struct {
	Content   string `json:"content"`
	IsError   bool   `json:"isError,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	ErrorText string `json:"error,omitempty"`
}

// GrantCheckParams asks whether one mediated side effect is inside the approved
// ceiling for the capability currently executing.
type GrantCheckParams struct {
	Grant string `json:"grant"`
}

// GrantCheckResult reports the decision. DeniedReason is surfaced to the audit
// trail so a reviewer can see what a plugin tried to do.
type GrantCheckResult struct {
	Allowed      bool   `json:"allowed"`
	DeniedReason string `json:"deniedReason,omitempty"`
}

// codec is a line-delimited JSON-RPC framer. One message per line keeps the ABI
// trivially debuggable and matches how the existing stdio MCP bridge behaves.
type codec struct {
	mu   sync.Mutex
	w    io.Writer
	r    *bufio.Reader
	next atomic.Int64
}

func newCodec(r io.Reader, w io.Writer) *codec {
	return &codec{w: w, r: bufio.NewReaderSize(r, 1<<20)}
}

func (c *codec) write(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("pluginhost: encode: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return errors.New("pluginhost: refusing to send an unframed message")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// readLine returns one decoded frame. It is only called by the instance reader
// goroutine, which owns dispatch.
func (c *codec) readRaw() (json.RawMessage, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return nil, fmt.Errorf("pluginhost: blank frame")
	}
	return json.RawMessage(trimmed), nil
}

// nextID hands out request identifiers for host-initiated calls.
func (c *codec) nextID() int64 { return c.next.Add(1) }

// callTimeout bounds one plugin round trip when the capability declared none.
const callTimeout = 60 * time.Second

// withTimeout annotates a context for one plugin call.
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = callTimeout
	}
	return context.WithTimeout(ctx, timeout)
}
