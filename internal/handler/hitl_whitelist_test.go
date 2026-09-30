package handler

import "testing"

func TestHITLBuiltInWhitelistExemptsWriteFile(t *testing.T) {
	// The merge rule lives on the approval policy, so the test builds exactly that and nothing
	// else: no agent, no configuration - which is the case where only the platform's built-in
	// exemptions can be what makes write_file run without approval.
	policy := NewHitlPolicy(nil, nil, nil, nil, nil)
	req := policy.hitlRequestWithMergedConfigWhitelist(&HITLRequest{
		Enabled: true,
		Mode:    "approval",
	})

	manager := NewHITLManager(nil, nil)
	manager.ActivateConversation("conversation-1", req)

	if manager.NeedsToolApproval("conversation-1", "write_file") {
		t.Fatal("write_file should use the built-in HITL exemption")
	}
	if !manager.NeedsToolApproval("conversation-1", "exec") {
		t.Fatal("non-exempt tools should still require approval")
	}
}
