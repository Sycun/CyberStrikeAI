package app

import (
	"github.com/gin-gonic/gin"
)

// registerRoleRoutes registers the role endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerRoleRoutes(protected *gin.RouterGroup) {
	roleHandler := deps.roleHandler

	// 角色管理
	protected.GET("/roles", roleHandler.GetRoles)
	protected.GET("/roles/:name", roleHandler.GetRole)
	protected.POST("/roles", roleHandler.CreateRole)
	protected.PUT("/roles/:name", roleHandler.UpdateRole)
	protected.DELETE("/roles/:name", roleHandler.DeleteRole)
}
