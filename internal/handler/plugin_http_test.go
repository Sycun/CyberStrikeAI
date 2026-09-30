package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/plugin"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The HTTP contract of "one click to extend": install by name, see it served immediately, unplug
// it, and get an honest served/not-served flag per unit.

type pluginTestEnv struct {
	plugins  *PluginHandler
	roles    *RoleHandler
	tools    *recordingToolLayer
	table    *plugin.Table
	bundles  string
	recorder *httptest.ResponseRecorder
}

// recordingToolLayer stands in for the config handler's tool-surface rebuild so the test can see
// whether the plug-in surface asked for one, and for which pack.
type recordingToolLayer struct {
	calls int
	err   error
}

func (r *recordingToolLayer) Rebuild() error {
	r.calls++
	return r.err
}

func newPluginTestEnv(t *testing.T, withBuiltInBundle bool) *pluginTestEnv {
	t.Helper()
	roles, _, table, dir := newRoleTestEnv(t)
	bundlesDir := filepath.Join(dir, "bundles")
	env := &pluginTestEnv{
		roles:   roles,
		table:   table,
		bundles: bundlesDir,
		tools:   &recordingToolLayer{},
	}
	env.plugins = NewPluginHandler(table, bundlesDir, roles, env.tools, nil, zap.NewNop())

	if withBuiltInBundle {
		// The real example pack, copied next to the test config so the install path is exercised
		// with a manifest somebody else would actually ship.
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "roles", "报告撰写.yaml"),
			"name: 报告撰写\ndescription: 交付视角\nuser_prompt: 以交付视角撰写\nenabled: true\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "skills", "finding-writeup", "SKILL.md"),
			"---\nname: finding-writeup\ndescription: 漏洞报告撰写\n---\n\n## Format\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "tools", "pandoc.yaml"),
			"name: pandoc\ncommand: /bin/true\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "agents", "report-analyst.md"),
			"---\ndescription: 报告分析子代理\n---\n\n# 报告分析\n\n汇总发现并成稿。\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", plugin.ManifestFileName),
			"id: reporting-pack\nname: 报告角色包\nversion: 1.0.0\ndescription: role+agent+skill+tool\nunits:\n  - kind: role\n    path: roles/报告撰写.yaml\n  - kind: agent\n    path: agents/report-analyst.md\n  - kind: skill\n    path: skills/finding-writeup\n  - kind: tool\n    path: tools/pandoc.yaml\n")
	}
	return env
}

func (e *pluginTestEnv) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/plugins", e.plugins.GetState)
	r.POST("/api/plugins/install", e.plugins.Install)
	r.DELETE("/api/plugins/bundles/:id", e.plugins.Uninstall)
	r.POST("/api/plugins/units/:kind/:name/enabled", e.plugins.EnableUnit)
	r.DELETE("/api/plugins/units/:kind/:name", e.plugins.RemoveLocalUnit)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	e.recorder = rec
	return rec
}

func decodeState(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("payload is not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return out
}

func TestPluginInstallServesTheBundleImmediately(t *testing.T) {
	env := newPluginTestEnv(t, true)
	if _, err := env.roles.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	rec := env.do(t, http.MethodGet, "/api/plugins", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/plugins returned %d: %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if len(state["bundles"].([]interface{})) != 0 {
		t.Fatalf("a bundle is installed before the install call")
	}

	rec = env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install returned %d: %s", rec.Code, rec.Body.String())
	}
	installed := decodeState(t, rec)
	if installed["refreshed"] != true {
		t.Fatalf("install did not refresh the served catalog: %v", installed)
	}
	if roles, _ := installed["roles"].(float64); roles < 2 {
		t.Fatalf("roles after install = %v, want the shipped role plus the bundled one", installed["roles"])
	}

	// The role is live for a run, not just recorded in the table.
	if _, ok := lookupRole(env.roles.config, "报告撰写"); !ok {
		t.Fatalf("bundled role not served after the HTTP install")
	}
	if _, ok := lookupRole(env.roles.config, "内置角色"); !ok {
		t.Fatalf("installing a pack removed a shipped role")
	}
	// Asserted here as present so the same id asserted as absent after the uninstall below
	// cannot be satisfied by an agent unit that was never registered in the first place.
	if _, ok := env.table.Unit("agent/report-analyst"); !ok {
		t.Fatalf("install did not register the bundled agent unit: %v", env.table.Units(plugin.KindAgent))
	}

	rec = env.do(t, http.MethodGet, "/api/plugins", "")
	state = decodeState(t, rec)
	bundles := state["bundles"].([]interface{})
	if len(bundles) != 1 {
		t.Fatalf("bundles = %v", state["bundles"])
	}
	bundle := bundles[0].(map[string]interface{})
	if bundle["id"] != "reporting-pack" || bundle["version"] != "1.0.0" {
		t.Fatalf("bundle view wrong: %v", bundle)
	}
	units := bundle["units"].([]interface{})
	if len(units) != 4 {
		t.Fatalf("bundle reports %d units, want 4", len(units))
	}
	servedByKind := map[string]bool{}
	reasonByKind := map[string]string{}
	for _, raw := range units {
		u := raw.(map[string]interface{})
		servedByKind[u["kind"].(string)] = u["served"].(bool)
		if r, ok := u["reason"].(string); ok {
			reasonByKind[u["kind"].(string)] = r
		}
	}
	if !servedByKind["role"] || !servedByKind["skill"] {
		t.Errorf("role/skill must report served: %v", servedByKind)
	}
	// The agent row is the one this clause caught being claimed wrong: the live server said
	// agent/served=false after the run path had already moved onto the table, and no assertion
	// covered it because the fixture pack had no agent unit.
	if !servedByKind["agent"] {
		t.Errorf("agent must report served: the run path loads agent paths from the table")
	}
	if reasonByKind["agent"] != "" {
		t.Errorf("a served unit carries a not-served reason: %q", reasonByKind["agent"])
	}
	if !servedByKind["tool"] {
		t.Errorf("tool must report served: the recipe list is rebuilt from the table, %v", servedByKind)
	}
	// The pack declares a tool unit, so installing it must rebuild the recipe layer and the MCP
	// tool surface - the same sequence POST /config/apply runs.
	if !installed["tools_rebuilt"].(bool) {
		t.Errorf("installing a pack with a tool recipe did not rebuild the tool layer: %v", installed)
	}
	if env.tools.calls != 1 {
		t.Fatalf("tool-layer rebuild calls after install = %d, want 1", env.tools.calls)
	}
	// The honesty clause that is left: an MCP declaration is tracked and listed, but the live path
	// for external servers is the MCP manager, not this table. See
	// TestEveryKindReportsItsActualServedState for the per-kind verdict.

	// Unplug: both the table and the served catalog go back.
	rec = env.do(t, http.MethodDelete, "/api/plugins/bundles/reporting-pack", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall returned %d: %s", rec.Code, rec.Body.String())
	}
	if env.tools.calls != 2 {
		t.Fatalf("tool-layer rebuild calls after uninstall = %d, want 2 (the pack's recipe must stop "+
			"being executable, not just leave the table)", env.tools.calls)
	}
	if got := decodeState(t, rec)["tools_rebuilt"]; got != true {
		t.Fatalf("uninstall did not report a tool-layer rebuild: %v", got)
	}
	if _, ok := lookupRole(env.roles.config, "报告撰写"); ok {
		t.Fatalf("unplugged role is still served")
	}
	if _, ok := lookupRole(env.roles.config, "内置角色"); !ok {
		t.Fatalf("uninstall took a shipped role with it")
	}
	if _, ok := env.table.Unit("agent/report-analyst"); ok {
		t.Fatalf("uninstall left the bundled agent unit in the table, so a run would still load it")
	}
	if _, err := plugin.Digest(filepath.Join(env.bundles, "reporting-pack", "roles", "报告撰写.yaml")); err != nil {
		t.Fatalf("uninstall deleted the pack's own file: %v", err)
	}
}

func TestPluginInstallRejectsPathsOutsideTheBundlesRoot(t *testing.T) {
	env := newPluginTestEnv(t, true)
	outside := filepath.Join(filepath.Dir(env.bundles), "..", "elsewhere")
	writeTestFile(t, filepath.Join(outside, "escape", plugin.ManifestFileName),
		"id: escape\nversion: 1.0.0\nunits:\n  - kind: role\n    path: r.yaml\n")
	writeTestFile(t, filepath.Join(outside, "escape", "r.yaml"), "name: escape\nenabled: true\n")

	for _, ref := range []string{"../../etc", "/etc", outside + "/escape", ""} {
		rec := env.do(t, http.MethodPost, "/api/plugins/install", fmt.Sprintf(`{"bundle":%q}`, ref))
		if ref == "" {
			if rec.Code == http.StatusOK {
				t.Fatalf("an empty bundle reference installed something")
			}
			continue
		}
		if rec.Code == http.StatusOK {
			t.Fatalf("install accepted a reference outside the bundles root: %q -> %s", ref, rec.Body.String())
		}
	}
	if len(env.table.Bundles()) != 0 {
		t.Fatalf("a refused install still landed in the table")
	}
}

func TestPluginInstallConflictNamesTheOwner(t *testing.T) {
	env := newPluginTestEnv(t, true)
	// A second pack claiming the same role identity as the first.
	writeTestFile(t, filepath.Join(env.bundles, "rival", "roles", "报告撰写.yaml"), "name: 报告撰写\nenabled: true\n")
	writeTestFile(t, filepath.Join(env.bundles, "rival", plugin.ManifestFileName),
		"id: rival\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/报告撰写.yaml\n")

	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("first install failed: %s", rec.Body.String())
	}
	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"rival"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("rival install returned %d, want 409: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "reporting-pack") {
		t.Fatalf("the conflict does not name the owning bundle: %s", body)
	}
	// The winner keeps serving; the loser changed nothing.
	if _, ok := lookupRole(env.roles.config, "报告撰写"); !ok {
		t.Fatalf("a refused install disturbed the served catalog")
	}
	if _, ok := env.table.Bundle("rival"); ok {
		t.Fatalf("the refused bundle is recorded as installed")
	}
}

func TestPluginEnableSwitchIsServedWithoutMovingFiles(t *testing.T) {
	env := newPluginTestEnv(t, true)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	path := filepath.Join(env.bundles, "reporting-pack", "roles", "报告撰写.yaml")
	before, err := plugin.Digest(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := env.table.SetEnabled("role/报告撰写", true); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, http.MethodPost, "/api/plugins/units/role/报告撰写/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := lookupRole(env.roles.config, "报告撰写"); !ok {
		t.Fatalf("a disabled role vanished from the listing instead of serving as disabled")
	}
	role, _ := lookupRole(env.roles.config, "报告撰写")
	if role.Enabled {
		t.Fatalf("the run path still treats the disabled role as enabled")
	}
	after, err := plugin.Digest(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("switching a bundle unit's state rewrote the pack's file")
	}

	// An unknown *kind* is a malformed request; an unknown name under a real kind is a miss.
	rec = env.do(t, http.MethodPost, "/api/plugins/units/bogus/x/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind returned %d, want 400: %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "/api/plugins/units/role/no-such-role/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown unit returned %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestPluginUnitDetachRespectsOwnership(t *testing.T) {
	env := newPluginTestEnv(t, true)
	local, err := plugin.NewUnit(plugin.KindRole, "临时角色", filepath.Join(env.bundles, "loose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.table.PutLocal(local); err != nil {
		t.Fatalf("PutLocal: %v", err)
	}
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}

	rec := env.do(t, http.MethodDelete, "/api/plugins/units/role/报告撰写", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("detaching a bundle-owned unit returned %d, want 409: %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodDelete, "/api/plugins/units/role/临时角色", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("detaching a scanned unit returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := env.table.Unit("role/临时角色"); ok {
		t.Fatalf("the detached unit is still in the table")
	}
	if _, err := plugin.Digest(filepath.Join(env.bundles, "loose.yaml")); err == nil {
		t.Logf("note: loose.yaml was created by PutLocal's caller, not by the endpoint")
	}
}

func TestPluginHandlerWithoutATableIsUnavailableNotPanic(t *testing.T) {
	h := NewPluginHandler(nil, "", nil, nil, nil, zap.NewNop())
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/plugins", h.GetState)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

// A pack that contributes only roles must not pay for a tool-surface rebuild: that path runs
// ClearTools and re-registers every built-in tool, so doing it for nothing would be a visible
// stall for concurrent runs.
func TestPluginInstallWithoutToolUnitsSkipsTheToolLayer(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeTestFile(t, filepath.Join(env.bundles, "role-only", "roles", "只加角色.yaml"),
		"name: 只加角色\nuser_prompt: 只有角色\nenabled: true\n")
	writeTestFile(t, filepath.Join(env.bundles, "role-only", plugin.ManifestFileName),
		"id: role-only\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/只加角色.yaml\n")

	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"role-only"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["refreshed"] != true {
		t.Fatalf("role catalog was not refreshed: %v", state)
	}
	if state["tools_rebuilt"] != false {
		t.Fatalf("a role-only pack rebuilt the whole tool surface: %v", state)
	}
	if env.tools.calls != 0 {
		t.Fatalf("RebuildToolLayer called %d times for a pack with no tool unit", env.tools.calls)
	}

	// The same pack's role unit must still switch without touching the tool layer.
	if rec := env.do(t, http.MethodPost, "/api/plugins/units/role/只加角色/enabled", `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if env.tools.calls != 0 {
		t.Fatalf("switching a role rebuilt the tool surface %d time(s)", env.tools.calls)
	}
}

// Forgetting the rebuilder at assembly time is not an error the endpoint can raise - the install
// did land - so it has to be a loud field in the response instead of a quiet "installed".
func TestPluginInstallReportsAMissingToolLayerRebuilder(t *testing.T) {
	env := newPluginTestEnv(t, true)
	env.plugins.tools = nil

	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["refreshed"] != false {
		t.Fatalf("a missing rebuilder reported refreshed=true: %v", state)
	}
	if state["tools_rebuilt"] != false {
		t.Fatalf("tools_rebuilt true without a rebuilder: %v", state)
	}
	msg, _ := state["tool_layer_error"].(string)
	if !strings.Contains(msg, "工具层未重建") {
		t.Fatalf("the response does not say the tool layer was not rebuilt: %q", msg)
	}
}

// A rebuild that fails must be reported the same way, not swallowed after the table changed.
func TestPluginInstallReportsAFailedToolLayerRebuild(t *testing.T) {
	env := newPluginTestEnv(t, true)
	env.tools.err = fmt.Errorf("注册表拒绝该配方")

	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	msg, _ := state["tool_layer_error"].(string)
	if !strings.Contains(msg, "注册表拒绝该配方") || state["refreshed"] != false {
		t.Fatalf("a failed rebuild was not reported: %v", state)
	}
}

func TestPluginToolUnitSwitchRebuildsTheToolLayer(t *testing.T) {
	env := newPluginTestEnv(t, true)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	before := env.tools.calls

	rec := env.do(t, http.MethodPost, "/api/plugins/units/tool/pandoc/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if env.tools.calls != before+1 {
		t.Fatalf("switching a tool unit rebuilt the layer %d extra time(s), want 1", env.tools.calls-before)
	}
	if got := decodeState(t, rec)["tools_rebuilt"]; got != true {
		t.Fatalf("the switch response does not report the rebuild: %v", got)
	}
}

// The per-kind served verdict, walked from the kind list itself: a kind added to plugin.Kinds
// without deciding how it is served fails here rather than shipping a field that quietly lies.
func TestEveryKindReportsItsActualServedState(t *testing.T) {
	served := map[plugin.Kind]bool{
		plugin.KindRole: true, plugin.KindAgent: true, plugin.KindSkill: true, plugin.KindTool: true,
	}
	for _, kind := range plugin.Kinds {
		isServed, reason := unitServed(plugin.Unit{Kind: kind})
		if served[kind] {
			if !isServed || reason != "" {
				t.Errorf("kind %q: served=%v reason=%q, want served with no reason", kind, isServed, reason)
			}
			continue
		}
		if isServed || strings.TrimSpace(reason) == "" {
			t.Errorf("kind %q: served=%v reason=%q, want not-served with a stated reason", kind, isServed, reason)
		}
	}
}
