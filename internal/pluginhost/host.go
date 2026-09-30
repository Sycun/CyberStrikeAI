package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/processguard"

	"go.uber.org/zap"
)

// Config describes one plugin instance: one trust domain runs one process, so two
// publishers never share an interpreter or its file descriptors.
type Config struct {
	// PluginID is the publisher namespace the instance serves, e.g. "acme".
	PluginID    string
	TrustDomain string
	Binary      string
	Args        []string
	WorkDir     string

	// AllowedEnvKeys is the only environment the child receives, plus the proxy
	// settings the host injects. Credentials are excluded structurally: the host
	// never copies os.Environ(), so an unset list still yields a minimal environment.
	AllowedEnvKeys []string

	// Grants is the capability manifest ceiling for this instance.
	Grants []string
	// Approved holds operator-approved egress tuples. With none, strict mode denies
	// all egress.
	Approved     []Tuple
	StrictEgress bool

	IdleTimeout   time.Duration
	CallTimeout   time.Duration
	StartTimeout  time.Duration
	MaxRestarts   int
	IsolationMode string
}

const defaultEnvKeys = "PATH,HOME,LANG,LC_ALL,TZ,TMPDIR,TMP,TEMP"

func (c *Config) withDefaults() {
	if c.TrustDomain == "" {
		c.TrustDomain = c.PluginID
	}
	if len(c.AllowedEnvKeys) == 0 {
		for _, key := range strings.Split(defaultEnvKeys, ",") {
			c.AllowedEnvKeys = append(c.AllowedEnvKeys, key)
		}
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 5 * time.Minute
	}
	if c.CallTimeout <= 0 {
		c.CallTimeout = callTimeout
	}
	if c.StartTimeout <= 0 {
		c.StartTimeout = 15 * time.Second
	}
	if c.MaxRestarts == 0 {
		c.MaxRestarts = 3
	}
}

// Validate rejects a configuration that could not be enforced.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.PluginID) == "" {
		return errors.New("pluginhost: PluginID is required")
	}
	if strings.TrimSpace(c.Binary) == "" {
		return fmt.Errorf("pluginhost: binary is required for plugin %s", c.PluginID)
	}
	if !path.IsAbs(c.Binary) {
		return fmt.Errorf("pluginhost: binary %q must be an absolute path, not resolved from the working directory", c.Binary)
	}
	if _, err := os.Stat(c.Binary); err != nil {
		return fmt.Errorf("pluginhost: binary %s is not runnable: %w", c.Binary, err)
	}
	return nil
}

// pendingCall is an in-flight host-initiated request.
type pendingCall struct {
	done chan *Response
}

// Instance owns one plugin process and its egress proxy.
type Instance struct {
	cfg Config

	mu        sync.Mutex
	startMu   sync.Mutex // serializes lazy startup
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	codec     *codec
	pending   map[int64]*pendingCall
	proxy     *Proxy
	group     processguard.Group
	launch    *processguard.Launch
	startedAt time.Time
	lastUsed  time.Time
	restarts  int
	closed    bool
	running   bool

	logger *zap.Logger
	wg     sync.WaitGroup
}

// NewInstance prepares but does not start the process: the first Invoke lazily
// boots the plugin, the same way an editor starts an extension host only when an
// extension is actually used.
func NewInstance(cfg Config, logger *zap.Logger) (*Instance, error) {
	cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Instance{cfg: cfg, logger: logger, pending: map[int64]*pendingCall{}}, nil
}

// Invoke runs one capability in the plugin and returns its result. A crashed
// plugin costs this call an error and is restarted for the next one; nothing in
// the host process can be taken down by plugin behavior.
func (i *Instance) Invoke(ctx context.Context, capabilityID string, params json.RawMessage, timeout time.Duration) (result *InvokeResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			err = fmt.Errorf("pluginhost: recovered while invoking %s: %v", capabilityID, recovered)
		}
	}()
	if timeout <= 0 {
		timeout = i.cfg.CallTimeout
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := i.ensureStarted(callCtx); err != nil {
		return nil, err
	}

	payload, err := json.Marshal(InvokeParams{
		CapabilityID: capabilityID,
		Params:       params,
		TimeoutMS:    timeout.Milliseconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("pluginhost: encode invoke params: %w", err)
	}

	response, err := i.roundTrip(callCtx, MethodInvoke, payload, timeout)
	if err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, response.Error
	}
	decoded := &InvokeResult{}
	if len(response.Result) > 0 {
		if err := json.Unmarshal(response.Result, decoded); err != nil {
			return nil, fmt.Errorf("pluginhost: decode invoke result: %w", err)
		}
	}
	i.markUsed()
	return decoded, nil
}

// roundTrip sends one request and waits for its correlated response.
func (i *Instance) roundTrip(ctx context.Context, method string, params json.RawMessage, timeout time.Duration) (*Response, error) {
	i.mu.Lock()
	if !i.running || i.codec == nil {
		i.mu.Unlock()
		return nil, ErrNotConfigured
	}
	codec := i.codec
	call := &pendingCall{done: make(chan *Response, 1)}
	id := codec.nextID()
	i.pending[id] = call
	i.mu.Unlock()

	request := Request{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := codec.write(request); err != nil {
		i.dropPending(id)
		return nil, fmt.Errorf("pluginhost: send %s: %w", method, err)
	}

	timer := time.NewTimer(timeout + 2*time.Second)
	defer timer.Stop()

	select {
	case response := <-call.done:
		return response, nil
	case <-ctx.Done():
		i.dropPending(id)
		i.restartAsync(fmt.Sprintf("%s timed out: %v", method, ctx.Err()))
		return nil, ctx.Err()
	case <-timer.C:
		i.dropPending(id)
		i.restartAsync(fmt.Sprintf("%s exceeded the hard deadline", method))
		return nil, fmt.Errorf("pluginhost: %s exceeded its hard deadline", method)
	}
}

func (i *Instance) dropPending(id int64) {
	i.mu.Lock()
	delete(i.pending, id)
	i.mu.Unlock()
}

// ensureStarted boots the process on demand and performs the ABI handshake.
func (i *Instance) ensureStarted(ctx context.Context) error {
	i.startMu.Lock()
	defer i.startMu.Unlock()

	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return errors.New("pluginhost: instance is closed")
	}
	if i.running {
		i.mu.Unlock()
		return nil
	}
	i.mu.Unlock()

	if err := i.start(ctx); err != nil {
		return err
	}

	params, err := json.Marshal(InitializeParams{
		Protocol:    ProtocolVersion,
		PluginID:    i.cfg.PluginID,
		TrustDomain: i.cfg.TrustDomain,
	})
	if err != nil {
		return err
	}
	response, err := i.roundTrip(ctx, MethodInitialize, params, i.cfg.StartTimeout)
	if err != nil {
		i.stop(err)
		return err
	}
	if response.Error != nil {
		i.stop(response.Error)
		return response.Error
	}
	handshake := InitializeResult{}
	if len(response.Result) > 0 {
		_ = json.Unmarshal(response.Result, &handshake)
	}
	if handshake.Protocol != ProtocolVersion {
		err := fmt.Errorf("%w: plugin reported %q, host speaks %q", ErrProtocolMismatch, handshake.Protocol, ProtocolVersion)
		i.stop(err)
		return err
	}
	i.markUsed()
	return nil
}

// start launches the child under processguard containment with a scrubbed
// environment and the egress proxy as its only network path.
func (i *Instance) start(ctx context.Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("pluginhost: recovered while starting %s: %v", i.cfg.PluginID, recovered)
			i.running = false
		}
	}()

	allowlist := NewAllowlist(i.cfg.Grants, i.cfg.Approved, i.cfg.StrictEgress)
	proxy, err := NewProxy(allowlist, WithAuditHook(func(target string, allowed bool, reason string) {
		i.logger.Warn("plugin egress decision",
			zap.String("plugin", i.cfg.PluginID),
			zap.String("target", target),
			zap.Bool("allowed", allowed),
			zap.String("reason", reason))
	}))
	if err != nil {
		return err
	}

	cmd := exec.Command(i.cfg.Binary, i.cfg.Args...)
	cmd.Dir = i.cfg.WorkDir
	cmd.Env = i.childEnv(proxy.ProxyEnv())

	group, err := processguard.New("plugin-" + i.cfg.PluginID)
	if err != nil {
		_ = proxy.Close()
		return fmt.Errorf("pluginhost: containment group: %w", err)
	}
	launch, err := group.Prepare(cmd)
	if err != nil {
		_ = proxy.Close()
		_ = group.Close(ctx)
		return fmt.Errorf("pluginhost: prepare: %w", err)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		launch.Dispose()
		_ = proxy.Close()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		launch.Dispose()
		_ = proxy.Close()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		launch.Dispose()
		_ = proxy.Close()
		return err
	}

	if err := cmd.Start(); err != nil {
		launch.Dispose()
		_ = proxy.Close()
		_ = group.Close(ctx)
		return fmt.Errorf("pluginhost: start %s: %w", i.cfg.Binary, err)
	}
	if err := launch.Commit(); err != nil {
		_ = cmd.Process.Kill()
		launch.Dispose()
		_ = proxy.Close()
		return fmt.Errorf("pluginhost: commit containment: %w", err)
	}

	i.mu.Lock()
	i.cmd = cmd
	i.stdin = stdin
	i.stdout = stdout
	i.codec = newCodec(stdout, stdin)
	i.proxy = proxy
	i.group = group
	i.launch = launch
	i.pending = map[int64]*pendingCall{}
	i.startedAt = time.Now()
	i.running = true
	i.mu.Unlock()

	i.wg.Add(3)
	go i.readLoop()
	go i.stderrLoop(stderr)
	go i.waitLoop(cmd, launch, group, proxy)
	return nil
}

// childEnv builds the child environment from the allowlist only. Nothing that
// looks like a credential is ever inherited, because inheritance is not the
// mechanism at all here.
func (i *Instance) childEnv(extra map[string]string) []string {
	env := make([]string, 0, len(i.cfg.AllowedEnvKeys)+len(extra))
	for _, key := range i.cfg.AllowedEnvKeys {
		if strings.Contains(strings.ToUpper(key), "KEY") ||
			strings.Contains(strings.ToUpper(key), "TOKEN") ||
			strings.Contains(strings.ToUpper(key), "SECRET") ||
			strings.Contains(strings.ToUpper(key), "PASSWORD") {
			i.logger.Warn("refusing to forward a credential-bearing environment key to a plugin",
				zap.String("plugin", i.cfg.PluginID), zap.String("key", key))
			continue
		}
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

// readLoop dispatches plugin frames: responses to waiters, callbacks handled here.
func (i *Instance) readLoop() {
	defer i.wg.Done()
	for {
		i.mu.Lock()
		codec := i.codec
		running := i.running
		i.mu.Unlock()
		if !running || codec == nil {
			return
		}

		raw, err := codec.readRaw()
		if err != nil {
			i.restartAsync("plugin stdout closed: " + err.Error())
			return
		}

		var probe struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			i.logger.Warn("dropping unparseable plugin frame", zap.String("plugin", i.cfg.PluginID))
			continue
		}

		if probe.Method != "" && probe.ID != nil {
			i.handleCallback(raw, probe.ID, probe.Method)
			continue
		}
		if probe.ID != nil {
			i.mu.Lock()
			call, ok := i.pending[*probe.ID]
			delete(i.pending, *probe.ID)
			i.mu.Unlock()
			if ok {
				call.done <- &Response{JSONRPC: "2.0", ID: *probe.ID, Result: probe.Result, Error: probe.Error}
			}
		}
	}
}

// handleCallback answers a plugin's request for a mediated side effect. The
// decision is the intersection of the manifest ceiling and operator approval, so
// a plugin cannot ask its way past either.
func (i *Instance) handleCallback(raw json.RawMessage, id *int64, method string) {
	response := &Response{JSONRPC: "2.0", ID: *id}
	defer func() {
		i.mu.Lock()
		codec := i.codec
		i.mu.Unlock()
		if codec != nil {
			_ = codec.write(response)
		}
	}()

	if !allowedCallbacks[method] {
		response.Error = &RPCError{Code: CodeMethodNotFound, Message: "callback method is not part of the ABI", Data: method}
		return
	}

	switch method {
	case CallbackGrantCheck:
		var params GrantCheckParams
		if err := json.Unmarshal(raw, &struct {
			Params *GrantCheckParams `json:"params"`
		}{Params: &params}); err != nil {
			response.Error = &RPCError{Code: CodeInvalidRequest, Message: "malformed grant check"}
			return
		}
		name, target := splitGrant(params.Grant)
		allowed, reason := i.grantAllowed(name, target)
		result, _ := json.Marshal(GrantCheckResult{Allowed: allowed, DeniedReason: reason})
		response.Result = result
		if !allowed {
			i.logger.Warn("plugin asked for a capability outside its ceiling",
				zap.String("plugin", i.cfg.PluginID), zap.String("grant", params.Grant), zap.String("reason", reason))
		}
	case CallbackFetch:
		response.Error = &RPCError{Code: CodeGrantRefused,
			Message: "host-side fetch must go through the injected egress proxy"}
	case CallbackLog, CallbackProgress:
		// Accepted and ignored for now; the audit plumbing is the caller's concern.
		result, _ := json.Marshal(map[string]bool{"ok": true})
		response.Result = result
	default:
		response.Error = &RPCError{Code: CodeMethodNotFound, Message: "unhandled callback"}
	}
}

func (i *Instance) grantAllowed(name, target string) (bool, string) {
	switch name {
	case "net.connect":
		if i.proxy == nil {
			return false, "egress proxy is not running"
		}
		host := target
		if !strings.Contains(host, ":") {
			host += ":443"
		}
		return i.proxy.allowlist.Allow(host, "CONNECT")
	case "process.exec", "fs.read", "fs.write", "db.query", "c2.exec", "c2.list_sessions", "progress.emit":
		for _, grant := range i.cfg.Grants {
			grantName, grantTarget := splitGrant(grant)
			if grantName != name {
				continue
			}
			if grantTarget == "" || grantTarget == "*" || grantTarget == target || target == "" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%s(%s) is not declared by this capability's manifest", name, target)
	default:
		return false, fmt.Sprintf("unknown mediated capability %q", name)
	}
}

func (i *Instance) stderrLoop(reader io.Reader) {
	defer i.wg.Done()
	buffer := make([]byte, 4096)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			i.logger.Info("plugin stderr",
				zap.String("plugin", i.cfg.PluginID),
				zap.String("line", strings.TrimSpace(string(buffer[:n]))))
		}
		if err != nil {
			return
		}
	}
}

// waitLoop reaps the child. A plugin crash is an event with an audit line, never a
// fault in this process.
func (i *Instance) waitLoop(cmd *exec.Cmd, launch *processguard.Launch, group processguard.Group, proxy *Proxy) {
	defer i.wg.Done()
	err := cmd.Wait()
	if launch != nil {
		launch.Dispose()
	}
	if group != nil {
		_ = group.Close(context.Background())
	}
	if proxy != nil {
		_ = proxy.Close()
	}

	i.mu.Lock()
	if cmd.Process != nil {
		_ = group.Release(cmd.Process.Pid)
	}
	wasRunning := i.running
	i.running = false
	i.cmd = nil
	i.codec = nil
	i.proxy = nil
	ids := make([]int64, 0, len(i.pending))
	for id := range i.pending {
		ids = append(ids, id)
	}
	for _, id := range ids {
		i.pending[id].done <- &Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: CodeUnavailable, Message: "plugin process exited"}}
		delete(i.pending, id)
	}
	i.mu.Unlock()

	if wasRunning {
		if err != nil {
			i.logger.Warn("plugin process exited abnormally; next call will restart it",
				zap.String("plugin", i.cfg.PluginID), zap.Error(err))
		} else {
			i.logger.Info("plugin process exited cleanly", zap.String("plugin", i.cfg.PluginID))
		}
	}
}

// restartAsync tears the instance down so the next Invoke starts a fresh process.
func (i *Instance) restartAsync(reason string) {
	i.mu.Lock()
	cmd := i.cmd
	i.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	i.logger.Warn("scheduling plugin restart", zap.String("plugin", i.cfg.PluginID), zap.String("reason", reason))
}

func (i *Instance) markUsed() {
	i.mu.Lock()
	i.lastUsed = time.Now()
	i.mu.Unlock()
}

// IdleSince reports how long the instance has been unused, for the idle reaper.
func (i *Instance) IdleSince(now time.Time) time.Duration {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.running {
		return time.Duration(i.cfg.IdleTimeout)
	}
	if i.lastUsed.IsZero() {
		return now.Sub(i.startedAt)
	}
	return now.Sub(i.lastUsed)
}

// Running reports whether a child process is currently attached.
func (i *Instance) Running() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.running
}

func (i *Instance) stop(reason error) {
	i.mu.Lock()
	cmd := i.cmd
	running := i.running
	if running {
		// Mark the instance down before killing: the reaper goroutine clears the
		// rest, and callers asking "is this usable now" must not see a live flag on a
		// process we just decided to terminate.
		i.running = false
	}
	i.mu.Unlock()
	if !running {
		return
	}
	if reason != nil {
		i.logger.Warn("stopping plugin instance", zap.String("plugin", i.cfg.PluginID), zap.Error(reason))
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// Close shuts the instance down permanently.
func (i *Instance) Close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	cmd := i.cmd
	proxy := i.proxy
	i.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_, _ = i.roundTripSafe(MethodShutdown, nil)
		_ = cmd.Process.Kill()
	}
	if proxy != nil {
		_ = proxy.Close()
	}
	i.wg.Wait()
	return nil
}

func (i *Instance) roundTripSafe(method string, params json.RawMessage) (*Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return i.roundTrip(ctx, method, params, 2*time.Second)
}

// Stats is the audit surface for one instance.
type Stats struct {
	PluginID    string
	Running     bool
	Restarts    int
	IdleSince   time.Duration
	Grants      []string
	ProxyAddr   string
	ApprovedOut time.Time
}

// Snapshot reports instance state for the monitor page.
func (i *Instance) Snapshot() Stats {
	i.mu.Lock()
	defer i.mu.Unlock()
	stats := Stats{PluginID: i.cfg.PluginID, Running: i.running, Restarts: i.restarts, Grants: i.cfg.Grants}
	if i.proxy != nil {
		stats.ProxyAddr = i.proxy.Addr()
	}
	if i.running {
		reference := i.lastUsed
		if reference.IsZero() {
			reference = i.startedAt
		}
		stats.IdleSince = time.Since(reference)
	}
	if len(i.cfg.Approved) > 0 {
		stats.ApprovedOut = i.cfg.Approved[0].Expires
	}
	return stats
}
