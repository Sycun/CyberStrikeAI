// Command gen emits the downstream artifacts that today exist as five separate
// hand-kept copies: the frontend capability enum, the machine-readable catalog
// the OpenAPI generator consumes, and the review-facing inventory table.
//
// Run with `make generate` (or go generate ./internal/capability/gen). The drift
// test in internal/capability fails if the committed artifacts differ from what
// this program produces, so a manifest change cannot land without regeneration.
//go:generate go run . -root ../../..

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"cyberstrike-ai/internal/app"
	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
)

func main() {
	root := flag.String("root", ".", "repository root containing tools/")
	flag.Parse()

	resolved, err := repoRoot(*root)
	if err != nil {
		fail(err)
	}

	tools, err := config.LoadToolsFromDir(filepath.Join(resolved, "tools"))
	if err != nil {
		fail(fmt.Errorf("load recipes: %w", err))
	}

	registry := capability.NewRegistry()
	builtins := capability.BuiltinSpecs()
	for _, spec := range builtins {
		spec.Builtin = true
	}
	if err := registry.RegisterAll(capability.LayerBuiltin, builtins); err != nil {
		fail(fmt.Errorf("register builtins: %w", err))
	}

	specs, rejections := app.RecipeSpecs(tools)
	if len(rejections) > 0 {
		for _, r := range rejections {
			fmt.Fprintf(os.Stderr, "rejected recipe %s: %s\n", r.ToolName, r.Reason)
		}
		fail(fmt.Errorf("%d recipes have no enforceable manifest", len(rejections)))
	}
	if err := registry.RegisterAll(capability.LayerRecipe, specs); err != nil {
		fail(fmt.Errorf("register recipes: %w", err))
	}

	entries := registry.Catalog()

	jsonData, err := capability.MarshalCatalog(entries)
	if err != nil {
		fail(err)
	}
	jsData, err := capability.RenderCatalogJS(entries)
	if err != nil {
		fail(err)
	}
	mdData := capability.RenderCatalogMarkdown(entries)

	targets := map[string][]byte{
		filepath.Join(resolved, "web", "static", "js", "generated", "capability-catalog.js"):   jsData,
		filepath.Join(resolved, "web", "static", "js", "generated", "capability-catalog.json"): jsonData,
		filepath.Join(resolved, "docs", "zh-CN", "capability-catalog.md"):                      mdData,
		filepath.Join(resolved, "internal", "capability", "testdata", "catalog.golden.json"):   jsonData,
	}
	for path, data := range targets {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fail(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fail(err)
		}
	}
	fmt.Printf("capability catalog: %d entries (%d builtin, %d recipe)\n",
		len(entries), len(builtins), len(specs))
}

// repoRoot walks up from the starting directory so `go generate` works no matter
// where it is invoked from.
func repoRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("no go.mod found above %s", start)
		}
		abs = parent
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
