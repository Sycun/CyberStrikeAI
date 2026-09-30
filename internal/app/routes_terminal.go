package app

import (
	"github.com/gin-gonic/gin"
)

// registerTerminalRoutes registers the terminal endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerTerminalRoutes(protected *gin.RouterGroup) {
	terminalHandler := deps.terminalHandler

	// 系统设置 - 终端（执行命令，提高运维效率）
	protected.POST("/terminal/run", terminalHandler.RunCommand)
	protected.POST("/terminal/run/stream", terminalHandler.RunCommandStream)
	protected.GET("/terminal/ws", terminalHandler.RunCommandWS)
}
