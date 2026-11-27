package knowledge

import (
	"context"
	"strings"
	"testing"

	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/mcp/builtin"
	"go.uber.org/zap"
)

func TestTaxonomyToolWorksWithoutVectorSearch(t *testing.T) {
	server := mcp.NewServer(zap.NewNop())
	RegisterTaxonomyTool(server, zap.NewNop())
	result, _, err := server.CallTool(context.Background(), builtin.ToolLookupSecurityTaxonomy, map[string]any{
		"source": "cwe", "query": "cwe-89", "limit": float64(1),
	})
	if err != nil || result.IsError || len(result.Content) != 1 ||
		!strings.Contains(result.Content[0].Text, "field=security_taxonomy") ||
		!strings.Contains(result.Content[0].Text, "CWE-89") {
		t.Fatalf("taxonomy result: %+v, %v", result, err)
	}
}
