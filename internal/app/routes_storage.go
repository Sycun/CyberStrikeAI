package app

import (
	"github.com/gin-gonic/gin"
)

// registerStorageRoutes covers the storage surface.
func (deps routeDeps) registerStorageRoutes(protected *gin.RouterGroup) {
	app := deps.app

	// 运行空间占用与垃圾清理
	protected.GET("/storage/meta", app.storageHandler.Meta)
	protected.GET("/storage/status", app.storageHandler.Status)
	protected.POST("/storage/cleanup", app.storageHandler.Cleanup)
}
