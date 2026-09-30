package app

import (
	"github.com/gin-gonic/gin"
)

// registerUpdateRoutes covers keeping this installation's own source up to date: what the
// tree is, what its remote has, applying it, watching the job, and undoing it.
//
// The paths are read-only status versus a mutating action because the mutating one
// changes what the platform itself runs - the same reason plugin install is its own
// permission rather than a flavour of write.
func (deps routeDeps) registerUpdateRoutes(protected *gin.RouterGroup) {
	updateHandler := deps.updateHandler

	protected.GET("/system/update", updateHandler.GetStatus)
	protected.GET("/system/update/job", updateHandler.Job)
	protected.POST("/system/update/check", updateHandler.Check)
	protected.POST("/system/update/apply", updateHandler.Apply)
	protected.POST("/system/update/rollback", updateHandler.Rollback)
}
