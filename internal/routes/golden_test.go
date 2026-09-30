package routes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteGolden regenerates the accepted route table. Run it deliberately when
// endpoints are added or removed:
//
//	CSAI_WRITE_ROUTE_GOLDEN=1 go test ./internal/routes -run TestWriteGolden
//
// It is opt-in by environment variable so an accidental run cannot bless a
// regression into the baseline.
func TestWriteGolden(t *testing.T) {
	if os.Getenv("CSAI_WRITE_ROUTE_GOLDEN") != "1" {
		t.Skip("set CSAI_WRITE_ROUTE_GOLDEN=1 to regenerate the golden route table")
	}
	table, err := Extract(filepath.Join("..", "app"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "app", "testdata", "routes.golden.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(table.Lines(), "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d routes to %s", len(table), path)
}
