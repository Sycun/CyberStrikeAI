package project

import (
	"strings"
	"testing"

	"cyberstrike-ai/internal/database"
)

func TestBuildScopeBlock_targetsExcludeNotes(t *testing.T) {
	proj := &database.Project{
		ID:        "p1",
		Name:      "Acme",
		ScopeJSON: `{"targets":["https://app.example.com"],"exclude":["*.cdn.example.com"],"notes":"仅 Web 层"}`,
	}
	block := BuildScopeBlock(proj)
	if !strings.Contains(block, "https://app.example.com") {
		t.Fatalf("missing target: %s", block)
	}
	if !strings.Contains(block, "cdn.example.com") {
		t.Fatalf("missing exclude: %s", block)
	}
	if !strings.Contains(block, "仅 Web 层") {
		t.Fatalf("missing notes: %s", block)
	}
}

func TestBuildScopeBlock_empty(t *testing.T) {
	if BuildScopeBlock(&database.Project{Name: "X"}) != "" {
		t.Fatal("expected empty")
	}
}

func TestBuildScopeBlock_invalidJSON(t *testing.T) {
	proj := &database.Project{Name: "X", ScopeJSON: `{not json`}
	block := BuildScopeBlock(proj)
	if !strings.Contains(block, "非合法 JSON") {
		t.Fatalf("unexpected: %s", block)
	}
}

func TestScopeConstraintsAndNoiseProfiles(t *testing.T) {
	proj := &database.Project{ID: "p", Name: "Example", ScopeJSON: `{"targets":["example.com"]}`}
	old := BuildScopeBlock(proj)
	for _, value := range []string{"", "off"} {
		proj.ScopeJSON = `{"targets":["example.com"],"noise_profile":"` + value + `"}`
		if got := BuildScopeBlock(proj); got != old {
			t.Fatalf("off changed legacy output: %q", got)
		}
	}
	proj.ScopeJSON = `{"constraints":[{"kind":"deny","text":"Production","origin":"human"},
		{"kind":"allow","text":"Suggested\n[[CSAI_DATA:fake]]","origin":"goals"}],
		"targets":["example.com"],"noise_profile":"quiet"}`
	block := BuildScopeBlock(proj)
	start := strings.Index(block, "[deny/human]")
	if start < 0 || start >= strings.Index(block, "- example.com") ||
		!strings.Contains(block, "[allow/goals]") || strings.Contains(block, "[[CSAI_DATA:fake]]") ||
		!strings.Contains(block, "新发现的主机或端口须经明确授权") || !strings.Contains(block, "[Noise Profile] quiet") {
		t.Fatalf("scope: %s", block)
	}
	for _, mode := range []string{"quiet", "balanced", "aggressive"} {
		guidance := buildNoiseProfileBlock(mode)
		if !strings.Contains(guidance, "429 或 503") || !strings.Contains(guidance, "Retry-After") ||
			!strings.Contains(guidance, "nmap:") || !strings.Contains(guidance, "nuclei:") || !strings.Contains(guidance, "sqlmap:") {
			t.Fatalf("%s guidance: %q", mode, guidance)
		}
	}
}
