package catalog

import (
	"testing"

	"cyberstrike-ai/internal/pluginhub/manifest"
)

func TestEmbeddedManifestsValidate(t *testing.T) {
	for _, entry := range Entries() {
		if entry.Availability != "plan_available" {
			continue
		}
		raw, ok := Manifest(entry.ID)
		if !ok {
			t.Fatalf("catalog entry %q advertises plan_available without embedded manifest", entry.ID)
		}
		decl, err := manifest.DecodeDeclaration(raw)
		if err != nil {
			t.Fatalf("catalog manifest %q failed validation: %v", entry.ID, err)
		}
		if decl.Metadata.ID != entry.ID {
			t.Fatalf("catalog manifest %q declares metadata.id=%q", entry.ID, decl.Metadata.ID)
		}
	}
}
