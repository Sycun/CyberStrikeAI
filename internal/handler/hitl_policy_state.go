package handler

import (
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/multiagent"
	"strings"
)

// The live approval rules: config.yaml as loaded, plus whatever the settings store has
// published on top of it. These used to be methods on AgentHandler, which meant the type that
// owns the agent loop also owned the question "what does an operator currently have set" - and
// every reader, including the endpoints in hitl_policy.go, reached into it.

// hitlSnapshot is the live HITL configuration: the published runtime snapshot when a settings
// store is attached, config.yaml's own value otherwise. Nil-safe on purpose - a handler built
// without a store in a test must keep reading the file rather than panic.
func (p *HitlPolicy) hitlSnapshot() config.HitlConfig {
	if p == nil {
		return config.HitlConfig{}
	}
	if p.settings != nil {
		return p.settings.Hitl()
	}
	if p.cfg == nil {
		return config.HitlConfig{}
	}
	return p.cfg.Hitl
}

// settingsConfigured reports whether a configuration was loaded at all, so an endpoint can say
// "not configured" instead of inventing a default out of a nil struct.
func (p *HitlPolicy) settingsConfigured() bool { return p != nil && p.cfg != nil }

// publishHitl writes through the shared settings store when there is one. Callers write
// config.yaml first and publish after, so a crash between the two leaves the file - which is
// what the next start-up reads - authoritative.
func (p *HitlPolicy) publishHitl(mutate func(hitl *config.HitlConfig)) {
	if p == nil || mutate == nil {
		return
	}
	if p.settings != nil {
		p.settings.UpdateHitl(mutate)
		return
	}
	if p.cfg == nil {
		return
	}
	mutate(&p.cfg.Hitl)
}

func (p *HitlPolicy) hitlEffectiveDefaultMode() string {
	if p != nil && p.settingsConfigured() {
		return normalizeHitlDefaultMode(p.hitlSnapshot().EffectiveDefaultMode())
	}
	return "off"
}

func (p *HitlPolicy) hitlEffectiveDefaultReviewer() string {
	if p != nil && p.settingsConfigured() {
		return normalizeHitlReviewer(p.hitlSnapshot().EffectiveDefaultReviewer())
	}
	return "human"
}

func (p *HitlPolicy) hitlEffectiveDefaultTimeoutSeconds() int {
	if p != nil && p.settingsConfigured() {
		timeout := p.hitlSnapshot().EffectiveDefaultTimeoutSeconds()
		if timeout < 0 {
			return 0
		}
		return timeout
	}
	return 300
}

// hitlEffectiveDefaultRequest is what "no conversation has chosen a rule yet" resolves to.
func (p *HitlPolicy) hitlEffectiveDefaultRequest() *HITLRequest {
	mode := p.hitlEffectiveDefaultMode()
	return &HITLRequest{
		Enabled:        mode != "off",
		Mode:           mode,
		Reviewer:       p.hitlEffectiveDefaultReviewer(),
		SensitiveTools: []string{},
		TimeoutSeconds: p.hitlEffectiveDefaultTimeoutSeconds(),
	}
}

// hitlConfigGlobalToolWhitelist is the exemption list from configuration, trimmed and
// de-duplicated, always with the platform's own meta tools re-added: replacing the whole table
// must not be able to un-exempt the tools a run cannot proceed without.
func (p *HitlPolicy) hitlConfigGlobalToolWhitelist() []string {
	if p == nil || !p.settingsConfigured() {
		return multiagent.MergeHitlExemptMetaTools(nil)
	}
	raw := p.hitlSnapshot().ToolWhitelist
	seen := make(map[string]struct{})
	out := make([]string, 0, len(raw)+len(multiagent.HitlExemptMetaTools))
	for _, t := range raw {
		n := strings.ToLower(strings.TrimSpace(t))
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, strings.TrimSpace(t))
	}
	return multiagent.MergeHitlExemptMetaTools(out)
}

func (p *HitlPolicy) hitlAuditEngineInfo() (backend, model string) {
	backend = config.HitlAuditBackendOpenAI
	if p == nil || !p.settingsConfigured() {
		return backend, ""
	}
	// Read from cfg rather than the snapshot: the audit engine is chosen from the file, and an
	// endpoint that reports which model is in play must agree with what a restart will load.
	backend = p.cfg.Hitl.EffectiveAuditBackend()
	if backend == config.HitlAuditBackendTypeSafe {
		_, _, model = p.cfg.Hitl.TypeSafeConfigEffective()
		return backend, model
	}
	return backend, strings.TrimSpace(p.cfg.Hitl.AuditModelEffective(p.cfg.OpenAI).Model)
}

// hitlRequestWithMergedConfigWhitelist unions a conversation's exempt tools with the global
// list before that conversation is activated, so saving a conversation rule cannot narrow the
// exemptions the operator configured globally.
func (p *HitlPolicy) hitlRequestWithMergedConfigWhitelist(req *HITLRequest) *HITLRequest {
	if req == nil {
		return nil
	}
	seen := make(map[string]struct{})
	union := make([]string, 0, len(req.SensitiveTools)+16)
	add := func(t string) {
		n := strings.ToLower(strings.TrimSpace(t))
		if n == "" {
			return
		}
		if _, ok := seen[n]; ok {
			return
		}
		seen[n] = struct{}{}
		union = append(union, strings.TrimSpace(t))
	}
	for _, t := range p.hitlConfigGlobalToolWhitelist() {
		add(t)
	}
	for _, t := range req.SensitiveTools {
		add(t)
	}
	out := *req
	out.SensitiveTools = multiagent.MergeHitlExemptMetaTools(union)
	return &out
}

// loadHITLConversationConfig is the rule a conversation runs under: its own saved config when
// it has one, the effective default when it has none. "Never saved" and "the store failed" are
// different answers and travel back separately, because the run path falls back to the default
// for the first and must not swallow the second.
func (p *HitlPolicy) loadHITLConversationConfig(conversationID string) (*HITLRequest, error) {
	cfg, err := p.manager.LoadConversationConfig(conversationID)
	if err != nil {
		return nil, err
	}
	has, err := p.manager.HasConversationConfig(conversationID)
	if err != nil {
		return nil, err
	}
	if !has {
		return p.hitlEffectiveDefaultRequest(), nil
	}
	return cfg, nil
}
