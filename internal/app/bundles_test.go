package app

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"
)

// scanShippedUnits scans the four built-in directories the way the server would at start-up.
func scanShippedUnits(t *testing.T, root string) []plugin.Unit {
	t.Helper()
	var out []plugin.Unit
	for _, scan := range []struct {
		kind plugin.Kind
		dir  string
		name plugin.Namer
	}{
		{plugin.KindRole, "roles", roleNamer},
		{plugin.KindTool, "tools", toolNamer},
		{plugin.KindAgent, "agents", nil},
		{plugin.KindSkill, "skills", nil},
	} {
		units, err := plugin.ScanDir(scan.kind, filepath.Join(root, scan.dir), scan.name)
		if err != nil {
			t.Fatalf("ScanDir(%s): %v", scan.kind, err)
		}
		out = append(out, units...)
	}
	return out
}

func unitIDs(units []plugin.Unit) map[string]bool {
	out := make(map[string]bool, len(units))
	for _, u := range units {
		out[u.ID] = true
	}
	return out
}

func exampleBundleDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "bundles"))
	if err != nil {
		t.Fatalf("read bundles/: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, "bundles", e.Name(), plugin.ManifestFileName)
		if _, err := os.Stat(path); err == nil {
			out = append(out, filepath.Dir(path))
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatalf("no bundle under bundles/ - the empty case would make every assertion below vacuous")
	}
	return out
}

// TestExampleBundlesInstallAlongsideShippedCapabilities is the end-to-end shape of "one click to
// extend": the shipped tree is in the table, a role pack installs on top of it without disturbing
// anything, and unplugging returns the table to exactly the identity set it started from.
func TestExampleBundlesInstallAlongsideShippedCapabilities(t *testing.T) {
	root := pluginRepoRoot(t)
	shipped := scanShippedUnits(t, root)
	before := unitIDs(shipped)
	if len(before) != len(shipped) {
		t.Fatalf("the built-in directories yield %d units but only %d distinct identities", len(shipped), len(before))
	}
	if len(before) < 140 {
		t.Fatalf("only %d shipped units scanned (measured 142)", len(before))
	}

	table := plugin.NewTable()
	for _, u := range shipped {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal %s: %v", u.ID, err)
		}
	}
	genAfterScan := table.Generation()

	added := map[string]bool{}
	dirs := exampleBundleDirs(t, root)
	for _, dir := range dirs {
		m, err := plugin.LoadManifestDir(dir)
		if err != nil {
			t.Fatalf("LoadManifestDir %s: %v", dir, err)
		}
		b, err := m.Resolve()
		if err != nil {
			t.Fatalf("Resolve %s: %v", dir, err)
		}
		if b.ID != filepath.Base(dir) {
			t.Errorf("bundle id %q does not match its directory name %q", b.ID, filepath.Base(dir))
		}
		for _, u := range b.Units {
			if before[u.ID] {
				t.Fatalf("bundle %s would collide with shipped %s; the example packs must be additive", b.ID, u.ID)
			}
			added[u.ID] = true
		}
		if err := table.InstallBundle(b); err != nil {
			t.Fatalf("InstallBundle %s: %v", b.ID, err)
		}
	}
	if len(added) < 3 {
		t.Fatalf("example bundles deliver only %d units; the sample pack has gone thin", len(added))
	}
	if table.Generation() <= genAfterScan {
		t.Fatalf("installing %d example units did not move the generation", len(added))
	}
	if got := table.Drifted(); len(got) != 0 {
		t.Errorf("installed bundles do not match their own digests: %v", got)
	}

	// The delivered role has to be readable by the *existing* loader, not just by the table:
	// that is what makes "install a pack" and "the server serves the role" the same event.
	var roleUnits []plugin.Unit
	for _, u := range table.Units(plugin.KindRole) {
		if added[u.ID] {
			roleUnits = append(roleUnits, u)
		}
	}
	if len(roleUnits) == 0 {
		t.Fatalf("no example bundle delivered a role")
	}
	for _, u := range roleUnits {
		role, err := config.LoadRoleFromFile(u.Path)
		if err != nil {
			t.Fatalf("the shipped role loader cannot read %s: %v", u.Path, err)
		}
		if role.Name != u.Name {
			t.Errorf("bundle role identity %q is not the name the loader sees (%q)", u.Name, role.Name)
		}
		if !role.Enabled {
			t.Errorf("bundle role %s is not enabled, so it would install invisibly", u.Name)
		}
	}

	for _, dir := range dirs {
		m, err := plugin.LoadManifestDir(dir)
		if err != nil {
			t.Fatalf("LoadManifestDir: %v", err)
		}
		if err := table.UninstallBundle(m.ID); err != nil {
			t.Fatalf("UninstallBundle %s: %v", m.ID, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dirs[0], plugin.ManifestFileName)); err != nil {
		t.Errorf("uninstall removed files from disk: %v", err)
	}

	after := map[string]bool{}
	for _, kind := range []plugin.Kind{plugin.KindRole, plugin.KindTool, plugin.KindAgent, plugin.KindSkill} {
		for _, u := range table.Units(kind) {
			after[u.ID] = true
		}
	}
	if len(after) != len(before) {
		t.Fatalf("after unplugging the example packs the table holds %d units, started with %d", len(after), len(before))
	}
	for id := range before {
		if !after[id] {
			t.Errorf("shipped unit %s did not survive the install/unplug round trip", id)
		}
	}
}
