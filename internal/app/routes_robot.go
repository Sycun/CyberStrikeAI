package app

import (
	"github.com/gin-gonic/gin"
)

// registerRobotRoutes registers the robot endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerRobotRoutes(protected *gin.RouterGroup) {
	robotHandler := deps.robotHandler
	wechatRobotHandler := deps.wechatRobotHandler

	// 机器人测试（需登录）：POST /api/robot/test，body: {"platform":"dingtalk","user_id":"test","text":"帮助"}，用于验证机器人逻辑
	protected.POST("/robot/test", robotHandler.HandleRobotTest)

	// 微信 iLink 扫码绑定（需登录）
	protected.POST("/robot/wechat/qrcode", wechatRobotHandler.HandleWechatQRCode)
	protected.GET("/robot/wechat/qrcode/status", wechatRobotHandler.HandleWechatQRCodeStatus)
	protected.POST("/robot/wechat/qrcode/verify", wechatRobotHandler.HandleWechatVerifyCode)
	protected.GET("/robot/wechat/status", wechatRobotHandler.HandleWechatStatus)
}
