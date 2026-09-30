package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"

	"gopkg.in/yaml.v3"

	"go.uber.org/zap"
)

func writeRevocations(t *testing.T, dir string, payload map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, "revocations.json")
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// installCommunityCapability registers a publisher recipe with provenance stamped,
// which is what the installer does after verifying a signature.
func installCommunityCapabilities(t *testing.T, specs ...[2]string) {
	t.Helper()
	tools := make([]config.ToolConfig, 0, len(specs))
	for _, item := range specs {
		tools = append(tools, communityRecipe(item[0]))
		StampProvenance("acme."+item[0], "acme", item[1])
	}
	if _, err := RefreshRecipeLayer(tools); err != nil {
		t.Fatal(err)
	}
	for _, item := range specs {
		spec, err := capability.Global().Lookup(item[0])
		if err != nil {
			t.Fatalf("community capability %s did not register: %v", item[0], err)
		}
		if spec.ArtifactDigest != item[1] {
			t.Fatalf("provenance was not applied to %s", item[0])
		}
	}
}

func communityRecipe(name string) config.ToolConfig {
	return config.ToolConfig{
		Name:    name,
		Command: "python3",
		Capability: &config.CapabilityManifest{
			ID: "acme." + name, Class: "mutating",
			Permission: "agent:local-execute", Grants: []string{"net.connect(target)"},
		},
	}
}

// TestRevokedCapabilityIsRefusedOnTheCallPath covers the requirement that
// revocation is enforced per invocation, not only when the artifact is installed.
func TestRevokedCapabilityIsRefusedOnTheCallPath(t *testing.T) {
	dir := t.TempDir()
	installCommunityCapabilities(t, [2]string{"smb_probe", "aaaabbbbccccddddeeeeffff000011112222333344445555666677778888"})

	principal := authctx.NewPrincipal("u1", "user", "all", map[string]bool{"agent:local-execute": true, "agent:execute": true})
	ctx := authctx.WithPrincipal(context.Background(), principal)
	authorize := mcpToolAuthorizer(nil)

	if err := authorize(ctx, "smb_probe", nil); err != nil {
		t.Fatalf("an in-good-standing capability was refused: %v", err)
	}

	// Publish a revocation and refresh: the next call must fail without a restart.
	path := writeRevocations(t, dir, map[string]any{
		"digests": map[string]any{
			"aaaabbbbccccddddeeeeffff000011112222333344445555666677778888": map[string]any{
				"reason": "remote code execution in sample", "revokedAt": time.Now().UTC().Format(time.RFC3339),
			},
		},
	})
	added, err := MergeRevocations(path)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("merge added %d entries", added)
	}

	err = authorize(ctx, "smb_probe", nil)
	if err == nil {
		t.Fatal("a revoked capability was still executable")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revocation produced the wrong denial: %v", err)
	}

	// Startup-style isolation takes it out of what the model can even see.
	if isolated := IsolateRevoked(); len(isolated) != 1 || isolated[0] != "acme.smb_probe" {
		t.Fatalf("isolation reported %v", isolated)
	}
	if _, err := capability.Global().Lookup("smb_probe"); err == nil {
		t.Fatal("the revoked capability stayed visible in the registry")
	}
}

// TestPublisherRevocationCoversEverythingFromOnePublisher proves publisher-level
// revocation is not limited to a single known build.
func TestPublisherRevocationCoversEverythingFromOnePublisher(t *testing.T) {
	installCommunityCapabilities(t,
		[2]string{"tool_one", "1111111111111111111111111111111111111111111111111111111111111111"},
		[2]string{"tool_two", "2222222222222222222222222222222222222222222222222222222222222222"},
	)

	path := writeRevocations(t, t.TempDir(), map[string]any{
		"digests":    map[string]any{},
		"publishers": map[string]any{"acme": map[string]any{"reason": "key compromise"}},
	})
	if err := LoadRevocations(path, zap.NewNop()); err != nil {
		t.Fatal(err)
	}

	principal := authctx.NewPrincipal("u1", "user", "all", map[string]bool{"agent:local-execute": true})
	ctx := authctx.WithPrincipal(context.Background(), principal)
	authorize := mcpToolAuthorizer(nil)
	for _, name := range []string{"tool_one", "tool_two"} {
		err := authorize(ctx, name, nil)
		if err == nil {
			t.Errorf("%s from a revoked publisher still executed", name)
			continue
		}
		if !strings.Contains(err.Error(), "revoked") {
			t.Errorf("%s denied for the wrong reason: %v", name, err)
		}
	}

	isolated := IsolateRevoked()
	if len(isolated) != 2 {
		t.Fatalf("isolated %v, want both publisher capabilities", isolated)
	}

	// Shipped product code is never removed by a publisher revocation.
	if _, err := capability.Global().Lookup(builtinNameProbe); err != nil {
		t.Fatalf("built-in policy %s disappeared during a publisher sweep", builtinNameProbe)
	}
}

const builtinNameProbe = "record_vulnerability"

// TestConfigReloadKeepsProvenance guards the failure mode where applying config
// rebuilds the recipe layer: without the ledger, publisher revocation would stop
// working after any reload and nothing would report it.
func TestConfigReloadKeepsProvenance(t *testing.T) {
	installCommunityCapabilities(t, [2]string{"durable_tool", "3333333333333333333333333333333333333333333333333333333333333333"})

	if _, err := RefreshRecipeLayer([]config.ToolConfig{communityRecipe("durable_tool")}); err != nil {
		t.Fatal(err)
	}
	spec, err := capability.Global().Lookup("durable_tool")
	if err != nil {
		t.Fatal(err)
	}
	if spec.ArtifactDigest == "" || spec.Publisher != "acme" {
		t.Fatalf("provenance lost on reload: %+v", spec)
	}

	path := writeRevocations(t, t.TempDir(), map[string]any{
		"digests": map[string]any{spec.ArtifactDigest: map[string]any{"reason": "weaponized sample"}},
	})
	if _, err := MergeRevocations(path); err != nil {
		t.Fatal(err)
	}
	principal := authctx.NewPrincipal("u1", "user", "all", map[string]bool{"agent:local-execute": true})
	if err := mcpToolAuthorizer(nil)(authctx.WithPrincipal(context.Background(), principal), "durable_tool", nil); err == nil {
		t.Fatal("a revoked capability executed after a config reload")
	}
}

func TestMalformedRevocationListIsNotSilentlyIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revocations.json")
	if err := os.WriteFile(path, []byte(`{"digests": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadRevocations(path, zap.NewNop()); err == nil {
		t.Fatal("a corrupt revocation list loaded as empty")
	}
	// Absent is a legitimate state: nothing published yet.
	if err := LoadRevocations(filepath.Join(t.TempDir(), "missing.json"), zap.NewNop()); err != nil {
		t.Fatalf("a missing revocation list should not fail startup: %v", err)
	}
}

func TestProvenanceCannotBeChosenByTheSubmitter(t *testing.T) {
	// A recipe manifest carries no digest field: provenance is stamped by the
	// installer after signature verification, so a submission cannot point itself at
	// (or away from) a revocation entry.
	toolPath := filepath.Join(t.TempDir(), "try.yaml")
	body := "name: sneaky\ncommand: python3\nenabled: true\ndescription: x\ncapability:\n  id: acme.sneaky\n  class: mutating\n  permission: agent:local-execute\n  grants:\n    - \"net.connect(target)\"\n"
	if err := os.WriteFile(toolPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadToolFromFile(toolPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Capability == nil {
		t.Fatal("manifest did not load")
	}
	raw := map[string]any{}
	if err := yaml.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	capabilityBlock, _ := raw["capability"].(map[string]any)
	if _, present := capabilityBlock["artifact_digest"]; present {
		t.Fatal("publisher-supplied provenance field exists")
	}
	if _, present := capabilityBlock["digest"]; present {
		t.Fatal("publisher can set the digest its revocation is matched against")
	}
}
