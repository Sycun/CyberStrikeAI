package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

const (
	PluginApprovalAdminConfirm   = "admin_confirm"
	PluginApprovalHostConfigOnly = "host_config_only"
)

// PluginsConfig is read at startup. The approval policy controls how locked
// plans become eligible for execution.
type PluginsConfig struct {
	ApprovalPolicy    string                 `yaml:"approval_policy,omitempty" json:"-"`
	Directory         string                 `yaml:"directory,omitempty" json:"-"`
	Egress            PluginEgress           `yaml:"egress,omitempty" json:"-"`
	ApprovedArtifacts map[string][]string    `yaml:"approved_artifacts,omitempty" json:"-"`
	Proxy             PluginProxyConfig      `yaml:"proxy,omitempty" json:"-"`
	LLMGateway        PluginLLMGatewayConfig `yaml:"llm_gateway,omitempty" json:"-"`
}

type PluginEgress struct {
	ProxyMode       string   `yaml:"proxy_mode,omitempty"`
	ProxyURL        string   `yaml:"proxy_url,omitempty"`
	GitHubTokenEnv  string   `yaml:"github_token_env,omitempty"`
	AdditionalHosts []string `yaml:"additional_hosts,omitempty"`
}

var pluginSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var pluginEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
var pluginRemoteHost = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]+$`)

func (p PluginsConfig) EffectiveApprovalPolicy() string {
	if p.ApprovalPolicy == "" {
		return PluginApprovalAdminConfirm
	}
	return p.ApprovalPolicy
}

func (p PluginsConfig) Validate() error {
	if err := p.Proxy.Validate(); err != nil {
		return err
	}
	if err := p.LLMGateway.Validate(); err != nil {
		return err
	}
	switch p.EffectiveApprovalPolicy() {
	case PluginApprovalAdminConfirm, PluginApprovalHostConfigOnly:
	default:
		return fmt.Errorf("plugins.approval_policy must be admin_confirm or host_config_only")
	}
	switch p.Egress.ProxyMode {
	case "", "system", "direct":
		if p.Egress.ProxyURL != "" {
			return fmt.Errorf("plugins.egress.proxy_url requires fixed mode")
		}
	case "fixed":
		u, err := url.Parse(p.Egress.ProxyURL)
		if err != nil || u.Host == "" || !strings.Contains("|http|https|socks5|", "|"+u.Scheme+"|") ||
			u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("invalid plugins.egress.proxy_url")
		}
	default:
		return fmt.Errorf("plugins.egress.proxy_mode must be system, direct or fixed")
	}
	if p.Egress.GitHubTokenEnv != "" && !pluginEnvName.MatchString(p.Egress.GitHubTokenEnv) {
		return fmt.Errorf("invalid plugins.egress.github_token_env")
	}
	for _, host := range p.Egress.AdditionalHosts {
		if !pluginRemoteHost.MatchString(host) || !strings.Contains(host, ".") || strings.Contains(host, "..") {
			return fmt.Errorf("invalid plugins.egress.additional_hosts entry")
		}
	}
	for declaration, artifacts := range p.ApprovedArtifacts {
		if !pluginSHA256.MatchString(declaration) || len(artifacts) == 0 {
			return fmt.Errorf("plugins.approved_artifacts requires declaration and artifact SHA-256 digests")
		}
		for _, artifact := range artifacts {
			if !pluginSHA256.MatchString(artifact) {
				return fmt.Errorf("invalid plugins.approved_artifacts artifact digest")
			}
		}
	}
	return nil
}

type PluginProxyConfig struct {
	Mode          string         `yaml:"mode,omitempty"`
	BindHost      string         `yaml:"bind_host,omitempty"`
	PortPool      PluginPortPool `yaml:"port_pool,omitempty"`
	HostSuffix    string         `yaml:"host_suffix,omitempty"`
	PublicOrigin  string         `yaml:"public_origin,omitempty"`
	ExternalPorts map[int]int    `yaml:"external_ports,omitempty"`
}

type PluginPortPool struct {
	Start int `yaml:"start,omitempty"`
	End   int `yaml:"end,omitempty"`
}

func (p PluginProxyConfig) Effective() PluginProxyConfig {
	if p.Mode == "" {
		p.Mode = "port"
	}
	if p.PortPool == (PluginPortPool{}) {
		p.PortPool = PluginPortPool{43100, 43131}
	}
	return p
}

func (p PluginProxyConfig) Validate() error {
	p = p.Effective()
	if p.Mode != "port" && p.Mode != "host" {
		return fmt.Errorf("plugins.proxy.mode must be port or host")
	}
	if p.Mode == "host" && (len(p.HostSuffix) > 180 || !pluginDNSHost.MatchString(p.HostSuffix) || netipAddr(p.HostSuffix)) {
		return fmt.Errorf("plugins.proxy.host_suffix must be a DNS suffix in host mode")
	}
	if p.PortPool.Start < 1024 || p.PortPool.End > 65535 || p.PortPool.End < p.PortPool.Start || p.PortPool.End-p.PortPool.Start > 2047 {
		return fmt.Errorf("invalid plugins.proxy.port_pool")
	}
	if p.BindHost != "" && strings.ContainsAny(p.BindHost, " /\\@#?[]") {
		return fmt.Errorf("invalid plugins.proxy.bind_host")
	}
	ports := map[int]bool{}
	for port := p.PortPool.Start; port <= p.PortPool.End; port++ {
		mapped := port
		if external, exists := p.ExternalPorts[port]; exists {
			mapped = external
		}
		if mapped < 1 || mapped > 65535 || ports[mapped] {
			return fmt.Errorf("invalid or duplicate plugins.proxy.external_ports value")
		}
		ports[mapped] = true
	}
	for port := range p.ExternalPorts {
		if p.Mode != "port" || port < p.PortPool.Start || port > p.PortPool.End {
			return fmt.Errorf("invalid plugins.proxy.external_ports key")
		}
	}
	if p.PublicOrigin != "" {
		u, err := url.Parse(p.PublicOrigin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
			u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("plugins.proxy.public_origin must be an HTTP(S) origin")
		}
	}
	return nil
}

var pluginDNSHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)

func netipAddr(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}

type PluginLLMGatewayConfig struct {
	ListenHost    string `yaml:"listen_host,omitempty"`
	ListenPort    int    `yaml:"listen_port,omitempty"`
	ContainerHost string `yaml:"container_host,omitempty"`
	Concurrency   int    `yaml:"concurrency,omitempty"`
	RPM           int    `yaml:"rpm,omitempty"`
	TokenBudget   int64  `yaml:"token_budget,omitempty"`
}

func (g PluginLLMGatewayConfig) Effective() PluginLLMGatewayConfig {
	if g.ListenHost == "" {
		g.ListenHost = "127.0.0.1"
	}
	if g.Concurrency == 0 {
		g.Concurrency = 4
	}
	if g.RPM == 0 {
		g.RPM = 30
	}
	if g.TokenBudget == 0 {
		g.TokenBudget = 200000
	}
	return g
}

func (g PluginLLMGatewayConfig) Validate() error {
	g = g.Effective()
	ip, err := netip.ParseAddr(g.ListenHost)
	if err != nil || (!ip.IsLoopback() && !ip.IsPrivate()) || ip.Zone() != "" {
		return fmt.Errorf("plugins.llm_gateway.listen_host must be a loopback or specific private IP")
	}
	if g.ContainerHost != "" {
		address, addrErr := netip.ParseAddr(g.ContainerHost)
		if g.ContainerHost != "host.docker.internal" && (addrErr != nil || !address.IsPrivate() || address.Zone() != "") {
			return fmt.Errorf("plugins.llm_gateway.container_host must be host.docker.internal or a private IP")
		}
	}
	if g.ListenPort < 0 || g.ListenPort > 65535 || g.Concurrency < 1 || g.Concurrency > 64 ||
		g.RPM < 1 || g.RPM > 1200 || g.TokenBudget < 1 || g.TokenBudget > 1000000000 {
		return fmt.Errorf("invalid plugins.llm_gateway port or limits")
	}
	return nil
}
