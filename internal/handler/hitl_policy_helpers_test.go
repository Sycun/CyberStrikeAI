package handler

import "testing"

// attachHitlPolicy wires the approval-policy collaborator into a handler built by struct
// literal in a test.
//
// It exists because those literals used to be enough on their own: the approval rules lived on
// AgentHandler, so a test that set config and logger got working reads for free. After the
// split a literal without a policy answers with "off"/"human"/300 defaults and can pass for the
// wrong reason, which is worse than failing. Production always builds the policy in
// NewAgentHandler; this mirrors that one line for the test's own constructor.
func attachHitlPolicy(t *testing.T, h *AgentHandler) *AgentHandler {
	t.Helper()
	if h == nil {
		t.Fatal("attachHitlPolicy: nil handler")
	}
	if h.hitlPolicy == nil {
		h.hitlPolicy = NewHitlPolicy(h.config, h.settings, h.hitlManager, h.hitlQueue, h.logger)
	}
	return h
}

// TestAttachHitlPolicyIsNotDeadCode guards the helper itself: an unused helper would mean no
// test needed it, and that would mean the split went untested at the literal-construction sites.
func TestAttachHitlPolicyIsNotDeadCode(t *testing.T) {
	h := attachHitlPolicy(t, &AgentHandler{config: nil, logger: nil})
	if h.HitlPolicy() == nil {
		t.Fatal("attachHitlPolicy produced no policy")
	}
	// A handler with no configuration must resolve to the documented defaults rather than
	// panic, because that is what the endpoints answer before config.yaml is loaded.
	if got := h.HitlPolicy().hitlEffectiveDefaultMode(); got != "off" {
		t.Fatalf("default mode = %q, want off", got)
	}
	if got := h.HitlPolicy().hitlEffectiveDefaultReviewer(); got != "human" {
		t.Fatalf("default reviewer = %q, want human", got)
	}
	if got := h.HitlPolicy().hitlEffectiveDefaultTimeoutSeconds(); got != 300 {
		t.Fatalf("default timeout = %d, want 300", got)
	}
}
