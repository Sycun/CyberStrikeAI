package pluginhub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub/manifest"
	"cyberstrike-ai/internal/pluginhub/runner"
	"cyberstrike-ai/internal/pluginhub/source"
	"cyberstrike-ai/internal/pluginhub/webproxy"
	"cyberstrike-ai/internal/security"

	"go.uber.org/zap"
)

type e2eFakeDriver struct {
	srv *httptest.Server
}

func (d *e2eFakeDriver) Probe(context.Context, string) runner.Probe {
	return runner.Probe{Available: true}
}

func (d *e2eFakeDriver) Prepare(context.Context, runner.Spec) error { return nil }

func (d *e2eFakeDriver) Start(_ context.Context, spec runner.Spec) (runner.State, error) {
	if spec.Model == nil || spec.Model.Env["PLUGIN_LLM_BASE_URL"] == "" || spec.Model.Env["PLUGIN_LLM_TOKEN"] == "" {
		return runner.State{}, ErrRuntimeUnavailable
	}
	baseURL := spec.Model.Env["PLUGIN_LLM_BASE_URL"]
	token := spec.Model.Env["PLUGIN_LLM_TOKEN"]
	model := spec.Model.Env["PLUGIN_LLM_MODEL"]
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<h1>Embedded Plugin OK</h1>"))
		case "/api/run-tool":
			// Simulate the plugin calling CSAI's LLM gateway with OpenAI tool_calls (and without explicit max_tokens).
			payload := map[string]any{
				"model": model,
				"tools": []map[string]any{{
					"type": "function",
					"function": map[string]any{
						"name":       "ping_target",
						"parameters": map[string]any{"type": "object"},
					},
				}},
				"messages": []map[string]any{
					{"role": "user", "content": "check target"},
					{"role": "assistant", "tool_calls": []map[string]any{{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "ping_target", "arguments": "{}"},
					}}},
					{"role": "tool", "tool_call_id": "call_1", "content": "alive"},
				},
			}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
		default:
			http.NotFound(w, r)
		}
	}))
	u, _ := url.Parse(d.srv.URL)
	var port int
	_, _ = json.Marshal(u.Port())
	return runner.State{Running: true, Endpoint: d.srv.URL, Port: port}, nil
}

func (d *e2eFakeDriver) Inspect(context.Context, runner.Spec) (runner.State, error) {
	if d.srv == nil {
		return runner.State{}, nil
	}
	return runner.State{Running: true, Endpoint: d.srv.URL}, nil
}

func (d *e2eFakeDriver) Stop(context.Context, runner.Spec) error {
	if d.srv != nil {
		d.srv.Close()
		d.srv = nil
	}
	return nil
}

func (d *e2eFakeDriver) Remove(ctx context.Context, spec runner.Spec) error {
	return d.Stop(ctx, spec)
}

func TestEndToEndInstallAutoBindEmbedProxyAndToolCalling(t *testing.T) {
	var upstreamSawMaxTokens bool
	var upstreamSawMaxCompletion bool
	var upstreamSawTools bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-secret-key" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		var fields map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		upstreamSawMaxTokens = fields["max_tokens"] != nil
		upstreamSawMaxCompletion = fields["max_completion_tokens"] != nil
		upstreamSawTools = fields["tools"] != nil
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"tool roundtrip ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8}}`))
	}))
	defer upstream.Close()

	db, err := database.NewDB(filepath.Join(t.TempDir(), "e2e.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	auth := security.NewAuthManager(2)
	pw, err := auth.AttachRBACStore(db)
	if err != nil {
		t.Fatal(err)
	}
	adminToken, adminExp, err := auth.Authenticate("admin", pw)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.PluginsConfig{
		Directory: filepath.Join(t.TempDir(), "plugins"),
		Proxy: config.PluginProxyConfig{
			Mode:     "port",
			BindHost: "127.0.0.1",
			PortPool: config.PluginPortPool{Start: 44100, End: 44120},
		},
	}
	mgr, err := New(db, cfg, t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	drv := &e2eFakeDriver{}
	mgr.drivers["git"] = drv
	mgr.ConfigureServices(auth, func() (*config.Config, error) {
		return &config.Config{
			AI: config.AIConfig{
				DefaultChannel: "primary",
				Channels: map[string]config.AIChannelConfig{
					"primary": {
						Name:     "Primary OpenAI",
						Provider: "openai",
						APIKey:   "upstream-secret-key",
						BaseURL:  upstream.URL + "/v1",
						Model:    "gpt-test",
					},
				},
			},
		}, nil
	})
	if err := mgr.StartServices("127.0.0.1", 8080, nil); err != nil {
		t.Fatal(err)
	}
	defer mgr.CloseServices()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = mgr.Close(stopCtx)
	}()
	for deadline := time.Now().Add(3 * time.Second); !mgr.Ready() && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
	}

	// Verify sanitized channel listing works without leaking API key.
	defaultCh, channels := mgr.AvailableLLMChannels()
	if defaultCh != "primary" || len(channels) != 1 || channels[0].Protocol != manifest.OpenAI {
		t.Fatalf("unexpected channels: default=%q list=%+v", defaultCh, channels)
	}

	commit := strings.Repeat("b", 40)
	manifestYAML := `
apiVersion: plugins.csai.io/v1
kind: WebPlugin
metadata:
  id: e2e-tool
  title: E2E Tool
  summary: End-to-end verification plugin
  version: "1.0.0"
  repository: https://github.com/example/e2e-tool
  license: MIT
distribution:
  - type: git
    url: https://github.com/example/e2e-tool
    revision: main
    dependencies: [{name: go, minVersion: "1.22"}]
    build: [{program: go, args: [build, .]}]
launch:
  entrypoint: e2e-tool
  env:
    PORT: listen_port
web:
  route: root
  entryPath: /
  healthPath: /healthz
access:
  mode: platform
llm:
  selection: single_active
  bindings:
    - protocol: openai_chat
      providerValue: openai
      env:
        PLUGIN_LLM_BASE_URL: gateway_v1_url
        PLUGIN_LLM_TOKEN: gateway_token
        PLUGIN_LLM_MODEL: model_alias
        PLUGIN_LLM_PROVIDER: provider
permissions:
  persistentData: false
  modelGateway: true
  egress: [managed_model_gateway]
`
	decl, err := manifest.DecodeDeclaration([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	inspBytes, _ := json.Marshal(source.Inspection{
		Repository:  "https://github.com/example/e2e-tool",
		ResolvedRef: "refs/heads/main",
		Commit:      commit,
		Manifest:    decl.Canonical,
		Candidates:  []string{"git"},
	})
	now := time.Now().UTC()
	if err := db.SavePluginInspection(ctx, "insp-e2e", "admin", string(inspBytes), now, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	planRes, err := mgr.CreatePlan(ctx, PlanRequest{
		Actor:        "admin",
		InspectionID: "insp-e2e",
		Driver:       "git",
		Isolation:    "standard",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Queue install with default AutoBindLLM (nil => auto-bind default channel).
	op, err := mgr.Queue(ctx, OperationRequest{
		Actor:      "admin",
		Kind:       "install",
		PlanID:     planRes.Record.ID,
		PlanDigest: planRes.Record.PlanDigest,
		Key:        "install-e2e-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitOp := func(opID string) database.PluginOperation {
		t.Helper()
		for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(40 * time.Millisecond) {
			got, err := db.GetPluginOperation(ctx, opID)
			if err == nil && got.Status != "queued" && got.Status != "running" {
				return got
			}
		}
		t.Fatalf("operation %s timed out", opID)
		return database.PluginOperation{}
	}
	if got := waitOp(op.ID); got.Status != "succeeded" {
		t.Fatalf("install failed: %+v (os=%s/%s)", got, runtime.GOOS, runtime.GOARCH)
	}

	detail, err := mgr.InstallDetail(ctx, op.InstallID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Observed != "healthy" || len(detail.LLMBindings) != 1 || detail.LLMBindings[0].Mode != "follow_default" {
		t.Fatalf("unexpected detail after auto-bind install: %+v", detail)
	}

	// Issue embed ticket and exchange it for an isolated browser session cookie.
	issueReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/plugins/installs/"+op.InstallID+"/embed-sessions", nil)
	issueReq.Host = "127.0.0.1:8080"
	issueReq.Header.Set("Origin", "http://127.0.0.1:8080")
	embed, err := mgr.IssueEmbed(issueReq, op.InstallID, adminToken, adminExp)
	if err != nil {
		t.Fatal(err)
	}

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"ticket": {embed.Ticket}}
	exReq, _ := http.NewRequest(http.MethodPost, embed.Action, strings.NewReader(form.Encode()))
	exReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	exReq.Header.Set("Origin", "http://127.0.0.1:8080")
	exResp, err := noRedirect.Do(exReq)
	if err != nil {
		t.Fatal(err)
	}
	defer exResp.Body.Close()
	if exResp.StatusCode != http.StatusSeeOther || len(exResp.Cookies()) != 1 {
		t.Fatalf("ticket exchange failed: status=%d cookies=%d", exResp.StatusCode, len(exResp.Cookies()))
	}
	sessionCookie := exResp.Cookies()[0]
	if sessionCookie.MaxAge != int((2 * time.Hour).Seconds()) {
		t.Fatalf("expected 2h session cookie, got MaxAge=%d", sessionCookie.MaxAge)
	}

	// Proxy GET / through isolated origin.
	pageReq, _ := http.NewRequest(http.MethodGet, embed.Origin+"/", nil)
	pageReq.AddCookie(sessionCookie)
	pageResp, err := noRedirect.Do(pageReq)
	if err != nil {
		t.Fatal(err)
	}
	pageBody, _ := io.ReadAll(pageResp.Body)
	pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK || !strings.Contains(string(pageBody), "Embedded Plugin OK") {
		t.Fatalf("proxy page failed: status=%d body=%q", pageResp.StatusCode, string(pageBody))
	}

	// Proxy POST /api/run-tool -> plugin calls CSAI LLM gateway -> mock OpenAI upstream with tools + default max_tokens.
	toolReq, _ := http.NewRequest(http.MethodPost, embed.Origin+"/api/run-tool", nil)
	toolReq.Header.Set("Origin", embed.Origin)
	toolReq.AddCookie(sessionCookie)
	toolResp, err := noRedirect.Do(toolReq)
	if err != nil {
		t.Fatal(err)
	}
	toolBody, _ := io.ReadAll(toolResp.Body)
	toolResp.Body.Close()
	if toolResp.StatusCode != http.StatusOK || !strings.Contains(string(toolBody), "tool roundtrip ok") {
		t.Fatalf("proxied tool roundtrip failed: status=%d body=%q", toolResp.StatusCode, string(toolBody))
	}
	if !upstreamSawTools || !upstreamSawMaxTokens || upstreamSawMaxCompletion {
		t.Fatalf("unexpected upstream request flags: tools=%v max_tokens=%v max_completion_tokens=%v",
			upstreamSawTools, upstreamSawMaxTokens, upstreamSawMaxCompletion)
	}

	// Uninstall and verify clean teardown + origin retirement.
	unOp, err := mgr.Queue(ctx, OperationRequest{
		Actor:     "admin",
		Kind:      "uninstall",
		InstallID: op.InstallID,
		Key:       "uninstall-e2e-1",
		Purge:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitOp(unOp.ID); got.Status != "succeeded" {
		t.Fatalf("uninstall failed: %+v", got)
	}
	if _, err := mgr.IssueEmbed(issueReq, op.InstallID, adminToken, adminExp); err != webproxy.ErrUnavailable {
		t.Fatalf("expected unavailable after uninstall, got %v", err)
	}
}
