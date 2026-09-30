package handler

import (
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/settings"
)

// Both ConfigHandler and AgentHandler were handed the same *config.Config, and
// each wrote the HITL section under a different (or no) lock while the other read
// it. The snapshot store replaces that: writers publish a new snapshot, readers
// hold an immutable one.

// SettingsStore is the shared publisher both handlers read and write through.
type SettingsStore = settings.Store

// SetSettings installs the snapshot store. Until it is set, handlers fall back to
// the single config pointer so callers that predate the store keep compiling.
func (h *ConfigHandler) SetSettings(store *SettingsStore) {
	if h == nil {
		return
	}
	h.settings = store
}

func (h *AgentHandler) SetSettings(store *SettingsStore) {
	if h == nil {
		return
	}
	h.settings = store
}

func (h *ConfigHandler) hitl() config.HitlConfig {
	if h == nil {
		return config.HitlConfig{}
	}
	if h.settings != nil {
		return h.settings.Hitl()
	}
	return h.config.Hitl
}

// hitlSnapshot is the live HITL configuration: the published runtime snapshot when the
// settings store is attached, config.yaml's own value otherwise. Named so the approval
// policy can read it through an interface instead of reaching into the handler.
func (h *AgentHandler) hitlSnapshot() config.HitlConfig {
	if h == nil {
		return config.HitlConfig{}
	}
	if h.settings != nil {
		return h.settings.Hitl()
	}
	return h.config.Hitl
}

// publishHitl mutates the HITL section and publishes a new snapshot. The mutator
// must not retain the pointer it receives.
func (h *ConfigHandler) publishHitl(mutate func(hitl *config.HitlConfig)) {
	if h == nil {
		return
	}
	if h.settings != nil {
		h.settings.UpdateHitl(mutate)
		return
	}
	mutate(&h.config.Hitl)
}

func (h *AgentHandler) publishHitl(mutate func(hitl *config.HitlConfig)) {
	if h == nil {
		return
	}
	if h.settings != nil {
		h.settings.UpdateHitl(mutate)
		return
	}
	mutate(&h.config.Hitl)
}

// settingsConfigured reports whether a configuration was loaded at all. An endpoint that
// reads defaults must be able to say "not configured" rather than inventing one.
func (h *AgentHandler) settingsConfigured() bool { return h != nil && h.config != nil }
