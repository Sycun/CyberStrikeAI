package app

import (
	"github.com/gin-gonic/gin"
)

// registerMcpRoutes registers the mcp endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerMcpRoutes(protected *gin.RouterGroup) {
	mcpServer := deps.mcpServer

	// MCP端点
	protected.POST("/mcp", func(c *gin.Context) {
		mcpServer.HandleHTTP(c.Writer, c.Request)
	})
}
