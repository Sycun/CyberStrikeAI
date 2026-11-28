// Package llmgateway exposes scoped model protocol transport for approved plugin
// instances. Client-defined function calls are data, and are never run here.
package llmgateway

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub/manifest"

	"github.com/google/uuid"
)

var ErrBinding = errors.New("invalid or unavailable plugin model channel")
var ErrUnavailable = errors.New("plugin model gateway unavailable")
var errQuota = errors.New("plugin model request quota exhausted")

type ConfigSnapshot func() (*config.Config, error)

type Service struct {
	cfg      config.PluginLLMGatewayConfig
	egress   config.PluginEgress
	db       *database.DB
	mu       sync.Mutex
	snapshot ConfigSnapshot
	server   *http.Server
	port     int
	bridgeIP string
	client   *http.Client
	active   map[string]map[string]context.CancelFunc
	windows  map[string]requestWindow
}

type requestWindow struct {
	minute   int64
	requests int
}

type LimitPatch struct {
	Concurrency int   `json:"concurrency"`
	RPM         int   `json:"rpm"`
	TokenBudget int64 `json:"token_budget"`
}

type BindingPatch struct {
	Protocol  string `json:"protocol,omitempty"`
	Mode      string `json:"mode"`
	ChannelID string `json:"channel_id,omitempty"`
}

type Patch struct {
	BindingPatch
	Bindings []BindingPatch `json:"bindings,omitempty"`
	Limits   LimitPatch     `json:"limits,omitempty"`
}

type Credential struct {
	Binding manifest.Binding
	Values  map[string]string
}

func New(db *database.DB, cfg config.PluginLLMGatewayConfig, egress config.PluginEgress) *Service {
	s := &Service{cfg: cfg.Effective(), egress: egress, db: db, active: map[string]map[string]context.CancelFunc{}, windows: map[string]requestWindow{}}
	s.client = &http.Client{Transport: &http.Transport{Proxy: s.outboundProxy, MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second,
		ResponseHeaderTimeout: 45 * time.Second, TLSHandshakeTimeout: 10 * time.Second, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return s
}

type ChannelOption struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Protocol  string `json:"protocol"`
	Model     string `json:"model"`
	IsDefault bool   `json:"is_default"`
}

func (s *Service) AvailableChannels() (string, []ChannelOption) {
	cfg, err := s.configuration()
	if err != nil {
		return "", []ChannelOption{}
	}
	defaultID := strings.TrimSpace(cfg.AI.DefaultChannel)
	options := make([]ChannelOption, 0, len(cfg.AI.Channels)+1)
	if len(cfg.AI.Channels) > 0 {
		ids := make([]string, 0, len(cfg.AI.Channels))
		for id := range cfg.AI.Channels {
			ids = append(ids, id)
		}
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				if ids[j] < ids[i] {
					ids[i], ids[j] = ids[j], ids[i]
				}
			}
		}
		for _, id := range ids {
			ch := cfg.AI.Channels[id]
			if strings.TrimSpace(ch.BaseURL) == "" || strings.TrimSpace(ch.Model) == "" {
				continue
			}
			name := strings.TrimSpace(ch.Name)
			if name == "" {
				name = id
			}
			provider := strings.TrimSpace(ch.Provider)
			if provider == "" {
				provider = "openai"
			}
			options = append(options, ChannelOption{
				ID:        id,
				Name:      name,
				Provider:  provider,
				Protocol:  providerProtocol(provider),
				Model:     ch.Model,
				IsDefault: id == defaultID,
			})
		}
	}
	if len(options) == 0 && strings.TrimSpace(cfg.OpenAI.BaseURL) != "" && strings.TrimSpace(cfg.OpenAI.Model) != "" {
		provider := strings.TrimSpace(cfg.OpenAI.Provider)
		if provider == "" {
			provider = "openai"
		}
		options = append(options, ChannelOption{
			ID:        "default",
			Name:      "Default",
			Provider:  provider,
			Protocol:  providerProtocol(provider),
			Model:     cfg.OpenAI.Model,
			IsDefault: true,
		})
		defaultID = "default"
	}
	return defaultID, options
}

// EnsureDefaultBinding automatically attaches CSAI's default LLM channel to a
// newly installed LLM-capable plugin when the default channel's protocol matches
// one of the manifest's declared bindings. Missing or incompatible default
// channels are ignored so installation still succeeds cleanly.
func (s *Service) EnsureDefaultBinding(ctx context.Context, id string, declared *manifest.LLM) (bool, error) {
	if declared == nil || len(declared.Bindings) == 0 {
		return false, nil
	}
	existing, err := s.db.PluginLLMBindings(ctx, id)
	if err != nil {
		return false, err
	}
	if len(existing) > 0 {
		return false, nil
	}
	cfg, err := s.configuration()
	if err != nil {
		return false, nil
	}
	selected, _, err := s.selection(cfg, database.PluginLLMBinding{Mode: "follow_default"})
	if err != nil {
		return false, nil
	}
	protocol := providerProtocol(selected.Provider)
	matched := false
	for _, b := range declared.Bindings {
		if b.Protocol == protocol {
			matched = true
			break
		}
	}
	if !matched {
		return false, nil
	}
	binding := database.PluginLLMBinding{
		Protocol:    protocol,
		Mode:        "follow_default",
		Concurrency: s.cfg.Concurrency,
		RPM:         s.cfg.RPM,
		TokenBudget: s.cfg.TokenBudget,
	}
	if err := s.db.SetPluginLLMBindings(ctx, id, []database.PluginLLMBinding{binding}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) SetSnapshot(snapshot ConfigSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = snapshot
}

func (s *Service) configuration() (*config.Config, error) {
	s.mu.Lock()
	fn := s.snapshot
	s.mu.Unlock()
	if fn == nil {
		return nil, ErrUnavailable
	}
	cfg, err := fn()
	if err != nil || cfg == nil {
		return nil, ErrUnavailable
	}
	return cfg, nil
}

func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(s.cfg.ListenHost, strconv.Itoa(s.cfg.ListenPort)))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.server = server
	go func() { _ = server.Serve(ln) }()
	// Rootful Linux Docker routes host-gateway to docker0. Bind this private
	// interface separately while retaining the loopback listener for native apps.
	if runtime.GOOS == "linux" && net.ParseIP(s.cfg.ListenHost).IsLoopback() &&
		(s.cfg.ContainerHost == "" || s.cfg.ContainerHost == "host.docker.internal") {
		if ip := dockerBridgeIP(); ip != "" {
			if bridge, err := net.Listen("tcp4", net.JoinHostPort(ip, strconv.Itoa(s.port))); err == nil {
				s.bridgeIP = ip
				go func() { _ = server.Serve(bridge) }()
			}
		}
	}
	return nil
}

func dockerBridgeIP() string {
	link, err := net.InterfaceByName("docker0")
	if err != nil {
		return ""
	}
	addrs, err := link.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil && ipNet.IP.IsPrivate() {
			return ipNet.IP.String()
		}
	}
	return ""
}

func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.active {
		s.cancelActiveLocked(id)
	}
	if s.server != nil {
		_ = s.server.Close()
		s.server = nil
		s.port = 0
		s.bridgeIP = ""
	}
}

func (s *Service) selection(cfg *config.Config, b database.PluginLLMBinding) (config.OpenAIConfig, string, error) {
	id := b.ChannelID
	switch b.Mode {
	case "follow_default":
		id = strings.TrimSpace(cfg.AI.DefaultChannel)
	case "channel":
		if strings.TrimSpace(id) == "" {
			return config.OpenAIConfig{}, "", ErrBinding
		}
	default:
		return config.OpenAIConfig{}, "", ErrBinding
	}
	channel, resolved, ok := cfg.AI.ResolveChannel(id)
	if !ok || strings.TrimSpace(channel.BaseURL) == "" || strings.TrimSpace(channel.Model) == "" ||
		(b.Protocol != "" && providerProtocol(channel.Provider) != b.Protocol) {
		return config.OpenAIConfig{}, "", ErrBinding
	}
	return channel, resolved, nil
}

func providerProtocol(provider string) string {
	if strings.EqualFold(provider, "claude") || strings.EqualFold(provider, "anthropic") {
		return manifest.Anthropic
	}
	return manifest.OpenAI
}

func (s *Service) Patch(ctx context.Context, id string, declared *manifest.LLM, patch Patch) ([]database.PluginLLMBinding, error) {
	if declared == nil || len(declared.Bindings) == 0 {
		return nil, ErrBinding
	}
	limit := patch.Limits
	if limit.Concurrency == 0 {
		limit.Concurrency = s.cfg.Concurrency
	}
	if limit.RPM == 0 {
		limit.RPM = s.cfg.RPM
	}
	if limit.TokenBudget == 0 {
		limit.TokenBudget = s.cfg.TokenBudget
	}
	if limit.Concurrency < 1 || limit.Concurrency > s.cfg.Concurrency || limit.RPM < 1 || limit.RPM > s.cfg.RPM || limit.TokenBudget < 1 || limit.TokenBudget > s.cfg.TokenBudget {
		return nil, ErrBinding
	}
	items := patch.Bindings
	if len(items) == 0 {
		items = []BindingPatch{patch.BindingPatch}
	} else if patch.BindingPatch != (BindingPatch{}) {
		return nil, ErrBinding
	}
	if len(items) > len(declared.Bindings) || declared.Selection == "single_active" && len(items) != 1 {
		return nil, ErrBinding
	}
	result := make([]database.PluginLLMBinding, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		b := database.PluginLLMBinding{Protocol: item.Protocol, Mode: item.Mode, ChannelID: item.ChannelID, Concurrency: limit.Concurrency, RPM: limit.RPM, TokenBudget: limit.TokenBudget}
		switch item.Mode {
		case "disabled":
			if len(items) != 1 || item.ChannelID != "" {
				return nil, ErrBinding
			}
			b.Protocol = ""
		case "channel", "follow_default":
			if item.Mode == "follow_default" && item.ChannelID != "" {
				return nil, ErrBinding
			}
			cfg, err := s.configuration()
			if err != nil {
				return nil, err
			}
			selected, resolved, err := s.selection(cfg, b)
			if err != nil {
				return nil, err
			}
			b.Protocol = providerProtocol(selected.Provider)
			if item.Mode == "channel" {
				b.ChannelID = resolved
			}
			allowed := false
			for _, binding := range declared.Bindings {
				if binding.Protocol == b.Protocol {
					allowed = true
					break
				}
			}
			if !allowed || seen[b.Protocol] {
				return nil, ErrBinding
			}
		default:
			return nil, ErrBinding
		}
		seen[b.Protocol] = true
		result = append(result, b)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.SetPluginLLMBindings(ctx, id, result); err != nil {
		return nil, err
	}
	s.cancelActiveLocked(id)
	return result, nil
}

func (s *Service) cancelActiveLocked(id string) {
	for _, cancel := range s.active[id] {
		cancel()
	}
	delete(s.active, id)
}

func (s *Service) Revoke(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelActiveLocked(id)
	return s.db.RevokePluginGatewayKeys(ctx, id)
}

func (s *Service) addressesLocked(container bool) (string, string, error) {
	port := s.port
	if port == 0 {
		return "", "", ErrUnavailable
	}
	host := s.cfg.ListenHost
	addHost := ""
	if container {
		host = s.cfg.ContainerHost
		if host == "" {
			if runtime.GOOS == "linux" {
				if net.ParseIP(s.cfg.ListenHost).IsLoopback() {
					if s.bridgeIP == "" {
						return "", "", ErrUnavailable
					}
					host = "host.docker.internal"
				} else {
					host = s.cfg.ListenHost
				}
			} else {
				host = "host.docker.internal"
			}
		}
		if host == "host.docker.internal" && runtime.GOOS == "linux" {
			if net.ParseIP(s.cfg.ListenHost).IsLoopback() && s.bridgeIP == "" {
				return "", "", ErrUnavailable
			}
			addHost = "host.docker.internal:host-gateway"
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)), addHost, nil
}

func (s *Service) Issue(ctx context.Context, id string, generation int64, declared *manifest.LLM, container bool) ([]Credential, string, string, error) {
	if declared == nil {
		return nil, "", "", nil
	}
	rows, err := s.db.PluginLLMBindings(ctx, id)
	if err != nil {
		return nil, "", "", err
	}
	var result []Credential
	root, hostMapping := "", ""
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		if row.Mode == "disabled" {
			continue
		}
		var binding *manifest.Binding
		for _, item := range declared.Bindings {
			if item.Protocol == row.Protocol {
				binding = &item
				break
			}
		}
		if binding == nil {
			return nil, "", "", ErrBinding
		}
		if s.snapshot == nil {
			return nil, "", "", ErrUnavailable
		}
		cfg, err := s.snapshot()
		if err != nil {
			return nil, "", "", ErrUnavailable
		}
		channel, _, err := s.selection(cfg, row)
		if err != nil {
			return nil, "", "", err
		}
		if root == "" {
			root, hostMapping, err = s.addressesLocked(container)
			if err != nil {
				return nil, "", "", err
			}
		}
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, "", "", err
		}
		token := "csai_p_" + base64.RawURLEncoding.EncodeToString(b[:])
		if err := s.db.SavePluginGatewayKey(ctx, id, generation, row.Protocol, manifest.Digest([]byte(token))); err != nil {
			return nil, "", "", err
		}
		provider := binding.ProviderValue
		if provider == "" {
			provider = channel.Provider
			if provider == "" {
				provider = "openai"
			}
		}
		result = append(result, Credential{Binding: *binding, Values: map[string]string{
			"gateway_v1_url": root + "/v1", "gateway_root_url": root, "gateway_token": token,
			"model_alias": "csai-managed", "provider": provider, "protocol": row.Protocol,
		}})
	}
	return result, root, hostMapping, nil
}

func (s *Service) admit(ctx context.Context, k database.PluginGatewayKey, digest string) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.db.PluginGatewayKey(ctx, digest)
	if err != nil || current.InstallID != k.InstallID || current.Generation != k.Generation {
		return nil, nil, ErrBinding
	}
	now := time.Now().Unix() / 60
	window := s.windows[k.InstallID]
	if window.minute != now {
		window = requestWindow{minute: now}
	}
	if window.requests >= k.Binding.RPM || len(s.active[k.InstallID]) >= k.Binding.Concurrency {
		return nil, nil, errQuota
	}
	window.requests++
	s.windows[k.InstallID] = window
	request, cancel := context.WithCancel(ctx)
	if s.active[k.InstallID] == nil {
		s.active[k.InstallID] = map[string]context.CancelFunc{}
	}
	id := uuid.NewString()
	s.active[k.InstallID][id] = cancel
	return request, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		cancel()
		delete(s.active[k.InstallID], id)
		if len(s.active[k.InstallID]) == 0 {
			delete(s.active, k.InstallID)
		}
	}, nil
}

func (s *Service) outboundProxy(r *http.Request) (*url.URL, error) {
	mode, address := s.egress.ProxyMode, s.egress.ProxyURL
	if mode == "direct" {
		return nil, nil
	}
	if mode == "" {
		cfg, err := s.configuration()
		if err != nil {
			return nil, ErrUnavailable
		}
		if cfg.Proxy.Mode == "fixed" {
			mode, address = "fixed", cfg.Proxy.URL
		}
	}
	if mode == "fixed" {
		if !strings.Contains(address, "://") {
			address = "http://" + address
		}
		return url.Parse(address)
	}
	return http.ProxyFromEnvironment(r)
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/_csai_gateway/ready" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	wanted := ""
	switch {
	case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
	case r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost:
		wanted = manifest.OpenAI
	case (r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/messages/count_tokens") && r.Method == http.MethodPost:
		wanted = manifest.Anthropic
	default:
		http.NotFound(w, r)
		return
	}
	token, ok := gatewayToken(r)
	if !ok {
		gatewayError(w, http.StatusUnauthorized, "invalid_plugin_token")
		return
	}
	digest := manifest.Digest([]byte(token))
	key, err := s.db.PluginGatewayKey(r.Context(), digest)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			gatewayError(w, http.StatusUnauthorized, "invalid_plugin_token")
		} else {
			gatewayError(w, http.StatusServiceUnavailable, "gateway_unavailable")
		}
		return
	}
	if wanted != "" && key.Binding.Protocol != wanted {
		gatewayError(w, http.StatusForbidden, "protocol_mismatch")
		return
	}
	cfg, err := s.configuration()
	if err != nil {
		gatewayError(w, http.StatusServiceUnavailable, "gateway_unavailable")
		return
	}
	channel, channelID, err := s.selection(cfg, key.Binding)
	if err != nil {
		gatewayError(w, http.StatusForbidden, "channel_unavailable")
		return
	}
	ctx, release, err := s.admit(r.Context(), key, digest)
	if err != nil {
		if errors.Is(err, errQuota) {
			gatewayError(w, http.StatusTooManyRequests, "plugin_rate_limit")
		} else {
			gatewayError(w, http.StatusUnauthorized, "invalid_plugin_token")
		}
		return
	}
	defer release()
	if r.URL.Path == "/v1/models" {
		models(w, key.Binding.Protocol)
		return
	}
	s.forward(ctx, w, r, key, channel, channelID)
}

func gatewayToken(r *http.Request) (string, bool) {
	token := strings.TrimSpace(r.Header.Get("x-api-key"))
	if values := r.Header.Values("Authorization"); len(values) > 0 {
		if len(values) != 1 {
			return "", false
		}
		scheme, value, ok := strings.Cut(values[0], " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token != "" {
			return "", false
		}
		token = strings.TrimSpace(value)
	}
	return token, len(token) == 50 && strings.HasPrefix(token, "csai_p_") && len(r.Header.Values("x-api-key")) <= 1
}
