package app

import (
	"context"
	"strings"
	"testing"

	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/knowledge"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/mcp/builtin"
	"go.uber.org/zap"
)

func TestTaxonomyToolUsesKnowledgeReadPermission(t *testing.T) {
	server := mcp.NewServer(zap.NewNop())
	server.SetToolAuthorizer(mcpToolAuthorizer(nil))
	knowledge.RegisterTaxonomyTool(server, zap.NewNop())
	args := map[string]any{"source": "wstg", "query": "WSTG-SESS-02"}
	denied := authctx.WithPrincipal(context.Background(), authctx.NewPrincipal("viewer", "viewer", database.RBACScopeOwn, nil))
	if result, _, err := server.CallTool(denied, builtin.ToolLookupSecurityTaxonomy, args); err == nil && (result == nil || !result.IsError) {
		t.Fatal("taxonomy was accessible without knowledge:read")
	}
	reader := authctx.WithPrincipal(context.Background(), authctx.NewPrincipal("reader", "reader", database.RBACScopeOwn, map[string]bool{"knowledge:read": true}))
	result, _, err := server.CallTool(reader, builtin.ToolLookupSecurityTaxonomy, args)
	if err != nil || result == nil || result.IsError || !strings.Contains(mcp.ToolResultPlainText(result), "WSTG-SESS-02") {
		t.Fatalf("knowledge reader request: %+v, %v", result, err)
	}
}
