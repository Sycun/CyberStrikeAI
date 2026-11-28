package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub"
	"cyberstrike-ai/internal/pluginhub/manifest"
	"cyberstrike-ai/internal/pluginhub/source"
	"cyberstrike-ai/internal/security"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type pluginInspectorFunc func(context.Context, string, string) (*source.Inspection, error)

func (f pluginInspectorFunc) Inspect(ctx context.Context, repository, revision string) (*source.Inspection, error) {
	return f(ctx, repository, revision)
}

func TestPluginManagementPreviewAndRBAC(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "plugins.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	auth := security.NewAuthManager(2)
	password, err := auth.AttachRBACStore(db)
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := auth.Authenticate("admin", password)
	if err != nil {
		t.Fatal(err)
	}
	addUser := func(username, scope string, permissions ...string) (string, string) {
		t.Helper()
		role, err := db.UpsertRBACRole("", username, "", scope, permissions)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := security.HashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		user, err := db.CreateRBACUser(username, username, hash, true, []string{role.ID})
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := auth.Authenticate(username, password)
		if err != nil {
			t.Fatal(err)
		}
		return user.ID, token
	}
	readerID, reader := addUser("plugin-reader", database.RBACScopeAssigned, "plugins:read")
	_, scopedManager := addUser("plugin-scoped-manager", database.RBACScopeOwn, "plugins:manage")
	_, otherManager := addUser("plugin-other-manager", database.RBACScopeAll, "plugins:manage")
	_, userWithoutPlugins := addUser("plugin-other", database.RBACScopeAll, "auth:self")

	raw, err := os.ReadFile("../pluginhub/manifest/testdata/web-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "https://git.example.org/examples/log-review-demo.git", "https://github.com/example/review"))
	commit := strings.Repeat("a", 40)
	manager, err := pluginhub.New(db, config.PluginsConfig{}, t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	handler := NewPluginHandler(db, nil, manager)
	inspections := 0
	handler.inspector = pluginInspectorFunc(func(_ context.Context, repository, revision string) (*source.Inspection, error) {
		inspections++
		if repository != "https://github.com/example/review" || (revision != "v1.0.0" && revision != "draft") {
			t.Fatalf("bad inspector inputs: %s %s", repository, revision)
		}
		inspection := &source.Inspection{
			Repository: repository, RequestedRef: revision, ResolvedRef: "refs/tags/v1.0.0",
			Commit: commit, Candidates: []string{"git"},
		}
		if revision != "draft" {
			declaration, err := manifest.DecodeDeclaration(raw)
			if err != nil {
				t.Fatal(err)
			}
			inspection.Manifest = declaration.Canonical
		}
		return inspection, nil
	})
	router := gin.New()
	group := router.Group("/api", security.AuthMiddleware(auth), security.RBACMiddleware(db))
	handler.Register(group)

	request := func(method, path, token, body string, expected int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, "/api/plugins"+path, strings.NewReader(body))
		if strings.HasPrefix(token, "cookie:") {
			req.AddCookie(&http.Cookie{Name: "auth_token", Value: strings.TrimPrefix(token, "cookie:")})
		} else if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != expected {
			t.Fatalf("%s %s: status %d, expected %d: %s", method, path, res.Code, expected, res.Body)
		}
		return res.Body.Bytes()
	}

	request(http.MethodGet, "/catalog", "", "", http.StatusUnauthorized)
	request(http.MethodGet, "/catalog", "cookie:"+admin, "", http.StatusUnauthorized)
	request(http.MethodGet, "/catalog", reader, "", http.StatusOK)
	request(http.MethodGet, "/catalog", userWithoutPlugins, "", http.StatusForbidden)
	request(http.MethodGet, "/schemas/manifest-v1", reader, "", http.StatusOK)
	body := `{"repository":"https://github.com/example/review","revision":"v1.0.0"}`
	request(http.MethodPost, "/imports/inspect", reader, body, http.StatusForbidden)
	request(http.MethodPost, "/imports/inspect", scopedManager, body, http.StatusForbidden)
	var inspection struct{ ID string }
	if err := json.Unmarshal(request(http.MethodPost, "/imports/inspect", admin, body, http.StatusCreated), &inspection); err != nil || inspection.ID == "" {
		t.Fatalf("missing inspection ID: %v", err)
	}
	if inspections != 1 {
		t.Fatalf("unapproved inspection ran: %d calls", inspections)
	}
	planRequest := `{"inspection_id":"` + inspection.ID + `","isolation":"standard","driver":"git"}`
	request(http.MethodPost, "/install-plans", otherManager, planRequest, http.StatusNotFound)
	var response struct {
		Plan   json.RawMessage `json:"plan"`
		Digest string          `json:"plan_digest"`
	}
	if err := json.Unmarshal(request(http.MethodPost, "/install-plans", admin, planRequest, http.StatusCreated), &response); err != nil {
		t.Fatal(err)
	}
	plan, err := manifest.DecodeInstallPlan(response.Plan)
	if err != nil || plan.Artifact.Commit != commit || plan.Isolation != "standard" || manifest.Digest(plan.Canonical) != response.Digest {
		t.Fatalf("plan %+v: %v; digest %s", plan, err, response.Digest)
	}
	request(http.MethodGet, "/install-plans/"+plan.ID, otherManager, "", http.StatusNotFound)
	request(http.MethodGet, "/install-plans/"+plan.ID, admin, "", http.StatusOK)
	confirmation := `{"plan_id":"` + plan.ID + `","plan_digest":"` + response.Digest + `","idempotency_key":"approve"}`
	request(http.MethodPost, "/installs", scopedManager, confirmation, http.StatusForbidden)
	request(http.MethodPost, "/installs", admin, `{}`, http.StatusUnprocessableEntity)
	draftRequest := `{"repository":"https://github.com/example/review","revision":"draft"}`
	var draftInspection struct{ ID string }
	if err := json.Unmarshal(request(http.MethodPost, "/imports/inspect", admin, draftRequest, http.StatusCreated), &draftInspection); err != nil {
		t.Fatal(err)
	}
	draftPlan := `{"inspection_id":"` + draftInspection.ID + `"}`
	request(http.MethodPost, "/install-plans", admin, draftPlan, http.StatusUnprocessableEntity)
	edited, err := manifest.DecodeDeclaration(raw)
	if err != nil {
		t.Fatal(err)
	}
	draftPlan = `{"inspection_id":"` + draftInspection.ID + `","driver":"git","manifest":` + string(edited.Canonical) + `}`
	request(http.MethodPost, "/install-plans", admin, strings.ReplaceAll(draftPlan, "example/review", "another/review"), http.StatusConflict)
	request(http.MethodPost, "/install-plans", admin, draftPlan, http.StatusCreated)
	item, err := db.Exec(`INSERT INTO plugin_installs
		(id, plugin_id, source_identity, title, owner_user_id, driver, created_at, updated_at)
		VALUES ('plugin-a','review','source-a','Review','admin','git',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := item.RowsAffected(); n != 1 {
		t.Fatalf("unexpected rows affected: %d", n)
	}
	request(http.MethodGet, "/installs/plugin-a", reader, "", http.StatusForbidden)
	if err := db.AssignResourceToUser(readerID, "plugin_install", "plugin-a"); err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, "/installs/plugin-a", reader, "", http.StatusOK)
	list := request(http.MethodGet, "/installs", reader, "", http.StatusOK)
	if !strings.Contains(string(list), "plugin-a") || strings.Contains(string(list), "source-a") {
		t.Fatalf("unexpected install summary: %s", list)
	}
	request(http.MethodPost, "/installs/plugin-a/embed-sessions", reader, `{}`, http.StatusForbidden)
	request(http.MethodPost, "/installs/plugin-a/embed-sessions", admin, `{}`, http.StatusForbidden)
	request(http.MethodGet, "/installs/plugin-a/logs", reader, "", http.StatusForbidden)
	if err := db.AppendPluginLog(context.Background(), "plugin-a", "fixture log line"); err != nil {
		t.Fatal(err)
	}
	page := request(http.MethodGet, "/installs/plugin-a/logs?limit=1", admin, "", http.StatusOK)
	if !strings.Contains(string(page), "fixture log line") || !strings.Contains(string(page), "next_cursor") {
		t.Fatalf("unexpected log page: %s", page)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	streamCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, server.URL+"/api/plugins/installs/plugin-a/logs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+admin)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream response: %s", res.Status)
	}
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") && strings.Contains(scanner.Text(), "fixture log line") {
			return
		}
	}
	t.Fatalf("log stream did not emit a fixture entry: %v", scanner.Err())
}
