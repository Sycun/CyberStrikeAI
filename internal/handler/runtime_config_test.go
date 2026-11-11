package handler

import (
	"cyberstrike-ai/internal/config"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestPiRuntimeSettingsSurviveSaveAndPartialUpdate(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("multi_agent:\n  eino_middleware:\n    tool_search_enable: true\n  eino_skills:\n    disable: false\n  eino_callbacks:\n    enabled: true\n"), &doc); err != nil {
		t.Fatal(err)
	}
	cfg := config.AgentRuntimeConfig{NodePath: "/custom/node", SessionDir: "/private/sessions", Purge: config.PiPromptProfileConfig{Instruction: "original", ModelInstructions: map[string]string{"provider/model": "model guidance"}}}
	empty := ""
	off := false
	applyAgentRuntimeUpdate(&cfg, &AgentRuntimeUpdate{Purge: &PiProfileUpdate{Enabled: &off, Instruction: &empty}})
	if cfg.NodePath != "/custom/node" || cfg.SessionDir != "/private/sessions" {
		t.Fatal("partial update erased deployment paths")
	}
	updateAgentRuntimeConfig(&doc, cfg)
	updateMultiAgentConfig(&doc, config.MultiAgentConfig{})
	var restored config.Config
	if err := doc.Decode(&restored); err != nil {
		t.Fatal(err)
	}
	if restored.AgentRuntime.Engine != "pi" || restored.AgentRuntime.Purge.EnabledEffective() || restored.AgentRuntime.Purge.Instruction != "" || restored.AgentRuntime.Purge.ModelInstructions["provider/model"] != "model guidance" {
		t.Fatalf("lost runtime settings: %+v", restored.AgentRuntime)
	}
	if !restored.MultiAgent.Runtime.ToolSearchEnable {
		t.Fatal("legacy runtime settings were discarded")
	}
	var raw map[string]any
	doc.Decode(&raw)
	ma := raw["multi_agent"].(map[string]any)
	for _, key := range []string{"eino_middleware", "eino_skills", "eino_callbacks"} {
		if _, ok := ma[key]; ok {
			t.Fatalf("retired key emitted: %s", key)
		}
	}
}
