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
	if h.hitlPolicy != nil {
		// Forwarded: the assembly builds the agent, then attaches the shared settings store. A
		// policy that captured the store at construction would keep reading config.yaml after
		// that, and an operator changing a default would watch it land in the file but not in
		// the running rules - the exact defect the live snapshot exists to prevent.
		h.HitlPolicy().settings = store
	}
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
