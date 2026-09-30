package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The HTTP contract of "update my own source in one click": status without a network
// round trip, an apply that returns a job handle instead of blocking a request open for
// the length of a compile, and refusals that stay distinguishable from accidents.

func newUpdateRouter(root string, restart func()) (*gin.Engine, *UpdateHandler) {
	gin.SetMode(gin.TestMode)
	h := NewUpdateHandler(root, nil, nil, restart)
	router := gin.New()
	router.GET("/api/system/update", h.GetStatus)
	router.GET("/api/system/update/job", h.Job)
	router.POST("/api/system/update/check", h.Check)
	router.POST("/api/system/update/apply", h.Apply)
	router.POST("/api/system/update/rollback", h.Rollback)
	return router, h
}

func doUpdate(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestUpdateStatusIsServedWithoutTouchingTheNetwork(t *testing.T) {
	router, _ := newUpdateRouter(t.TempDir(), nil)

	w := doUpdate(router, http.MethodGet, "/api/system/update", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	var body struct {
		Status struct {
			Root       string `json:"root"`
			Installed  bool   `json:"installed"`
			CheckError string `json:"checkError"`
			CanBuild   bool   `json:"canBuild"`
		} `json:"status"`
		Job        *struct{ ID string } `json:"job"`
		CanRestart bool                 `json:"canRestart"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status.Root == "" || body.Status.Installed {
		t.Errorf("a plain directory must report its root and that it is not a git tree: %+v", body.Status)
	}
	if body.Status.CheckError == "" {
		t.Error("the reason an update cannot run must be readable on the page, not just a status code")
	}
	if body.Job != nil {
		t.Error("job must be null before anything has been applied")
	}
	if body.CanRestart {
		t.Error("canRestart must be false when no restart hook was wired")
	}
}

func TestUpdateApplyReturnsAJobAndReportsAnHonestFailure(t *testing.T) {
	router, _ := newUpdateRouter(t.TempDir(), nil)

	w := doUpdate(router, http.MethodPost, "/api/system/update/apply", `{"restart":false}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("apply should be accepted and return a handle, got %d %s", w.Code, w.Body)
	}
	var started struct {
		JobID string `json:"job_id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.JobID == "" {
		t.Fatal("apply must return the job id the page polls")
	}

	// The job runs against a non-git directory, so it finishes with a refusal rather
	// than a merge. What matters is that the reported failure is the real reason.
	deadline := time.Now().Add(10 * time.Second)
	var finished struct {
		Job struct {
			State   string `json:"state"`
			Failure struct {
				Reason string `json:"reason"`
			} `json:"failure"`
		} `json:"job"`
	}
	for time.Now().Before(deadline) {
		w = doUpdate(router, http.MethodGet, "/api/system/update/job", "")
		if err := json.Unmarshal(w.Body.Bytes(), &finished); err != nil {
			t.Fatal(err)
		}
		if finished.Job.State != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finished.Job.State != "failed" {
		t.Fatalf("job state = %q, want failed for a non-git install", finished.Job.State)
	}
	if finished.Job.Failure.Reason != "not_a_repo" {
		t.Errorf("failure reason = %q, want not_a_repo", finished.Job.Failure.Reason)
	}
}

func TestUpdateApplyRefusesRestartWhenNothingCanStartTheProcessAgain(t *testing.T) {
	router, _ := newUpdateRouter(t.TempDir(), nil)

	w := doUpdate(router, http.MethodPost, "/api/system/update/apply", `{"restart":true}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: restarting without a supervisor would just stop the platform", w.Code)
	}
	if !strings.Contains(w.Body.String(), "重启") {
		t.Errorf("the refusal must say why: %s", w.Body)
	}
}

func TestUpdateApplyRejectsMalformedBody(t *testing.T) {
	router, _ := newUpdateRouter(t.TempDir(), nil)

	w := doUpdate(router, http.MethodPost, "/api/system/update/apply", `{"restart": "yes please"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400 for a body that is not valid JSON", w.Code, w.Body)
	}
	// A rejected request must not have left a job behind: the page would then poll a
	// phantom update.
	if job := doUpdate(router, http.MethodGet, "/api/system/update/job", ""); !strings.Contains(job.Body.String(), `"job":null`) {
		t.Errorf("a 400 apply must not create a job, got %s", job.Body)
	}
}

func TestUpdateRollbackWithoutARecordedUpdateIsAConflict(t *testing.T) {
	root := t.TempDir()
	router, _ := newUpdateRouter(root, nil)

	w := doUpdate(router, http.MethodPost, "/api/system/update/rollback", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s, want 409", w.Code, w.Body)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != "no_state" {
		t.Errorf("reason = %v, want no_state so the page can say there is nothing to undo", body["reason"])
	}
}
