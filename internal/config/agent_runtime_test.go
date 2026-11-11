package config

import "testing"

func TestAgentRuntimeIsPiOnly(t *testing.T) {
	if (AgentRuntimeConfig{}).Validate() != nil || (AgentRuntimeConfig{Engine: "pi"}).Validate() != nil {
		t.Fatal("Pi default rejected")
	}
	if (AgentRuntimeConfig{Engine: "eino"}).Validate() == nil {
		t.Fatal("retired engine accepted")
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
