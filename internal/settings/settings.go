// Package settings publishes configuration to the running process through an
// atomic snapshot instead of a shared mutable pointer.
//
// The defect this replaces: one *config.Config was handed to 24 packages and
// written at runtime from several handlers under two different mutexes, while
// ApplyConfig read it under a third. Any field could therefore be read while
// half-updated, and the race detector never saw it because no CI ran -race.
package settings

import (
	"sync"
	"sync/atomic"

	"cyberstrike-ai/internal/config"
)

// Store is the single runtime authority for configuration.
//
// Readers get an immutable snapshot; writers replace the whole snapshot. A
// writer never mutates the object a reader is holding, which is what makes the
// swap safe without a lock on the read path.
type Store struct {
	mu      sync.Mutex // serializes writers only
	current atomic.Pointer[config.Config]
}

// New seeds the store. A nil cfg yields an empty-but-valid snapshot so callers
// never have to nil-check their way through a config read.
func New(cfg *config.Config) *Store {
	s := &Store{}
	s.Replace(cfg)
	return s
}

// Current returns the live snapshot. The result must be treated as read-only;
// use Update to change anything.
func (s *Store) Current() *config.Config {
	if cfg := s.current.Load(); cfg != nil {
		return cfg
	}
	return &config.Config{}
}

// Replace installs a new snapshot wholesale. Used after a config file reload.
func (s *Store) Replace(cfg *config.Config) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.Store(cfg)
}

// Update applies a mutation to a deep-enough copy of the live snapshot and
// publishes the result. The mutator runs with the writer lock, so two concurrent
// updates cannot lose one another's changes.
func (s *Store) Update(mutate func(cfg *config.Config)) *config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.Current())
	mutate(next)
	s.current.Store(next)
	return next
}

// Hitl returns the human-in-the-loop slice of the live snapshot. It is the most
// frequently rewritten section at runtime, so it gets its own accessor instead of
// every caller reaching through the whole config.
func (s *Store) Hitl() config.HitlConfig {
	return s.Current().Hitl
}

// UpdateHitl changes only the HITL section and publishes a new snapshot.
func (s *Store) UpdateHitl(mutate func(hitl *config.HitlConfig)) {
	s.Update(func(cfg *config.Config) { mutate(&cfg.Hitl) })
}

// clone copies the config by value and duplicates every field the process writes
// at runtime. Assignment-only fields are already safe after a value copy; the
// slices and pointers below are the known runtime-write surface, and sharing one
// of them would let a writer mutate what a reader is holding.
func clone(in *config.Config) *config.Config {
	out := *in

	out.Hitl.ToolWhitelist = append([]string(nil), in.Hitl.ToolWhitelist...)
	if in.Hitl.DefaultTimeoutSeconds != nil {
		v := *in.Hitl.DefaultTimeoutSeconds
		out.Hitl.DefaultTimeoutSeconds = &v
	}
	if in.Hitl.RetentionDays != nil {
		v := *in.Hitl.RetentionDays
		out.Hitl.RetentionDays = &v
	}
	if in.ToolGuard != nil {
		v := *in.ToolGuard
		out.ToolGuard = &v
	}
	out.Security.Tools = append([]config.ToolConfig(nil), in.Security.Tools...)
	out.ExternalMCP.Servers = cloneMap(in.ExternalMCP.Servers)
	out.Roles = cloneMap(in.Roles)
	return &out
}

// cloneMap copies a configuration map so a writer replacing one entry cannot
// rewrite what a concurrent reader is iterating.
func cloneMap[V any](in map[string]V) map[string]V {
	if in == nil {
		return nil
	}
	out := make(map[string]V, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
