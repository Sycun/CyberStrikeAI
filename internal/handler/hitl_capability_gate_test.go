package handler

import (
	"strconv"
	"sync"
	"testing"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/settings"

	"go.uber.org/zap"
)

// TestTwoHandlersOneStoreIsRaceFree is the S4 regression. ConfigHandler and
// AgentHandler both used to hold the same *config.Config and wrote the HITL
// section through different locks (one of them with no lock at all), so a config
// apply racing an agent turn corrupted shared memory. Both now publish through
// one snapshot store.
func TestTwoHandlersOneStoreIsRaceFree(t *testing.T) {
	cfg := &config.Config{Hitl: config.HitlConfig{
		DefaultMode:     "approval",
		DefaultReviewer: "human",
		ToolWhitelist:   []string{"read_file"},
	}}
	store := settings.New(cfg)

	configHandler := &ConfigHandler{config: cfg, logger: zap.NewNop()}
	agentHandler := &AgentHandler{config: cfg, logger: zap.NewNop()}
	configHandler.SetSettings(store)
	agentHandler.SetSettings(store)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 150; j++ {
				configHandler.publishHitl(func(hitl *config.HitlConfig) {
					hitl.DefaultReviewer = "config-" + strconv.Itoa(n)
					hitl.ToolWhitelist = append([]string{"read_file"}, "glob")
				})
			}
		}(i)

		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 150; j++ {
				timeout := j
				agentHandler.publishHitl(func(hitl *config.HitlConfig) {
					hitl.DefaultMode = "review_edit"
					hitl.DefaultTimeoutSeconds = &timeout
					hitl.AuditAgentPrompt = "agent-" + strconv.Itoa(n)
				})
			}
		}(i)

		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 300; j++ {
				if got := agentHandler.hitl(); len(got.ToolWhitelist) == 0 {
					t.Error("reader observed an emptied whitelist")
					return
				}
				_ = configHandler.hitl().DefaultMode
			}
		}()
	}
	wg.Wait()

	// The legacy fallback must still work for handlers constructed without a store.
	legacy := &AgentHandler{config: &config.Config{}, logger: zap.NewNop()}
	legacy.publishHitl(func(hitl *config.HitlConfig) { hitl.DefaultMode = "off" })
	if legacy.hitl().DefaultMode != "off" {
		t.Fatal("store-less handler lost its write path")
	}
}

// TestHITLExemptionCannotBeWidenedByRequestBody pins the S2 intersection: a tool
// is exempt only when the operator listed it in config.yaml, and a destructive
// capability is never exempt regardless of either list.
func TestHITLExemptionCannotBeWidenedByRequestBody(t *testing.T) {
	manager := NewHITLManager(nil, zap.NewNop())
	manager.SetGlobalWhitelist([]string{"read_file"})

	// A session that claims exec is exempt for itself changes nothing: the
	// operator's list is the half that decides.
	manager.ActivateConversation("conv-1", &HITLRequest{
		Enabled: true, Mode: "approval",
		SensitiveTools: []string{"read_file", "exec"},
	})
	if manager.NeedsToolApproval("conv-1", "read_file") {
		t.Fatal("operator-whitelisted tool should stay exempt")
	}
	if !manager.NeedsToolApproval("conv-1", "exec") {
		t.Fatal("a request body widened the exemption set")
	}

	// Destructive capabilities keep their approval floor even when both lists
	// name them and the session never opted into HITL at all.
	manager.SetGlobalWhitelist([]string{"nmap"})
	if spec, err := capability.Global().Lookup("nmap"); err == nil && spec.RequiresHumanDecision() {
		if manager.NeedsToolApproval("conv-never-activated", "nmap") != true {
			t.Fatal("an inactive session escaped the approval floor")
		}
	}
	// An unregistered name is not exempt: it is simply not in the operator's list.
	// The registry refuses it at execution, so no exemption can be claimed for it.
	if !manager.NeedsToolApproval("conv-1", "not_a_registered_capability") {
		t.Fatal("an unnamed tool was exempted without appearing in either whitelist")
	}
}
