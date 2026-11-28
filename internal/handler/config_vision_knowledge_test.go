package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestTestEmbeddingAndClearingSavedKnowledgeOverrides(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var seenAuth string
	embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		seenAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3,0.4]}]}`))
	}))
	defer embedServer.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	initialYAML := strings.Join([]string{
		"openai:",
		"  provider: openai_compatible",
		"  base_url: " + embedServer.URL + "/v1",
		"  api_key: sk-main-channel",
		"  model: demo-chat",
		"vision:",
		"  enabled: true",
		"  provider: claude",
		"  model: qwen-vl-max",
		"  timeout_seconds: 90",
		"knowledge:",
		"  enabled: false",
		"  base_path: knowledge_base",
		"  embedding:",
		"    provider: openai",
		"    model: text-embedding-v4",
		"    base_url: https://stale.example.test/v1",
		"    api_key: sk-stale",
		"",
	}, "\n")
	if err := os.WriteFile(cfgPath, []byte(initialYAML), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	cfg := &config.Config{
		OpenAI: config.OpenAIConfig{
			Provider: "openai_compatible",
			BaseURL:  embedServer.URL + "/v1",
			APIKey:   "sk-main-channel",
			Model:    "demo-chat",
		},
		Vision: config.VisionConfig{
			Enabled:        true,
			Provider:       "claude",
			Model:          "qwen-vl-max",
			TimeoutSeconds: 90,
		},
		Knowledge: config.KnowledgeConfig{
			Enabled:  false,
			BasePath: "knowledge_base",
			Embedding: config.EmbeddingConfig{
				Provider: "openai",
				Model:    "text-embedding-v4",
				BaseURL:  "https://stale.example.test/v1",
				APIKey:   "sk-stale",
			},
		},
	}

	h := &ConfigHandler{
		config:     cfg,
		configPath: cfgPath,
		logger:     zap.NewNop(),
	}

	// 1. TestEmbedding falls back to OpenAI base_url + masked OpenAI api_key when embedding fields are blank.
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	probeBody := `{"embedding":{"provider":"openai","model":"text-embedding-v4","base_url":"","api_key":""},"openai":{"provider":"openai_compatible","base_url":"` + embedServer.URL + `/v1","api_key":"********","model":"demo-chat"}}`
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/config/test-embedding", strings.NewReader(probeBody))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.TestEmbedding(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("TestEmbedding HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var probeResp struct {
		Success    bool   `json:"success"`
		Dimensions int    `json:"dimensions"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &probeResp); err != nil {
		t.Fatalf("decode probe response: %v", err)
	}
	if !probeResp.Success || probeResp.Dimensions != 4 {
		t.Fatalf("unexpected probe response: %+v", probeResp)
	}
	if seenAuth != "Bearer sk-main-channel" {
		t.Fatalf("expected fallback Bearer sk-main-channel, got %q", seenAuth)
	}

	// 2. Clearing knowledge.embedding.base_url / api_key and vision.provider / timeout_seconds persists empty values to disk.
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	updateBody := `{
		"vision":{"enabled":true,"provider":"","base_url":"","api_key":"","model":"qwen-vl-max","timeout_seconds":0},
		"knowledge":{"enabled":true,"base_path":"knowledge_base","embedding":{"provider":"openai","model":"text-embedding-v4","base_url":"","api_key":""}}
	}`
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(updateBody))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.UpdateConfig(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("UpdateConfig HTTP %d: %s", rec.Code, rec.Body.String())
	}
	savedYAML, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read saved config.yaml: %v", err)
	}
	text := string(savedYAML)
	if strings.Contains(text, "stale.example.test") || strings.Contains(text, "sk-stale") {
		t.Fatalf("stale embedding fields were not cleared in config.yaml:\n%s", text)
	}
	if strings.Contains(text, "provider: claude") || strings.Contains(text, "timeout_seconds: 90") {
		t.Fatalf("stale vision fields were not cleared in config.yaml:\n%s", text)
	}
}
