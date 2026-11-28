package update

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBranchSwitchForwardAndBackPreservesProtectedFiles(t *testing.T) {
	ctx := context.Background()
	upstream := t.TempDir()
	mustGit(t, upstream, "init", "--initial-branch=main")
	mustGit(t, upstream, "config", "user.name", "Test")
	mustGit(t, upstream, "config", "user.email", "test@example.com")
	writeFile(t, filepath.Join(upstream, "app.txt"), "main-v1\n")
	writeFile(t, filepath.Join(upstream, "roles", "default.yaml"), "upstream-v1\n")
	mustGit(t, upstream, "add", ".")
	mustGit(t, upstream, "commit", "-m", "main v1")

	// Create feature branch with new commit.
	mustGit(t, upstream, "checkout", "-b", "feat/plugin-capability-platform")
	writeFile(t, filepath.Join(upstream, "app.txt"), "feat-v1\n")
	writeFile(t, filepath.Join(upstream, "plugin.txt"), "plugin-ready\n")
	mustGit(t, upstream, "add", ".")
	mustGit(t, upstream, "commit", "-m", "feat plugin")

	// Advance main independently so main and feat diverge!
	mustGit(t, upstream, "checkout", "main")
	writeFile(t, filepath.Join(upstream, "app.txt"), "main-v2\n")
	mustGit(t, upstream, "add", ".")
	mustGit(t, upstream, "commit", "-m", "main v2")

	// Clone main at v1 as install tree.
	install := t.TempDir()
	mustGit(t, install, "clone", "--branch", "main", upstream, ".")
	mustGit(t, install, "reset", "--hard", "HEAD~1")
	writeFile(t, filepath.Join(install, "config.yaml"), "custom: true\n")
	writeFile(t, filepath.Join(install, "roles", "default.yaml"), "operator-edited\n")

	// 1. ListBranches lists default main first + feature branch.
	def, branches, err := ListBranches(ctx, Options{Root: install, Repo: upstream})
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}
	if def != "main" || len(branches) < 2 || branches[0] != "main" || !slices.Contains(branches, "feat/plugin-capability-platform") {
		t.Fatalf("unexpected branches: default=%q list=%v", def, branches)
	}

	// 2. Switch main -> feat/plugin-capability-platform.
	optsFeat := Options{Root: install, Repo: upstream, Branch: "feat/plugin-capability-platform", BinaryName: "none"}
	snap, err := Check(ctx, optsFeat)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.BranchSwitch || !snap.UpdateAvailable || snap.Diverged || snap.TargetBranch != "feat/plugin-capability-platform" {
		t.Fatalf("unexpected check snapshot for branch switch: %+v", snap)
	}
	if _, err := Apply(ctx, optsFeat, nil); err != nil {
		t.Fatalf("Apply branch switch failed: %v", err)
	}
	if got := mustGit(t, install, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat/plugin-capability-platform" {
		t.Fatalf("expected feat branch, got %q", got)
	}
	if data, err := os.ReadFile(filepath.Join(install, "plugin.txt")); err != nil || string(data) != "plugin-ready\n" {
		t.Fatalf("expected plugin.txt on feat branch: %q %v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(install, "roles", "default.yaml")); err != nil || string(data) != "operator-edited\n" {
		t.Fatalf("operator protected file was not preserved: %q %v", string(data), err)
	}

	// 3. Switch back feat/plugin-capability-platform -> main (diverged history!).
	optsMain := Options{Root: install, Repo: upstream, Branch: "", BinaryName: "none"}
	backSnap, err := Check(ctx, optsMain)
	if err != nil {
		t.Fatal(err)
	}
	if !backSnap.BranchSwitch || !backSnap.UpdateAvailable || backSnap.Diverged || backSnap.TargetBranch != "main" {
		t.Fatalf("unexpected check snapshot switching back to main: %+v", backSnap)
	}
	if _, err := Apply(ctx, optsMain, nil); err != nil {
		t.Fatalf("Apply switch back to main failed: %v", err)
	}
	if got := mustGit(t, install, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("expected main branch, got %q", got)
	}
	if data, err := os.ReadFile(filepath.Join(install, "app.txt")); err != nil || string(data) != "main-v2\n" {
		t.Fatalf("expected main-v2 after switching back: %q %v", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(install, "plugin.txt")); !os.IsNotExist(err) {
		t.Fatalf("plugin.txt should be removed after switching back to main")
	}
}
