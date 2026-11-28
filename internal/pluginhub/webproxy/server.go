// Package webproxy serves application UI on installation-specific origins.
package webproxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"

	"github.com/google/uuid"
	"golang.org/x/net/publicsuffix"
)

var ErrUnavailable = errors.New("plugin web origin unavailable")
var ErrForbidden = errors.New("plugin web origin or permission rejected")

const SessionPath = "/_csai_plugin/session"
const maxSessions = 1024
const ticketTTL = 45 * time.Second
const sessionTTL = 2 * time.Hour

type Target struct {
	Endpoint          string
	Route             string
	EntryPath         string
	Generation        int64
	ApplicationBearer []string
}

type Resolve func(context.Context, string) (Target, error)
type Authorize func(context.Context, string, string) bool

type Service struct {
	cfg             config.PluginProxyConfig
	db              *database.DB
	resolve         Resolve
	authorize       Authorize
	isPlatformToken func(string) bool
	mu              sync.Mutex
	listeners       map[string]*http.Server
	tickets         map[[32]byte]grant
	sessions        map[[32]byte]*browserSession
	tlsConfig       *tls.Config
	bindHost        string
	mainPort        int
	started         bool
}

type grant struct {
	install, parentOrigin, pluginOrigin, mainToken string
	generation                                     int64
	expires                                        time.Time
}

type browserSession struct {
	grant
	jar    http.CookieJar
	ctx    context.Context
	cancel context.CancelFunc
}

type Embed struct {
	Ticket    string    `json:"ticket"`
	Action    string    `json:"action"`
	Origin    string    `json:"origin"`
	ExpiresAt time.Time `json:"expires_at"`
}

func New(cfg config.PluginProxyConfig, db *database.DB, resolve Resolve, authorize Authorize) *Service {
	return &Service{cfg: cfg.Effective(), db: db, resolve: resolve, authorize: authorize, listeners: map[string]*http.Server{},
		tickets: map[[32]byte]grant{}, sessions: map[[32]byte]*browserSession{}}
}

// SetPlatformTokenCheck is configured during application assembly.
func (s *Service) SetPlatformTokenCheck(fn func(string) bool) { s.isPlatformToken = fn }

func (s *Service) Start(bindHost string, mainPort int, tlsConfig *tls.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.BindHost != "" {
		bindHost = s.cfg.BindHost
	}
	s.bindHost, s.mainPort, s.tlsConfig, s.started = bindHost, mainPort, tlsConfig, true
}

func (s *Service) isReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func (s *Service) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = false
	for id, server := range s.listeners {
		_ = server.Close()
		delete(s.listeners, id)
	}
	clear(s.tickets)
	for key, session := range s.sessions {
		session.cancel()
		delete(s.sessions, key)
	}
}

func (s *Service) Revoke(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, ticket := range s.tickets {
		if ticket.install == id {
			delete(s.tickets, key)
		}
	}
	for key, session := range s.sessions {
		if session.install == id {
			session.cancel()
			delete(s.sessions, key)
		}
	}
}

func (s *Service) Retire(ctx context.Context, id string) error {
	s.Revoke(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if listener := s.listeners[id]; listener != nil {
		_ = listener.Close()
		delete(s.listeners, id)
	}
	return s.db.RetirePluginWebOrigin(ctx, id)
}

func (s *Service) parentOrigin(r *http.Request) (string, error) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	expected := scheme + "://" + r.Host
	if s.cfg.PublicOrigin != "" {
		expected = s.cfg.PublicOrigin
	}
	normalized, err := canonicalOrigin(expected)
	if err != nil {
		return "", ErrForbidden
	}
	origins := r.Header.Values("Origin")
	if len(origins) > 1 {
		return "", ErrForbidden
	}
	if len(origins) == 1 {
		client, err := canonicalOrigin(origins[0])
		if err != nil || client != normalized {
			return "", ErrForbidden
		}
	}
	return normalized, nil
}

var dnsOrigin = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)

func canonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery ||
		(u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") {
		return "", ErrForbidden
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err != nil {
		if len(host) > 253 || !dnsOrigin.MatchString(host) {
			return "", ErrForbidden
		}
	} else if ip.Zone() != "" {
		return "", ErrForbidden
	}
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", ErrForbidden
		}
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}

func (s *Service) Issue(r *http.Request, id, token string, mainExpiry time.Time) (Embed, error) {
	ctx := r.Context()
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id || token == "" || !s.authorize(ctx, id, token) {
		return Embed{}, ErrForbidden
	}
	parent, err := s.parentOrigin(r)
	if err != nil {
		return Embed{}, err
	}
	target, err := s.resolve(ctx, id)
	if err != nil {
		return Embed{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return Embed{}, ErrUnavailable
	}
	now := time.Now()
	s.prune(now)
	if len(s.tickets)+len(s.sessions) >= maxSessions {
		return Embed{}, ErrUnavailable
	}
	origin, err := s.origin(ctx, id, parent)
	if err != nil {
		return Embed{}, err
	}
	tokenText, hash, err := opaque()
	if err != nil {
		return Embed{}, err
	}
	expires := now.Add(ticketTTL)
	if mainExpiry.Before(expires) {
		expires = mainExpiry
	}
	if !expires.After(now) {
		return Embed{}, ErrForbidden
	}
	s.tickets[hash] = grant{install: id, parentOrigin: parent, pluginOrigin: origin, generation: target.Generation, mainToken: token, expires: expires}
	return Embed{Ticket: tokenText, Action: origin + SessionPath, Origin: origin, ExpiresAt: expires}, nil
}

func (s *Service) origin(ctx context.Context, id, parent string) (string, error) {
	row, err := s.db.PluginWebOrigin(ctx, id)
	parentURL, _ := url.Parse(parent)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		row = database.PluginWebOrigin{InstallID: id, Mode: s.cfg.Mode}
		if s.cfg.Mode == "host" {
			row.Hostname = "p-" + id + "." + s.cfg.HostSuffix
			if row.Hostname == parentURL.Hostname() {
				return "", ErrForbidden
			}
		} else {
			rows, err := s.db.PluginWebOrigins(ctx)
			if err != nil {
				return "", err
			}
			used := map[int]bool{}
			for _, entry := range rows {
				if entry.Mode == "port" {
					used[entry.Port] = true
				}
			}
			parentPort := parentURL.Port()
			if parentPort == "" {
				parentPort = "80"
				if parentURL.Scheme == "https" {
					parentPort = "443"
				}
			}
			for port := s.cfg.PortPool.Start; port <= s.cfg.PortPool.End; port++ {
				if used[port] || port == s.mainPort || strconv.Itoa(s.externalPort(port)) == parentPort {
					continue
				}
				row.Port = port
				if err := s.listen(row); err == nil {
					break
				}
				row.Port = 0
			}
			if row.Port == 0 {
				return "", ErrUnavailable
			}
		}
		if err := s.db.SavePluginWebOrigin(ctx, row); err != nil {
			if srv := s.listeners[id]; srv != nil {
				_ = srv.Close()
				delete(s.listeners, id)
			}
			return "", err
		}
	case err != nil:
		return "", err
	case row.Retired != nil || row.Mode != s.cfg.Mode:
		return "", ErrUnavailable
	}
	host := row.Hostname
	if row.Mode == "port" {
		if row.Port < s.cfg.PortPool.Start || row.Port > s.cfg.PortPool.End || row.Port == s.mainPort {
			return "", ErrUnavailable
		}
		if err := s.listen(row); err != nil {
			return "", err
		}
		host = net.JoinHostPort(parentURL.Hostname(), strconv.Itoa(s.externalPort(row.Port)))
	} else if parentURL.Port() != "" {
		host = net.JoinHostPort(host, parentURL.Port())
	}
	origin, err := canonicalOrigin(parentURL.Scheme + "://" + host)
	if err != nil || origin == parent {
		return "", ErrForbidden
	}
	return origin, nil
}

func (s *Service) externalPort(port int) int {
	if mapped, ok := s.cfg.ExternalPorts[port]; ok {
		return mapped
	}
	return port
}

// listen runs with the origin lock held. Each port is scoped to its installation,
// even if two browser origins use the same host-only cookie namespace.
func (s *Service) listen(row database.PluginWebOrigin) error {
	if s.listeners[row.InstallID] != nil {
		return nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(s.bindHost, strconv.Itoa(row.Port)))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s.Handler(row.InstallID), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 32 << 10}
	if s.tlsConfig != nil {
		server.TLSConfig = s.tlsConfig.Clone()
	}
	s.listeners[row.InstallID] = server
	go func() {
		if server.TLSConfig != nil {
			_ = server.ServeTLS(ln, "", "")
		} else {
			_ = server.Serve(ln)
		}
	}()
	return nil
}

func (s *Service) Wrap(main http.Handler) http.Handler {
	if s.cfg.Mode != "host" {
		return main
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = name
		}
		host = strings.ToLower(host)
		label, ok := strings.CutSuffix(host, "."+s.cfg.HostSuffix)
		if !ok || !strings.HasPrefix(label, "p-") {
			main.ServeHTTP(w, r)
			return
		}
		id := strings.TrimPrefix(label, "p-")
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id {
			http.NotFound(w, r)
			return
		}
		s.Handler(id).ServeHTTP(w, r)
	})
}

func (s *Service) prune(now time.Time) {
	for key, ticket := range s.tickets {
		if !ticket.expires.After(now) {
			delete(s.tickets, key)
		}
	}
	for key, session := range s.sessions {
		if !session.expires.After(now) {
			session.cancel()
			delete(s.sessions, key)
		}
	}
}

func opaque() (string, [32]byte, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", [32]byte{}, err
	}
	value := base64.RawURLEncoding.EncodeToString(b[:])
	return value, sha256.Sum256([]byte(value)), nil
}

func cookieName(id string, secure bool) string {
	prefix := "csai_plugin_"
	if secure {
		prefix = "__Host-" + prefix
	}
	return prefix + id
}

func (s *Service) exchange(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil || len(r.PostForm["ticket"]) != 1 || len(r.URL.Query()) != 0 {
		http.Error(w, "invalid ticket", http.StatusBadRequest)
		return
	}
	hash := sha256.Sum256([]byte(r.PostForm.Get("ticket")))
	s.mu.Lock()
	now := time.Now()
	s.prune(now)
	ticket, ok := s.tickets[hash]
	if !ok || ticket.install != id || !matchesOrigin(r, ticket.pluginOrigin) || !requestOrigin(r, ticket.parentOrigin, true) {
		s.mu.Unlock()
		http.Error(w, "ticket rejected", http.StatusForbidden)
		return
	}
	delete(s.tickets, hash)
	s.mu.Unlock()
	if !s.authorize(r.Context(), id, ticket.mainToken) {
		http.Error(w, "ticket revoked", http.StatusForbidden)
		return
	}
	target, err := s.resolve(r.Context(), id)
	if err != nil || target.Generation != ticket.generation {
		http.Error(w, "ticket expired", http.StatusForbidden)
		return
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	raw, key, err := opaque()
	if err != nil {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionCtx, cancel := context.WithTimeout(context.Background(), sessionTTL)
	ticket.expires = now.Add(sessionTTL)
	s.mu.Lock()
	if !s.started || len(s.tickets)+len(s.sessions) >= maxSessions {
		s.mu.Unlock()
		cancel()
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	s.sessions[key] = &browserSession{grant: ticket, jar: jar, ctx: sessionCtx, cancel: cancel}
	s.mu.Unlock()
	secure := strings.HasPrefix(ticket.pluginOrigin, "https://")
	sameSite := http.SameSiteLaxMode
	if secure {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName(id, secure), Value: raw, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: sameSite, Partitioned: secure, MaxAge: int(sessionTTL.Seconds())})
	w.Header().Set("Cache-Control", "no-store")
	entry := target.EntryPath
	if entry == "" {
		entry = "/"
	}
	http.Redirect(w, r, routePrefix(id, target.Route)+entry, http.StatusSeeOther)
}

func matchesOrigin(r *http.Request, expected string) bool {
	u, err := url.Parse(expected)
	if err != nil {
		return false
	}
	actual, err := canonicalOrigin(u.Scheme + "://" + r.Host)
	return err == nil && actual == expected
}

func requestOrigin(r *http.Request, want string, required bool) bool {
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return !required
	}
	if len(values) != 1 {
		return false
	}
	origin, err := canonicalOrigin(values[0])
	return err == nil && origin == want
}
