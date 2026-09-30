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
	// The interrupt log surface moved onto its own collaborator; the paths and methods did not, so
	// testdata/routes.golden.txt still has to match registration-for-registration.
	hitlQueue := agentHandler.HITLQueue()

	protected.GET("/hitl/pending", agentHandler.ListHITLPending)
	protected.GET("/hitl/logs", hitlQueue.ListHITLLogs)
	protected.DELETE("/hitl/logs", hitlQueue.DeleteHITLLogs)
	protected.GET("/hitl/logs/:id", hitlQueue.GetHITLLog)
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
