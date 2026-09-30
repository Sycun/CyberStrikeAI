package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tuple is the unit an operator approves: a target, a port set, a method, and a
// validity window. Store artifacts can request a grant, but the effective
// allowlist is the intersection of what the manifest declares and what a human
// approved - a submission can never widen the approved scope.
type Tuple struct {
	Host    string
	Ports   []int
	Method  string
	Expires time.Time
}

// matches reports whether this tuple permits the requested destination.
func (t Tuple) matches(host, method string, port int) bool {
	if !t.matchHost(host) {
		return false
	}
	if t.Method != "" && t.Method != "*" && !strings.EqualFold(t.Method, method) {
		return false
	}
	if len(t.Ports) > 0 {
		allowed := false
		for _, candidate := range t.Ports {
			if candidate == port {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	if !t.Expires.IsZero() && time.Now().After(t.Expires) {
		return false
	}
	return true
}

func (t Tuple) matchHost(host string) bool {
	need := strings.ToLower(strings.TrimSpace(t.Host))
	got := strings.ToLower(strings.TrimSpace(host))
	if need == "" || got == "" {
		return false
	}
	if need == "*" {
		return true
	}
	if strings.HasPrefix(need, ".") {
		return got == need[1:] || strings.HasSuffix(got, need)
	}
	return need == got
}

// Allowlist is the effective egress ceiling for one plugin instance.
type Allowlist struct {
	mu     sync.RWMutex
	grants []string // from the capability manifest, e.g. "net.connect(target)"
	tuples []Tuple  // approved by an operator, time-bounded
	strict bool     // when true, an empty tuple set denies everything
}

// NewAllowlist builds a ceiling from declared grants plus approved tuples. With no
// approved tuples and strict mode on, everything is denied: fail closed.
func NewAllowlist(grants []string, tuples []Tuple, strict bool) *Allowlist {
	return &Allowlist{grants: grants, tuples: tuples, strict: strict}
}

// Allow reports whether host:port may be reached with the given method.
func (a *Allowlist) Allow(hostPort, method string) (bool, string) {
	host, port, err := splitHostPort(hostPort)
	if err != nil {
		return false, err.Error()
	}
	a.mu.RLock()
	defer a.mu.RUnlock()

	if !a.declaredAllows(host) {
		return false, fmt.Sprintf("capability manifest declares no net.connect grant covering %s", host)
	}
	for _, tuple := range a.tuples {
		if tuple.matches(host, method, port) {
			return true, ""
		}
	}
	if len(a.tuples) == 0 && !a.strict {
		return true, ""
	}
	return false, fmt.Sprintf("%s is outside the operator-approved target set", hostPort)
}

// declaredAllows checks the manifest side of the intersection. A grant target of
// "target" or "*" means the capability may reach whatever the approval allows; a
// specific target narrows it further.
func (a *Allowlist) declaredAllows(host string) bool {
	if len(a.grants) == 0 {
		return false
	}
	for _, grant := range a.grants {
		name, target := splitGrant(grant)
		if name != "net.connect" {
			continue
		}
		switch strings.TrimSpace(target) {
		case "", "*", "target":
			return true
		default:
			if tupleHost(target).matchHost(host) {
				return true
			}
		}
	}
	return false
}

func tupleHost(host string) Tuple { return Tuple{Host: host} }

func splitGrant(grant string) (name, target string) {
	open := strings.Index(grant, "(")
	if open < 0 {
		return strings.TrimSpace(grant), ""
	}
	name = strings.TrimSpace(grant[:open])
	target = strings.TrimSpace(strings.TrimSuffix(grant[open+1:], ")"))
	return name, target
}

func splitHostPort(hostPort string) (string, int, error) {
	host, portString, err := net.SplitHostPort(strings.TrimSpace(hostPort))
	if err != nil {
		// A bare host means the default for the scheme, decided by the caller.
		return strings.TrimSuffix(hostPort, ":"), 0, nil
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q: %w", portString, err)
	}
	return host, port, nil
}

// SetApproved replaces the approved tuple set, e.g. after a HITL decision binds a
// narrower scope or an operator revokes it.
func (a *Allowlist) SetApproved(tuples []Tuple) {
	a.mu.Lock()
	a.tuples = tuples
	a.mu.Unlock()
}

// ErrEgressDenied is what the proxy returns for a refusal.
var ErrEgressDenied = errors.New("pluginhost: egress denied by capability allowlist")

// Proxy is the host-side CONNECT/absolute-form forward proxy a plugin is pointed at
// for network access.
//
// Scope limit worth stating plainly: this enforces the approved tuple set for
// traffic that uses the injected proxy settings. It is not by itself an OS-level
// network boundary - a plugin that opens raw sockets can reach loopback without
// asking (the standard library does exactly this), and Go never proxies loopback.
// Making the ceiling unconditional needs a network namespace per instance, which
// is Linux-only and privileged; processguard currently provides cgroup/rlimit
// containment only. Until that lands, treat the proxy as mediation-plus-audit and
// keep the real privilege boundary in the capability grants the host will honour
// on the plugin's behalf.
type Proxy struct {
	server    *http.Server
	allowlist *Allowlist
	listener  net.Listener
	audit     func(target string, allowed bool, reason string)

	mu       sync.Mutex
	lastAddr string
}

// ProxyOption configures a Proxy.
type ProxyOption func(*Proxy)

// WithAuditHook records every egress decision, allowed or not.
func WithAuditHook(fn func(target string, allowed bool, reason string)) ProxyOption {
	return func(p *Proxy) { p.audit = fn }
}

// NewProxy starts a listener on 127.0.0.1 with an ephemeral port.
func NewProxy(allowlist *Allowlist, opts ...ProxyOption) (*Proxy, error) {
	if allowlist == nil {
		return nil, errors.New("pluginhost: proxy requires an allowlist")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("pluginhost: listen egress proxy: %w", err)
	}
	p := &Proxy{allowlist: allowlist, listener: listener}
	for _, opt := range opts {
		opt(p)
	}

	// Not a ServeMux: a CONNECT request carries no URL path, so pattern matching by
	// path would answer 404 before the policy was ever consulted.
	p.server = &http.Server{Handler: http.HandlerFunc(p.handle), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		if err := p.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.record("", false, fmt.Sprintf("proxy server stopped: %v", err))
		}
	}()

	p.mu.Lock()
	p.lastAddr = listener.Addr().String()
	p.mu.Unlock()
	return p, nil
}

// Addr is the proxy address to hand the child process.
func (p *Proxy) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAddr
}

// Close stops the proxy. A plugin left with a dead proxy simply fails its next
// request; it cannot reach the network by any other route.
func (p *Proxy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}

func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	// Absolute-form request: only meaningful when it came through a proxy.
	if r.URL == nil || r.URL.Host == "" {
		p.record(r.RemoteAddr, false, "request without an absolute target")
		http.Error(w, ErrEgressDenied.Error(), http.StatusForbidden)
		return
	}
	target := r.URL.Host
	allowed, reason := p.allow(target, r.Method)
	if !allowed {
		p.record(target, false, reason)
		http.Error(w, ErrEgressDenied.Error(), http.StatusForbidden)
		return
	}
	p.record(target, true, "")
	// The host performs the request so credentials stay in the core; plugins get
	// the response body only.
	outRequest, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for key, values := range r.Header {
		if isHopByHop(key) || strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") {
			continue
		}
		for _, value := range values {
			outRequest.Header.Add(key, value)
		}
	}
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(outRequest)
	if err != nil {
		http.Error(w, fmt.Sprintf("host-side request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	allowed, reason := p.allow(target, "CONNECT")
	if !allowed {
		p.record(target, false, reason)
		http.Error(w, ErrEgressDenied.Error(), http.StatusForbidden)
		return
	}
	p.record(target, true, "")

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	upstream, err := dialer.DialContext(r.Context(), "tcp", target)
	if err != nil {
		p.record(target, false, fmt.Sprintf("dial failed: %v", err))
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}

	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(upstream, client) }()
	go func() { defer wg.Done(); _, _ = io.Copy(client, upstream) }()
	wg.Wait()
	_ = upstream.Close()
	_ = client.Close()
}

func (p *Proxy) allow(target, method string) (bool, string) {
	host := target
	if _, _, err := splitHostPort(target); err == nil {
		host = target
	}
	if !strings.Contains(host, ":") {
		host = host + ":443"
	}
	return p.allowlist.Allow(host, method)
}

func (p *Proxy) record(target string, allowed bool, reason string) {
	if p.audit == nil {
		return
	}
	p.audit(target, allowed, reason)
}

func isHopByHop(header string) bool {
	switch strings.ToLower(header) {
	case "connection", "proxy-connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade", "host", "proxy-authorization":
		return true
	}
	return false
}

// ProxyEnv is the environment the child needs to route through the proxy. Nothing
// else is passed: no API keys, no session tokens, no database paths.
func (p *Proxy) ProxyEnv() map[string]string {
	addr := p.Addr()
	return map[string]string{
		"HTTP_PROXY":  "http://" + addr,
		"HTTPS_PROXY": "http://" + addr,
		"ALL_PROXY":   "http://" + addr,
		"NO_PROXY":    "",
	}
}
