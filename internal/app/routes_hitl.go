package app

import (
	"cyberstrike-ai/internal/security"
	"github.com/gin-gonic/gin"
)

// registerHitlRoutes registers the hitl endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerHitlRoutes(protected *gin.RouterGroup) {
	agentHandler := deps.agentHandler

	protected.GET("/hitl/pending", agentHandler.ListHITLPending)
	protected.GET("/hitl/logs", agentHandler.ListHITLLogs)
	protected.DELETE("/hitl/logs", agentHandler.DeleteHITLLogs)
	protected.GET("/hitl/logs/:id", agentHandler.GetHITLLog)
	protected.POST("/hitl/decision", agentHandler.DecideHITLInterrupt)
	protected.POST("/hitl/dismiss", agentHandler.DismissHITLInterrupt)
	protected.GET("/hitl/config/:conversationId", agentHandler.GetHITLConversationConfig)
	protected.PUT("/hitl/config", agentHandler.UpsertHITLConversationConfig)
	protected.GET("/hitl/tool-whitelist", agentHandler.GetHITLGlobalToolWhitelist)
	// 免审批白名单决定哪些工具跳过人工审批，因此它是运维者所有的配置面：
	// 只有 config:write（管理员）可以改，会话侧栏的请求体不再能拓宽豁免范围。
	protected.PUT("/hitl/tool-whitelist", security.RequirePermission("config:write"), agentHandler.SetHITLGlobalToolWhitelist)
	protected.POST("/hitl/tool-whitelist", security.RequirePermission("config:write"), agentHandler.MergeHITLGlobalToolWhitelist)
	protected.GET("/hitl/default-config", agentHandler.GetHITLDefaultConfig)
	protected.PUT("/hitl/default-config", security.RequirePermission("config:write"), agentHandler.UpdateHITLDefaultConfig)
	protected.GET("/hitl/default-reviewer", agentHandler.GetHITLDefaultReviewer)
	protected.PUT("/hitl/default-reviewer", security.RequirePermission("config:write"), agentHandler.UpdateHITLDefaultReviewer)
	protected.GET("/hitl/audit-strategy", agentHandler.GetHITLAuditStrategy)
	protected.PUT("/hitl/audit-strategy", agentHandler.UpdateHITLAuditStrategy)
}
