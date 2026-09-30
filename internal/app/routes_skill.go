package app

import (
	"github.com/gin-gonic/gin"
)

// registerSkillRoutes registers the skill endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerSkillRoutes(protected *gin.RouterGroup) {
	skillsHandler := deps.skillsHandler

	// Skills管理（具体路径需注册在 /skills/:name 之前）
	protected.GET("/skills", skillsHandler.GetSkills)
	protected.GET("/skills/stats", skillsHandler.GetSkillStats)
	protected.DELETE("/skills/stats", skillsHandler.ClearSkillStats)
	protected.GET("/skills/:name/files", skillsHandler.ListSkillPackageFiles)
	protected.GET("/skills/:name/file", skillsHandler.GetSkillPackageFile)
	protected.PUT("/skills/:name/file", skillsHandler.PutSkillPackageFile)
	protected.GET("/skills/:name/bound-roles", skillsHandler.GetSkillBoundRoles)
	protected.POST("/skills", skillsHandler.CreateSkill)
	protected.PUT("/skills/:name", skillsHandler.UpdateSkill)
	protected.DELETE("/skills/:name", skillsHandler.DeleteSkill)
	protected.DELETE("/skills/:name/stats", skillsHandler.ClearSkillStatsByName)
	protected.GET("/skills/:name", skillsHandler.GetSkill)
}
