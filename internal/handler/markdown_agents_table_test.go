package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/agents"
	"cyberstrike-ai/internal/plugin"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The markdown agent API and the run path must agree about what exists. Both go through the
// capability table now, so a bundle's sub-agent is listed, viewable, and not clobberable here.

type markdownTableEnv struct {
	h     *MarkdownAgentsHandler
	dir   string
	table *plugin.Table
}

func newMarkdownTableEnv(t *testing.T) *markdownTableEnv {
	t.Helper()
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	writeTestFile(t, filepath.Join(agentsDir, "recon.md"),
		"---\nid: recon\nname: 侦察\ndescription: built-in sub agent\ntools: []\nmax_iterations: 0\n---\n\n## 职责\n\nshipped\n")

	table := plugin.NewTable()
	units, err := plugin.ScanDir(plugin.KindAgent, agentsDir, nil)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	for _, u := range units {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal: %v", err)
		}
	}
	plugin.Install(table)
	t.Cleanup(func() { plugin.Install(nil) })

	return &markdownTableEnv{
		h:     NewMarkdownAgentsHandler(agentsDir, zap.NewNop()),
		dir:   agentsDir,
		table: table,
	}
}

func (e *markdownTableEnv) installBundle(t *testing.T) {
	t.Helper()
	bundleDir := filepath.Join(filepath.Dir(e.dir), "bundles", "mobile-pack")
	writeTestFile(t, filepath.Join(bundleDir, "agents", "mobile-app-analyst.md"),
		"---\nid: mobile-app-analyst\nname: 移动端包分析\ndescription: from a pack\ntools: []\nmax_iterations: 0\n---\n\n## 职责\n\nbundled\n")
	writeTestFile(t, filepath.Join(bundleDir, plugin.ManifestFileName),
		"id: mobile-pack\nversion: 1.0.0\nunits:\n  - kind: agent\n    path: agents/mobile-app-analyst.md\n")
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

func (e *markdownTableEnv) do(t *testing.T, method, target, filename, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	switch target {
	case "list":
		r.GET("/api/multi-agent/markdown-agents", e.h.ListMarkdownAgents)
	case "get":
		r.GET("/api/multi-agent/markdown-agents/:filename", e.h.GetMarkdownAgent)
	case "create":
		r.POST("/api/multi-agent/markdown-agents", e.h.CreateMarkdownAgent)
	case "update":
		r.PUT("/api/multi-agent/markdown-agents/:filename", e.h.UpdateMarkdownAgent)
	case "delete":
		r.DELETE("/api/multi-agent/markdown-agents/:filename", e.h.DeleteMarkdownAgent)
	default:
		t.Fatalf("unknown target %q", target)
	}
	path := "/api/multi-agent/markdown-agents"
	if filename != "" {
		path += "/" + filename
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func listAgentRows(t *testing.T, rec *httptest.ResponseRecorder) []map[string]interface{} {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Agents []map[string]interface{} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return payload.Agents
}

func TestMarkdownAgentListShowsBundledDefinitions(t *testing.T) {
	env := newMarkdownTableEnv(t)

	rows := listAgentRows(t, env.do(t, http.MethodGet, "list", "", ""))
	if len(rows) != 1 {
		t.Fatalf("listing before install = %v, want only the built-in agent", rows)
	}
	if rows[0]["read_only"] != false {
		t.Errorf("a built-in agent is marked read-only: %v", rows[0])
	}

	env.installBundle(t)
	rows = listAgentRows(t, env.do(t, http.MethodGet, "list", "", ""))
	if len(rows) != 2 {
		t.Fatalf("listing after install = %v, want the built-in agent plus the bundled one", rows)
	}
	var bundled map[string]interface{}
	for _, row := range rows {
		if row["filename"] == "mobile-app-analyst.md" {
			bundled = row
		}
	}
	if bundled == nil {
		t.Fatalf("bundled agent missing from the listing: %v", rows)
	}
	if bundled["bundle"] != "mobile-pack" || bundled["read_only"] != true {
		t.Fatalf("bundled row lacks its ownership marker: %v", bundled)
	}
}

func TestBundledMarkdownAgentIsReadableAndNotWritable(t *testing.T) {
	env := newMarkdownTableEnv(t)
	env.installBundle(t)

	rec := env.do(t, http.MethodGet, "get", "mobile-app-analyst.md", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reading a bundled agent returned %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "移动端包分析") {
		t.Fatalf("bundled definition content not served: %s", rec.Body.String())
	}

	packDir := filepath.Join(filepath.Dir(env.dir), "bundles", "mobile-pack")
	before, err := plugin.Digest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	rec = env.do(t, http.MethodPut, "update", "mobile-app-analyst.md",
		`{"filename":"mobile-app-analyst.md","name":"hijacked","description":"d","instruction":"x"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("updating a bundled agent returned %d, want 409: %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodDelete, "delete", "mobile-app-analyst.md", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("deleting a bundled agent returned %d, want 409: %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "create", "",
		`{"filename":"mobile-app-analyst.md","name":"shadow","description":"d","instruction":"x"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("creating over a bundled identity returned %d, want 409: %s", rec.Code, rec.Body.String())
	}

	after, err := plugin.Digest(packDir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("refused writes still changed the bundle")
	}
	if _, err := plugin.Digest(filepath.Join(env.dir, "mobile-app-analyst.md")); err == nil {
		t.Fatalf("a shadow copy of the bundled agent was written into the built-in directory")
	}
}

// TestCreatedMarkdownAgentIsVisibleToTheRunPath: the run path reads the table, so the admin API
// has to register what it creates. Without that, a created agent would vanish from runs.
func TestCreatedMarkdownAgentIsVisibleToTheRunPath(t *testing.T) {
	env := newMarkdownTableEnv(t)

	rec := env.do(t, http.MethodPost, "create", "",
		`{"filename":"triage.md","name":"分诊","description":"d","instruction":"do it"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := env.table.Unit("agent/triage"); !ok {
		t.Fatalf("the created agent was not registered in the capability table")
	}
	load, err := agents.LoadMarkdownAgents(env.dir)
	if err != nil {
		t.Fatalf("run-path load: %v", err)
	}
	var found bool
	for _, sub := range load.SubAgents {
		if sub.Name == "分诊" {
			found = true
		}
	}
	if !found {
		// Matched by name, not by id: CreateMarkdownAgent derives the id from a slug of the
		// name, and a Chinese name has no slug, so it lands as "agent".
		t.Fatalf("the created agent is not visible to a run: %+v", load.SubAgents)
	}

	rec = env.do(t, http.MethodDelete, "delete", "triage.md", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := env.table.Unit("agent/triage"); ok {
		t.Fatalf("the deleted agent is still in the capability table")
	}
}
