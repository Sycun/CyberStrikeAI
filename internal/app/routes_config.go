package app

import (
	"github.com/gin-gonic/gin"
)

// registerConfigRoutes registers the config endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerConfigRoutes(protected *gin.RouterGroup) {
	configHandler := deps.configHandler

	// 配置管理
	protected.GET("/config", configHandler.GetConfig)
	protected.GET("/config/tools", configHandler.GetTools)
	protected.GET("/config/tools/:name/schema", configHandler.GetToolSchema)
	protected.PUT("/config", configHandler.UpdateConfig)
	protected.POST("/config/apply", configHandler.ApplyConfig)
	protected.POST("/config/test-openai", configHandler.TestOpenAI)
	protected.POST("/config/test-typesafe", configHandler.TestTypeSafe)
	protected.POST("/config/test-vision", configHandler.TestVision)
	protected.POST("/config/list-models", configHandler.ListModels)
}
