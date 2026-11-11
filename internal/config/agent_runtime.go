package config

import (
	"fmt"
	"strings"
)

// AgentRuntimeConfig keeps runtime selection separate from model/provider selection.
type AgentRuntimeConfig struct {
	Engine     string                `yaml:"engine,omitempty" json:"engine,omitempty"`
	NodePath   string                `yaml:"node_path,omitempty" json:"node_path,omitempty"`
	WorkerPath string                `yaml:"worker_path,omitempty" json:"worker_path,omitempty"`
	SessionDir string                `yaml:"session_dir,omitempty" json:"session_dir,omitempty"`
	Purge      PiPromptProfileConfig `yaml:"purge,omitempty" json:"purge,omitempty"`
}

type PiPromptProfileConfig struct {
	Enabled           *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Instruction       string            `yaml:"instruction,omitempty" json:"instruction,omitempty"`
	ModelInstructions map[string]string `yaml:"model_instructions,omitempty" json:"model_instructions,omitempty"`
}

func (c AgentRuntimeConfig) Validate() error {
	engine := strings.ToLower(strings.TrimSpace(c.Engine))
	if engine != "" && engine != "pi" {
		return fmt.Errorf("unsupported agent runtime %q: this version only supports pi", c.Engine)
	}
	return nil
}
func (c PiPromptProfileConfig) EnabledEffective() bool { return c.Enabled == nil || *c.Enabled }

func (c PiPromptProfileConfig) InstructionFor(provider, model string) string {
	selected := c.ModelInstructions[provider+"/"+model]
	if selected == "" {
		selected = c.ModelInstructions[model]
	}
	return strings.TrimSpace(strings.Join([]string{c.Instruction, selected}, "\n\n"))
}
