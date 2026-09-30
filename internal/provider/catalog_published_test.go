package provider_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/provider"
)

// The catalog is a Go table, and a table nobody can read is how vendor differences creep
// back into call sites as string comparisons. These tests keep the published artifacts honest:
// the golden JSON and the markdown must say exactly what the live table says.

const (
	catalogGoldenPath = "internal/provider/testdata/provider-catalog.golden.json"
	catalogDocsPath   = "docs/zh-CN/provider-catalog.md"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test working directory")
		}
		dir = parent
	}
}

// TestPublishedArtifactsAreCurrent compares the committed files with what the live table
// publishes, field for field and byte for byte. Field-for-field is the point: an earlier
// version of this test hand-picked a handful of fields, and raising a vendor's context
// window through it stayed green.
func TestPublishedArtifactsAreCurrent(t *testing.T) {
	root := moduleRoot(t)

	want, err := provider.PublishJSON()
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, catalogGoldenPath))
	if err != nil {
		t.Fatalf("published catalog missing (run `make generate`): %v", err)
	}
	if diff := firstJSONDifference(t, want, got); diff != "" {
		t.Fatalf("the published catalog JSON is stale: %s\nrun `make generate` and commit the result", diff)
	}

	wantMarkdown := provider.PublishMarkdown()
	gotMarkdown, err := os.ReadFile(filepath.Join(root, catalogDocsPath))
	if err != nil {
		t.Fatalf("provider catalog document missing (run `make generate`): %v", err)
	}
	if string(wantMarkdown) != string(gotMarkdown) {
		t.Errorf("the published catalog document is stale:\n  want %d bytes, got %d bytes\n  first line at which they differ: %s",
			len(wantMarkdown), len(gotMarkdown), firstDifferenceLine(string(wantMarkdown), string(gotMarkdown)))
	}

	// Probes: the artifact must be the real table, not an empty or invented one.
	published := provider.Publish()
	if len(published.Rows) < 5 {
		t.Fatalf("the live catalog publishes %d rows; the table or the scan is broken", len(published.Rows))
	}
	var names []string
	for _, row := range published.Rows {
		names = append(names, row.Vendor)
	}
	if !contains(names, "anthropic") {
		t.Fatal("probe: the catalog must include anthropic")
	}
	if contains(names, "definitely-not-a-vendor") {
		t.Fatal("probe: the catalog must not invent vendors")
	}
	t.Logf("published %d vendors: %v", len(names), names)
}

// firstJSONDifference reports the first vendor/field where the two documents disagree, so a
// failure names the row instead of dumping two files.
func firstJSONDifference(t *testing.T, want, got []byte) string {
	t.Helper()
	var wanted, actual provider.Published
	if err := json.Unmarshal(want, &wanted); err != nil {
		t.Fatalf("decode the live publish: %v", err)
	}
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatalf("decode the committed catalog: %v", err)
	}
	if len(wanted.Rows) != len(actual.Rows) {
		return fmt.Sprintf("it has %d rows, the table publishes %d", len(actual.Rows), len(wanted.Rows))
	}
	for i := range wanted.Rows {
		w, err := json.Marshal(wanted.Rows[i])
		if err != nil {
			t.Fatal(err)
		}
		g, err := json.Marshal(actual.Rows[i])
		if err != nil {
			t.Fatal(err)
		}
		if string(w) != string(g) {
			return fmt.Sprintf("vendor %s:\n  table   %s\n  committed %s", wanted.Rows[i].Vendor, w, g)
		}
	}
	return ""
}

func firstDifferenceLine(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	for i := 0; i < len(w) && i < len(g); i++ {
		if w[i] != g[i] {
			return fmt.Sprintf("line %d:\n  table   %s\n  committed %s", i+1, w[i], g[i])
		}
	}
	return "the files differ only in length"
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// TestResolveAlwaysProducesAUsableDialect is the trap the published catalog exposed: a
// Lookup returns the row as written, and rows without a base URL cannot build an endpoint.
// Resolve is what callers use, so it must never hand back a dialect that fails at request
// time with an error naming no vendor.
func TestResolveAlwaysProducesAUsableDialect(t *testing.T) {
	catalog := provider.Default()
	for _, vendor := range append(catalog.Vendors(), "some_unknown_gateway") {
		dialect := catalog.Resolve(vendor, "")
		endpoint, err := dialect.EndpointURL()
		if err != nil {
			t.Fatalf("Resolve(%q, \"\") is unusable: %v", vendor, err)
		}
		if !strings.HasPrefix(endpoint, "https://") {
			t.Fatalf("Resolve(%q, \"\") = %q, want an https endpoint", vendor, endpoint)
		}
	}
	// An explicit base URL wins, and a trailing slash is not allowed to become a double one.
	got := catalog.Resolve("openai", "https://gateway.example.com/api/v1/").BaseURL
	if want := "https://gateway.example.com/api/v1"; got != want {
		t.Fatalf("explicit base URL = %q, want %q", got, want)
	}
}

// TestAnthropicChannelDefaultIsNotTheChatDefault records a deliberate difference from the
// code this table replaced: the old settings handler special-cased the literal provider name
// "claude" only, so `provider: anthropic` with no base_url was sent to api.openai.com and
// failed. Both spellings now resolve to the Anthropic host.
func TestAnthropicChannelDefaultIsNotTheChatDefault(t *testing.T) {
	for _, vendor := range []string{"anthropic", "claude"} {
		if got := provider.DefaultBaseURLFor(vendor); !strings.Contains(got, "api.anthropic.com") {
			t.Errorf("DefaultBaseURLFor(%q) = %q, want the Anthropic host", vendor, got)
		}
	}
	for _, vendor := range []string{"deepseek", "dashscope"} {
		if got := provider.DefaultBaseURLFor(vendor); !strings.Contains(got, "api.openai.com") {
			t.Errorf("DefaultBaseURLFor(%q) = %q; those channels are OpenAI-compatible and expect a configured URL", vendor, got)
		}
	}
}
