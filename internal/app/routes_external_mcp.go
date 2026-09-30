package app

import (
	"github.com/gin-gonic/gin"
)

// registerExternalMCPRoutes covers the external mcp surface.
func (deps routeDeps) registerExternalMCPRoutes(protected *gin.RouterGroup) {
	externalMCPHandler := deps.externalMCPHandler

	// 外部MCP管理
	protected.GET("/external-mcp", externalMCPHandler.GetExternalMCPs)
	protected.GET("/external-mcp/stats", externalMCPHandler.GetExternalMCPStats)
	protected.GET("/external-mcp/:name", externalMCPHandler.GetExternalMCP)
	protected.PUT("/external-mcp/:name", externalMCPHandler.AddOrUpdateExternalMCP)
	protected.DELETE("/external-mcp/:name", externalMCPHandler.DeleteExternalMCP)
	protected.POST("/external-mcp/:name/start", externalMCPHandler.StartExternalMCP)
	protected.POST("/external-mcp/:name/stop", externalMCPHandler.StopExternalMCP)
}
