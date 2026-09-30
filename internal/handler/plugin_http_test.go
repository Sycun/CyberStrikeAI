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
	table    *plugin.Table
	bundles  string
	recorder *httptest.ResponseRecorder
}

func newPluginTestEnv(t *testing.T, withBuiltInBundle bool) *pluginTestEnv {
	t.Helper()
	roles, _, table, dir := newRoleTestEnv(t)
	bundlesDir := filepath.Join(dir, "bundles")
	env := &pluginTestEnv{
		roles:   roles,
		table:   table,
		bundles: bundlesDir,
	}
	env.plugins = NewPluginHandler(table, bundlesDir, roles, nil, zap.NewNop())

	if withBuiltInBundle {
		// The real example pack, copied next to the test config so the install path is exercised
		// with a manifest somebody else would actually ship.
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "roles", "报告撰写.yaml"),
			"name: 报告撰写\ndescription: 交付视角\nuser_prompt: 以交付视角撰写\nenabled: true\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "skills", "finding-writeup", "SKILL.md"),
			"---\nname: finding-writeup\ndescription: 漏洞报告撰写\n---\n\n## Format\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "tools", "pandoc.yaml"),
			"name: pandoc\ncommand: /bin/true\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", plugin.ManifestFileName),
			"id: reporting-pack\nname: 报告角色包\nversion: 1.0.0\ndescription: role+skill+tool\nunits:\n  - kind: role\n    path: roles/报告撰写.yaml\n  - kind: skill\n    path: skills/finding-writeup\n  - kind: tool\n    path: tools/pandoc.yaml\n")
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
	if len(units) != 3 {
		t.Fatalf("bundle reports %d units, want 3", len(units))
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
	// The honesty clause: a tool recipe is tracked but no run path reads tools from the table,
	// so claiming it were live would be the paper-contract failure this whole layer exists to avoid.
	if servedByKind["tool"] {
		t.Errorf("tool unit reported as served, but tool loading still goes through /config/apply")
	}
	if !strings.Contains(reasonByKind["tool"], "config/apply") {
		t.Errorf("tool unit carries no reason: %q", reasonByKind["tool"])
	}

	// Unplug: both the table and the served catalog go back.
	rec = env.do(t, http.MethodDelete, "/api/plugins/bundles/reporting-pack", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := lookupRole(env.roles.config, "报告撰写"); ok {
		t.Fatalf("unplugged role is still served")
	}
	if _, ok := lookupRole(env.roles.config, "内置角色"); !ok {
		t.Fatalf("uninstall took a shipped role with it")
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
	h := NewPluginHandler(nil, "", nil, nil, zap.NewNop())
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/plugins", h.GetState)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}
