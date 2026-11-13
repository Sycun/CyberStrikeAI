package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestBatchTaskPagesKeepTotalsAndFullEvidence(t *testing.T) {
	h, owner := longTaskFixture(t)
	h.batchTaskManager = NewBatchTaskManager(zap.NewNop())
	h.batchTaskManager.SetDB(h.db)
	messages := make([]string, 365)
	for i := range messages {
		messages[i] = fmt.Sprintf("Local fixture %d", i+1)
	}
	queue, err := h.batchTaskManager.CreateBatchQueue("365 fixtures", "", "single", "manual", "", "", nil, 2, messages)
	if err != nil {
		t.Fatal(err)
	}
	h.db.SetResourceOwner("batch_task", queue.ID, owner.OwnerID)
	full := strings.Repeat("证据", 1000) + "FULL_EVIDENCE_TAIL"
	h.batchTaskManager.UpdateTaskStatus(queue.ID, queue.Tasks[0].ID, "completed", full, "")
	h.batchTaskManager.UpdateTaskStatus(queue.ID, queue.Tasks[25].ID, "running", "", "")
	h.batchTaskManager.UpdateQueueStatus(queue.ID, "paused")
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(security.ContextSessionKey, security.Session{UserID: owner.OwnerID, Scope: database.RBACScopeOwn, Permissions: map[string]bool{"tasks:read": true}})
	})
	r.Use(security.RBACMiddleware(h.db))
	r.GET("/api/batch-tasks/:queueId", h.GetBatchQueue)
	r.GET("/api/batch-tasks/:queueId/tasks/:taskId", h.GetBatchTask)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	first := get("/api/batch-tasks/" + queue.ID + "?task_page=1&task_page_size=20")
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	var page struct {
		Queue      BatchTaskQueue `json:"queue"`
		Stats      map[string]int `json:"task_stats"`
		Pagination struct {
			Page, Total, Offset int
			Pages               int `json:"total_pages"`
		} `json:"task_pagination"`
	}
	if err = json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Queue.Tasks) != 20 || page.Pagination.Total != 365 || page.Pagination.Pages != 19 || page.Stats["running"] != 1 || page.Stats["completed"] != 1 || strings.Contains(first.Body.String(), "FULL_EVIDENCE_TAIL") {
		t.Fatalf("bad preview: %+v", page)
	}
	last := get("/api/batch-tasks/" + queue.ID + "?task_page=999&task_page_size=20")
	json.Unmarshal(last.Body.Bytes(), &page)
	if len(page.Queue.Tasks) != 5 || page.Pagination.Page != 19 || page.Pagination.Offset != 360 {
		t.Fatalf("bad last page: %+v", page)
	}
	detail := get("/api/batch-tasks/" + queue.ID + "/tasks/" + queue.Tasks[0].ID)
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), "FULL_EVIDENCE_TAIL") {
		t.Fatal("full evidence lost", detail.Body.String())
	}
	legacy := get("/api/batch-tasks/" + queue.ID)
	json.Unmarshal(legacy.Body.Bytes(), &page)
	if len(page.Queue.Tasks) != 365 || page.Queue.Tasks[0].Result != full {
		t.Fatal("preview mutated live queue or legacy API")
	}
	other, err := h.db.CreateRBACUser("page-other", "Other", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := h.batchTaskManager.CreateBatchQueue("foreign", "", "single", "manual", "", "", nil, 1, []string{"private"})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.db.SetResourceOwner("batch_task", foreign.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	denied := get("/api/batch-tasks/" + foreign.ID + "/tasks/" + foreign.Tasks[0].ID)
	if denied.Code != 403 {
		t.Fatal("foreign evidence was accessible", denied.Code)
	}
	missing := get("/api/batch-tasks/" + queue.ID + "/tasks/not-in-this-queue")
	if missing.Code != 404 {
		t.Fatal(missing.Code)
	}
}

func TestBatchQueueSnapshotWhileWorkersUpdate(t *testing.T) {
	manager := NewBatchTaskManager(zap.NewNop())
	queue, err := manager.CreateBatchQueue("snapshot", "", "single", "manual", "", "", nil, 1, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	id := queue.Tasks[0].ID
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		for i := 0; i < 100; i++ {
			manager.UpdateTaskStatus(queue.ID, id, "running", "", "")
			manager.UpdateTaskStatus(queue.ID, id, "completed", "evidence", "")
		}
	}()
	go func() {
		defer group.Done()
		for i := 0; i < 100; i++ {
			snapshot, ok := manager.SnapshotBatchQueue(queue.ID)
			if !ok {
				t.Error("snapshot missing")
				return
			}
			json.Marshal(snapshot)
		}
	}()
	group.Wait()
}
