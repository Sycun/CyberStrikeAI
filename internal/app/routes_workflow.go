package app

import (
	"github.com/gin-gonic/gin"
)

// registerWorkflowRoutes registers the workflow endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerWorkflowRoutes(protected *gin.RouterGroup) {
	workflowHandler := deps.workflowHandler

	// 工作流定义（图结构固定，业务字段保存在 graph_json 中）
	protected.GET("/workflows/runs/pending", workflowHandler.ListPendingRuns)
	protected.GET("/workflows/runs/:runId/replay", workflowHandler.ReplayRun)
	protected.GET("/workflows/runs/:runId", workflowHandler.GetRun)
	protected.POST("/workflows/runs/:runId/resume", workflowHandler.ResumeRun)
	protected.POST("/workflows/validate", workflowHandler.Validate)
	protected.POST("/workflows/dry-run", workflowHandler.DryRun)
	protected.POST("/workflows/generate-draft", workflowHandler.GenerateDraft)
	protected.GET("/workflows/:id/package", workflowHandler.ExportPackage)
	protected.GET("/workflows", workflowHandler.List)
	protected.GET("/workflows/:id", workflowHandler.Get)
	protected.POST("/workflows", workflowHandler.Create)
	protected.PUT("/workflows/:id", workflowHandler.Update)
	protected.DELETE("/workflows/:id", workflowHandler.Delete)
	protected.POST("/workflow-package-inspections", workflowHandler.CreatePackageInspection)
	protected.GET("/workflow-package-inspections/:inspectionId", workflowHandler.GetPackageInspection)
	protected.POST("/workflow-package-imports", workflowHandler.ApplyPackageImport)
	protected.GET("/workflow-package-imports/:importId", workflowHandler.GetPackageImport)
}
