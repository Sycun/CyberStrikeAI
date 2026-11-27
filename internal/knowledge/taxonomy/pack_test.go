package taxonomy

import "testing"

func TestReferencePack(t *testing.T) {
	pack, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for source, id := range map[string]string{"wstg": "WSTG-SESS-02", "cwe": "CWE-89", "attack": "T1078.001"} {
		if entry, ok := pack.Lookup(source, id); !ok || entry.ID != id {
			t.Fatalf("%s lookup: %+v", source, entry)
		}
	}
	if out := pack.Search("attack", " t1078 ", 2); len(out) != 2 || out[0].ID != "T1078" {
		t.Fatalf("exact match order: %+v", out)
	}
	if out := pack.Search("wstg", "encrypted CREDENTIALS", 0); len(out) != 1 || out[0].ID != "WSTG-ATHN-01" {
		t.Fatalf("AND search: %+v", out)
	}
	pack = &Pack{entries: map[string][]Entry{"fixture": make([]Entry, 25)}}
	for i := range pack.entries["fixture"] {
		pack.entries["fixture"][i].Title = "searchable"
	}
	if len(pack.Search("fixture", "searchable", 0)) != 5 || len(pack.Search("fixture", "searchable", 100)) != 20 {
		t.Fatal("incorrect result limits")
	}
}

func TestExpandedReferencePack(t *testing.T) {
	pack, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		source string
		count  int
		ids    []string
	}{
		{"wstg", 230, []string{"WSTG-APIT-02", "WSTG-ATHZ-04", "WSTG-INPV-05", "WSTG-v42-INPV-05", "WSTG-INJT-05", "WSTG-CONF-05", "WSTG-SESS-09"}},
		{"cwe", 956, []string{"CWE-78", "CWE-285", "CWE-295", "CWE-601", "CWE-639", "CWE-732", "CWE-863", "CWE-1321"}},
		{"attack", 706, []string{"T1021.001", "T1046", "T1059.001", "T1087.002", "T1110.003", "T1550.002"}},
	} {
		entries := pack.entries[fixture.source]
		if len(entries) < fixture.count {
			t.Fatalf("%s entry count: %d", fixture.source, len(entries))
		}
		seen := make(map[string]bool, len(entries))
		for _, item := range entries {
			if item.ID == "" || item.Title == "" || seen[item.ID] {
				t.Fatalf("%s has an invalid or duplicate entry: %+v", fixture.source, item)
			}
			seen[item.ID] = true
		}
		for _, id := range fixture.ids {
			item, ok := pack.Lookup(fixture.source, id)
			if !ok || item.ID != id {
				t.Fatalf("%s cannot resolve %s", fixture.source, id)
			}
		}
	}
	if out := pack.Search("wstg", "api broken object", 20); len(out) == 0 || out[0].ID != "WSTG-APIT-02" {
		t.Fatalf("API search: %+v", out)
	}
	if out := pack.Search("wstg", "wstg-v42-inpv-05", 1); len(out) != 1 || out[0].Title != "Testing for SQL Injection" {
		t.Fatalf("versioned WSTG search: %+v", out)
	}
}
