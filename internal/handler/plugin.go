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
	logger    *zap.Logger
	audit     *audit.Service
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
func NewPluginHandler(table *plugin.Table, bundlesDir string, publisher catalogPublisher, auditSvc *audit.Service, logger *zap.Logger) *PluginHandler {
	return &PluginHandler{table: table, bundles: bundlesDir, republish: publisher, audit: auditSvc, logger: logger}
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

// servedKinds lists the kinds whose run path reads the table. A unit of any other kind is
// installed - it has an identity, it is listed, it can be removed - but the response says so
// plainly instead of letting "installed" read as "live".
var servedKinds = map[plugin.Kind]string{
	plugin.KindRole:  "",
	plugin.KindAgent: "",
	plugin.KindSkill: "",
}

func unitServed(u plugin.Unit) (bool, string) {
	if gap, ok := servedKinds[u.Kind]; ok {
		return true, gap
	}
	switch u.Kind {
	case plugin.KindTool:
		return false, "tool recipes load through POST /config/apply and the capability registry, which requires the operator approval floor; not wired to the table yet"
	case plugin.KindMCP:
		return false, "external MCP servers add and remove through their own live manager, which registers a capability spec per tool; the table tracks the declaration but does not feed that path"
	default:
		return false, "no run path reads this kind from the capability table yet"
	}
}

func toUnitView(u plugin.Unit) unitView {
	served, reason := unitServed(u)
	return unitView{
		ID: u.ID, Kind: string(u.Kind), Name: u.Name, Path: u.Path,
		Bundle: u.Bundle, Enabled: u.Enabled, Served: served, Reason: reason,
	}
}

func (h *PluginHandler) bundleView(b *plugin.Bundle) gin.H {
	units := make([]unitView, 0, len(b.Units))
	for _, u := range b.Units {
		units = append(units, toUnitView(u))
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
				standalone = append(standalone, toUnitView(u))
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
	if err := h.table.InstallBundle(bundle); err != nil {
		h.replyMutationError(c, "install", bundle.ID, err)
		return
	}
	report := h.republishCatalog(c)
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "bundle_install", "安装能力包", "plugin_bundle", bundle.ID, map[string]interface{}{
			"version": bundle.Version,
			"units":   len(bundle.Units),
			"roles":   report.roles,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"message":   "能力包已安装并生效",
		"bundle":    h.bundleView(bundle),
		"roles":     report.roles,
		"refreshed": report.refreshed,
	})
}

// Uninstall handles DELETE /api/plugins/bundles/:id.
func (h *PluginHandler) Uninstall(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "能力包 ID 不能为空"})
		return
	}
	if _, ok := h.table.Bundle(id); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("能力包 %q 未安装", id)})
		return
	}
	if err := h.table.UninstallBundle(id); err != nil {
		h.replyMutationError(c, "uninstall", id, err)
		return
	}
	report := h.republishCatalog(c)
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "bundle_uninstall", "卸载能力包", "plugin_bundle", id, map[string]interface{}{
			"roles": report.roles,
		})
	}
	c.JSON(http.StatusOK, gin.H{"message": "能力包已卸载", "id": id, "roles": report.roles, "refreshed": report.refreshed})
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
	report := h.republishCatalog(c)
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "unit_enabled", "启停能力单元", "plugin_unit", id, map[string]interface{}{
			"enabled": *body.Enabled, "roles": report.roles,
		})
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新", "unit": toUnitView(unit), "roles": report.roles})
}

// RemoveLocalUnit handles DELETE /api/plugins/units/:kind/:name for a unit that came from a
// scanned directory. A bundle-owned unit is refused by the table, not by a check duplicated here.
func (h *PluginHandler) RemoveLocalUnit(c *gin.Context) {
	id, ok := unitIDFromPath(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind 与 name 不能为空"})
		return
	}
	if err := h.table.RemoveLocal(id); err != nil {
		h.replyMutationError(c, "remove", id, err)
		return
	}
	report := h.republishCatalog(c)
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "unit_detach", "从能力表摘除单元", "plugin_unit", id, map[string]interface{}{"roles": report.roles})
	}
	c.JSON(http.StatusOK, gin.H{"message": "已从能力表摘除（不删除文件）", "id": id, "roles": report.roles})
}

type republishReport struct {
	roles     int
	refreshed bool
}

// republishCatalog rebuilds the served configuration from the table after any mutation.
//
// A failure here is reported in the response rather than turning a successful install into a 500:
// the table *is* the source of truth, so the install did land, and rolling it back silently
// would be its own surprise. The operator sees "installed, catalog not refreshed" and can retry.
func (h *PluginHandler) republishCatalog(c *gin.Context) republishReport {
	if h.republish == nil {
		return republishReport{}
	}
	roles, err := h.republish.Reload()
	if err != nil {
		h.logger.Warn("能力表已变更，但角色目录未能刷新", zap.Error(err))
		c.Set("pluginRefreshError", err.Error())
		return republishReport{refreshed: false}
	}
	return republishReport{roles: roles, refreshed: true}
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
