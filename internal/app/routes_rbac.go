package app

import (
	"github.com/gin-gonic/gin"
)

// registerRbacRoutes registers the rbac endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerRbacRoutes(protected *gin.RouterGroup) {
	rbacHandler := deps.rbacHandler

	protected.GET("/rbac/me", rbacHandler.Me)
	protected.GET("/rbac/metadata", rbacHandler.Metadata)
	protected.GET("/rbac/users", rbacHandler.ListUsers)
	protected.POST("/rbac/users", rbacHandler.CreateUser)
	protected.PUT("/rbac/users/:id", rbacHandler.UpdateUser)
	protected.DELETE("/rbac/users/:id", rbacHandler.DeleteUser)
	protected.GET("/rbac/roles", rbacHandler.ListRoles)
	protected.POST("/rbac/roles", rbacHandler.CreateRole)
	protected.PUT("/rbac/roles/:id", rbacHandler.UpdateRole)
	protected.DELETE("/rbac/roles/:id", rbacHandler.DeleteRole)
	protected.GET("/rbac/resource-assignments", rbacHandler.ListResourceAssignments)
	protected.GET("/rbac/resources", rbacHandler.ListAssignableResources)
	protected.POST("/rbac/resource-assignments", rbacHandler.AssignResource)
	protected.DELETE("/rbac/resource-assignments/:id", rbacHandler.DeleteResourceAssignment)
}
