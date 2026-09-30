package llm

import (
	"strings"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/reasoning"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
)

// This file is the only place the reasoning rules touch Eino's typed config. The rules
// themselves live in internal/reasoning against its ChatModelTarget interface, which is what
// keeps that package - and everything that configures a model through it - out of the SDK
// import count the P6 convergence criterion measures.

// einoChatModelTarget adapts an Eino OpenAI chat-model config to reasoning.ChatModelTarget.
type einoChatModelTarget struct {
	cfg *einoopenai.ChatModelConfig
}

func (t einoChatModelTarget) ExtraFields() map[string]any { return t.cfg.ExtraFields }

func (t einoChatModelTarget) SetExtraFields(fields map[string]any) { t.cfg.ExtraFields = fields }

func (t einoChatModelTarget) SetEffort(level string) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "":
		t.cfg.ReasoningEffort = ""
	case "low":
		t.cfg.ReasoningEffort = einoopenai.ReasoningEffortLevelLow
	case "medium":
		t.cfg.ReasoningEffort = einoopenai.ReasoningEffortLevelMedium
	case "high":
		t.cfg.ReasoningEffort = einoopenai.ReasoningEffortLevelHigh
	}
}

// ApplyReasoningToChatModelConfig merges reasoning intent into an Eino chat-model config.
// A nil config is ignored, which is the guard the reasoning package used to make itself.
func ApplyReasoningToChatModelConfig(cfg *einoopenai.ChatModelConfig, oa *config.OpenAIConfig, intent *reasoning.ClientIntent) {
	if cfg == nil {
		return
	}
	reasoning.ApplyToChatModelConfig(einoChatModelTarget{cfg: cfg}, oa, intent)
}

// ApplyPlanExecutePlannerModelConfig configures the plan_execute planner chat model, which
// must not carry thinking or reasoning fields alongside a forced tool_choice.
func ApplyPlanExecutePlannerModelConfig(cfg *einoopenai.ChatModelConfig, oa *config.OpenAIConfig) {
	if cfg == nil {
		return
	}
	reasoning.ApplyPlanExecutePlannerModelConfig(einoChatModelTarget{cfg: cfg}, oa)
}
