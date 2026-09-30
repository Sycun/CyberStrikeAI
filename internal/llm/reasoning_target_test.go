package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"cyberstrike-ai/internal/config"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
)

// These tests cover the adapter itself: internal/reasoning holds the rules against its own
// ChatModelTarget interface, so only here can anything prove that the rules actually reach
// the wire payload Eino builds.

var reasoningPayloadKeys = []string{"thinking", "reasoning_effort", "output_config", "reasoning"}

// TestReasoningTargetMapsEffortOntoSDKFields pins the three names the target interface
// accepts, plus the clearing behaviour.
func TestReasoningTargetMapsEffortOntoSDKFields(t *testing.T) {
	for level, want := range map[string]einoopenai.ReasoningEffortLevel{
		"low":    einoopenai.ReasoningEffortLevelLow,
		"medium": einoopenai.ReasoningEffortLevelMedium,
		"high":   einoopenai.ReasoningEffortLevelHigh,
	} {
		cfg := &einoopenai.ChatModelConfig{ReasoningEffort: einoopenai.ReasoningEffortLevelHigh}
		einoChatModelTarget{cfg: cfg}.SetEffort(level)
		if cfg.ReasoningEffort != want {
			t.Errorf("SetEffort(%q) = %q, want %q", level, cfg.ReasoningEffort, want)
		}
	}
	cfg := &einoopenai.ChatModelConfig{ReasoningEffort: einoopenai.ReasoningEffortLevelLow}
	einoChatModelTarget{cfg: cfg}.SetEffort("")
	if cfg.ReasoningEffort != "" {
		t.Errorf("SetEffort(\"\") should clear the field, got %q", cfg.ReasoningEffort)
	}
}

func TestReasoningTargetKeepsExtraFieldsIdentity(t *testing.T) {
	fields := map[string]any{"keep": true}
	cfg := &einoopenai.ChatModelConfig{}
	target := einoChatModelTarget{cfg: cfg}
	target.SetExtraFields(fields)
	// The rules mutate the map returned by ExtraFields(); that must be the same map the
	// config holds, or every field they add would be silently dropped.
	target.ExtraFields()["added"] = 1
	if cfg.ExtraFields["keep"] != true || cfg.ExtraFields["added"] != 1 {
		t.Fatalf("config fields = %#v, want both keep and added", cfg.ExtraFields)
	}
}

func TestApplyReasoningOff_wirePayloadOmitsThinking(t *testing.T) {
	var requestBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &requestBody); err != nil {
			t.Errorf("decode request body: %v; body=%s", err, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	cfg := &einoopenai.ChatModelConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "gpt-4o-mini",
	}
	oa := &config.OpenAIConfig{
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4o-mini",
		Reasoning: config.OpenAIReasoningConfig{
			Mode: "off", Effort: "high", Profile: "openai_compat",
		},
	}
	ApplyReasoningToChatModelConfig(cfg, oa, nil)
	model, err := einoopenai.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new chat model: %v", err)
	}
	if _, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, key := range reasoningPayloadKeys {
		if _, ok := requestBody[key]; ok {
			t.Fatalf("wire payload unexpectedly contains %q: %#v", key, requestBody)
		}
	}
}
