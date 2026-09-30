package app

import (
	"github.com/gin-gonic/gin"
)

// registerMonitorRoutes registers the monitor endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerMonitorRoutes(protected *gin.RouterGroup) {
	monitorHandler := deps.monitorHandler

	// 监控
	protected.GET("/monitor", monitorHandler.Monitor)
	protected.GET("/monitor/execution/:id", monitorHandler.GetExecution)
	protected.POST("/monitor/execution/:id/cancel", monitorHandler.CancelExecution)
	protected.POST("/monitor/executions/names", monitorHandler.BatchGetToolNames)
	protected.DELETE("/monitor/execution/:id", monitorHandler.DeleteExecution)
	protected.DELETE("/monitor/executions", monitorHandler.DeleteExecutions)
	protected.GET("/monitor/stats", monitorHandler.GetStats)
	protected.GET("/monitor/calls-timeline", monitorHandler.GetCallsTimeline)
}
