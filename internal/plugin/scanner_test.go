package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanDirOverASyntheticTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "roles", "Web应用扫描.yaml"), "name: Web应用扫描\nenabled: true\n")
	writeFile(t, filepath.Join(root, "roles", "CTF.yml"), "name: CTF\nenabled: true\n")
	writeFile(t, filepath.Join(root, "roles", "README.md"), "not a role\n")
	writeFile(t, filepath.Join(root, "roles", ".Web应用扫描.yaml.swp"), "editor junk\n")
	writeFile(t, filepath.Join(root, "roles", "nested"), "a file that is not yaml\n")
	if err := os.MkdirAll(filepath.Join(root, "roles", "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	roles, err := ScanDir(KindRole, filepath.Join(root, "roles"), nil)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if got := names(roles); strings.Join(got, ",") != "CTF,Web应用扫描" {
		t.Fatalf("roles = %v, want exactly the two yaml files (no README, no swap file, no dir)", got)
	}
	for _, u := range roles {
		if len(u.Digest) != 64 {
			t.Errorf("role %s has no digest", u.Name)
		}
		if u.Bundle != "" {
			t.Errorf("scanned role %s claims bundle %q", u.Name, u.Bundle)
		}
	}

	// An absent optional directory is empty, not an error: config.LoadRolesFromDir behaves the
	// same way and the table has to agree with it.
	missing, err := ScanDir(KindRole, filepath.Join(root, "nope"), nil)
	if err != nil || len(missing) != 0 {
		t.Fatalf("absent dir: units=%v err=%v", missing, err)
	}
	if _, err := ScanDir(Kind("bogus"), root, nil); err == nil {
		t.Errorf("ScanDir accepted an unknown kind")
	}
}

func TestScanDirShapeRulesPerKind(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "agents", "web.md"), "---\nname: web\n---\n")
	writeFile(t, filepath.Join(root, "agents", "README.md"), "docs, not an agent\n")
	writeFile(t, filepath.Join(root, "agents", "note.txt"), "not markdown\n")
	agents, err := ScanDir(KindAgent, filepath.Join(root, "agents"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(agents); strings.Join(got, ",") != "web" {
		t.Fatalf("agents = %v, want only web.md", got)
	}

	skills := filepath.Join(root, "skills")
	writeFile(t, filepath.Join(skills, "good", "SKILL.md"), "---\nname: good\n---\n")
	writeFile(t, filepath.Join(skills, "good", "reference.md"), "extra\n")
	if err := os.MkdirAll(filepath.Join(skills, "no-skill-md"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(skills, "loose.md"), "a file among skill dirs\n")
	got, err := ScanDir(KindSkill, skills, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names(got), ",") != "good" {
		t.Fatalf("skills = %v, want only the directory holding SKILL.md", names(got))
	}
	// A skill's digest covers the whole directory, so adding reference.md is drift.
	before := got[0].Digest
	writeFile(t, filepath.Join(skills, "good", "reference.md"), "changed\n")
	after, err := ScanDir(KindSkill, skills, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Digest == before {
		t.Fatalf("skill digest ignored a changed file inside the directory")
	}
}

// TestScanDirUsesTheCallersNamingRule is the anti-drift guard: roles are keyed by the `name:`
// field, not the file name, exactly like config.LoadRolesFromDir. A scanner that keyed on the
// file name would install "file-key" and leave "Real Name" unreachable.
func TestScanDirUsesTheCallersNamingRule(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "file-key.yaml"), "name: Real Name\nenabled: true\n")
	namer := func(kind Kind, path string) (string, error) {
		if kind != KindRole {
			return "", nil
		}
		return "Real Name", nil
	}
	got, err := ScanDir(KindRole, root, namer)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "role/Real Name" {
		t.Fatalf("units = %#v, want role/Real Name", got)
	}
	// An empty answer falls back to the file name rather than producing a nameless unit.
	got, err = ScanDir(KindRole, root, func(Kind, string) (string, error) { return "  ", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "role/file-key" {
		t.Fatalf("fallback units = %#v, want role/file-key", got)
	}
	// A namer that fails stops the scan: half a directory of units is how drift starts.
	if _, err := ScanDir(KindRole, root, func(Kind, string) (string, error) { return "", os.ErrInvalid }); err == nil {
		t.Fatalf("ScanDir tolerated a failing namer")
	}
}

// TestScannedUnitsLandInTheTableAndCanBePluggedAgainstBundles covers the hand-off the whole
// design rests on: the shipped directories are just another provider, and a bundle that
// collides with one of them is refused by identity, not by luck.
func TestScannedUnitsLandInTheTableAndCanBePluggedAgainstBundles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "CTF.yaml"), "name: CTF\nenabled: true\n")
	units, err := ScanDir(KindRole, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	table := NewTable()
	for _, u := range units {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal: %v", err)
		}
	}
	if _, ok := table.Unit("role/CTF"); !ok {
		t.Fatalf("shipped role not addressable by identity")
	}

	// A pack that renames the same file yields the same identity, so it collides.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "roles", "CTF.yaml"), "name: CTF\nenabled: true\n")
	writeFile(t, filepath.Join(dir, ManifestFileName), `id: ctf-pack
version: 1.0.0
units:
  - kind: role
    path: roles/CTF.yaml
`)
	m, err := LoadManifestDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := table.InstallBundle(b); err == nil {
		t.Fatalf("ctf-pack replaced the shipped CTF role")
	}
	// Unplugging the shipped role is the operator's call, and only then can the pack land.
	if err := table.RemoveLocal("role/CTF"); err != nil {
		t.Fatalf("RemoveLocal: %v", err)
	}
	if err := table.InstallBundle(b); err != nil {
		t.Fatalf("install after unplugging the shipped role: %v", err)
	}
	if u, ok := table.Unit("role/CTF"); !ok || u.Bundle != "ctf-pack" {
		t.Fatalf("role/CTF = %#v, want it owned by ctf-pack", u)
	}
}
