package project

import (
	"strings"

	"cyberstrike-ai/internal/promptguard"
)

// FrameDerived wraps one host-built block in a fresh nonce envelope.
// The index builder keeps its own length budget; callers apply this once when
// the block is copied into a model prompt.
func FrameDerived(field, content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	env := promptguard.NewEnvelope()
	return env.Intro() + "\n" + env.Wrap(field, content)
}
