package app

import (
	"github.com/gin-gonic/gin"
)

// registerAttackChainRoutes registers the attack chain endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerAttackChainRoutes(protected *gin.RouterGroup) {
	attackChainHandler := deps.attackChainHandler

	// 攻击链可视化
	protected.GET("/attack-chain/:conversationId", attackChainHandler.GetAttackChain)
	protected.POST("/attack-chain/:conversationId/regenerate", attackChainHandler.RegenerateAttackChain)
}
