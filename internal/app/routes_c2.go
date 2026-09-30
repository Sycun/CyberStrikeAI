package app

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// registerC2Routes registers the c2 endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerC2Routes(protected *gin.RouterGroup) {
	c2Handler := deps.c2Handler
	app := deps.app

	// C2 管理（未启用时返回 503，避免 Handler 空指针）
	c2Routes := protected.Group("/c2")

	c2Routes.Use(func(c *gin.Context) {
		if app.c2Manager == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "c2_disabled",
				"message": "C2 功能已在系统设置中关闭",
				"enabled": false,
			})
			return
		}
		c.Next()
	})
	c2Routes.GET("/listeners", c2Handler.ListListeners)
	c2Routes.POST("/listeners", c2Handler.CreateListener)
	c2Routes.GET("/listeners/:id", c2Handler.GetListener)
	c2Routes.PUT("/listeners/:id", c2Handler.UpdateListener)
	c2Routes.DELETE("/listeners/:id", c2Handler.DeleteListener)
	c2Routes.POST("/listeners/:id/start", c2Handler.StartListener)
	c2Routes.POST("/listeners/:id/stop", c2Handler.StopListener)
	c2Routes.GET("/sessions", c2Handler.ListSessions)
	c2Routes.DELETE("/sessions", c2Handler.DeleteSessions)
	c2Routes.GET("/sessions/:id", c2Handler.GetSession)
	c2Routes.DELETE("/sessions/:id", c2Handler.DeleteSession)
	c2Routes.PUT("/sessions/:id/sleep", c2Handler.SetSessionSleep)
	c2Routes.PUT("/sessions/:id/note", c2Handler.SetSessionNote)
	c2Routes.GET("/tasks", c2Handler.ListTasks)
	c2Routes.DELETE("/tasks", c2Handler.DeleteTasks)
	c2Routes.GET("/tasks/:id", c2Handler.GetTask)
	c2Routes.POST("/tasks", c2Handler.CreateTask)
	c2Routes.POST("/tasks/:id/cancel", c2Handler.CancelTask)
	c2Routes.GET("/tasks/:id/wait", c2Handler.WaitTask)
	c2Routes.POST("/sessions/:id/tasks", c2Handler.CreateTask)
	c2Routes.POST("/payloads/oneliner", c2Handler.PayloadOneliner)
	c2Routes.POST("/payloads/build", c2Handler.PayloadBuild)
	c2Routes.GET("/payloads/:id/download", c2Handler.PayloadDownload)
	c2Routes.GET("/events", c2Handler.ListEvents)
	c2Routes.DELETE("/events", c2Handler.DeleteEvents)
	c2Routes.GET("/events/stream", c2Handler.EventStream)
	c2Routes.POST("/files/upload", c2Handler.UploadFileForImplant)
	c2Routes.GET("/files", c2Handler.ListFiles)
	c2Routes.GET("/tasks/:id/result-file", c2Handler.DownloadResultFile)
	c2Routes.GET("/profiles", c2Handler.ListProfiles)
	c2Routes.GET("/profiles/:id", c2Handler.GetProfile)
	c2Routes.POST("/profiles", c2Handler.CreateProfile)
	c2Routes.PUT("/profiles/:id", c2Handler.UpdateProfile)
	c2Routes.DELETE("/profiles/:id", c2Handler.DeleteProfile)
}
