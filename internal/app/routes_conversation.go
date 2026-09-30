package app

import (
	"github.com/gin-gonic/gin"
)

// registerConversationRoutes registers the conversation endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerConversationRoutes(protected *gin.RouterGroup) {
	conversationHandler := deps.conversationHandler
	openAPIHandler := deps.openAPIHandler

	// 对话历史
	protected.GET("/usage/tokens", conversationHandler.GetTokenUsageStats)
	protected.POST("/conversations", conversationHandler.CreateConversation)
	protected.GET("/conversations", conversationHandler.ListConversations)
	protected.GET("/conversations/:id", conversationHandler.GetConversation)
	protected.GET("/conversations/:id/token-usage", conversationHandler.GetConversationTokenUsageStats)
	protected.GET("/conversations/:id/plan-tasks", conversationHandler.GetConversationPlanTasks)
	protected.GET("/messages/:id/process-details", conversationHandler.GetMessageProcessDetails)
	protected.GET("/process-details/:id", conversationHandler.GetProcessDetail)
	protected.PUT("/conversations/:id", conversationHandler.UpdateConversation)
	protected.PUT("/conversations/:id/project", conversationHandler.SetConversationProject)
	protected.DELETE("/conversations/:id", conversationHandler.DeleteConversation)
	protected.POST("/conversations/:id/delete-turn", conversationHandler.DeleteConversationTurn)
	protected.PUT("/conversations/:id/pinned", conversationHandler.UpdateConversationPinned)

	// OpenAPI结果聚合端点（可选，用于获取对话的完整结果）
	protected.GET("/conversations/:id/results", openAPIHandler.GetConversationResults)
}
