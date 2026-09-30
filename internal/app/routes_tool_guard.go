package app

import (
	"github.com/gin-gonic/gin"
)

// registerToolGuardRoutes covers the tool guard surface.
func (deps routeDeps) registerToolGuardRoutes(protected *gin.RouterGroup) {
	configHandler := deps.configHandler

	protected.GET("/tool-guard", configHandler.GetToolGuard)
	protected.PUT("/tool-guard", configHandler.UpdateToolGuard)
	protected.POST("/tool-guard/test", configHandler.TestToolGuard)
}
