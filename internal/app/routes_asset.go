package app

import (
	"cyberstrike-ai/internal/security"
	"github.com/gin-gonic/gin"
)

// registerAssetRoutes registers the asset endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerAssetRoutes(protected *gin.RouterGroup) {
	assetHandler := deps.assetHandler

	// 资产管理
	protected.GET("/assets", assetHandler.List)
	protected.GET("/assets/selection", assetHandler.Selection)
	protected.GET("/assets/stats", assetHandler.Stats)
	protected.POST("/assets/import", assetHandler.Import)
	protected.POST("/assets/scan-links", assetHandler.RecordScans)
	protected.PUT("/assets/bulk", assetHandler.BulkUpdate)
	protected.PUT("/assets/project-binding", assetHandler.UpdateProjectBinding)
	protected.POST("/assets/batch-delete", assetHandler.BatchDelete)
	protected.POST("/assets/merge", security.RequirePermission("asset:write"), assetHandler.Merge)
	protected.PUT("/assets/:id", assetHandler.Update)
	protected.DELETE("/assets/:id", assetHandler.Delete)
}
