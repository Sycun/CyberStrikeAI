package pluginhub

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"

	"go.uber.org/zap"
)

func TestCatalogStoreCRUDAndRestore(t *testing.T) {
	ctx := context.Background()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "catalog.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mgr, err := New(db, config.PluginsConfig{}, t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	// Built-in entries present initially
	initial, hidden, err := mgr.ListCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial) < 3 || hidden != 0 {
		t.Fatalf("expected >=3 built-ins and 0 hidden, got %d / %d", len(initial), hidden)
	}

	// Add custom inspection-only entry
	entry, err := mgr.SaveCatalogEntry(ctx, CatalogInput{
		ID:         "my-scanner",
		Title:      "My Scanner",
		Repository: "https://github.com/example/my-scanner.git",
		Revision:   "main",
		Summary:    "Custom security scanner",
	})
	if err != nil {
		t.Fatalf("save inspection-only entry: %v", err)
	}
	if entry.ID != "my-scanner" || entry.Repository != "https://github.com/example/my-scanner" || entry.Availability != "inspection_only" || entry.Builtin {
		t.Fatalf("unexpected saved entry: %+v", entry)
	}

	// Add custom entry with YAML manifest -> plan_available
	raw, err := os.ReadFile("manifest/testdata/web-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rawStr := strings.ReplaceAll(string(raw), "https://git.example.org/examples/log-review-demo.git", "https://github.com/example/log-review-demo")
	withManifest, err := mgr.SaveCatalogEntry(ctx, CatalogInput{
		Manifest: rawStr,
	})
	if err != nil {
		t.Fatalf("save manifest entry: %v", err)
	}
	if withManifest.ID != "log-review" || withManifest.Availability != "plan_available" {
		t.Fatalf("unexpected manifest entry: %+v", withManifest)
	}
	if rawManifest, ok := mgr.CatalogManifest(ctx, "log-review"); !ok || len(rawManifest) == 0 {
		t.Fatalf("expected CatalogManifest to resolve custom manifest")
	}

	// Mismatched repository vs manifest must fail
	if _, err := mgr.SaveCatalogEntry(ctx, CatalogInput{
		Repository: "https://github.com/other/repo",
		Manifest:   rawStr,
	}); !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("expected ErrSourceMismatch, got %v", err)
	}

	// Delete custom entry -> hard deleted
	if err := mgr.DeleteCatalogEntry(ctx, "my-scanner"); err != nil {
		t.Fatalf("delete custom entry: %v", err)
	}
	if err := mgr.DeleteCatalogEntry(ctx, "my-scanner"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows on second delete, got %v", err)
	}

	// Delete built-in entry -> hidden tombstone, hidden_builtins increments, manifest inaccessible
	if err := mgr.DeleteCatalogEntry(ctx, "artex"); err != nil {
		t.Fatalf("hide built-in entry: %v", err)
	}
	afterHide, hidden, err := mgr.ListCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hidden != 1 {
		t.Fatalf("expected hidden_builtins=1, got %d", hidden)
	}
	for _, item := range afterHide {
		if item.ID == "artex" {
			t.Fatalf("expected artex to be hidden")
		}
	}
	if _, ok := mgr.CatalogManifest(ctx, "artex"); ok {
		t.Fatalf("expected hidden built-in manifest to be unavailable")
	}

	// Restore hidden built-ins
	restored, err := mgr.RestoreBuiltinCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if restored != 1 {
		t.Fatalf("expected 1 restored, got %d", restored)
	}
	afterRestore, hidden, err := mgr.ListCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hidden != 0 {
		t.Fatalf("expected hidden_builtins=0 after restore, got %d", hidden)
	}
	foundArtex := false
	for _, item := range afterRestore {
		if item.ID == "artex" {
			foundArtex = true
			break
		}
	}
	if !foundArtex {
		t.Fatalf("expected artex to reappear after restore")
	}
}
