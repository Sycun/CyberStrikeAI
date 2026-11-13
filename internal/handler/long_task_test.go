package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/taskflow"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func longTaskFixture(t *testing.T) (*AgentHandler, *taskflow.Task) {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "tasks.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = db.BootstrapRBAC("fixture-password-hash", security.PermissionCatalog); err != nil {
		t.Fatal(err)
	}
	user, err := db.GetRBACUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	h := &AgentHandler{db: db, config: cfg, logger: zap.NewNop(), tasks: NewAgentTaskManager(), taskEventBus: NewTaskEventBus(), hitlManager: NewHITLManager(db, zap.NewNop())}
	if err = h.hitlManager.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	if err = h.InitLongTasks(func() (*config.Config, error) { return cfg, nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.longTasks.Close)
	now := time.Now().UTC()
	task := &taskflow.Task{ID: "task", OwnerID: user.ID, ExecutorID: user.ID, Name: "fixture", Objective: "Inspect the local fixture", Scope: "Local only", Criteria: "Record observed fixture result", Concurrency: 1, MaxRounds: 10, RemainingSeconds: 90, Status: "draft", Steps: []taskflow.Step{}, Guidance: []string{}, CreatedAt: now, UpdatedAt: now}
	if err = h.longTasks.Store.Create(task); err != nil {
		t.Fatal(err)
	}
	return h, task
}
func taskRouter(h *AgentHandler, user string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(security.ContextSessionKey, security.Session{UserID: user, Scope: database.RBACScopeOwn, Permissions: map[string]bool{"tasks:read": true, "tasks:write": true, "tasks:delete": true}})
	})
	r.Use(security.RBACMiddleware(h.db))
	r.GET("/api/long-tasks", h.ListLongTasks)
	r.GET("/api/long-tasks/:id", h.GetLongTask)
	r.GET("/api/long-tasks/:id/events", h.LongTaskEvents)
	r.GET("/api/long-tasks/:id/report", h.LongTaskReport)
	r.POST("/api/long-tasks/:id/control/:action", h.ControlLongTask)
	r.DELETE("/api/long-tasks/:id", h.DeleteLongTask)
	r.POST("/api/long-tasks", h.CreateLongTask)
	return r
}
func TestLongTaskAccessRevisionAndOrigin(t *testing.T) {
	h, task := longTaskFixture(t)
	other, err := h.db.CreateRBACUser("other", "Other", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign := taskRouter(h, other.ID)
	for _, path := range []string{"/api/long-tasks/task", "/api/long-tasks/task/events", "/api/long-tasks/task/report"} {
		w := httptest.NewRecorder()
		foreign.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 403 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	foreign.ServeHTTP(w, httptest.NewRequest("GET", "/api/long-tasks", nil))
	if !strings.Contains(w.Body.String(), `"tasks":[]`) {
		t.Fatal(w.Body.String())
	}
	owned := taskRouter(h, task.OwnerID)
	req := httptest.NewRequest("POST", "/api/long-tasks/task/control/guidance", strings.NewReader(`{"revision":99,"message":"change direction"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	owned.ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	req = httptest.NewRequest("POST", "/api/long-tasks/task/control/guidance", strings.NewReader(`{"revision":0,"message":"change direction"}`))
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: "fixture"})
	req.Header.Set("Origin", "https://other.example")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	owned.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, _ := h.longTasks.Store.Get(task.ID)
	if got.Revision != 0 || len(got.Guidance) != 0 {
		t.Fatal("rejected writes changed state")
	}
	req = httptest.NewRequest("POST", "/api/long-tasks/task/control/guidance", strings.NewReader(`{"revision":0,"message":"use supplied evidence"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	owned.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, _ = h.longTasks.Store.Get(task.ID)
	if len(got.Guidance) != 1 {
		t.Fatal(got)
	}
}

func TestLongTaskMemoryKeepsTaskBoundaryAndPriorAttempts(t *testing.T) {
	h, task := longTaskFixture(t)
	var ids []string
	for _, name := range []string{"attempt-one", "attempt-two", "foreign"} {
		conv, err := h.db.CreateConversation(name, database.ConversationCreateMeta{})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := h.db.AddMessage(conv.ID, "assistant", "fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = h.db.AddProcessDetail(msg.ID, conv.ID, "tool_result", name+" needle", nil); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, conv.ID)
	}
	h.longTasks.Store.Change(task.ID, "", "", "", func(t *taskflow.Task) error {
		t.Steps = []taskflow.Step{{ID: "s1", ConversationID: ids[1], Conversations: ids[:2], Summary: "observed needle"}}
		return nil
	})
	result, err := h.longTaskRuntime.memory(task.ID, map[string]any{"query": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "attempt-one") || !strings.Contains(result.Content, "attempt-two") || strings.Contains(result.Content, "foreign") {
		t.Fatal(result.Content)
	}
	result, err = h.longTaskRuntime.memory(task.ID, map[string]any{"query": "needle", "step_id": "not-in-task"})
	if err != nil || strings.Contains(result.Content, "attempt-one") {
		t.Fatal(result, err)
	}
}

func longTaskReply(w http.ResponseWriter, tool, id string, args map[string]any, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	delta := map[string]any{"role": "assistant"}
	reason := "stop"
	if tool != "" {
		b, _ := json.Marshal(args)
		delta["tool_calls"] = []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": tool, "arguments": string(b)}}}
		reason = "tool_calls"
	} else {
		delta["content"] = text
	}
	for _, choice := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": reason}} {
		b, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{choice}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}
func TestLongTaskNativePiLifecycle(t *testing.T) {
	t.Run("successful_receipts", func(t *testing.T) { testLongTaskNativePiLifecycle(t, false) })
	t.Run("failed_tools_cannot_be_claimed_successful", func(t *testing.T) { testLongTaskNativePiLifecycle(t, true) })
}
func testLongTaskNativePiLifecycle(t *testing.T, denied bool) {
	worker, err := filepath.Abs("../../runtime/pi/dist/worker.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(worker); err != nil {
		t.Skip("build runtime/pi for native integration")
	}
	h, task := longTaskFixture(t)
	var planned atomic.Bool
	var plannerAttempts atomic.Int32
	var workerTurns, toolRuns atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		raw, _ := json.Marshal(body["messages"])
		text := string(raw)
		if strings.Contains(text, "你是任务管理的规划器") {
			if !denied && plannerAttempts.Add(1) == 1 {
				http.Error(w, "temporary rate limit", 429)
				return
			}
			if !planned.Swap(true) {
				longTaskReply(w, "", "", nil, `{"strategy":"inspect then verify","summary":"inspect local fixture","steps":[{"id":"s1","title":"inspect","instruction":"Read the local fixture once and preserve evidence","depends_on":[]}],"retire":[],"question":""}`)
			} else {
				longTaskReply(w, "", "", nil, `{"strategy":"verify","summary":"fixture observed","steps":[],"retire":[],"question":""}`)
			}
			return
		}
		if strings.Contains(text, "你是独立验收者") {
			longTaskReply(w, "", "", nil, `{"complete":true,"summary":"fixture verified","missing":"","evidence":["s1"]}`)
			return
		}
		switch workerTurns.Add(1) {
		case 1:
			longTaskReply(w, "fixture_echo", "echo-1", map[string]any{}, "")
		case 2:
			longTaskReply(w, "task_checkpoint", "cp-1", map[string]any{"summary": "Observed fixture-evidence in tool result"}, "")
		case 3:
			longTaskReply(w, "task_memory", "memory-1", map[string]any{"query": "fixture-evidence"}, "")
		case 4:
			longTaskReply(w, "task_result", "result-1", map[string]any{"status": "completed", "summary": "Verified fixture-evidence via fixture_echo. No remaining gaps."}, "")
		default:
			longTaskReply(w, "", "", nil, "fixture complete")
		}
	}))
	defer model.Close()
	cfg := h.config
	cfg.AgentRuntime = config.AgentRuntimeConfig{Engine: "pi", WorkerPath: worker, SessionDir: t.TempDir()}
	cfg.OpenAI = config.OpenAIConfig{Provider: "openai", APIKey: "fixture-key", Model: "fixture", BaseURL: model.URL + "/v1", MaxTotalTokens: 32000, MaxCompletionTokens: 2048}
	cfg.Agent.MaxIterations = 10
	cfg.MultiAgent.Skills.Disable = true
	srv := mcp.NewServer(zap.NewNop())
	srv.SetToolAuthorizer(func(context.Context, string, map[string]interface{}) error {
		if denied {
			return fmt.Errorf("fixture policy denied")
		}
		return nil
	})
	srv.RegisterTool(mcp.Tool{Name: "fixture_echo", Description: "Read local fixture", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(context.Context, map[string]interface{}) (*mcp.ToolResult, error) {
		toolRuns.Add(1)
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "fixture-evidence"}}}, nil
	})
	h.agent = agent.NewAgent(&cfg.OpenAI, &cfg.Agent, srv, nil, zap.NewNop(), 10)
	h.tasks.SetToolCanceler(h.cancelRunningMCPToolsForConversation)
	if err = h.longTasks.Start(task.ID, task.OwnerID, task.Revision); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	var got *taskflow.Task
	for time.Now().Before(deadline) {
		got, err = h.longTasks.Store.Get(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !taskflow.Active(got.Status) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if denied {
		if got.Status != "blocked" || toolRuns.Load() != 0 || len(got.Steps) != 1 || got.Steps[0].Status != "failed" || !strings.Contains(got.Steps[0].Summary, "没有成功工具结果") {
			t.Fatalf("false completion was not rejected: %+v steps=%+v", got, got.Steps)
		}
		return
	}
	if got.Status != "completed" || toolRuns.Load() != 1 || len(got.Steps) != 1 || got.Steps[0].ConversationID == "" {
		if len(got.Steps) > 0 {
			details, _ := h.db.GetProcessDetailsByConversation(got.Steps[0].ConversationID)
			for _, group := range details {
				for _, detail := range group {
					if detail.EventType == "tool_result" {
						t.Logf("tool result: %s %s", detail.Message, detail.Data)
					}
				}
			}
		}
		t.Fatalf("native lifecycle failed: %+v steps=%+v toolRuns=%d", got, got.Steps, toolRuns.Load())
	}
	messages, err := h.db.GetMessages(got.Steps[0].ConversationID)
	if err != nil || len(messages) < 2 {
		t.Fatalf("execution conversation missing: %v", err)
	}
}

func TestLongTaskFullTraceAndTailSearch(t *testing.T) {
	h, task := longTaskFixture(t)
	var refs, convs []string
	for _, name := range []string{"owned", "foreign"} {
		conv, _ := h.db.CreateConversation(name, database.ConversationCreateMeta{})
		msg, _ := h.db.AddMessage(conv.ID, "assistant", "result", nil)
		ref, err := h.db.AddProcessDetailWithID(msg.ID, conv.ID, "tool_result", strings.Repeat("prefix ", 4000)+"tail-evidence", nil)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
		convs = append(convs, conv.ID)
	}
	if err := h.longTasks.Store.Change(task.ID, "", "", "", func(t *taskflow.Task) error {
		t.Steps = []taskflow.Step{{ID: "s1", Status: "completed", ConversationID: convs[0], Summary: "summary"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	found, err := h.longTaskRuntime.memory(task.ID, map[string]any{"query": "tail-evidence"})
	if err != nil || !strings.Contains(found.Content, "tail-evidence") || strings.Contains(found.Content, refs[1]) {
		t.Fatalf("tail search: %s %v", found.Content, err)
	}
	trace, err := h.longTaskRuntime.trace(task.ID, map[string]any{"trace_id": refs[0], "offset": float64(28000)})
	if err != nil || !strings.Contains(trace.Content, "tail-evidence") {
		t.Fatalf("full trace: %s %v", trace.Content, err)
	}
	if _, err = h.longTaskRuntime.trace(task.ID, map[string]any{"trace_id": refs[1]}); err == nil {
		t.Fatal("foreign trace exposed")
	}
}

func TestLongTaskCreateOnlySavesDraftAndRejectsOversizedBody(t *testing.T) {
	h, task := longTaskFixture(t)
	router := taskRouter(h, task.OwnerID)
	body := `{"name":"new","objective":"inspect fixture","scope":"local only","criteria":"observed evidence"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/long-tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"status":"draft"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved struct {
		Task taskflow.Task `json:"task"`
	}
	json.Unmarshal(w.Body.Bytes(), &saved)
	if saved.Task.Rounds != 0 || !h.db.UserCanAccessResource(task.OwnerID, database.RBACScopeAssigned, "long_task", saved.Task.ID) {
		t.Fatal("draft executed or creator lost access")
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/long-tasks", strings.NewReader(`{"objective":"`+strings.Repeat("x", 70<<10)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLongTaskModelRetryClassification(t *testing.T) {
	for _, text := range []string{"429 rate limit", "502 upstream", "connection reset"} {
		if !transientLongTaskModelError(fmt.Errorf("%s", text)) {
			t.Fatal(text)
		}
	}
	for _, text := range []string{"401 invalid api key", "403 forbidden", "unknown model", "policy denied"} {
		if transientLongTaskModelError(fmt.Errorf("%s", text)) {
			t.Fatal(text)
		}
	}
	var p taskflow.Proposal
	if decodeLongTaskJSON("{} trailing", &p) == nil || decodeLongTaskJSON(`{"unknown":true}`, &p) == nil {
		t.Fatal("invalid proposal decoded")
	}
}

func TestLongTaskControlRequiresRevision(t *testing.T) {
	h, task := longTaskFixture(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/long-tasks/task/control/pause", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	taskRouter(h, task.OwnerID).ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, _ := h.longTasks.Store.Get(task.ID)
	if got.Status != "draft" {
		t.Fatal(got)
	}
}
func TestLongTaskJSONRepairCannotRetainPartialFields(t *testing.T) {
	var p taskflow.Proposal
	if decodeLongTaskJSON(`{"strategy":"should not persist","unknown":true}`, &p) == nil {
		t.Fatal("unknown field accepted")
	}
	if p.Strategy != "" {
		t.Fatal("partially decoded state escaped")
	}
}
