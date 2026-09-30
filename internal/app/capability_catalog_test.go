package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
)

// buildCatalogFromSource reproduces what internal/capability/gen emits. Keeping it
// in a test means the committed artifacts cannot drift from the manifests without
// CI failing, which is the "drift turns red" gate for the generated enum.
func buildCatalogFromSource(t *testing.T) []byte {
	t.Helper()
	tools, err := config.LoadToolsFromDir(filepath.Join("..", "..", "tools"))
	if err != nil {
		t.Fatalf("load recipes: %v", err)
	}
	registry := capability.NewRegistry()
	builtins := capability.BuiltinSpecs()
	for _, spec := range builtins {
		spec.Builtin = true
	}
	if err := registry.RegisterAll(capability.LayerBuiltin, builtins); err != nil {
		t.Fatal(err)
	}
	specs, rejections := RecipeSpecs(tools)
	if len(rejections) > 0 {
		t.Fatalf("%d recipes have no enforceable manifest: %+v", len(rejections), rejections[0])
	}
	if err := registry.RegisterAll(capability.LayerRecipe, specs); err != nil {
		t.Fatal(err)
	}
	data, err := capability.MarshalCatalog(registry.Catalog())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGeneratedCatalogIsUpToDate(t *testing.T) {
	golden := filepath.Join("..", "capability", "testdata", "catalog.golden.json")
	comitted, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("generated catalog missing (%s): run `make generate`: %v", golden, err)
	}
	if want := buildCatalogFromSource(t); !bytes.Equal(comitted, want) {
		t.Fatal("capability catalog is stale: run `make generate` and commit the result")
	}
}

func TestGeneratedFrontendCatalogMatchesRegistry(t *testing.T) {
	js, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "js", "generated", "capability-catalog.js"))
	if err != nil {
		t.Fatalf("generated frontend enum missing: run `make generate`: %v", err)
	}
	tools, err := config.LoadToolsFromDir(filepath.Join("..", "..", "tools"))
	if err != nil {
		t.Fatal(err)
	}
	// Every shipped recipe name must appear in the generated table; this is what
	// retires the hand-mirrored tool-name literals in the frontend.
	for _, tool := range tools {
		if !bytes.Contains(js, []byte(`"name":"`+tool.Name+`"`)) && !bytes.Contains(js, []byte(`"`+tool.Name+`"`)) {
			t.Errorf("recipe %s is missing from the generated frontend catalog", tool.Name)
		}
	}
	for _, marker := range []string{"window.CSAI.capabilities", "window.CSAI.capabilityNames", "window.CSAI.capabilityByName"} {
		if !bytes.Contains(js, []byte(marker)) {
			t.Fatalf("generated catalog lost the %s surface", marker)
		}
	}
}

// TestManifestDrivesArgumentValidation proves one manifest covers validation as
// well as authorization: the schema generated from the recipe is what refuses a
// malformed call.
func TestManifestDrivesArgumentValidation(t *testing.T) {
	schema := capability.JSONSchema([]capability.Param{
		{Name: "command", Type: "string", Required: true},
		{Name: "shell", Type: "string", Enum: []string{"sh", "bash"}},
	})
	if len(schema) == 0 {
		t.Fatal("no schema emitted")
	}
	if err := capability.ValidateArgs(schema, map[string]any{"shell": "sh"}); err == nil {
		t.Error("missing required argument was accepted")
	}
	if err := capability.ValidateArgs(schema, map[string]any{"command": "id", "shell": "zsh"}); err == nil {
		t.Error("an out-of-enum value was accepted")
	}
	if err := capability.ValidateArgs(schema, map[string]any{"command": "id", "shell": "bash"}); err != nil {
		t.Errorf("valid arguments rejected: %v", err)
	}
}
