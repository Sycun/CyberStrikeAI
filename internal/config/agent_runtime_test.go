package config

import "testing"

func TestAgentRuntimeDefaultAndExplicitFallback(t *testing.T) {
	if !(AgentRuntimeConfig{}).UsesPi() {
		t.Fatal("Pi must be the default")
	}
	if (AgentRuntimeConfig{Engine: "eino"}).UsesPi() {
		t.Fatal("explicit fallback ignored")
	}
	if (AgentRuntimeConfig{Engine: "typo"}).Validate() == nil {
		t.Fatal("invalid runtime accepted")
	}
}
func TestPiProfileModelOverrideIsAdditive(t *testing.T) {
	c := PiPromptProfileConfig{Instruction: "global", ModelInstructions: map[string]string{"model": "general", "claude/model": "specific"}}
	if c.InstructionFor("claude", "model") != "global\n\nspecific" {
		t.Fatal("specific model instructions not selected")
	}
	if c.InstructionFor("openai", "model") != "global\n\ngeneral" {
		t.Fatal("model instructions not selected")
	}
}
