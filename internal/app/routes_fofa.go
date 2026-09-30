package app

import (
	"github.com/gin-gonic/gin"
)

// registerFofaRoutes registers the fofa endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerFofaRoutes(protected *gin.RouterGroup) {
	fofaHandler := deps.fofaHandler

	// 信息收集 - FOFA 查询（后端代理）
	protected.POST("/fofa/search", fofaHandler.Search)
	// 信息收集 - 自然语言解析为 FOFA 语法（需人工确认后再查询）
	protected.POST("/fofa/parse", fofaHandler.ParseNaturalLanguage)
}
