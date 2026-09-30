package app

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp/builtin"
)

// repoToolsDir is the shipped recipe directory; the contract is that every one
// of these registers, because a recipe without a manifest is refused at runtime.
func repoToolsDir() string { return filepath.Join("..", "..", "tools") }

// TestShippedRecipesAllRegister is the P1/P2 gate: 90 shipped recipes must each
// declare an enforceable capability manifest. A rejection here means the tool
// would silently become unexecutable for users.
func TestShippedRecipesAllRegister(t *testing.T) {
	tools, err := config.LoadToolsFromDir(repoToolsDir())
	if err != nil {
		t.Fatalf("load recipes: %v", err)
	}
	if len(tools) < 80 {
		t.Fatalf("expected the shipped recipe set, got %d tools", len(tools))
	}

	manifested := 0
	for _, tool := range tools {
		if tool.Capability != nil {
			manifested++
		}
	}
	if manifested != len(tools) {
		t.Fatalf("%d of %d recipes declare a capability manifest", manifested, len(tools))
	}

	specs, rejections := RecipeSpecs(tools)
	if len(rejections) > 0 {
		for _, r := range rejections {
			t.Errorf("recipe %s rejected: %s", r.ToolName, r.Reason)
		}
		t.FailNow()
	}
	if len(specs) != len(tools) {
		t.Fatalf("registered %d specs for %d recipes", len(specs), len(tools))
	}

	registry := capability.NewRegistry()
	if err := registry.RegisterAll(capability.LayerRecipe, specs); err != nil {
		t.Fatalf("recipe layer registration: %v", err)
	}

	// The three recipes that execute model-supplied code must be destructive and
	// must not authorize through the historical coarse fallback.
	for _, name := range []string{"exec", "angr", "pwntools", "execute-python-script", "install-python-package"} {
		spec, err := registry.Lookup(name)
		if err != nil {
			t.Fatalf("recipe %s is not registered: %v", name, err)
		}
		if spec.Class != capability.ClassDestructive {
			t.Errorf("recipe %s class = %s, want destructive", name, spec.Class)
		}
		if !spec.RequiresHumanDecision() {
			t.Errorf("recipe %s does not require per-call authorization", name)
		}
		if spec.Permission == capability.CoarseFallbackPermission {
			t.Errorf("recipe %s still authorizes through %s", name, spec.Permission)
		}
		if len(spec.Grants) == 0 {
			t.Errorf("recipe %s declares no capability grants", name)
		}
	}
}

// TestDeclaredConstantNamesHavePolicies is the drift gate that replaces the three
// hand-maintained name lists. builtin.GetAllBuiltinTools() is now derived from
// the policy table, so comparing the two would prove nothing; instead this reads
// the declared string constants straight out of constants.go and asserts every
// one of them has a policy, and that no policy names a tool nobody declared.
func TestDeclaredConstantNamesHavePolicies(t *testing.T) {
	declared := declaredBuiltinConstants(t)

	for _, name := range declared {
		spec, err := capability.Global().Lookup(name)
		if err != nil {
			t.Errorf("tool %q is declared in builtin constants but has no capability policy", name)
			continue
		}
		if !spec.Builtin {
			t.Errorf("tool %q resolved to %s, which is not a built-in policy", name, spec.ID)
		}
	}

	// Every policy must be reachable by name: either a declared built-in constant
	// or a shipped recipe. A name in neither list is a policy nobody can call.
	recipes := map[string]bool{}
	for _, tool := range shippedRecipes(t) {
		recipes[tool.Name] = true
	}
	for _, spec := range capability.BuiltinSpecs() {
		if spec.Virtual {
			continue
		}
		known := false
		for _, name := range declared {
			if name == spec.Name {
				known = true
			}
		}
		if !known && !recipes[spec.Name] {
			t.Errorf("capability table has %q (%s) which is neither a builtin constant nor a shipped recipe", spec.Name, spec.ID)
		}
	}
}

func shippedRecipes(t *testing.T) []config.ToolConfig {
	t.Helper()
	tools, err := config.LoadToolsFromDir(repoToolsDir())
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

// declaredBuiltinConstants parses the const block of internal/mcp/builtin/constants.go
// for its string literals, so the check reads the source of truth rather than a
// second copy of it.
func declaredBuiltinConstants(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "mcp", "builtin", "constants.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Values) != 1 {
				continue
			}
			lit, ok := value.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			unquoted, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			names = append(names, unquoted)
		}
	}
	if len(names) < 50 {
		t.Fatalf("expected the shipped built-in name list, parsed %d", len(names))
	}
	return names
}

// TestUnregisteredCapabilityFailsClosed proves the removal of the fail-open
// default branch: an unknown name is refused even for a principal holding the
// coarse local-execute permission.
func TestUnregisteredCapabilityFailsClosed(t *testing.T) {
	_, err := RefreshRecipeLayer(nil)
	if err != nil {
		t.Fatal(err)
	}
	authorize := mcpToolAuthorizer(nil)
	principal := authctx.NewPrincipal("u1", "user", database.RBACScopeAll, map[string]bool{
		"agent:execute": true, "agent:local-execute": true, "agent:destructive-execute": true,
	})
	ctx := authctx.WithPrincipal(context.Background(), principal)
	for _, name := range []string{"community_tool", "angr", "exec", "vendor.new_capability"} {
		if err := authorize(ctx, name, nil); err == nil {
			t.Errorf("%s was authorized without a registered manifest", name)
		}
	}
}

// TestDestructiveRecipeCannotReuseCoarsePermission is the store-facing invariant:
// a submitted artifact can never inherit the catch-all grant.
func TestDestructiveRecipeCannotReuseCoarsePermission(t *testing.T) {
	_, err := capability.SpecFromRecipe(capability.RecipeManifest{
		ID: "acme.bad_recipe", ToolName: "bad_recipe", Class: "destructive",
		Permission: capability.CoarseFallbackPermission, Grants: []string{"process.exec(sh)"},
	})
	if err == nil {
		t.Fatal("a destructive recipe was accepted with the coarse fallback permission")
	}

	// Approval cannot be talked out of a destructive class either.
	spec, err := capability.SpecFromRecipe(capability.RecipeManifest{
		ID: "acme.bad_recipe2", ToolName: "bad_recipe2", Class: "destructive",
		Permission: "agent:destructive-execute", Approval: "never",
		Grants: []string{"process.exec(sh)"},
	})
	if err == nil && !spec.RequiresHumanDecision() {
		t.Fatal("a destructive recipe escaped the approval floor")
	}
}

// TestCapabilityIdentityRules pins the one identity string the platform uses.
func TestCapabilityIdentityRules(t *testing.T) {
	if got := capability.CoreID("c2_task"); got != "core.c2_task" {
		t.Fatalf("CoreID = %q", got)
	}
	publisher, name, err := capability.ParseID("acme-team.sub.scanner")
	if err != nil || publisher != "acme-team" || name != "sub.scanner" {
		t.Fatalf("ParseID = %q %q %v", publisher, name, err)
	}
	for _, bad := range []string{"core", "UPPER.case", "a..b", ""} {
		if _, _, err := capability.ParseID(bad); err == nil {
			t.Errorf("identity %q should be rejected", bad)
		}
	}

	registry := capability.NewRegistry()
	seed, err := capability.SpecFromRecipe(capability.RecipeManifest{
		ID: "core.nmap", ToolName: "nmap", Class: "mutating",
		Permission: "agent:local-execute", Grants: []string{"net.connect(target)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(capability.LayerRecipe, seed); err != nil {
		t.Fatal(err)
	}
	// "cora.nmap" is one edit from a shipped name: exactly the typosquat a store
	// must flag before a user installs it.
	if hits := registry.CheckTyposquat("cora.nmap"); len(hits) == 0 {
		t.Fatal("typosquat of a shipped capability was not detected")
	}
	if hits := registry.CheckTyposquat("unrelated.vendor.tool"); len(hits) != 0 {
		t.Fatalf("unrelated identity flagged: %v", hits)
	}
}

// TestRecipeLayerReplacementKeepsBuiltinPolicies is the ClearTools regression:
// reloading recipes must never drop a built-in policy.
func TestRecipeLayerReplacementKeepsBuiltinPolicies(t *testing.T) {
	tools := []config.ToolConfig{{
		Name:    "reload_probe",
		Command: "true",
		Capability: &config.CapabilityManifest{
			ID: "core.reload_probe", Class: "mutating",
			Permission: "agent:local-execute", Grants: []string{"process.exec(true)"},
		},
	}}
	if _, err := RefreshRecipeLayer(tools); err != nil {
		t.Fatal(err)
	}
	registry := capability.Global()
	if _, err := registry.Lookup("reload_probe"); err != nil {
		t.Fatalf("recipe did not register: %v", err)
	}
	if _, err := RefreshRecipeLayer(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup("reload_probe"); err == nil {
		t.Fatal("replaced recipe layer survived removal")
	}
	for _, name := range []string{builtin.ToolWebshellExec, builtin.ToolC2Task, builtin.ToolRecordVulnerability} {
		if _, err := registry.Lookup(name); err != nil {
			t.Fatalf("built-in policy %s was dropped by a recipe reload", name)
		}
	}
}

// TestApprovalGrantIsSingleUse proves one human decision releases exactly one
// invocation, scoped to the capability that was approved.
func TestApprovalGrantIsSingleUse(t *testing.T) {
	ledger := capability.NewApprovalLedger(0)
	ledger.Grant("conv-1", builtin.ToolWebshellExec, 1)
	if !ledger.Consume("conv-1", builtin.ToolWebshellExec) {
		t.Fatal("grant was not consumable")
	}
	if ledger.Consume("conv-1", builtin.ToolWebshellExec) {
		t.Fatal("a spent grant authorized a second call")
	}
	if ledger.Consume("conv-2", builtin.ToolWebshellExec) {
		t.Fatal("a grant leaked across conversations")
	}
	if ledger.Consume("conv-1", builtin.ToolC2Task) {
		t.Fatal("a grant leaked across capabilities")
	}
	// Approving a capability with no approval floor is a no-op: grants cannot
	// become a way to bypass the permission check.
	ledger.Grant("conv-1", builtin.ToolQueryAssets, 5)
	if ledger.Pending("conv-1") != 0 {
		t.Fatal("a readonly capability was granted an approval token")
	}
}
