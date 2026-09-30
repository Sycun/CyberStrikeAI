package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/agents"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

	"go.uber.org/zap"
)

// An installed pack that only exists until the next restart is not installed; it is session
// state. Every run path reads the capability table, and the table is rebuilt from disk at
// start-up, so the packs under <configDir>/bundles have to be put back into it here.
func TestBundlesOnDiskAreReinstalledAtBoot(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"roles", "agents", "skills", "tools", "bundles"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFileAt(t, filepath.Join(root, "roles", "内置角色.yaml"),
		"name: 内置角色\nuser_prompt: 内置\nenabled: true\n")
	writeFileAt(t, filepath.Join(root, "agents", "recon.md"), "---\nid: recon\nname: 侦察\ndescription: 侦察\n---\n\n# 侦察\n")

	pack := filepath.Join(root, "bundles", "restart-pack")
	writeFileAt(t, filepath.Join(pack, "roles", "重启仍在.yaml"),
		"name: 重启仍在\nuser_prompt: 包提供的角色\nenabled: true\n")
	writeFileAt(t, filepath.Join(pack, "agents", "pack-agent.md"),
		"---\nid: pack-agent\nname: 包代理\ndescription: 包提供的子代理\n---\n\n# 包代理\n")
	writeFileAt(t, filepath.Join(pack, plugin.ManifestFileName),
		"id: restart-pack\nname: 重启仍在包\nversion: 1.0.0\nunits:\n"+
			"  - kind: role\n    path: roles/重启仍在.yaml\n"+
			"  - kind: agent\n    path: agents/pack-agent.md\n")

	// A pack that tries to shadow a shipped role: the built-in wins, and the pack is refused by
	// name - not silently preferred because it happened to load first.
	shadow := filepath.Join(root, "bundles", "shadow-pack")
	writeFileAt(t, filepath.Join(shadow, "roles", "内置角色.yaml"),
		"name: 内置角色\nuser_prompt: 冒充内置\nenabled: true\n")
	writeFileAt(t, filepath.Join(shadow, plugin.ManifestFileName),
		"id: shadow-pack\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/内置角色.yaml\n")

	broken := filepath.Join(root, "bundles", "broken-pack")
	writeFileAt(t, filepath.Join(broken, plugin.ManifestFileName),
		"id: broken-pack\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/缺失.yaml\n")

	table := plugin.NewTable()
	cfg := &config.Config{AgentsDir: "agents"}
	cfg.Security.ToolsDir = "tools"
	configPath := filepath.Join(root, "config.yaml")
	writeFileAt(t, configPath, "server:\n  port: 0\n")

	if err := scanBuiltInCapabilities(table, cfg, configPath, zap.NewNop()); err != nil {
		t.Fatalf("scanBuiltInCapabilities: %v", err)
	}
	// What RoleHandler.Reload does first at boot: the shipped roles go into the table before any
	// pack is re-installed, which is what makes a shadowing pack refuseable rather than winning.
	roleUnits, err := plugin.ScanDir(plugin.KindRole, filepath.Join(root, "roles"), func(kind plugin.Kind, path string) (string, error) {
		role, err := config.LoadRoleFromFile(path)
		if err != nil {
			return "", err
		}
		return role.Name, nil
	})
	if err != nil {
		t.Fatalf("ScanDir(roles): %v", err)
	}
	for _, u := range roleUnits {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal %s: %v", u.ID, err)
		}
	}
	installed, refused := installBundlesFromDisk(table, filepath.Join(root, "bundles"), zap.NewNop())
	if installed != 1 {
		t.Fatalf("boot re-installed %d packs, want 1 (refused=%v)", installed, refused)
	}
	if len(refused) != 2 {
		t.Fatalf("refused %d packs, want shadow-pack and broken-pack: %v", len(refused), refused)
	}
	joined := strings.Join(refused, "\n")
	if !strings.Contains(joined, "shadow-pack") || !strings.Contains(joined, "内置角色") {
		t.Errorf("the shadowing pack is not refused by naming the identity it clashes with: %v", refused)
	}
	if !strings.Contains(joined, "broken-pack") {
		t.Errorf("a pack with an unreadable manifest is not reported: %v", refused)
	}

	// The pack's units are in the table and attributed to it, so the console and every run path
	// see them after a restart without anybody clicking install again.
	unit, ok := table.Unit("role/重启仍在")
	if !ok {
		t.Fatal("the pack's role is missing from the table after boot")
	}
	if unit.Bundle != "restart-pack" {
		t.Errorf("boot-installed unit lost its bundle ownership: %+v", unit)
	}
	if _, ok := table.Unit("agent/pack-agent"); !ok {
		t.Error("the pack's agent is missing from the table after boot")
	}
	if builtIn, ok := table.Unit("role/内置角色"); !ok || builtIn.Bundle != "" {
		t.Fatalf("the shipped role was replaced by the pack: %+v ok=%v", builtIn, ok)
	}

	// And the run path actually serves them: install means "the next agent run has it", and a
	// restart must not be able to take that away.
	previous := plugin.Global()
	plugin.Install(table)
	t.Cleanup(func() { plugin.Install(previous) })
	load, err := agents.LoadMarkdownAgents(filepath.Join(root, "agents"))
	if err != nil {
		t.Fatalf("LoadMarkdownAgents: %v", err)
	}
	var names []string
	for _, entry := range load.FileEntries {
		names = append(names, entry.Filename)
	}
	if len(load.SubAgents) != 2 {
		t.Fatalf("after boot the run path sees %d sub-agents (%v), want the shipped one plus the pack's", len(load.SubAgents), names)
	}
}

func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
