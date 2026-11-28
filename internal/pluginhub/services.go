package pluginhub

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub/llmgateway"
	"cyberstrike-ai/internal/pluginhub/manifest"
	"cyberstrike-ai/internal/pluginhub/runner"
	"cyberstrike-ai/internal/pluginhub/webproxy"
	"cyberstrike-ai/internal/security"
)

func (m *Manager) ConfigureServices(auth *security.AuthManager, snapshot llmgateway.ConfigSnapshot) {
	m.webAuth = auth
	if auth != nil {
		m.web.SetPlatformTokenCheck(func(raw string) bool { _, ok := auth.ValidateToken(raw); return ok })
	}
	m.gateway.SetSnapshot(snapshot)
}

func (m *Manager) StartServices(bindHost string, mainPort int, tlsConfig *tls.Config) error {
	m.web.Start(bindHost, mainPort, tlsConfig)
	return m.gateway.Start()
}

func (m *Manager) CloseServices() {
	m.web.Shutdown()
	m.gateway.Close()
}

func (m *Manager) WebHandler(main http.Handler) http.Handler { return m.web.Wrap(main) }

func (m *Manager) IssueEmbed(r *http.Request, id, token string, expires time.Time) (webproxy.Embed, error) {
	return m.web.Issue(r, id, token, expires)
}

func (m *Manager) webAuthorization(ctx context.Context, id, token string) bool {
	if m.webAuth == nil {
		return false
	}
	session, ok := m.webAuth.ValidateToken(token)
	return ok && session.Permissions["plugins:use"] && m.db.UserCanAccessResource(session.UserID, session.ScopeFor("plugins:use"), "plugin_install", id)
}

func (m *Manager) webTarget(ctx context.Context, id string) (webproxy.Target, error) {
	i, err := m.db.RuntimePluginInstall(ctx, id)
	if err != nil || i.Observed != "healthy" || i.Endpoint == "" || i.Version == "" {
		return webproxy.Target{}, webproxy.ErrUnavailable
	}
	spec, _, err := m.spec(ctx, id, i.Version)
	if err != nil {
		return webproxy.Target{}, webproxy.ErrUnavailable
	}
	return webproxy.Target{Endpoint: i.Endpoint, Generation: i.Generation, Route: spec.Declaration.Web.Route,
		EntryPath: spec.Declaration.Web.EntryPath, ApplicationBearer: spec.Declaration.Access.ApplicationBearer}, nil
}

func (m *Manager) AvailableLLMChannels() (string, []llmgateway.ChannelOption) {
	return m.gateway.AvailableChannels()
}

type InstallDetail struct {
	database.PluginInstallSummary
	Version     string                      `json:"version,omitempty"`
	Repository  string                      `json:"repository,omitempty"`
	Summary     string                      `json:"summary,omitempty"`
	License     string                      `json:"license,omitempty"`
	Generation  int64                       `json:"generation"`
	Web         *manifest.Web               `json:"web,omitempty"`
	Permissions *manifest.Permissions       `json:"permissions,omitempty"`
	Extensions  *manifest.Extensions        `json:"extensions,omitempty"`
	LLMDeclared *manifest.LLM               `json:"llm_declared,omitempty"`
	LLMBindings []database.PluginLLMBinding `json:"llm_bindings"`
	ProxyMode   string                      `json:"proxy_mode"`
}

func (m *Manager) InstallDetail(ctx context.Context, id string) (InstallDetail, error) {
	summary, err := m.db.GetPluginInstall(ctx, id)
	if err != nil {
		return InstallDetail{}, err
	}
	detail := InstallDetail{
		PluginInstallSummary: summary,
		LLMBindings:          []database.PluginLLMBinding{},
		ProxyMode:            m.proxyMode,
	}
	if bindings, err := m.db.PluginLLMBindings(ctx, id); err == nil && len(bindings) > 0 {
		detail.LLMBindings = bindings
	}
	if rt, err := m.db.RuntimePluginInstall(ctx, id); err == nil {
		detail.Generation = rt.Generation
		if rt.Version != "" {
			if spec, _, err := m.spec(ctx, id, rt.Version); err == nil && spec.Declaration != nil {
				decl := spec.Declaration
				detail.Version = decl.Metadata.Version
				detail.Repository = decl.Metadata.Repository
				detail.Summary = decl.Metadata.Summary
				detail.License = decl.Metadata.License
				web := decl.Web
				perms := decl.Permissions
				ext := decl.Extensions
				detail.Web = &web
				detail.Permissions = &perms
				detail.Extensions = &ext
				detail.LLMDeclared = decl.LLM
			}
		}
	}
	return detail, nil
}

func (m *Manager) PatchLLM(ctx context.Context, id string, patch llmgateway.Patch) ([]database.PluginLLMBinding, bool, error) {
	install, err := m.db.RuntimePluginInstall(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if install.Version == "" || install.Observed == "removed" {
		return nil, false, llmgateway.ErrBinding
	}
	spec, _, err := m.spec(ctx, id, install.Version)
	if err != nil {
		return nil, false, err
	}
	bindings, err := m.gateway.Patch(ctx, id, spec.Declaration.LLM, patch)
	return bindings, install.Observed == "healthy" || install.Observed == "starting", err
}

func (m *Manager) stopTraffic(ctx context.Context, id string) error {
	err := m.db.BlockPluginTraffic(ctx, m.owner, id)
	m.revokeServices(ctx, id)
	return err
}

func (m *Manager) revokeServices(ctx context.Context, id string) {
	m.web.Revoke(id)
	_ = m.gateway.Revoke(ctx, id)
}

func (m *Manager) startPlugin(ctx context.Context, driver runner.Driver, spec runner.Spec) (runner.State, error) {
	m.revokeServices(ctx, spec.InstallID)
	generation, err := m.db.BeginPluginLaunch(ctx, m.owner, spec.InstallID)
	if err != nil {
		return runner.State{}, err
	}
	credentials, root, hostMapping, err := m.gateway.Issue(ctx, spec.InstallID, generation, spec.Declaration.LLM, spec.Plan.Artifact.Driver == "container")
	if err != nil {
		m.revokeAfterStartFailure(spec.InstallID)
		return runner.State{}, fmt.Errorf("prepare model gateway binding: %w", err)
	}
	if len(credentials) > 0 {
		cfg := &runner.ModelConfig{Env: map[string]string{}, GatewayRoot: root, DockerHostMapping: hostMapping}
		for _, credential := range credentials {
			for name, slot := range credential.Binding.Env {
				cfg.Env[name] = credential.Values[slot]
			}
			if template := credential.Binding.ConfigTemplate; template != nil {
				values := map[string]string{}
				for name, slot := range template.Values {
					values[name] = credential.Values[slot]
				}
				cfg.Templates = append(cfg.Templates, runner.ModelTemplate{Manifest: *template, Values: values})
			}
		}
		spec.Model = cfg
	}
	state, err := driver.Start(ctx, spec)
	if err != nil {
		m.revokeAfterStartFailure(spec.InstallID)
	}
	return state, err
}

func (m *Manager) revokeAfterStartFailure(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m.revokeServices(ctx, id)
}
