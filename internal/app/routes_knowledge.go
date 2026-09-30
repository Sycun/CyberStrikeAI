package app

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// registerKnowledgeRoutes registers the knowledge endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerKnowledgeRoutes(protected *gin.RouterGroup) {
	app := deps.app

	// 知识库管理（始终注册路由，通过 App 实例动态获取 handler）
	knowledgeRoutes := protected.Group("/knowledge")

	// Handlers are resolved per request through the App instance, so a rebuild of
	// the knowledge subsystem does not need the routes to be re-registered.
	knowledgeRoutes.GET("/categories", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"categories": []string{},
				"enabled":    false,
				"message":    "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.GetCategories(c)
	})
	knowledgeRoutes.GET("/items", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"items":   []interface{}{},
				"enabled": false,
				"message": "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.GetItems(c)
	})
	knowledgeRoutes.GET("/items/:id", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"enabled": false,
				"message": "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.GetItem(c)
	})
	knowledgeRoutes.POST("/items", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"enabled": false,
				"error":   "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.CreateItem(c)
	})
	knowledgeRoutes.PUT("/items/:id", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"enabled": false,
				"error":   "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.UpdateItem(c)
	})
	knowledgeRoutes.DELETE("/items/:id", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"enabled": false,
				"error":   "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.DeleteItem(c)
	})
	knowledgeRoutes.GET("/index-status", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"enabled":          false,
				"total_items":      0,
				"indexed_items":    0,
				"progress_percent": 0,
				"is_complete":      false,
				"message":          "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.GetIndexStatus(c)
	})
	knowledgeRoutes.POST("/index", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"enabled": false,
				"error":   "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.StartIndex(c)
	})
	knowledgeRoutes.POST("/scan", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"enabled": false,
				"error":   "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.ScanKnowledgeBase(c)
	})
	knowledgeRoutes.GET("/retrieval-logs", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"logs":    []interface{}{},
				"enabled": false,
				"message": "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.GetRetrievalLogs(c)
	})
	knowledgeRoutes.DELETE("/retrieval-logs/:id", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"enabled": false,
				"error":   "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.DeleteRetrievalLog(c)
	})
	knowledgeRoutes.POST("/search", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"results": []interface{}{},
				"enabled": false,
				"message": "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.Search(c)
	})
	knowledgeRoutes.GET("/stats", func(c *gin.Context) {
		if app.knowledgeHandler == nil {
			c.JSON(http.StatusOK, gin.H{
				"enabled":          false,
				"total_categories": 0,
				"total_items":      0,
				"message":          "知识库功能未启用，请前往系统设置启用知识检索功能",
			})
			return
		}
		app.knowledgeHandler.GetStats(c)
	})
}
