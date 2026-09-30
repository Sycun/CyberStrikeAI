package app

import (
	"github.com/gin-gonic/gin"
)

// registerPluginRoutes covers the hot-plug surface: what is installed, and install / unplug /
// enable. Every route here changes what the running process serves without a restart, so the
// route table is verified against testdata/routes.golden.txt like the rest.
func (deps routeDeps) registerPluginRoutes(protected *gin.RouterGroup) {
	pluginHandler := deps.pluginHandler

	protected.GET("/plugins", pluginHandler.GetState)
	protected.POST("/plugins/install", pluginHandler.Install)
	protected.DELETE("/plugins/bundles/:id", pluginHandler.Uninstall)
	// kind and name are separate segments because a unit identity contains a slash
	// ("role/CTF"); putting it in one segment would need escaping that gin's router unescapes
	// before matching, so the route would never hit.
	protected.POST("/plugins/units/:kind/:name/enabled", pluginHandler.EnableUnit)
	protected.DELETE("/plugins/units/:kind/:name", pluginHandler.RemoveLocalUnit)
}
