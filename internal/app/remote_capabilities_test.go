package app

import (
	"context"
	"testing"

	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"
)

// Remote MCP tools used to be admitted by one namespace policy that ignored the tool name
// entirely. These tests cover the identity itself, and the two properties that make the change
// safe to land: nothing is allowed that was not allowed before, and nothing that worked before
// starts failing because a spec is missing.

func remoteTools(names ...string) []mcp.Tool {
	out := make([]mcp.Tool, 0, len(names))
	for _, n := range names {
		out = append(out, mcp.Tool{Name: n, Description: "desc " + n, ShortDescription: "short " + n})
	}
	return out
}

func remotePrincipal(scope string, permissions ...string) context.Context {
	set := map[string]bool{}
	for _, p := range permissions {
		set[p] = true
	}
	return authctx.WithPrincipal(context.Background(), authctx.NewPrincipal("u1", "user", scope, set))
}

func TestRemoteToolInventoryBecomesAddressableCapabilities(t *testing.T) {
	const server = "probe-inventory"
	t.Cleanup(func() { _ = applyRemoteToolInventory(server, nil) })

	if err := applyRemoteToolInventory(server, remoteTools("search_code", "create_issue")); err != nil {
		t.Fatalf("applyRemoteToolInventory: %v", err)
	}

	spec, ok := lookupRemoteToolSpec(server + "::search_code")
	if !ok {
		t.Fatalf("the remote tool has no capability identity")
	}
	if spec.ID != "remote."+server+".search_code" {
		t.Errorf("ID = %q", spec.ID)
	}
	if spec.Permission != externalMCPNamespacePermission {
		t.Errorf("Permission = %q, want the same floor as before this change", spec.Permission)
	}
	if spec.Runtime != capability.RuntimeMCPRemote {
		t.Errorf("Runtime = %q", spec.Runtime)
	}
	if spec.Builtin {
		t.Errorf("a remote tool must not claim the shipped-binary identity space")
	}
	if spec.Title != "short search_code" {
		t.Errorf("Title = %q, want the server's short description", spec.Title)
	}

	// The same tool of another server is a different identity, and one server's inventory says
	// nothing about another's.
	if _, other := lookupRemoteToolSpec("other-server::search_code"); other {
		t.Errorf("an unrelated server resolved as registered")
	}
}

func TestRemoteAuthorizerDecidesOnTheToolIdentity(t *testing.T) {
	const server = "probe-authorizer"
	t.Cleanup(func() { _ = applyRemoteToolInventory(server, nil) })
	if err := applyRemoteToolInventory(server, remoteTools("run_sql")); err != nil {
		t.Fatalf("applyRemoteToolInventory: %v", err)
	}
	authorize := externalMCPToolAuthorizer()

	allowed := remotePrincipal(database.RBACScopeAll, externalMCPNamespacePermission)
	if err := authorize(allowed, server+"::run_sql", nil); err != nil {
		t.Fatalf("the inherited permission no longer admits the remote tool: %v", err)
	}
	if err := authorize(remotePrincipal(database.RBACScopeAll), server+"::run_sql", nil); err == nil {
		t.Fatalf("a principal without the permission was allowed")
	}

	// Now pin this one tool to a different permission. The point of the identity is exactly that
	// a rule can name a single remote tool, so the decision must follow the tool and not the
	// namespace.
	if err := capability.Global().RegisterSubset(capability.LayerRemote, server, []*capability.Spec{{
		ID:         remoteToolID(server, "run_sql"),
		Name:       remoteToolName(server, "run_sql"),
		Class:      capability.ClassMutating,
		Runtime:    capability.RuntimeMCPRemote,
		Permission: "plugins:install",
	}}); err != nil {
		t.Fatalf("override register: %v", err)
	}
	if err := authorize(allowed, server+"::run_sql", nil); err == nil {
		t.Fatalf("the per-tool permission was ignored; the namespace floor still admitted the call")
	}
	special := remotePrincipal(database.RBACScopeAll, "plugins:install")
	if err := authorize(special, server+"::run_sql", nil); err != nil {
		t.Fatalf("the per-tool permission was not honoured: %v", err)
	}
	// A sibling tool of the same server keeps the inherited policy, so the override is per tool
	// and not per server.
	if err := applyRemoteToolInventory(server, remoteTools("run_sql", "read_docs")); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if err := authorize(allowed, server+"::read_docs", nil); err != nil {
		t.Fatalf("a sibling remote tool was caught by the override: %v", err)
	}
}

// TestRemoteAuthorizerFallsBackToTheNamespacePolicy is the no-regression clause: a tool the
// registry has never seen (server not connected yet, or an inventory refresh in flight) is still
// decided by the declared namespace policy. That is a registered policy, not an unregistered pass.
func TestRemoteAuthorizerFallsBackToTheNamespacePolicy(t *testing.T) {
	authorize := externalMCPToolAuthorizer()
	const unknown = "not-yet-registered-server::some_tool"
	if _, ok := lookupRemoteToolSpec(unknown); ok {
		t.Fatalf("the test server is unexpectedly registered")
	}
	if err := authorize(remotePrincipal(database.RBACScopeAll, externalMCPNamespacePermission), unknown, nil); err != nil {
		t.Fatalf("fallback denied a call the old code allowed: %v", err)
	}
	if err := authorize(remotePrincipal(database.RBACScopeAssigned, externalMCPNamespacePermission), unknown, nil); err == nil {
		t.Fatalf("fallback lost the global-scope floor the namespace policy enforces")
	}
}

func TestEmptyInventoryDropsOnlyThatServersRemoteEntries(t *testing.T) {
	const a, b = "probe-drop-a", "probe-drop-b"
	t.Cleanup(func() {
		_ = applyRemoteToolInventory(a, nil)
		_ = applyRemoteToolInventory(b, nil)
	})
	if err := applyRemoteToolInventory(a, remoteTools("one")); err != nil {
		t.Fatal(err)
	}
	if err := applyRemoteToolInventory(b, remoteTools("two")); err != nil {
		t.Fatal(err)
	}
	if err := applyRemoteToolInventory(a, nil); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, ok := lookupRemoteToolSpec(a + "::one"); ok {
		t.Errorf("stopped server still has a capability")
	}
	if _, ok := lookupRemoteToolSpec(b + "::two"); !ok {
		t.Errorf("dropping one server removed another server's capability")
	}
	// The namespace policy itself is a built-in and must survive every remote churn.
	if _, err := capability.Global().Lookup(externalMCPNamespacePermission); err != nil {
		t.Fatalf("the namespace policy disappeared: %v", err)
	}
}

func TestRemoteInventoryRejectsAnEmptyServerName(t *testing.T) {
	if err := applyRemoteToolInventory("  ", remoteTools("x")); err == nil {
		t.Fatalf("an ownerless inventory update was accepted")
	}
}
