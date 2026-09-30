package handler

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/plugin"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// PluginHandler is the "one click to extend" surface: list what is plugged in, install a bundle,
// unplug it, switch a unit on or off - all against the live capability table, so none of it
// restarts the process.
type PluginHandler struct {
	table     *plugin.Table
	bundles   string // the only directory a bundle may be installed from
	republish catalogPublisher
	// tools rebuilds the recipe layer plus the MCP tool surface after a mutation that touched a
	// tool unit. It is a constructor argument for the same reason the audit service is: leaving
	// it out is not an error, it is a bundle whose recipe silently never becomes executable
	// while the table says it is installed.
	tools toolLayerRebuilder
	// mcp is the live external-MCP manager. A pack that declares a server writes it there and
	// removes it from there, so the declaration has one home; installing never starts it.
	mcp    MCPProvisioner
	logger *zap.Logger
	audit  *audit.Service
}

// toolLayerRebuilder is the piece of the tool layer that owns the recipe list and the MCP tool
// surface; *ToolLayer implements it.
type toolLayerRebuilder interface {
	Rebuild() error
}

// catalogPublisher is the piece that turns table state into served configuration. RoleHandler
// owns it today; the point of naming it here is that installing must never be able to leave the
// table and the live catalog disagreeing.
type catalogPublisher interface {
	Reload() (int, error)
}

// NewPluginHandler takes the audit service as a constructor argument rather than through a
// SetAudit method on purpose: every other handler is wired with a setter that the assembly has
// to remember, which is why an audit-completeness gate exists for them. Here forgetting is a
// compile error, so the handler is also deliberately outside that gate's scope.
func NewPluginHandler(table *plugin.Table, bundlesDir string, publisher catalogPublisher, tools toolLayerRebuilder, mcpManager MCPProvisioner, auditSvc *audit.Service, logger *zap.Logger) *PluginHandler {
	return &PluginHandler{table: table, bundles: bundlesDir, republish: publisher, tools: tools, mcp: mcpManager, audit: auditSvc, logger: logger}
}

type unitView struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Bundle  string `json:"bundle,omitempty"`
	Enabled bool   `json:"enabled"`
	Served  bool   `json:"served"`
	Reason  string `json:"reason,omitempty"`
}

// servedKinds lists the kinds whose run path reads the table. Every kind is in it now: a role,
// agent, skill or recipe is served from the table's paths, and an MCP declaration is written into
// the live external-MCP manager by install and removed by unplug. What stays separate is
// *started*: a declared server arrives disabled, because running a process is the operator's call
// and not a side effect of clicking install - the unit's own enabled flag carries that.
var servedKinds = map[plugin.Kind]string{
	plugin.KindRole:  "",
	plugin.KindAgent: "",
	plugin.KindSkill: "",
	plugin.KindTool:  "",
	plugin.KindMCP:   "",
}

func unitServed(u plugin.Unit) (bool, string) {
	if gap, ok := servedKinds[u.Kind]; ok {
		return true, gap
	}
	return false, "unknown capability kind: no run path is wired to it"
}

func toUnitView(u plugin.Unit) unitView {
	served, reason := unitServed(u)
	return unitView{
		ID: u.ID, Kind: string(u.Kind), Name: u.Name, Path: u.Path,
		Bundle: u.Bundle, Enabled: u.Enabled, Served: served, Reason: reason,
	}
}

// liveUnitView is toUnitView for a unit that is actually installed, where an MCP unit's served flag
// has a second condition: the live manager has to still hold the declaration the pack made.
//
// Without this the console would keep claiming a server is served after config.yaml grew a
// declaration of the same name (the file wins there, and the pack loses the live slot).
func (h *PluginHandler) liveUnitView(u plugin.Unit) unitView {
	v := toUnitView(u)
	if u.Kind != plugin.KindMCP || !v.Served {
		return v
	}
	if h.mcp == nil {
		v.Served, v.Reason = false, "未接入外部 MCP 管理器：包声明的服务器只写在能力表里，无人连接"
		return v
	}
	if _, owned := h.mcp.PackOwner(u.Name); !owned {
		v.Served, v.Reason = false, "外部 MCP 管理器已不再持有本包对该名称的声明（配置文件里有同名服务器）"
	}
	return v
}

func (h *PluginHandler) bundleView(b *plugin.Bundle) gin.H {
	units := make([]unitView, 0, len(b.Units))
	for _, u := range b.Units {
		units = append(units, h.liveUnitView(u))
	}
	return gin.H{
		"id": b.ID, "name": b.Name, "version": b.Version,
		"description": b.Description, "dir": b.Dir, "units": units,
	}
}

// GetState answers GET /api/plugins: bundles, standalone units, table generation, and drift.
func (h *PluginHandler) GetState(c *gin.Context) {
	if h == nil || h.table == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "capability table unavailable"})
		return
	}
	bundles := make([]gin.H, 0)
	for _, b := range h.table.Bundles() {
		bundles = append(bundles, h.bundleView(b))
	}
	standalone := make([]unitView, 0)
	for _, kind := range plugin.Kinds {
		for _, u := range h.table.Units(kind) {
			if u.Bundle == "" {
				standalone = append(standalone, h.liveUnitView(u))
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"bundles":     bundles,
		"standalone":  standalone,
		"generation":  h.table.Generation(),
		"drift":       h.table.Drifted(),
		"bundlesRoot": h.bundles,
		"servedKinds": servedKindNames(),
	})
}

func servedKindNames() []string {
	out := make([]string, 0, len(servedKinds))
	for k := range servedKinds {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

type installRequest struct {
	// Bundle is a directory name *inside* the configured bundles root, or that directory's path
	// when it is inside the root. Anything else is refused: this endpoint installs files, so the
	// set of places it can read from has to be small and explicit.
	Bundle string `json:"bundle"`
}

// Install handles POST /api/plugins/install: the one-click extend.
func (h *PluginHandler) Install(c *gin.Context) {
	var req installRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数（需要 {\"bundle\":\"<包名>\"}）: " + err.Error()})
		return
	}
	dir, err := h.resolveBundleDir(req.Bundle)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	bundle, err := loadBundle(dir)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.checkMCPConflicts(bundle); err != nil {
		// Refused before anything changed: a pack must not be able to replace a server the
		// operator declared in config.yaml, whose tools may be in use right now.
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if err := h.table.InstallBundle(bundle); err != nil {
		h.replyMutationError(c, "install", bundle.ID, err)
		return
	}
	declared, mcpMessage := h.installMCPDeclarations(bundle)
	report := h.republishCatalog(c, bundleHasKind(bundle, plugin.KindTool))
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "bundle_install", "安装能力包", "plugin_bundle", bundle.ID, map[string]interface{}{
			"version": bundle.Version,
			"units":   len(bundle.Units),
			"roles":   report.roles,
		})
	}
	body := mergeToolLayerReport(gin.H{
		"message":   "能力包已安装并生效",
		"bundle":    h.bundleView(bundle),
		"roles":     report.roles,
		"refreshed": report.refreshed,
	}, report)
	if declared > 0 || mcpMessage != "" {
		body["mcp_declared"] = declared
		body["mcp_started"] = false
		if mcpMessage != "" {
			body["mcp_message"] = mcpMessage
		}
	}
	c.JSON(http.StatusOK, body)
}

// installMCPDeclarations writes a pack's server declarations into the live manager and switches
// the units off, so the table, the manager and the console all say "declared, not started".
func (h *PluginHandler) installMCPDeclarations(bundle *plugin.Bundle) (int, string) {
	servers := mcpUnitsOf(bundle.Units)
	if len(servers) == 0 {
		return 0, ""
	}
	updated := make([]plugin.Unit, 0, len(servers))
	for _, u := range servers {
		off, err := h.table.SetEnabled(u.ID, false)
		if err != nil {
			return 0, fmt.Sprintf("%s: %v", u.ID, err)
		}
		updated = append(updated, off)
	}
	bundle.Units = replaceUnits(bundle.Units, updated)
	return h.provisionMCP(bundle.ID, updated)
}

// replaceUnits swaps the refreshed copies of a few units back into a bundle's unit list, so a
// response built from the bundle reports the state the table actually holds.
func replaceUnits(units []plugin.Unit, refreshed []plugin.Unit) []plugin.Unit {
	byID := make(map[string]plugin.Unit, len(refreshed))
	for _, u := range refreshed {
		byID[u.ID] = u
	}
	out := make([]plugin.Unit, 0, len(units))
	for _, u := range units {
		if r, ok := byID[u.ID]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, u)
	}
	return out
}

// Uninstall handles DELETE /api/plugins/bundles/:id.
func (h *PluginHandler) Uninstall(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "能力包 ID 不能为空"})
		return
	}
	existing, ok := h.table.Bundle(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("能力包 %q 未安装", id)})
		return
	}
	// The kinds this pack contributes decide whether the tool surface has to be rebuilt and
	// whether the MCP manager loses a server, and the bundle is gone from the table once the
	// uninstall succeeds, so read it first.
	wantTools := bundleHasKind(existing, plugin.KindTool)
	if err := h.table.UninstallBundle(id); err != nil {
		h.replyMutationError(c, "uninstall", id, err)
		return
	}
	removed, mcpMessage := h.dropMCP(existing.Units)
	report := h.republishCatalog(c, wantTools)
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "bundle_uninstall", "卸载能力包", "plugin_bundle", id, map[string]interface{}{
			"roles": report.roles,
		})
	}
	body := mergeToolLayerReport(gin.H{
		"message": "能力包已卸载", "id": id, "roles": report.roles, "refreshed": report.refreshed,
	}, report)
	if removed > 0 || mcpMessage != "" {
		body["mcp_removed"] = removed
		if mcpMessage != "" {
			body["mcp_message"] = mcpMessage
		}
	}
	c.JSON(http.StatusOK, body)
}

// EnableUnit handles POST /api/plugins/units/:kind/:name/enabled - flip without touching
// sources.
//
// For a unit inside the built-in directories this is the switch the UI already exposes; for a
// bundle unit it is the only switch available, because editing a pack's file would make the
// installed content disagree with its recorded digest.
func (h *PluginHandler) EnableUnit(c *gin.Context) {
	id, ok := unitIDFromPath(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind 与 name 不能为空"})
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数: " + err.Error()})
		return
	}
	if body.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled 不能为空"})
		return
	}
	unit, err := h.table.SetEnabled(id, *body.Enabled)
	if err != nil {
		h.replyMutationError(c, "enable", id, err)
		return
	}
	report := h.republishCatalog(c, unitIDKind(id) == plugin.KindTool)
	mcpStarted, mcpMessage := false, ""
	if unitIDKind(id) == plugin.KindMCP {
		// The switch is the operator's decision to run the process, so this is where a declared
		// server is allowed to start - never install.
		declared, msg := h.provisionMCP(unit.Bundle, []plugin.Unit{unit})
		mcpStarted = declared > 0 && unit.Enabled
		mcpMessage = msg
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "unit_enabled", "启停能力单元", "plugin_unit", id, map[string]interface{}{
			"enabled": *body.Enabled, "roles": report.roles,
		})
	}
	respBody := mergeToolLayerReport(gin.H{
		"message": "已更新", "unit": h.liveUnitView(unit), "roles": report.roles, "refreshed": report.refreshed,
	}, report)
	if unitIDKind(id) == plugin.KindMCP {
		respBody["mcp_applied"] = mcpStarted
		if mcpMessage != "" {
			respBody["mcp_message"] = mcpMessage
		}
	}
	c.JSON(http.StatusOK, respBody)
}

// RemoveLocalUnit handles DELETE /api/plugins/units/:kind/:name for a unit that came from a
// scanned directory. A bundle-owned unit is refused by the table, not by a check duplicated here.
func (h *PluginHandler) RemoveLocalUnit(c *gin.Context) {
	id, ok := unitIDFromPath(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind 与 name 不能为空"})
		return
	}
	victim, _ := h.table.Unit(id)
	if err := h.table.RemoveLocal(id); err != nil {
		h.replyMutationError(c, "remove", id, err)
		return
	}
	dropped, mcpMessage := 0, ""
	if victim.Kind == plugin.KindMCP {
		dropped, mcpMessage = h.dropMCP([]plugin.Unit{victim})
	}
	report := h.republishCatalog(c, unitIDKind(id) == plugin.KindTool)
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "unit_detach", "从能力表摘除单元", "plugin_unit", id, map[string]interface{}{"roles": report.roles})
	}
	body := mergeToolLayerReport(gin.H{
		"message": "已从能力表摘除（不删除文件）", "id": id, "roles": report.roles, "refreshed": report.refreshed,
	}, report)
	if victim.Kind == plugin.KindMCP {
		body["mcp_removed"] = dropped
		if mcpMessage != "" {
			body["mcp_message"] = mcpMessage
		}
	}
	c.JSON(http.StatusOK, body)
}

// mergeToolLayerReport states the tool-layer outcome in the body, including why it did not happen.
func mergeToolLayerReport(body gin.H, r republishReport) gin.H {
	body["tools_rebuilt"] = r.tools
	if r.toolMessage != "" {
		body["tool_layer_error"] = r.toolMessage
	}
	return body
}

type republishReport struct {
	roles       int
	tools       bool
	refreshed   bool
	toolMessage string
}

// republishCatalog rebuilds the served configuration from the table after any mutation.
//
// A failure here is reported in the response rather than turning a successful install into a 500:
// the table *is* the source of truth, so the install did land, and rolling it back silently
// would be its own surprise. The operator sees "installed, catalog not refreshed" and can retry.
//
// wantTools is decided by the caller from what actually changed: rebuilding the tool surface runs
// ClearTools and re-registers every built-in tool, so a role-only pack must not pay for it.
func (h *PluginHandler) republishCatalog(c *gin.Context, wantTools bool) republishReport {
	report := republishReport{refreshed: true}
	if h.republish == nil {
		// No publisher wired: the roles catalog is not being refreshed, and claiming otherwise
		// would be the paper contract this response exists to avoid.
		report.refreshed = false
	} else {
		roles, err := h.republish.Reload()
		if err != nil {
			h.logger.Warn("能力表已变更，但角色目录未能刷新", zap.Error(err))
			c.Set("pluginRefreshError", err.Error())
			return republishReport{refreshed: false}
		}
		report.roles = roles
	}
	if !wantTools {
		return report
	}
	if h.tools == nil {
		h.logger.Warn("能力表已变更，但装配未接入工具层重建，配方不会生效")
		c.Set("pluginRefreshError", "未接入工具层重建")
		return republishReport{roles: report.roles, refreshed: false,
			toolMessage: "工具层未重建：装配没有接入工具层重建器，配方需重启或 POST /config/apply"}
	}
	if err := h.tools.Rebuild(); err != nil {
		h.logger.Warn("能力表已变更，但工具层未能重建", zap.Error(err))
		c.Set("pluginRefreshError", err.Error())
		return republishReport{roles: report.roles, refreshed: false,
			toolMessage: "工具层未重建: " + err.Error()}
	}
	report.tools = true
	return report
}

// bundleHasKind reports whether any unit of the bundle is of this kind.
func bundleHasKind(b *plugin.Bundle, kind plugin.Kind) bool {
	if b == nil {
		return false
	}
	for _, u := range b.Units {
		if u.Kind == kind {
			return true
		}
	}
	return false
}

func unitIDKind(id string) plugin.Kind {
	kind, _, ok := strings.Cut(id, "/")
	if !ok {
		return ""
	}
	return plugin.Kind(kind)
}

// resolveBundleDir confines every install to the configured bundles root.
func (h *PluginHandler) resolveBundleDir(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("bundle 不能为空：传 bundles 目录下的包名或其路径")
	}
	if h.bundles == "" {
		return "", fmt.Errorf("未配置能力包目录")
	}
	absRoot, err := filepath.Abs(h.bundles)
	if err != nil {
		return "", fmt.Errorf("解析能力包根目录失败: %w", err)
	}
	candidate := filepath.Join(absRoot, filepath.Base(filepath.Clean("/"+ref)))
	if strings.ContainsRune(ref, os.PathSeparator) || filepath.IsAbs(ref) {
		candidate, err = filepath.Abs(ref)
		if err != nil {
			return "", fmt.Errorf("解析能力包路径失败: %w", err)
		}
	}
	rel, err := filepath.Rel(absRoot, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("能力包必须位于 %s 之内", absRoot)
	}
	if _, err := os.Stat(filepath.Join(candidate, plugin.ManifestFileName)); err != nil {
		return "", fmt.Errorf("在 %s 找不到 %s", candidate, plugin.ManifestFileName)
	}
	return candidate, nil
}

// unitIDFromPath rebuilds "<kind>/<name>" from the two route segments, rejecting an unknown kind
// rather than letting it reach the table as an identity nothing else can name.
func unitIDFromPath(c *gin.Context) (string, bool) {
	kind := plugin.Kind(strings.TrimSpace(c.Param("kind")))
	name := strings.TrimSpace(c.Param("name"))
	if !kind.Valid() || name == "" {
		return "", false
	}
	return plugin.UnitIDFor(kind, name), true
}

func loadBundle(dir string) (*plugin.Bundle, error) {
	m, err := plugin.LoadManifestDir(dir)
	if err != nil {
		return nil, err
	}
	return m.Resolve()
}

// replyMutationError keeps a conflict (409) distinguishable from a server fault (500): the
// message names the bundle that already holds the identity, which is the answer the operator
// needs in order to act.
func (h *PluginHandler) replyMutationError(c *gin.Context, op, subject string, err error) {
	var conflict *plugin.ErrConflict
	if errors.As(err, &conflict) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "not installed") {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	h.logger.Error("能力表变更失败", zap.String("op", op), zap.String("subject", subject), zap.Error(err))
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

// availableBundle is one directory under the bundles root, whether or not it is installed.
type availableBundle struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	Description string     `json:"description,omitempty"`
	Installed   bool       `json:"installed"`
	Units       []unitView `json:"units"`
	// Error is a manifest that could not be read or resolved. It is per pack: one half-written
	// bundle.yaml must not turn the list of "what can I install" into a 500.
	Error string `json:"error,omitempty"`
}

// ListAvailable answers GET /api/plugins/available - the catalogue the console offers for one
// click. It reads only directory names and their manifests under the configured bundles root, and
// never returns a path that escapes it.
func (h *PluginHandler) ListAvailable(c *gin.Context) {
	if h == nil || h.table == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "capability table unavailable"})
		return
	}
	root := h.bundles
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusOK, gin.H{"bundlesRoot": root, "bundles": []availableBundle{}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]availableBundle, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, statErr := os.Stat(filepath.Join(dir, plugin.ManifestFileName)); statErr != nil {
			continue
		}
		item := availableBundle{ID: entry.Name()}
		bundle, loadErr := loadBundle(dir)
		if loadErr != nil {
			item.Error = loadErr.Error()
			out = append(out, item)
			continue
		}
		item.Name = bundle.Name
		item.Version = bundle.Version
		item.Description = bundle.Description
		if _, installed := h.table.Bundle(bundle.ID); installed {
			item.Installed = true
		}
		item.Units = make([]unitView, 0, len(bundle.Units))
		for _, u := range bundle.Units {
			// The catalogue states what installing *would* wire, so it uses the static verdict:
			// nothing is declared yet and an MCP row must not read as shadowed before it exists.
			item.Units = append(item.Units, toUnitView(u))
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	c.JSON(http.StatusOK, gin.H{"bundlesRoot": root, "bundles": out})
}
