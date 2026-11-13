package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"github.com/gin-gonic/gin"
)

func TestManagedTaskListMergesBeforePagingAndKeepsResourceScopes(t *testing.T) {
	h, task := longTaskFixture(t)
	initial, err := h.db.ListManagedTasks(task.OwnerID, database.RBACScopeOwn, "goal", "", "", 30, 0)
	if err != nil || len(initial) != 1 || initial[0].UpdatedAt == "" {
		t.Fatalf("invalid stored timestamp: %+v %v", initial, err)
	}
	other, err := h.db.CreateRBACUser("list-other", "Other", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id, owner, title, status, created string) {
		t.Helper()
		_, err := h.db.Exec(`INSERT INTO batch_task_queues(id,title,owner_user_id,status,created_at) VALUES(?,?,?,?,?)`, id, title, owner, status, created)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(task.ID, other.ID, "foreign with the same ID", "pending", "2030-01-03T00:00:00Z")
	insert("owned", task.OwnerID, "owned checklist", "pending", "2030-01-02T00:00:00Z")
	insert("shared", other.ID, "shared checklist", "pending", "2030-01-01T00:00:00Z")
	if err = h.db.AssignResourceToUser(task.OwnerID, "batch_task", "shared"); err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.Exec(`UPDATE long_tasks SET updated_at='2030-01-04T00:00:00Z' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(security.ContextSessionKey, security.Session{UserID: task.OwnerID, Scope: database.RBACScopeOwn, Permissions: map[string]bool{"tasks:read": true}})
	})
	r.Use(security.RBACMiddleware(h.db))
	r.GET("/api/tasks", h.ListManagedTasks)
	get := func(query string) struct {
		Tasks []database.ManagedTaskRow `json:"tasks"`
		More  bool                      `json:"has_more"`
	} {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/tasks"+query, nil))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var body struct {
			Tasks []database.ManagedTaskRow `json:"tasks"`
			More  bool                      `json:"has_more"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	first := get("?limit=1")
	second := get("?limit=1&offset=1")
	third := get("?limit=1&offset=2")
	if len(first.Tasks) != 1 || first.Tasks[0].Mode != "goal" || !first.More || second.Tasks[0].ID != "owned" || third.Tasks[0].ID != "shared" || third.More {
		t.Fatalf("bad merged pages: %+v %+v %+v", first, second, third)
	}
	if rows := get("?mode=list").Tasks; len(rows) != 2 {
		t.Fatalf("cross-resource exposure or missing assignment: %+v", rows)
	}
	if rows := get("?q=foreign").Tasks; len(rows) != 0 {
		t.Fatalf("foreign row exposed: %+v", rows)
	}
	if rows := get("?status=pending").Tasks; len(rows) != 2 {
		t.Fatalf("bad status filtering: %+v", rows)
	}
	if rows := get("?q=owned").Tasks; len(rows) != 1 || rows[0].ID != "owned" {
		t.Fatalf("bad search: %+v", rows)
	}
}

func TestManagedTaskListPreservesScheduledAndPartialOutcomes(t *testing.T) {
	h, task := longTaskFixture(t)
	for _, id := range []string{"partial", "failed", "scheduled"} {
		if _, err := h.db.Exec(`INSERT INTO batch_task_queues(id,title,owner_user_id,status,created_at,schedule_mode,schedule_enabled,next_run_at) VALUES(?,?,?,'completed','2030-01-01T00:00:00Z','cron',1,'2030-01-02T00:00:00Z')`, id, id, task.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	for i, item := range []struct{ queue, status string }{{"partial", "completed"}, {"partial", "failed"}, {"failed", "failed"}, {"scheduled", "completed"}} {
		if _, err := h.db.Exec(`INSERT INTO batch_tasks(id,queue_id,message,status) VALUES(?,?,?,?)`, i, item.queue, "PRIVATE EXECUTION INSTRUCTION", item.status); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := h.db.ListManagedTasks(task.OwnerID, database.RBACScopeOwn, "list", "", "", 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]database.ManagedTaskRow{}
	for _, row := range rows {
		found[row.ID] = row
	}
	if found["partial"].Status != "partial" || found["partial"].CompletedSteps != 1 || found["partial"].TotalSteps != 2 || found["failed"].Status != "failed" || found["scheduled"].Status != "scheduled" || found["scheduled"].NextRunAt == "" {
		t.Fatalf("outcomes lost: %+v", found)
	}
}
