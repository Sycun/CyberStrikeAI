package security

import (
	"net/http"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/routes"
)

func TestEveryProtectedRouteHasCatalogPermission(t *testing.T) {
	// The route table lives across the per-domain registrars now, so the inventory is
	// read through the shared extractor rather than one file, or the audit would
	// silently cover only the handful of routes left in the wiring function.
	table, err := routes.Extract(filepath.Join("..", "app"))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, entry := range table {
		switch entry.Method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			continue
		}
		// Same scope as before the wiring split: everything behind the authenticated
		// group, including the C2 and knowledge sub-groups.
		if entry.Receiver != "protected" && entry.Receiver != "c2Routes" && entry.Receiver != "knowledgeRoutes" {
			continue
		}
		found++
		permission := permissionForRequest(entry.Method, entry.GinPath)
		if permission == "" {
			t.Errorf("unmapped protected route: %s %s", entry.Method, entry.GinPath)
			continue
		}
		if _, ok := PermissionCatalog[permission]; !ok {
			t.Errorf("route %s %s maps to unknown permission %q", entry.Method, entry.GinPath, permission)
		}
	}
	if found < 250 {
		t.Fatalf("route inventory unexpectedly small: %d", found)
	}
}
