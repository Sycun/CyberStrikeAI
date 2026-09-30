package app

import (
	"github.com/gin-gonic/gin"
)

// registerNotificationRoutes registers the notification endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerNotificationRoutes(protected *gin.RouterGroup) {
	notificationHandler := deps.notificationHandler

	protected.GET("/notifications/summary", notificationHandler.GetSummary)
	protected.POST("/notifications/read", notificationHandler.MarkRead)
}
