package app

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The approval-configuration endpoints answer from HitlPolicy, not from AgentHandler. This is
// the gate that keeps the split from quietly reversing: a route re-hung on the agent, or a
// twin method added back to it, fails here rather than in review.

var policyEndpoints = []struct{ path, method, oldName string }{
	{"/hitl/config/:conversationId", "GetConversationConfig", "GetHITLConversationConfig"},
	{"/hitl/config", "UpsertConversationConfig", "UpsertHITLConversationConfig"},
	{"/hitl/tool-whitelist", "GetGlobalToolWhitelist", "GetHITLGlobalToolWhitelist"},
	{"/hitl/tool-whitelist", "SetGlobalToolWhitelist", "SetHITLGlobalToolWhitelist"},
	{"/hitl/tool-whitelist", "MergeGlobalToolWhitelist", "MergeHITLGlobalToolWhitelist"},
	{"/hitl/default-config", "GetDefaultConfig", "GetHITLDefaultConfig"},
	{"/hitl/default-config", "UpdateDefaultConfig", "UpdateHITLDefaultConfig"},
	{"/hitl/default-reviewer", "GetDefaultReviewer", "GetHITLDefaultReviewer"},
	{"/hitl/default-reviewer", "UpdateDefaultReviewer", "UpdateHITLDefaultReviewer"},
	{"/hitl/audit-strategy", "GetAuditStrategy", "GetHITLAuditStrategy"},
	{"/hitl/audit-strategy", "UpdateAuditStrategy", "UpdateHITLAuditStrategy"},
}

func handlerSources(t *testing.T) string {
	t.Helper()
	// The whole directory, not one file: a twin method can be added to any of the twenty-two
	// files AgentHandler is spread across, and a single-file reader would report a clean
	// handler while the god object grows back.
	files, err := filepath.Glob(filepath.Join("..", "handler", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 40 {
		t.Fatalf("the scan saw %d handler files; it is reading a subset", len(files))
	}
	var b strings.Builder
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		seen++
		b.Write(data)
	}
	if seen < 40 {
		t.Fatalf("only %d non-test handler files were read", seen)
	}
	return b.String()
}

func TestApprovalConfigRoutesAnswerFromThePolicy(t *testing.T) {
	routes, err := os.ReadFile("routes_hitl.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(routes)
	// One path can carry several methods (GET, PUT and POST on /hitl/tool-whitelist are three
	// different endpoints), so the assertion is "some registration line names this path and
	// this method", not "every line naming the path uses the method".
	lines := strings.Split(text, "\n")
	for _, ep := range policyEndpoints {
		want := `"` + ep.path + `"`
		found := false
		for _, line := range lines {
			if strings.Contains(line, want) && strings.Contains(line, "HitlPolicy()."+ep.method) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s %s is not registered on the policy collaborator", ep.method, ep.path)
		}
	}
}

func TestApprovalConfigMethodsAreNotOnAgentHandler(t *testing.T) {
	src := handlerSources(t)
	var twins []string
	for _, ep := range policyEndpoints {
		// The old names are the ones the sidebar and the docs already use; if one reappears on
		// the agent it is a second implementation of the same rule, and the two will drift.
		re := regexp.MustCompile(`func \([a-z]+ \*AgentHandler\) ` + ep.oldName + `\(`)
		if re.MatchString(src) {
			twins = append(twins, ep.oldName)
		}
	}
	sort.Strings(twins)
	if len(twins) > 0 {
		t.Errorf("AgentHandler grew these approval-configuration methods back: %v. They belong on HitlPolicy.", twins)
	}

	// And the collaborator must still own all of them - the scan is only meaningful if a
	// deletion is what made the first assertion pass.
	var missing []string
	for _, ep := range policyEndpoints {
		re := regexp.MustCompile(`func \([a-z]+ \*HitlPolicy\) ` + ep.method + `\(`)
		if !re.MatchString(src) {
			missing = append(missing, ep.method)
		}
	}
	if len(missing) > 0 {
		t.Errorf("HitlPolicy lost %d of its %d endpoints: %v", len(missing), len(policyEndpoints), missing)
	}
}
