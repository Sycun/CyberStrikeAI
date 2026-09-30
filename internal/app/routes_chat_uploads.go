package app

import (
	"github.com/gin-gonic/gin"
)

// registerChatUploadsRoutes covers the chat uploads surface.
func (deps routeDeps) registerChatUploadsRoutes(protected *gin.RouterGroup) {
	chatUploadsHandler := deps.chatUploadsHandler

	// 对话附件（chat_uploads）管理
	protected.GET("/chat-uploads", chatUploadsHandler.List)
	protected.GET("/chat-uploads/export", chatUploadsHandler.Export)
	protected.GET("/chat-uploads/download", chatUploadsHandler.Download)
	protected.GET("/chat-uploads/path", chatUploadsHandler.ResolvePath)
	protected.GET("/chat-uploads/content", chatUploadsHandler.GetContent)
	protected.POST("/chat-uploads", chatUploadsHandler.Upload)
	protected.POST("/chat-uploads/mkdir", chatUploadsHandler.Mkdir)
	protected.DELETE("/chat-uploads", chatUploadsHandler.Delete)
	protected.PUT("/chat-uploads/rename", chatUploadsHandler.Rename)
	protected.PUT("/chat-uploads/content", chatUploadsHandler.PutContent)
}
