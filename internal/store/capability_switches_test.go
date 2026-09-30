package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// The switch table is read at start-up, before any request has proved that an identity refers to a
// unit that exists. So the shape of the identity is checked on the way in, and the row carries the
// source path it was about so a stale one can be recognised later.

func TestSwitchRecordsRoundTrip(t *testing.T) {
	switches := NewCapabilitySwitches(openDB(t))
	if err := switches.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := switches.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema is not idempotent: %v", err)
	}

	if err := switches.Record("role/移动端安全测试", filepath.Join("bundles", "p", "roles", "r.yaml"), false); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Re-recording the same identity replaces the decision instead of adding a row.
	if err := switches.Record("role/移动端安全测试", filepath.Join("bundles", "p", "roles", "r.yaml"), true); err != nil {
		t.Fatalf("Record again: %v", err)
	}
	if err := switches.Record("skill/triage", "/srv/skills/triage", false); err != nil {
		t.Fatalf("Record skill: %v", err)
	}

	rows, err := switches.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%v, want 2 identities with the latest decision each", rows)
	}
	byID := map[string]Switch{}
	for _, r := range rows {
		byID[r.UnitID] = r
	}
	if !byID["role/移动端安全测试"].Enabled {
		t.Fatal("the last decision was not the one stored")
	}
	if byID["skill/triage"].Enabled || byID["skill/triage"].Path != "/srv/skills/triage" {
		t.Fatalf("skill row lost its decision or path: %+v", byID["skill/triage"])
	}

	if err := switches.Forget("skill/triage"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	rows, _ = switches.All()
	if len(rows) != 1 || rows[0].UnitID != "role/移动端安全测试" {
		t.Fatalf("after Forget rows=%v", rows)
	}
}

func TestSwitchRejectsIdentitiesThatAreNotUnits(t *testing.T) {
	switches := NewCapabilitySwitches(openDB(t))
	if err := switches.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"", "role/", "报告撰写", "role/sub/name", "../role/x", "widget/thing", "ROLE/x",
	} {
		if err := switches.Record(bad, "/p", false); err == nil {
			t.Errorf("Record(%q) was accepted", bad)
		}
	}
	// A path is what makes a row recognisable as stale, so an empty one is not a switch either.
	if err := switches.Record("role/ok", "  ", false); err == nil {
		t.Error("Record accepted an identity with no source path")
	}
	if rows, err := switches.All(); err != nil || len(rows) != 0 {
		t.Fatalf("rejected writes still landed: rows=%v err=%v", rows, err)
	}
}

func TestForgetIgnoresIdentitiesItCannotParse(t *testing.T) {
	switches := NewCapabilitySwitches(openDB(t))
	if err := switches.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	if err := switches.Record("tool/semgrep", "/p/semgrep.yaml", false); err != nil {
		t.Fatal(err)
	}
	if err := switches.Forget("nonsense", "", "role/"); err != nil {
		t.Fatalf("Forget of unrecordable identities: %v", err)
	}
	rows, _ := switches.All()
	if len(rows) != 1 {
		t.Fatalf("a junk identity in Forget took real rows with it: %v", rows)
	}
}

func TestSwitchNilDatabaseFailsLoud(t *testing.T) {
	var switches *CapabilitySwitches
	if err := switches.EnsureSchema(); err == nil || !strings.Contains(err.Error(), "requires a database") {
		t.Fatalf("EnsureSchema on a nil store said %v", err)
	}
	if err := switches.Record("role/x", "/p", false); err == nil {
		t.Fatal("Record on a nil store claimed success")
	}
	if _, err := switches.All(); err == nil {
		t.Fatal("All on a nil store claimed success")
	}
}

// The slot key is what makes a saved switch survive being reached through a different absolute
// path, while still telling apart two identities whose files moved.
func TestSwitchPathKeyKeepsTheSlotNotTheRoot(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"/srv/csai/bundles/p/roles/x.yaml", "bundles/p/roles/x.yaml", true},
		{"/srv/csai/roles/x.yaml", "/other/roles/x.yaml", true},
		{"/srv/csai/bundles/p/roles/x.yaml", "/srv/csai/bundles/p/agents/x.yaml", false},
		{"/srv/csai/roles/x.yaml", "/srv/csai/roles/y.yaml", false},
		{"roles/x.yaml", "x.yaml", false},
	}
	for _, tc := range cases {
		gotA, gotB := SwitchPathKey(tc.a), SwitchPathKey(tc.b)
		if same := gotA == gotB; same != tc.same {
			t.Errorf("key(%q)=%q key(%q)=%q, want same=%v", tc.a, gotA, tc.b, gotB, tc.same)
		}
	}
	if got := SwitchPathKey("/srv/csai/bundles/p/roles/x.yaml"); got != "roles/x.yaml" {
		t.Errorf("key shape is %q, want the directory plus the file name", got)
	}
}
