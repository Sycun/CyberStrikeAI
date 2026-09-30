package app

import (
	"github.com/gin-gonic/gin"
)

// registerProjectRoutes registers the project endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerProjectRoutes(protected *gin.RouterGroup) {
	projectHandler := deps.projectHandler

	// 项目管理与事实黑板
	protected.GET("/projects/dashboard-summary", projectHandler.GetDashboardSummary)
	protected.GET("/projects", projectHandler.ListProjects)
	protected.POST("/projects", projectHandler.CreateProject)
	protected.GET("/projects/:id/stats", projectHandler.GetProjectStats)
	protected.GET("/projects/:id/conversations", projectHandler.ListProjectConversations)
	protected.GET("/projects/:id", projectHandler.GetProject)
	protected.PUT("/projects/:id", projectHandler.UpdateProject)
	protected.DELETE("/projects/:id", projectHandler.DeleteProject)
	protected.GET("/projects/:id/fact-graph", projectHandler.GetFactGraph)
	protected.GET("/projects/:id/fact-edges", projectHandler.ListFactEdges)
	protected.POST("/projects/:id/fact-edges", projectHandler.CreateFactEdge)
	protected.DELETE("/projects/:id/fact-edges/:edgeId", projectHandler.DeleteFactEdge)
	protected.POST("/projects/:id/promote-attack-chain/:conversationId", projectHandler.PromoteAttackChain)
	protected.GET("/projects/:id/facts", projectHandler.ListFacts)
	protected.POST("/projects/:id/facts", projectHandler.CreateFact)
	protected.PUT("/projects/:id/facts/:factId", projectHandler.UpdateFact)
	protected.DELETE("/projects/:id/facts/:factId", projectHandler.DeleteFact)
	protected.POST("/projects/:id/facts/deprecate", projectHandler.DeprecateFact)
	protected.POST("/projects/:id/facts/restore", projectHandler.RestoreFact)
}
