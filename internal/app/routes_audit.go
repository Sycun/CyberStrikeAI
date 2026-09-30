package app

import (
	"github.com/gin-gonic/gin"
)

// registerAuditRoutes registers the audit endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerAuditRoutes(protected *gin.RouterGroup) {
	auditHandler := deps.auditHandler

	// 平台审计日志
	protected.GET("/audit/meta", auditHandler.Meta)
	protected.GET("/audit/summary", auditHandler.Summary)
	protected.GET("/audit/logs", auditHandler.ListLogs)
	protected.GET("/audit/logs/export", auditHandler.ExportLogs)
	protected.GET("/audit/logs/:id", auditHandler.GetLog)
}
