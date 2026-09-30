package app

import (
	"github.com/gin-gonic/gin"
)

// registerWebshellRoutes registers the webshell endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerWebshellRoutes(protected *gin.RouterGroup) {
	webshellHandler := deps.webshellHandler

	// WebShell 管理（代理执行 + 连接配置存 SQLite）
	protected.GET("/webshell/connections", webshellHandler.ListConnections)
	protected.POST("/webshell/connections", webshellHandler.CreateConnection)
	protected.GET("/webshell/connections/:id/ai-history", webshellHandler.GetAIHistory)
	protected.GET("/webshell/connections/:id/ai-conversations", webshellHandler.ListAIConversations)
	protected.GET("/webshell/connections/:id/state", webshellHandler.GetConnectionState)
	protected.PUT("/webshell/connections/:id", webshellHandler.UpdateConnection)
	protected.PUT("/webshell/connections/:id/state", webshellHandler.SaveConnectionState)
	protected.DELETE("/webshell/connections/:id", webshellHandler.DeleteConnection)
	protected.POST("/webshell/exec", webshellHandler.Exec)
	protected.POST("/webshell/file", webshellHandler.FileOp)
}
