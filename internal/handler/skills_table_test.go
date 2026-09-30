package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The run path started serving bundled skills before this API could see them: every skill
// endpoint resolved against one configured directory. That gap was found by running the real
// server, and these tests keep it from coming back.

type skillTableEnv struct {
	h     *SkillsHandler
	root  string
	dir   string
	table *plugin.Table
}

func newSkillTableEnv(t *testing.T) *skillTableEnv {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "skills")
	writeTestFile(t, filepath.Join(root, "built-in-skill", "SKILL.md"),
		"---\nname: built-in-skill\ndescription: shipped\n---\n\n## Body\n\nshipped body\n")

	cfg := &config.Config{SkillsDir: root}
	h := &SkillsHandler{config: cfg, configPath: filepath.Join(dir, "config.yaml"), logger: zap.NewNop()}
	table := plugin.NewTable()
	plugin.Install(table)
	t.Cleanup(func() { plugin.Install(nil) })
	// Seed the built-in directory the way assembly does (scanBuiltInCapabilities); a table that
	// exists but holds nothing is not a state the server can reach.
	units, err := plugin.ScanDir(plugin.KindSkill, root, nil)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	for _, u := range units {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal: %v", err)
		}
	}
	return &skillTableEnv{h: h, root: root, dir: dir, table: table}
}

func (e *skillTableEnv) installBundle(t *testing.T) {
	t.Helper()
	bundleDir := filepath.Join(e.dir, "bundles", "pack")
	writeTestFile(t, filepath.Join(bundleDir, "skills", "packaged-skill", "SKILL.md"),
		"---\nname: packaged-skill\ndescription: from a bundle\n---\n\n## Body\n\nbundled body with a\ttab\n")
	writeTestFile(t, filepath.Join(bundleDir, plugin.ManifestFileName),
		"id: pack\nversion: 1.0.0\nunits:\n  - kind: skill\n    path: skills/packaged-skill\n")
	m, err := plugin.LoadManifestDir(bundleDir)
	if err != nil {
		t.Fatalf("LoadManifestDir: %v", err)
	}
	b, err := m.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := e.table.InstallBundle(b); err != nil {
		t.Fatalf("InstallBundle: %v", err)
	}
}

func (e *skillTableEnv) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/skills", e.h.GetSkills)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func (e *skillTableEnv) call(t *testing.T, method, path, target, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	switch target {
	case "put-file":
		r.PUT("/api/skills/:name/file", e.h.PutSkillPackageFile)
	case "update":
		r.PUT("/api/skills/:name", e.h.UpdateSkill)
	case "delete":
		r.DELETE("/api/skills/:name", e.h.DeleteSkill)
	case "detail":
		r.GET("/api/skills/:name", e.h.GetSkill)
	case "create":
		r.POST("/api/skills", e.h.CreateSkill)
	default:
		t.Fatalf("unknown target %q", target)
	}
	full := path
	req := httptest.NewRequest(method, full, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func listedDirNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Skills []struct {
			DirName     string `json:"dir_name"`
			Description string `json:"description"`
			Path        string `json:"path"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	out := make([]string, 0, len(payload.Skills))
	for _, s := range payload.Skills {
		out = append(out, s.DirName)
	}
	return out
}

func TestSkillListingSeesBundledSkills(t *testing.T) {
	env := newSkillTableEnv(t)

	if got := listedDirNames(t, env.get(t, "/api/skills")); len(got) != 1 || got[0] != "built-in-skill" {
		t.Fatalf("listing before install = %v, want only the shipped skill", got)
	}

	env.installBundle(t)
	got := listedDirNames(t, env.get(t, "/api/skills"))
	if len(got) != 2 {
		t.Fatalf("listing after install = %v, want the shipped skill plus the bundled one", got)
	}
	if got[0] != "built-in-skill" || got[1] != "packaged-skill" {
		t.Fatalf("listing = %v, want both names sorted by directory", got)
	}

	if err := env.table.UninstallBundle("pack"); err != nil {
		t.Fatalf("UninstallBundle: %v", err)
	}
	if got = listedDirNames(t, env.get(t, "/api/skills")); len(got) != 1 {
		t.Fatalf("listing after unplug = %v, want only the shipped skill", got)
	}
}

func TestSkillDetailReadsFromTheBundledDirectory(t *testing.T) {
	env := newSkillTableEnv(t)
	env.installBundle(t)

	rec := env.call(t, http.MethodGet, "/api/skills/packaged-skill?depth=full", "detail", "packaged-skill", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("detail for a bundled skill returned %d: %s", rec.Code, rec.Body.String())
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	body, _ := json.Marshal(payload)
	if !strings.Contains(string(body), "bundled body") {
		t.Fatalf("bundled skill body not served: %s", body)
	}
	if !strings.Contains(string(body), filepath.Join(env.dir, "bundles", "pack", "skills", "packaged-skill")) {
		t.Fatalf("detail does not point at the bundle's own directory: %s", body)
	}
}

// TestWritesToBundledSkillsAreRefused: without this, editing a bundled skill through the API
// would create a same-named directory in the built-in skills root and the two would fight -
// the same class of leak the role catalog fixed.
func TestWritesToBundledSkillsAreRefused(t *testing.T) {
	env := newSkillTableEnv(t)
	env.installBundle(t)
	packDir := filepath.Join(env.dir, "bundles", "pack")
	before, err := plugin.Digest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	rec := env.call(t, http.MethodPut, "/api/skills/packaged-skill/file", "put-file", "packaged-skill",
		`{"path":"SKILL.md","content":"---\nname: packaged-skill\ndescription: hijacked\n---\nbody\n"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("writing into a bundled skill returned %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "pack") {
		t.Fatalf("the refusal does not name the owning bundle: %s", rec.Body.String())
	}

	rec = env.call(t, http.MethodDelete, "/api/skills/packaged-skill", "delete", "packaged-skill", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("deleting a bundled skill returned %d, want 409: %s", rec.Code, rec.Body.String())
	}

	after, err := plugin.Digest(packDir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("a refused write still changed the bundle's files")
	}
	// And nothing was created behind its back in the built-in directory either.
	if _, err := plugin.Digest(filepath.Join(env.root, "packaged-skill")); err == nil {
		t.Fatalf("a shadow copy of the bundled skill was created under the built-in skills root")
	}
}

func TestBuiltInSkillRemainsEditableAndDeletable(t *testing.T) {
	env := newSkillTableEnv(t)
	env.installBundle(t)

	rec := env.call(t, http.MethodPut, "/api/skills/built-in-skill/file", "put-file", "built-in-skill",
		`{"path":"notes.md","content":"edited\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("writing a built-in skill returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := plugin.Digest(filepath.Join(env.root, "built-in-skill", "notes.md")); err != nil {
		t.Fatalf("the edit did not land in the built-in directory: %v", err)
	}

	rec = env.call(t, http.MethodDelete, "/api/skills/built-in-skill", "delete", "built-in-skill", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deleting a built-in skill returned %d: %s", rec.Code, rec.Body.String())
	}
	if got := listedDirNames(t, env.get(t, "/api/skills")); len(got) != 1 || got[0] != "packaged-skill" {
		t.Fatalf("listing after deleting the built-in skill = %v", got)
	}
}

// TestListingFallsBackWithoutATable keeps the pre-plug-in behaviour available: a process that
// never installed a table (unit tests, or an assembly where the scan failed) still lists its
// configured directory rather than nothing.
func TestListingFallsBackWithoutATable(t *testing.T) {
	env := newSkillTableEnv(t)
	env.installBundle(t)
	plugin.Install(nil)

	got := listedDirNames(t, env.get(t, "/api/skills"))
	if len(got) != 1 || got[0] != "built-in-skill" {
		t.Fatalf("listing without a table = %v, want only the built-in directory's skills", got)
	}
}

// TestSkillCreatedThroughTheAPIIsListed: the listing moved to the table, so creating a skill has
// to register it there too. Otherwise a created skill writes files nothing lists any more.
func TestSkillCreatedThroughTheAPIIsListed(t *testing.T) {
	env := newSkillTableEnv(t)
	if got := listedDirNames(t, env.get(t, "/api/skills")); len(got) != 1 {
		t.Fatalf("starting listing = %v, want one built-in skill", got)
	}

	rec := env.call(t, http.MethodPost, "/api/skills", "create", "",
		`{"name":"authored-skill","description":"written through the API","content":"## Steps\n\n1. do\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create returned %d: %s", rec.Code, rec.Body.String())
	}
	got := listedDirNames(t, env.get(t, "/api/skills"))
	if len(got) != 2 || got[0] != "authored-skill" || got[1] != "built-in-skill" {
		t.Fatalf("listing after create = %v, want both skills sorted by directory name", got)
	}
	if _, ok := env.table.Unit(plugin.UnitIDFor(plugin.KindSkill, "authored-skill")); !ok {
		t.Fatalf("the created skill is not in the capability table")
	}
	// A created skill is addressable by the same resolution the listing used.
	detail := env.call(t, http.MethodGet, "/api/skills/authored-skill?depth=full", "detail", "authored-skill", "")
	if detail.Code != http.StatusOK {
		t.Fatalf("detail for the authored skill returned %d: %s", detail.Code, detail.Body.String())
	}
}
