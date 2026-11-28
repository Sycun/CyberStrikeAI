package llmgateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub/manifest"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

func gatewayFixture(t *testing.T, endpoint string) (*Service, *database.DB, *config.Config, *sync.RWMutex, string) {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "gateway.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	id := uuid.NewString()
	_, err = db.Exec(`INSERT INTO plugin_installs(id,plugin_id,source_identity,title,driver,observed_state,generation,created_at,updated_at)
		VALUES(?,?,?,?,?,'starting',1,?,?)`, id, id, id, id, "release", time.Now().UTC(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AI: config.AIConfig{DefaultChannel: "oa", Channels: map[string]config.AIChannelConfig{
		"oa": {Provider: "openai", Model: "fixture-openai", BaseURL: endpoint + "/edge/v1", APIKey: "server-openai", Headers: map[string]string{"X-Provider-Marker": "server"}},
		"an": {Provider: "claude", Model: "fixture-claude", BaseURL: endpoint + "/edge", APIKey: "server-anthropic", Headers: map[string]string{"X-Provider-Marker": "server"}},
	}}}
	var lock sync.RWMutex
	gateway := New(db, config.PluginLLMGatewayConfig{}, config.PluginEgress{ProxyMode: "direct"})
	gateway.SetSnapshot(func() (*config.Config, error) {
		lock.RLock()
		defer lock.RUnlock()
		copy := *cfg
		copy.AI.Channels = map[string]config.AIChannelConfig{}
		for k, v := range cfg.AI.Channels {
			copy.AI.Channels[k] = v
		}
		return &copy, nil
	})
	if err := gateway.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gateway.Close)
	return gateway, db, cfg, &lock, id
}

func gatewayCall(t *testing.T, root, path, token, body string, anthropic bool) *http.Response {
	t.Helper()
	method := http.MethodPost
	if body == "" {
		method = http.MethodGet
	}
	r, err := http.NewRequest(method, root+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Provider-Marker", "injected")
	r.Header.Set("X-Forwarded-Host", "unapproved.example")
	if anthropic {
		r.Header.Set("X-API-Key", token)
		r.Header.Set("anthropic-version", "2023-06-01")
	} else {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func gatewayBody(t *testing.T, resp *http.Response, want int) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != want {
		t.Fatalf("HTTP %d want %d: %s %v", resp.StatusCode, want, body, err)
	}
	return string(body)
}

func TestPluginGatewayNativeProtocolsAndUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Provider-Marker") != "server" || r.Header.Get("X-Forwarded-Host") != "" {
			http.Error(w, "unsafe header", 500)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		stream := string(fields["stream"]) == "true"
		switch r.URL.Path {
		case "/edge/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer server-openai" || string(fields["model"]) != `"fixture-openai"` ||
				!strings.Contains(string(raw), `"role":"tool"`) || !strings.Contains(string(raw), `"tool_call_id":"fxo"`) ||
				!strings.Contains(string(raw), `"reasoning_effort":"medium"`) || !strings.Contains(string(raw), `"image_url"`) ||
				string(fields["parallel_tool_calls"]) != "false" || fields["tools"] == nil || fields["tool_choice"] == nil {
				http.Error(w, "OpenAI fixture mismatch", 500)
				return
			}
			if stream {
				if !strings.Contains(string(fields["stream_options"]), `"include_usage":true`) {
					http.Error(w, "usage not requested", 500)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"fxo\"}]}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
				return
			}
			_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"id":"fxo","function":{"name":"read_demo_note"}}]}}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`)
		case "/edge/v1/messages":
			if r.Header.Get("X-API-Key") != "server-anthropic" || r.Header.Get("anthropic-version") != "2023-06-01" ||
				string(fields["model"]) != `"fixture-claude"` || !strings.Contains(string(raw), `"tool_use_id":"fxa"`) ||
				!strings.Contains(string(raw), `"signature":"signed-fixture"`) || !strings.Contains(string(raw), `"type":"document"`) ||
				!strings.Contains(string(raw), `"type":"image"`) || !strings.Contains(string(raw), `"effort":"high"`) ||
				fields["tools"] == nil || fields["tool_choice"] == nil {
				http.Error(w, "Anthropic fixture mismatch", 500)
				return
			}
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":13}}}\n\nevent: content_block_start\ndata: {\"content_block\":{\"type\":\"tool_use\",\"id\":\"fxa\"}}\n\nevent: content_block_delta\ndata: {\"delta\":{\"type\":\"signature_delta\",\"signature\":\"signed-fixture\"}}\n\nevent: message_delta\ndata: {\"usage\":{\"output_tokens\":3}}\n\n")
				return
			}
			_, _ = io.WriteString(w, `{"content":[{"type":"tool_use","id":"fxa","name":"read_demo_note"}],"usage":{"input_tokens":13,"output_tokens":3}}`)
		case "/edge/v1/messages/count_tokens":
			if r.Header.Get("X-API-Key") != "server-anthropic" || fields["max_tokens"] != nil {
				http.Error(w, "count fixture mismatch", 500)
				return
			}
			_, _ = io.WriteString(w, `{"input_tokens":12}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	g, db, cfg, lock, id := gatewayFixture(t, upstream.URL)
	declared := &manifest.LLM{Selection: "multi", Bindings: []manifest.Binding{{Protocol: manifest.OpenAI}, {Protocol: manifest.Anthropic}}}
	ctx := context.Background()
	if _, err := g.Patch(ctx, id, declared, Patch{Bindings: []BindingPatch{{Mode: "follow_default"}, {Mode: "channel", ChannelID: "an"}}}); err != nil {
		t.Fatal(err)
	}
	creds, root, _, err := g.Issue(ctx, id, 1, declared, false)
	if err != nil || len(creds) != 2 {
		t.Fatalf("issue: %d %v", len(creds), err)
	}
	tokens := map[string]string{}
	for _, credential := range creds {
		tokens[credential.Binding.Protocol] = credential.Values["gateway_token"]
	}
	oa, an := tokens[manifest.OpenAI], tokens[manifest.Anthropic]
	if oa == an {
		t.Fatal("protocols shared a token")
	}
	if _, err := db.PluginGatewayKey(ctx, oa); err == nil {
		t.Fatal("raw credential was stored")
	}
	gatewayBody(t, gatewayCall(t, root, "/v1/models", oa, "", false), 200)
	const openai = `{"model":"csai-managed","reasoning_effort":"medium","parallel_tool_calls":false,"tool_choice":"auto","tools":[{"type":"function","function":{"name":"read_demo_note"}}],"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},{"role":"assistant","tool_calls":[{"id":"fxo"}]},{"role":"tool","tool_call_id":"fxo","content":"fixed text"}]}`
	const anthropic = `{"model":"csai-managed","max_tokens":32,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"tool_choice":{"type":"auto"},"tools":[{"name":"read_demo_note","input_schema":{"type":"object"}}],"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"fixture","signature":"signed-fixture"},{"type":"tool_use","id":"fxa"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"fxa","content":"fixed text"},{"type":"image","source":{"type":"base64","data":"AA=="}},{"type":"document","source":{"type":"base64","data":"AA=="}}]}]}`
	for _, stream := range []bool{false, true} {
		add := func(value string) string {
			if !stream {
				return value
			}
			return strings.TrimSuffix(value, "}") + `,"stream":true}`
		}
		if text := gatewayBody(t, gatewayCall(t, root, "/v1/chat/completions", oa, add(openai), false), 200); !strings.Contains(text, "fxo") {
			t.Fatal("OpenAI tool output dropped")
		}
		if text := gatewayBody(t, gatewayCall(t, root, "/v1/messages", an, add(anthropic), true), 200); !strings.Contains(text, "fxa") {
			t.Fatal("Anthropic tool output dropped")
		}
	}
	gatewayBody(t, gatewayCall(t, root, "/v1/messages/count_tokens", an, `{"model":"csai-managed"}`, true), 200)
	gatewayBody(t, gatewayCall(t, root, "/v1/messages", oa, anthropic, false), 403)
	gatewayBody(t, gatewayCall(t, root, "/v1/chat/completions", oa,
		`{"model":"csai-managed","base_url":"https://unapproved.example","headers":{"Authorization":"override"}}`, false), 400)
	gatewayBody(t, gatewayCall(t, root, "/v1/messages", an, `{"model":"csai-managed","tools":[{"type":"web_search_20250305","name":"web_search"}]}`, true), 400)
	waitUntil(t, func() bool {
		var total, rows int
		err := db.QueryRow("SELECT COALESCE(SUM(charged_tokens),0), COUNT(*) FROM plugin_model_usage WHERE status='ok'").Scan(&total, &rows)
		return err == nil && rows == 5 && total == 64
	})
	lock.Lock()
	delete(cfg.AI.Channels, "oa")
	delete(cfg.AI.Channels, "an")
	cfg.OpenAI = config.OpenAIConfig{BaseURL: upstream.URL, Model: "legacy-should-not-be-used"}
	lock.Unlock()
	gatewayBody(t, gatewayCall(t, root, "/v1/models", oa, "", false), 403)
	gatewayBody(t, gatewayCall(t, root, "/v1/models", an, "", true), 403)
}

func TestPluginGatewaySingleActiveAndRevocation(t *testing.T) {
	disconnected := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	}))
	defer upstream.Close()
	g, _, _, _, id := gatewayFixture(t, upstream.URL)
	ctx := context.Background()
	declared := &manifest.LLM{Selection: "single_active", Bindings: []manifest.Binding{{Protocol: manifest.OpenAI}, {Protocol: manifest.Anthropic}}}
	if _, err := g.Patch(ctx, id, declared, Patch{BindingPatch: BindingPatch{Mode: "channel", ChannelID: "oa"}, Limits: LimitPatch{Concurrency: 1, RPM: 2, TokenBudget: 10000}}); err != nil {
		t.Fatal(err)
	}
	creds, root, _, err := g.Issue(ctx, id, 1, declared, false)
	if err != nil || len(creds) != 1 {
		t.Fatalf("single-active credentials %d %v", len(creds), err)
	}
	old := creds[0].Values["gateway_token"]
	stream := gatewayCall(t, root, "/v1/chat/completions", old, `{"model":"csai-managed","stream":true,"max_tokens":12}`, false)
	defer stream.Body.Close()
	line, err := bufio.NewReader(stream.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data:") {
		t.Fatalf("failed to flush: %q %v", line, err)
	}
	r := gatewayCall(t, root, "/v1/chat/completions", old, `{"model":"csai-managed","max_tokens":12}`, false)
	gatewayBody(t, r, 429)
	if _, err := g.Patch(ctx, id, declared, Patch{BindingPatch: BindingPatch{Mode: "channel", ChannelID: "an"}, Limits: LimitPatch{TokenBudget: 30}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not canceled on binding change")
	}
	gatewayBody(t, gatewayCall(t, root, "/v1/models", old, "", false), 401)
	creds, _, _, err = g.Issue(ctx, id, 1, declared, false)
	if err != nil || len(creds) != 1 || creds[0].Binding.Protocol != manifest.Anthropic {
		t.Fatal("single-active route did not switch")
	}
	token := creds[0].Values["gateway_token"]
	gatewayBody(t, gatewayCall(t, root, "/v1/messages", token, `{"model":"csai-managed","max_tokens":32}`, true), 429)
	if err := g.Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	gatewayBody(t, gatewayCall(t, root, "/v1/models", token, "", true), 401)
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("fixture state did not converge")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
