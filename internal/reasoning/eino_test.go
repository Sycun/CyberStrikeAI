package reasoning

import (
	"testing"

	"cyberstrike-ai/internal/config"
)

var reasoningPayloadKeysForTest = []string{"thinking", "reasoning_effort", "output_config", "reasoning"}

// fakeChatModel is the test double for ChatModelTarget: the same two fields the SDK config
// carries, so every assertion below still describes the behaviour of the rules rather than
// of Eino. The mapping onto the real SDK type is covered in internal/llm.
type fakeChatModel struct {
	ReasoningEffort string
	Fields          map[string]any
}

func (f *fakeChatModel) ExtraFields() map[string]any { return f.Fields }

func (f *fakeChatModel) SetExtraFields(fields map[string]any) { f.Fields = fields }

func (f *fakeChatModel) SetEffort(level string) { f.ReasoningEffort = level }

func assertNoReasoningFields(t *testing.T, cfg *fakeChatModel) {
	t.Helper()
	if cfg.ReasoningEffort != "" {
		t.Fatalf("expected ReasoningEffort omitted, got %q", cfg.ReasoningEffort)
	}
	for _, key := range reasoningPayloadKeysForTest {
		if _, ok := cfg.Fields[key]; ok {
			t.Fatalf("expected %q omitted, got %#v", key, cfg.Fields)
		}
	}
}

func TestEffortStringForAPI_passthrough(t *testing.T) {
	cases := map[string]string{
		"max":    "max",
		"xhigh":  "xhigh",
		"HIGH":   "high",
		"Medium": "medium",
	}
	for in, want := range cases {
		if got := effortStringForAPI(in); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeEffort_maxAndXhigh(t *testing.T) {
	if normalizeEffort("xhigh") != "xhigh" {
		t.Fatal("xhigh not accepted")
	}
	if normalizeEffort("max") != "max" {
		t.Fatal("max not accepted")
	}
}

func TestApplyOpenAICompat_xhighExtraField(t *testing.T) {
	cfg := &fakeChatModel{}
	oa := &config.OpenAIConfig{
		Reasoning: config.OpenAIReasoningConfig{
			Profile: "openai_compat",
			Mode:    "on",
			Effort:  "xhigh",
		},
	}
	ApplyToChatModelConfig(cfg, oa, nil)
	if cfg.Fields == nil {
		t.Fatal("expected ExtraFields")
	}
	if got, _ := cfg.Fields["reasoning_effort"].(string); got != "xhigh" {
		t.Fatalf("reasoning_effort=%q", got)
	}
}

func TestAgenticOpenAIExtraFields_openAICompatReasoningEffort(t *testing.T) {
	oa := &config.OpenAIConfig{
		Reasoning: config.OpenAIReasoningConfig{
			Profile: "openai_compat",
			Mode:    "on",
			Effort:  "high",
			ExtraRequestFields: map[string]interface{}{
				"vendor_option": true,
			},
		},
	}
	got := AgenticOpenAIExtraFields(oa, nil)
	if got["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort=%#v, want high in %#v", got["reasoning_effort"], got)
	}
	if got["vendor_option"] != true {
		t.Fatalf("vendor option not preserved: %#v", got)
	}
}

func TestAgenticOpenAIExtraFields_reasoningOffPreservesUnrelatedFields(t *testing.T) {
	oa := &config.OpenAIConfig{
		Model: "gpt-4o-mini",
		Reasoning: config.OpenAIReasoningConfig{
			Profile: "openai_compat",
			Mode:    "off",
			Effort:  "high",
			ExtraRequestFields: map[string]interface{}{
				"reasoning_effort": "high",
				"thinking":         map[string]any{"type": "enabled"},
				"vendor_option":    true,
			},
		},
	}
	got := AgenticOpenAIExtraFields(oa, nil)
	for _, key := range reasoningPayloadKeysForTest {
		if _, ok := got[key]; ok {
			t.Fatalf("agentic fields unexpectedly contain %q: %#v", key, got)
		}
	}
	if got["vendor_option"] != true {
		t.Fatalf("vendor option not preserved: %#v", got)
	}
}

func TestAgenticOpenAIPlannerExtraFields_deepseekDisablesThinking(t *testing.T) {
	oa := &config.OpenAIConfig{
		BaseURL: "https://api.deepseek.com",
		Model:   "deepseek-chat",
		Reasoning: config.OpenAIReasoningConfig{
			Profile: "auto",
			Mode:    "on",
			ExtraRequestFields: map[string]interface{}{
				"reasoning_effort": "high",
				"vendor_option":    true,
			},
		},
	}
	got := AgenticOpenAIPlannerExtraFields(oa)
	if got["reasoning_effort"] != nil {
		t.Fatalf("planner should strip reasoning_effort: %#v", got)
	}
	thinking, ok := got["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("expected deepseek thinking disabled, got %#v", got)
	}
	if got["vendor_option"] != true {
		t.Fatalf("vendor option not preserved: %#v", got)
	}
}

func TestAgenticOpenAIPlannerExtraFields_openAIProfileWinsOverDeepseekEndpoint(t *testing.T) {
	oa := &config.OpenAIConfig{
		BaseURL: "https://api.deepseek.com/v1",
		Model:   "deepseek-v4-flash",
		Reasoning: config.OpenAIReasoningConfig{
			Profile: "openai_compat",
			Mode:    "on",
			Effort:  "high",
			ExtraRequestFields: map[string]interface{}{
				"reasoning_effort": "high",
				"vendor_option":    true,
			},
		},
	}
	got := AgenticOpenAIPlannerExtraFields(oa)
	for _, key := range reasoningPayloadKeysForTest {
		if _, ok := got[key]; ok {
			t.Fatalf("planner fields unexpectedly contain %q: %#v", key, got)
		}
	}
	if got["vendor_option"] != true {
		t.Fatalf("vendor option not preserved: %#v", got)
	}
}

func TestApplyPlanExecutePlannerModelConfig_stripsReasoningWhenGlobalOn(t *testing.T) {
	cfg := &fakeChatModel{Fields: map[string]any{
		"thinking":         map[string]any{"type": "enabled"},
		"reasoning_effort": "high",
		"vendor_option":    true,
	}}
	oa := &config.OpenAIConfig{
		BaseURL: "https://antchat.example.com/v1",
		Model:   "minimax-m3",
		Reasoning: config.OpenAIReasoningConfig{
			Profile: "openai_compat",
			Mode:    "on",
			Effort:  "high",
		},
	}
	ApplyPlanExecutePlannerModelConfig(cfg, oa)
	assertNoReasoningFields(t, cfg)
	if cfg.Fields["vendor_option"] != true {
		t.Fatalf("expected unrelated extra field preserved, got %#v", cfg.Fields)
	}
}

func TestApplyPlanExecutePlannerModelConfig_openAIProfileWinsOverDeepseekEndpoint(t *testing.T) {
	cfg := &fakeChatModel{Fields: map[string]any{
		"thinking":         map[string]any{"type": "enabled"},
		"reasoning_effort": "high",
		"vendor_option":    true,
	}}
	oa := &config.OpenAIConfig{
		BaseURL: "https://api.deepseek.com/v1",
		Model:   "deepseek-v4-flash",
		Reasoning: config.OpenAIReasoningConfig{
			Profile: "openai_compat",
			Mode:    "on",
			Effort:  "high",
		},
	}
	ApplyPlanExecutePlannerModelConfig(cfg, oa)
	assertNoReasoningFields(t, cfg)
	if cfg.Fields["vendor_option"] != true {
		t.Fatalf("expected unrelated extra field preserved, got %#v", cfg.Fields)
	}
}

func TestApplyReasoningOff_omitsAllReasoningFields(t *testing.T) {
	cfg := &fakeChatModel{Fields: map[string]any{
		"thinking":      map[string]any{"type": "enabled"},
		"output_config": map[string]any{"effort": "high"},
	}}
	oa := &config.OpenAIConfig{
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4o-mini",
		Reasoning: config.OpenAIReasoningConfig{
			Mode:    "off",
			Effort:  "high",
			Profile: "openai_compat",
			ExtraRequestFields: map[string]interface{}{
				"thinking":      map[string]any{"type": "disabled"},
				"reasoning":     map[string]any{"effort": "high"},
				"vendor_option": true,
			},
		},
	}
	ApplyToChatModelConfig(cfg, oa, nil)
	assertNoReasoningFields(t, cfg)
	if cfg.Fields["vendor_option"] != true {
		t.Fatalf("expected unrelated extra field preserved, got %#v", cfg.Fields)
	}
}

func TestApplyReasoningOff_openAICompatDeepseekModelOmitsAllReasoningFields(t *testing.T) {
	allowClient := false
	cfg := &fakeChatModel{Fields: map[string]any{
		"thinking":         map[string]any{"type": "enabled"},
		"reasoning_effort": "high",
	}}
	oa := &config.OpenAIConfig{
		Provider: "openai_compatible",
		BaseURL:  "http://your-gateway:port/v1",
		Model:    "deepseek-v4-flash-0731",
		Reasoning: config.OpenAIReasoningConfig{
			Mode:                 "off",
			Effort:               "high",
			Profile:              "openai_compat",
			AllowClientReasoning: &allowClient,
			ExtraRequestFields: map[string]interface{}{
				"thinking":      map[string]any{"type": "disabled"},
				"output_config": map[string]any{"effort": "high"},
				"vendor_option": true,
			},
		},
	}
	ApplyToChatModelConfig(cfg, oa, nil)
	assertNoReasoningFields(t, cfg)
	if cfg.Fields["vendor_option"] != true {
		t.Fatalf("expected unrelated extra field preserved, got %#v", cfg.Fields)
	}
}

func TestAgenticOpenAIExtraFields_openAICompatDeepseekModelOmitsAllReasoningFields(t *testing.T) {
	oa := &config.OpenAIConfig{
		Provider: "openai_compatible",
		BaseURL:  "http://your-gateway:port/v1",
		Model:    "deepseek-v4-flash-0731",
		Reasoning: config.OpenAIReasoningConfig{
			Mode:    "off",
			Effort:  "high",
			Profile: "openai_compat",
			ExtraRequestFields: map[string]interface{}{
				"thinking":         map[string]any{"type": "disabled"},
				"reasoning_effort": "high",
				"vendor_option":    true,
			},
		},
	}
	got := AgenticOpenAIExtraFields(oa, nil)
	for _, key := range reasoningPayloadKeysForTest {
		if _, ok := got[key]; ok {
			t.Fatalf("agentic fields unexpectedly contain %q: %#v", key, got)
		}
	}
	if got["vendor_option"] != true {
		t.Fatalf("vendor option not preserved: %#v", got)
	}
}

func TestAgenticOpenAIPlannerExtraFields_openAICompatDeepseekModelOmitsAllReasoningFields(t *testing.T) {
	oa := &config.OpenAIConfig{
		Provider: "openai_compatible",
		BaseURL:  "http://your-gateway:port/v1",
		Model:    "deepseek-v4-flash-0731",
		Reasoning: config.OpenAIReasoningConfig{
			Mode:    "on",
			Effort:  "high",
			Profile: "openai_compat",
			ExtraRequestFields: map[string]interface{}{
				"thinking":         map[string]any{"type": "enabled"},
				"reasoning_effort": "high",
				"vendor_option":    true,
			},
		},
	}
	got := AgenticOpenAIPlannerExtraFields(oa)
	for _, key := range reasoningPayloadKeysForTest {
		if _, ok := got[key]; ok {
			t.Fatalf("planner fields unexpectedly contain %q: %#v", key, got)
		}
	}
	if got["vendor_option"] != true {
		t.Fatalf("vendor option not preserved: %#v", got)
	}
}

func TestApplyReasoningOff_clientOverrideOmit(t *testing.T) {
	cfg := &fakeChatModel{}
	oa := &config.OpenAIConfig{Reasoning: config.OpenAIReasoningConfig{
		Mode: "on", Effort: "high", Profile: "openai_compat",
	}}
	ApplyToChatModelConfig(cfg, oa, &ClientIntent{Mode: "off", Effort: "high"})
	assertNoReasoningFields(t, cfg)
}

func TestApplyReasoningOff_deepseekExplicitlyDisablesDefaultThinking(t *testing.T) {
	for _, profile := range []string{"deepseek_compat", "auto"} {
		t.Run(profile, func(t *testing.T) {
			cfg := &fakeChatModel{Fields: map[string]any{
				"reasoning_effort": "high",
				"vendor_option":    true,
			}}
			oa := &config.OpenAIConfig{
				BaseURL: "https://api.deepseek.com",
				Model:   "deepseek-v4-pro",
				Reasoning: config.OpenAIReasoningConfig{
					Mode: "off", Effort: "high", Profile: profile,
				},
			}
			ApplyToChatModelConfig(cfg, oa, nil)
			if cfg.ReasoningEffort != "" {
				t.Fatalf("expected ReasoningEffort omitted, got %q", cfg.ReasoningEffort)
			}
			if _, ok := cfg.Fields["reasoning_effort"]; ok {
				t.Fatalf("expected reasoning_effort omitted, got %#v", cfg.Fields)
			}
			thinking, ok := cfg.Fields["thinking"].(map[string]any)
			if !ok || thinking["type"] != "disabled" {
				t.Fatalf("expected DeepSeek thinking disabled, got %#v", cfg.Fields)
			}
			if cfg.Fields["vendor_option"] != true {
				t.Fatalf("expected unrelated extra field preserved, got %#v", cfg.Fields)
			}
		})
	}
}

func TestApplyReasoningOff_openAIProfileWinsOverDeepseekEndpoint(t *testing.T) {
	cfg := &fakeChatModel{Fields: map[string]any{
		"reasoning_effort": "high",
		"vendor_option":    true,
	}}
	oa := &config.OpenAIConfig{
		BaseURL: "https://api.deepseek.com",
		Model:   "deepseek-v4-pro",
		Reasoning: config.OpenAIReasoningConfig{
			Mode: "off", Effort: "high", Profile: "openai_compat",
		},
	}
	ApplyToChatModelConfig(cfg, oa, nil)
	assertNoReasoningFields(t, cfg)
	if cfg.Fields["vendor_option"] != true {
		t.Fatalf("expected unrelated extra field preserved, got %#v", cfg.Fields)
	}
}

func TestApplyOpenAICompat_maxPassthrough(t *testing.T) {
	cfg := &fakeChatModel{}
	oa := &config.OpenAIConfig{
		Reasoning: config.OpenAIReasoningConfig{
			Profile: "openai_compat",
			Mode:    "on",
			Effort:  "max",
		},
	}
	ApplyToChatModelConfig(cfg, oa, nil)
	got, _ := cfg.Fields["reasoning_effort"].(string)
	if got != "max" {
		t.Fatalf("max effort wire=%q, want max", got)
	}
}

func TestApplyClaude_adaptiveOutputConfigEffort(t *testing.T) {
	cfg := &fakeChatModel{}
	oa := &config.OpenAIConfig{
		Provider: "claude",
		Model:    "claude-opus-4-8",
		Reasoning: config.OpenAIReasoningConfig{
			Mode:   "on",
			Effort: "xhigh",
		},
	}
	ApplyToChatModelConfig(cfg, oa, nil)
	th, ok := cfg.Fields["thinking"].(map[string]any)
	if !ok || th["type"] != "adaptive" {
		t.Fatalf("thinking=%#v", cfg.Fields["thinking"])
	}
	oc, ok := cfg.Fields["output_config"].(map[string]any)
	if !ok {
		t.Fatal("expected output_config")
	}
	if oc["effort"] != "xhigh" {
		t.Fatalf("effort=%v", oc["effort"])
	}
}

func TestApplyClaude_sonnet37OfficialBudget(t *testing.T) {
	cfg := &fakeChatModel{}
	oa := &config.OpenAIConfig{
		Provider: "claude",
		Model:    "claude-3-7-sonnet-latest",
		Reasoning: config.OpenAIReasoningConfig{
			Mode:   "on",
			Effort: "low", // 3.7 has no output_config.effort; effort is not mapped to budget_tokens
		},
	}
	ApplyToChatModelConfig(cfg, oa, nil)
	th, ok := cfg.Fields["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" {
		t.Fatalf("thinking=%#v", cfg.Fields["thinking"])
	}
	if th["budget_tokens"] != claudeSonnet37DefaultBudgetTokens {
		t.Fatalf("budget_tokens=%v, want official example %d", th["budget_tokens"], claudeSonnet37DefaultBudgetTokens)
	}
	if _, hasOC := cfg.Fields["output_config"]; hasOC {
		t.Fatal("sonnet 3.7 should not set output_config")
	}
}

func TestApplyClaude_onWithoutEffortOmitsOutputConfig(t *testing.T) {
	cfg := &fakeChatModel{}
	oa := &config.OpenAIConfig{
		Provider: "claude",
		Model:    "claude-sonnet-4-6",
		Reasoning: config.OpenAIReasoningConfig{
			Mode: "on",
		},
	}
	ApplyToChatModelConfig(cfg, oa, nil)
	if _, hasOC := cfg.Fields["output_config"]; hasOC {
		t.Fatal("on without explicit effort should omit output_config (API default high)")
	}
}

func TestApplyClaude_autoWithoutEffortSkipsOutputConfig(t *testing.T) {
	cfg := &fakeChatModel{}
	oa := &config.OpenAIConfig{
		Provider: "claude",
		Model:    "claude-sonnet-4-6",
		Reasoning: config.OpenAIReasoningConfig{
			Mode: "auto",
		},
	}
	ApplyToChatModelConfig(cfg, oa, nil)
	if _, hasOC := cfg.Fields["output_config"]; hasOC {
		t.Fatal("auto without effort should omit output_config")
	}
}
