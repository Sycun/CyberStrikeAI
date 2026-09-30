package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// This is the suite the report asks for: one set of behavioural expectations,
// applied to every dialect, so "works with vendor X" stops being a claim about a
// code branch and becomes a measured property. It is also the only objective judge
// for whether two near-duplicate code paths can be merged safely.

func vendors(t *testing.T) []Dialect {
	t.Helper()
	catalog := Default()
	out := make([]Dialect, 0, len(catalog.Vendors()))
	for _, name := range catalog.Vendors() {
		dialect, ok := catalog.Lookup(name)
		if !ok {
			t.Fatalf("catalog lists %s but Lookup fails", name)
		}
		dialect.BaseURL = "https://api.invalid"
		out = append(out, dialect)
	}
	if len(out) < 5 {
		t.Fatalf("expected the shipped vendor table, got %d", len(out))
	}
	return out
}

// TestAgenticBackendSetMatchesTheLegacyRule pins the migration: before the dialect
// catalog existed, the runtime accepted a fixed set of provider name strings. If a
// catalog edit silently adds or removes a vendor from that path, the model factory
// starts refusing (or accepting) providers it should not, and no other test notices.
func TestAgenticBackendSetMatchesTheLegacyRule(t *testing.T) {
	accepted := map[string]bool{
		"": true, "openai": true, "openai_compatible": true, "claude": true, "anthropic": true,
	}
	refused := map[string]bool{
		"deepseek": true, "dashscope": true, "some-new-gateway": true, "mistral": true,
	}
	for vendor := range accepted {
		if !AgenticBackendSupported(vendor) {
			t.Errorf("provider %q was supported before the catalog and is not now", vendor)
		}
	}
	for vendor := range refused {
		if AgenticBackendSupported(vendor) {
			t.Errorf("provider %q was refused before the catalog and is now accepted", vendor)
		}
	}
	if !IsAnthropicMessagesVendor("claude") || !IsAnthropicMessagesVendor("ANTHROPIC") {
		t.Fatal("the messages dialect must be recognised case-insensitively")
	}
	if IsAnthropicMessagesVendor("openai") || IsAnthropicMessagesVendor("unknown") {
		t.Fatal("a chat dialect was reported as the messages dialect")
	}
	if got := EffectiveProviderName("openai_compatible"); got != "openai" {
		t.Fatalf("channel alias resolution = %q, want openai", got)
	}
	if got := EffectiveProviderName(""); got != "openai" {
		t.Fatalf("empty provider = %q, want openai", got)
	}
	if got := EffectiveProviderName("claude"); got != "claude" {
		t.Fatalf("claude must not be aliased, got %q", got)
	}
}

// TestVendorDefaultsAreData locks the settings-handler migration: a vendor's default
// base URL and whether model discovery is attempted must come from the catalog, and
// the fallback must keep the pre-catalog behaviour for anything not listed.
func TestVendorDefaultsAreData(t *testing.T) {
	cases := map[string]string{
		"claude":            "https://api.anthropic.com",
		"anthropic":         "https://api.anthropic.com",
		"openai":            "https://api.openai.com/v1",
		"openai_compatible": "https://api.openai.com/v1",
		"":                  "https://api.openai.com/v1",
		"unlisted-gateway":  "https://api.openai.com/v1",
	}
	for vendor, want := range cases {
		if got := DefaultBaseURLFor(vendor); got != want {
			t.Errorf("DefaultBaseURLFor(%q) = %q, want %q", vendor, got, want)
		}
	}
	for _, vendor := range []string{"claude", "anthropic"} {
		if ListsModels(vendor) {
			t.Errorf("%s must not attempt model discovery", vendor)
		}
	}
	for _, vendor := range []string{"openai", "openai_compatible", "deepseek", "some-gateway", ""} {
		if !ListsModels(vendor) {
			t.Errorf("%s lost model discovery that it had before the catalog", vendor)
		}
	}
}

func TestEveryVendorDeclaresAKnownDialect(t *testing.T) {
	for _, dialect := range vendors(t) {
		switch dialect.API {
		case APIOpenAIChat, APIOpenAIResponses, APIAnthropicMessages:
		default:
			t.Errorf("%s: unknown API %q", dialect.Vendor, dialect.API)
		}
		if len(dialect.OverflowMarkers) == 0 {
			t.Errorf("%s: no context-overflow markers, so overflow would classify as unknown", dialect.Vendor)
		}
		if len(dialect.Retry.RetryableStatus) == 0 {
			t.Errorf("%s: no retry posture", dialect.Vendor)
		}
		if dialect.ContextWindow <= 0 {
			t.Errorf("%s: context window must be known for budget maths", dialect.Vendor)
		}
	}
}

// TestContextOverflowIsClassedTheSameWayAcrossVendors is the one that matters most in
// practice: the run must compact or fail, not retry a 400 forever.
func TestContextOverflowIsClassedTheSameWayAcrossVendors(t *testing.T) {
	for _, dialect := range vendors(t) {
		for _, marker := range dialect.OverflowMarkers {
			status := http.StatusBadRequest
			if got := dialect.ClassifyError(status, fmt.Sprintf(`{"error":{"message":"%s something"}}`, marker)); got != ClassContextOverflow {
				t.Errorf("%s: marker %q classified as %q, want context_overflow", dialect.Vendor, marker, got)
			}
			if dialect.IsRetryableStatus(status) {
				t.Errorf("%s: a 400 overflow would be retried", dialect.Vendor)
			}
		}
	}
}

func TestAbortAndTransientAreClassedTheSameWayAcrossVendors(t *testing.T) {
	for _, dialect := range vendors(t) {
		if got := dialect.ClassifyError(499, ""); got != ClassAborted {
			t.Errorf("%s: 499 -> %q, want aborted", dialect.Vendor, got)
		}
		if got := dialect.ClassifyError(http.StatusOK, "read failed: context canceled"); got != ClassAborted {
			t.Errorf("%s: canceled stream -> %q, want aborted", dialect.Vendor, got)
		}
		if got := dialect.ClassifyError(http.StatusTooManyRequests, "slow down"); got != ClassRateLimited {
			t.Errorf("%s: 429 -> %q, want rate_limited", dialect.Vendor, got)
		}
		if got := dialect.ClassifyError(http.StatusBadGateway, "upstream blew up"); got != ClassTransient {
			t.Errorf("%s: 502 -> %q, want transient", dialect.Vendor, got)
		}
		if got := dialect.ClassifyError(http.StatusUnauthorized, "bad key"); got != ClassAuth {
			t.Errorf("%s: 401 -> %q, want auth", dialect.Vendor, got)
		}
	}
}

func TestToolCallWithoutResultIsHandledByDeclarationNotByBranch(t *testing.T) {
	dangling := Conversation{
		{Role: RoleUser, Text: "scan it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "nmap", Arguments: `{"target":"x"}`}}},
	}
	orphanResult := Conversation{
		{Role: RoleUser, Text: "scan it"},
		{Role: RoleTool, Text: "done"},
	}

	for _, dialect := range vendors(t) {
		err := dialect.Validate(dangling)
		if dialect.Capabilities.RejectsDanglingToolCalls {
			if err == nil {
				t.Errorf("%s: a tool call with no result was accepted", dialect.Vendor)
			}
		} else if err != nil {
			t.Errorf("%s: dangling call refused although the dialect does not require results: %v", dialect.Vendor, err)
		}

		if err := dialect.Validate(orphanResult); err == nil && dialect.Capabilities.RejectsDanglingToolCalls {
			t.Errorf("%s: a tool result with no id was accepted", dialect.Vendor)
		}
	}
}

func TestSurrogatePairsSurviveEveryDialect(t *testing.T) {
	// Astral-plane text plus a literal escaped surrogate pair: both must arrive intact,
	// because a mangled emoji or CJK codepoint inside a tool argument breaks the call.
	// The escaped pair is produced through the JSON decoder rather than in Go source,
	// which rejects lone surrogate escapes - this is exactly the path a vendor's stream
	// takes on the way in.
	var escapedPair string
	if err := json.Unmarshal([]byte(`"\ud83d\ude00"`), &escapedPair); err != nil {
		t.Fatal(err)
	}
	if escapedPair != "\U0001F600" {
		t.Fatalf("decoder did not produce the emoji: %q", escapedPair)
	}
	content := "结果: 🔍𐍈 escaped=" + escapedPair
	for _, dialect := range vendors(t) {
		conv := Conversation{{Role: RoleUser, Text: content}}
		payload, err := dialect.RenderRequest("m", conv, 100, nil)
		if err != nil {
			t.Fatalf("%s: render: %v", dialect.Vendor, err)
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s: marshal: %v", dialect.Vendor, err)
		}
		if !strings.Contains(string(encoded), "结果") {
			t.Errorf("%s: payload lost the CJK text: %s", dialect.Vendor, encoded)
		}
		decoded := map[string]any{}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("%s: unmarshal: %v", dialect.Vendor, err)
		}
		parsed, _, err := dialect.ParseRequest(decoded)
		if err != nil {
			t.Fatalf("%s: parse: %v", dialect.Vendor, err)
		}
		if parsed[0].Text != content {
			t.Errorf("%s: text changed through the wire: %q != %q", dialect.Vendor, parsed[0].Text, content)
		}
		round, err := MarshalRoundTrip(content)
		if err != nil || round != content {
			t.Errorf("%s: marshal round trip failed: %q %v", dialect.Vendor, round, err)
		}
	}
}

func richTranscript() Conversation {
	return Conversation{
		{Role: RoleSystem, Text: "You are careful."},
		{Role: RoleUser, Text: "check the host 10.0.0.7"},
		{Role: RoleAssistant, Text: "port-scanning now", ToolCalls: []ToolCall{
			{ID: "call-1", Name: "nmap", Arguments: `{"target":"10.0.0.7","ports":[22,443]}`},
		}},
		{Role: RoleTool, ToolCallID: "call-1", Name: "nmap", Text: `{"open":[22]}`},
		{Role: RoleAssistant, Text: "22 and 443 are open"},
	}
}

// TestCrossProviderHandoffPreservesMeaning is the handoff case: a session moved from
// one vendor to another must keep tool identity, ordering and results, or the next
// model sees a transcript that lies about what already happened.
func TestCrossProviderHandoffPreservesMeaning(t *testing.T) {
	original := richTranscript()
	catalog := Default()

	pairs := [][2]string{{"openai", "claude"}, {"claude", "openai"}, {"deepseek", "anthropic"}, {"openai_compatible", "claude"}}
	for _, pair := range pairs {
		from, okFrom := catalog.Lookup(pair[0])
		to, okTo := catalog.Lookup(pair[1])
		if !okFrom || !okTo {
			t.Fatalf("catalog missing %v", pair)
		}
		from.BaseURL = "https://api.invalid"
		to.BaseURL = "https://api.invalid"

		payload, err := from.RenderRequest("model-a", original, 500, map[string]any{"stream": true})
		if err != nil {
			t.Fatalf("%s render: %v", pair[0], err)
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		decoded := map[string]any{}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		reinterpreted, modelA, err := from.ParseRequest(decoded)
		if err != nil {
			t.Fatalf("%s parse: %v", pair[0], err)
		}
		if modelA != "model-a" {
			t.Fatalf("%s lost the model name: %q", pair[0], modelA)
		}
		if err := to.Validate(reinterpreted); err != nil {
			t.Fatalf("%s -> %s handoff invalid: %v", pair[0], pair[1], err)
		}
		repainted, err := to.RenderRequest("model-b", reinterpreted, 500, nil)
		if err != nil {
			t.Fatalf("%s render: %v", pair[1], err)
		}
		roundTripped, _, err := to.ParseRequest(repainted)
		if err != nil {
			t.Fatalf("%s parse: %v", pair[1], err)
		}

		if len(roundTripped) < len(reinterpreted) {
			t.Fatalf("%s -> %s lost turns: %d < %d", pair[0], pair[1], len(roundTripped), len(reinterpreted))
		}
		if !hasToolCall(roundTripped, "call-1", "nmap") {
			t.Errorf("%s -> %s lost the tool call identity: %+v", pair[0], pair[1], roundTripped)
		}
		if !hasToolResult(roundTripped, "call-1") {
			t.Errorf("%s -> %s lost the tool result", pair[0], pair[1])
		}
		if !hasSystemTurn(roundTripped, "You are careful.") {
			t.Errorf("%s -> %s lost the system turn", pair[0], pair[1])
		}
	}
}

func hasToolCall(conv Conversation, id, name string) bool {
	for _, message := range conv {
		for _, call := range message.ToolCalls {
			if call.ID == id && call.Name == name {
				return true
			}
		}
	}
	return false
}

func hasToolResult(conv Conversation, id string) bool {
	for _, message := range conv {
		if message.Role == RoleTool && message.ToolCallID == id {
			return true
		}
	}
	return false
}

func hasSystemTurn(conv Conversation, text string) bool {
	for _, message := range conv {
		if message.Role == RoleSystem && strings.Contains(message.Text, text) {
			return true
		}
	}
	return false
}

func TestEndpointAndAuthShape(t *testing.T) {
	catalog := Default()
	chat, _ := catalog.Lookup("openai")
	chat.BaseURL = "https://api.openai.com/v1/"
	url, err := chat.EndpointURL()
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("chat endpoint = %q", url)
	}

	claude, _ := catalog.Lookup("claude")
	claude.BaseURL = "https://api.anthropic.com"
	url, err = claude.EndpointURL()
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("messages endpoint = %q", url)
	}
	claude.BaseURL = "https://gateway.invalid/v1"
	if url, _ := claude.EndpointURL(); url != "https://gateway.invalid/v1/messages" {
		t.Fatalf("a base URL already ending in /v1 was doubled: %q", url)
	}

	headers := http.Header{}
	chat.ApplyAuth(headers, "sk-chat")
	if got := headers.Get("Authorization"); got != "Bearer sk-chat" {
		t.Fatalf("chat auth = %q", got)
	}
	headers = http.Header{}
	claude.ApplyAuth(headers, "sk-ant")
	if headers.Get("Authorization") != "" {
		t.Fatal("the messages dialect must not send a bearer token")
	}
	if headers.Get("x-api-key") != "sk-ant" || headers.Get("anthropic-version") == "" {
		t.Fatalf("messages auth incomplete: %+v", headers)
	}

	if _, err := (&Dialect{Vendor: "x", API: APIOpenAIChat}).EndpointURL(); err == nil {
		t.Fatal("a dialect with no base URL produced an endpoint")
	}
}

// TestAddingAVendorIsDataNotCode is the report's gate for this layer: a gateway with
// its own quirks joins by registering a row, and every behavioural test above would
// then cover it without touching a call site.
func TestAddingAVendorIsDataNotCode(t *testing.T) {
	// A copy, not Default(): registering into the shared catalog leaks into every later
	// test in the package, and the published-catalog test (rightly) fails when the vendor
	// set changes underneath it.
	catalog := NewCatalog()
	if err := catalog.Register(Dialect{
		Vendor: "acme-gateway", API: APIAnthropicMessages, ContextWindow: 64_000, MaxTokensDefault: 4_096,
		Capabilities:    Capabilities{ThinkingBlocks: true, RejectsDanglingToolCalls: true},
		OverflowMarkers: []string{"window is too large"},
	}); err != nil {
		t.Fatal(err)
	}
	resolved := catalog.Resolve("acme-gateway", "https://gw.invalid/v1")
	if !resolved.IsAnthropicMessages() {
		t.Fatal("registered vendor did not resolve to its dialect")
	}
	if got := resolved.ClassifyError(400, `{"error":"the window is too large"}`); got != ClassContextOverflow {
		t.Fatalf("overflow classified as %q", got)
	}
	if err := resolved.Validate(richTranscript()); err != nil {
		t.Fatalf("registered vendor rejected a valid transcript: %v", err)
	}
	if _, err := resolved.RenderRequest("m", richTranscript(), 0, nil); err != nil {
		t.Fatalf("registered vendor render failed: %v", err)
	}
	// Unknown names degrade to the chat dialect rather than refusing to run.
	fallback := catalog.Resolve("some-new-thing", "https://other.invalid")
	if fallback.API != APIOpenAIChat || fallback.Vendor != "some-new-thing" {
		t.Fatalf("unknown vendor fallback = %+v", fallback)
	}
	if err := catalog.Register(Dialect{Vendor: "bad", API: "carrier-pigeon"}); err == nil {
		t.Fatal("an unknown API was accepted into the catalog")
	}
	if err := catalog.Register(Dialect{Vendor: "", API: APIOpenAIChat}); err == nil {
		t.Fatal("a nameless dialect was accepted")
	}
}

func TestReasoningAndToolChoiceInteractionFollowsTheMatrix(t *testing.T) {
	catalog := Default()
	unsafe, _ := catalog.Lookup("openai_compatible")
	unsafe.BaseURL = "https://gw.invalid"
	payload, err := unsafe.RenderRequest("m", Conversation{{Role: RoleUser, Text: "hi"}}, 100, map[string]any{
		"tool_choice": "required", "reasoning_effort": "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := payload["tool_choice"]; got != "auto" {
		t.Fatalf("unsafe gateway got a forced tool_choice: %v", got)
	}

	strict, _ := catalog.Lookup("openai")
	strict.BaseURL = "https://api.openai.com/v1"
	payload, err = strict.RenderRequest("m", Conversation{{Role: RoleUser, Text: "hi"}}, 100, map[string]any{
		"tool_choice": "required", "reasoning_effort": "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := payload["tool_choice"]; got != "required" {
		t.Fatalf("a dialect that supports forced choice was downgraded: %v", got)
	}

	messages, _ := catalog.Lookup("claude")
	messages.BaseURL = "https://api.anthropic.com"
	payload, err = messages.RenderRequest("m", richTranscript(), 1024, map[string]any{"thinking_budget_tokens": 2000})
	if err != nil {
		t.Fatalf("claude render: %v", err)
	}
	thinking, ok := payload["thinking"].(map[string]any)
	if !ok || thinking["budget_tokens"] != 2000 {
		t.Fatalf("thinking budget missing: %+v", payload["thinking"])
	}
	if _, present := payload["reasoning_effort"]; present {
		t.Fatal("a reasoning_effort field leaked into the messages dialect")
	}
	if _, present := payload["system"].(string); !present {
		t.Fatal("the system turn must be the top-level system field for this dialect")
	}
}

func TestStreamDeltaMergeIsIdenticalAcrossDialects(t *testing.T) {
	// One vendor emits whole tool-call objects per chunk, the other streams argument
	// fragments. Merging must land on the same accumulated call.
	for _, dialect := range vendors(t) {
		text := &strings.Builder{}
		calls := map[string]*ToolCall{}
		NormalizeAssistantDelta(text, calls, Message{Text: "he"})
		NormalizeAssistantDelta(text, calls, Message{ToolCalls: []ToolCall{{ID: "c1", Name: "nmap", Arguments: `{"tar`}}})
		NormalizeAssistantDelta(text, calls, Message{Text: "llo"})
		NormalizeAssistantDelta(text, calls, Message{ToolCalls: []ToolCall{{ID: "c1", Arguments: `get":"1.2.3.4"}`}}})
		if text.String() != "hello" {
			t.Errorf("%s: text merge = %q", dialect.Vendor, text.String())
		}
		merged, ok := calls["c1"]
		if !ok || merged.Arguments != `{"target":"1.2.3.4"}` || merged.Name != "nmap" {
			t.Errorf("%s: tool call merge = %+v", dialect.Vendor, calls)
		}
	}
}
