package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The HTTP layer resolves its configuration through a snapshot installed at assembly time, and
// falls back to the boot config when none is installed. That fallback exists so unit tests can
// construct a handler directly - it must never be what production runs on, because then the
// role catalog would silently stop updating and every reader would keep serving start-up values.
//
// So: this is a gate, not documentation. It fails if assembly stops installing the store, or
// stops publishing the catalog it just wired.
func TestAssemblyInstallsTheLiveConfigStoreAndPublishesRoles(t *testing.T) {
	root := moduleRootForWiringTest(t)
	dir := filepath.Join(root, "internal/app")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read internal/app: %v", err)
	}
	fset := token.NewFileSet()
	installed := 0
	published := 0
	tableInstalled := 0
	remoteObservers := 0
	scanned := 0
	scannedCalled := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				// A bare call in this package: the capability scan, which lives here.
				if id, isIdent := call.Fun.(*ast.Ident); isIdent && id.Name == "scanBuiltInCapabilities" {
					scannedCalled++
				}
				return true
			}
			switch sel.Sel.Name {
			case "InstallSettingsStore":
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "handler" {
					installed++
				}
			case "SetToolInventoryObserver":
				// Without this line the remote capability identities are built by code nobody
				// calls, and every external MCP tool silently falls back to the namespace policy.
				remoteObservers++
			case "Install":
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "plugin" {
					tableInstalled++
				}
			case "Reload":
				published++
			}
			return true
		})
	}
	if remoteObservers != 1 {
		t.Fatalf("SetToolInventoryObserver is called %d times in internal/app, want exactly 1: with no "+
			"observer the per-tool remote capabilities are never registered and every external MCP call "+
			"quietly falls back to the namespace-wide policy", remoteObservers)
	}
	if scanned < 10 {
		t.Fatalf("only %d non-test files scanned in internal/app: the walker is not reading the package", scanned)
	}
	if installed != 1 {
		t.Fatalf("handler.InstallSettingsStore is called %d times in internal/app, want exactly 1 "+
			"(0 means every handler silently falls back to the boot config; more than 1 means two "+
			"snapshots can disagree)", installed)
	}
	if tableInstalled != 1 {
		t.Fatalf("plugin.Install is called %d times in internal/app, want exactly 1: without a global "+
			"table the skill middleware silently keeps the single-directory backend, so a bundle's "+
			"skills would look installed while nothing served them", tableInstalled)
	}
	if scannedCalled < 1 {
		t.Fatalf("scanBuiltInCapabilities is never called: the table would be empty, and preferring it " +
			"over Eino's backend would take every shipped skill away from a run")
	}
	if published < 1 {
		t.Fatalf("no role catalog publish in assembly: the store would be installed but empty, so "+
			"the roles API and every run path would serve nothing (files scanned: %d)", scanned)
	}
	t.Logf("assembly wiring: %d files scanned, 1 live-store install, 1 table install, "+
		"1 remote inventory observer, %d capability scan call(s), %d catalog publish call(s)",
		scanned, scannedCalled, published)
}

func moduleRootForWiringTest(t *testing.T) string {
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
