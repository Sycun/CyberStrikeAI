package app

import (
	"github.com/gin-gonic/gin"
)

// registerTaskRoutes registers the task endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerTaskRoutes(protected *gin.RouterGroup) {
	agentHandler := deps.agentHandler

	// 批量任务管理
	protected.POST("/batch-tasks", agentHandler.CreateBatchQueue)
	protected.GET("/batch-tasks", agentHandler.ListBatchQueues)
	protected.GET("/batch-tasks/:queueId", agentHandler.GetBatchQueue)
	protected.POST("/batch-tasks/:queueId/start", agentHandler.StartBatchQueue)
	protected.POST("/batch-tasks/:queueId/rerun", agentHandler.RerunBatchQueue)
	protected.POST("/batch-tasks/:queueId/pause", agentHandler.PauseBatchQueue)
	protected.PUT("/batch-tasks/:queueId/metadata", agentHandler.UpdateBatchQueueMetadata)
	protected.PUT("/batch-tasks/:queueId/schedule", agentHandler.UpdateBatchQueueSchedule)
	protected.PUT("/batch-tasks/:queueId/schedule-enabled", agentHandler.SetBatchQueueScheduleEnabled)
	protected.DELETE("/batch-tasks/:queueId", agentHandler.DeleteBatchQueue)
	protected.PUT("/batch-tasks/:queueId/tasks/:taskId", agentHandler.UpdateBatchTask)
	protected.POST("/batch-tasks/:queueId/tasks/:taskId/run", agentHandler.RunSingleBatchTask)
	protected.POST("/batch-tasks/:queueId/tasks", agentHandler.AddBatchTask)
	protected.DELETE("/batch-tasks/:queueId/tasks/:taskId", agentHandler.DeleteBatchTask)
}
