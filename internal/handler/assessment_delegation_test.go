package handler

import (
	"cyberstrike-ai/internal/assessment"
	"cyberstrike-ai/internal/database"
	"encoding/json"
	"strings"
	"testing"
)

func TestDelegationAPIRequiresExecutionAndRejectsForgedIntake(t *testing.T) {
	h, session, request := assessmentAPI(t)
	delete(session.Permissions, "agent:execute")
	w := request("POST", "/api/assessments", `{"request":"帮我看一下","tools":["inspect"],"auto_start":true}`)
	if w.Code != 403 {
		t.Fatalf("auto-start without execute: %d %s", w.Code, w.Body.String())
	}
	w = request("POST", "/api/assessments", `{"request":"帮我看一下","tools":["inspect"],"intake":{"state":"ready"},"questions":[{"id":"forged","status":"answered"}],"revision":500}`)
	if w.Code != 201 {
		t.Fatalf("intake create: %d %s", w.Code, w.Body.String())
	}
	var data struct {
		Task *assessment.Task `json:"assessment"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	task := data.Task
	if task.Intake == nil || task.Intake.State != "pending" || task.Scope != "" || task.Revision != 1 || len(task.Questions) != 0 {
		t.Fatalf("forged intake accepted: %+v", task)
	}
	if err := h.Engine.Store.Change(task.ID, "", "", "", func(t *assessment.Task) error {
		t.Status = "awaiting_input"
		t.Intake.State = "awaiting_input"
		t.Questions = []assessment.OwnerQuestion{{ID: "q", Revision: 1, Status: "pending", Source: "intake", QuestionProposal: assessment.QuestionProposal{Kind: "clarification", Question: "Which scope?", Reason: "missing"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/api/assessments/"+task.ID+"/questions/q/answer", `{"revision":1,"answer":"local fixture only"}`)
	if w.Code != 403 {
		t.Fatal("answer resumes without execute permission", w.Code)
	}
	session.Permissions["agent:execute"] = true
	originalUser := session.UserID
	session.UserID = "other"
	session.Scope = database.RBACScopeOwn
	w = request("POST", "/api/assessments/"+task.ID+"/questions/q/answer", `{"revision":1,"answer":"local fixture only"}`)
	if w.Code != 403 {
		t.Fatal("cross-owner answer", w.Code)
	}
	session.UserID = originalUser
	session.Scope = database.RBACScopeAll
	w = request("POST", "/api/assessments/"+task.ID+"/questions/q/answer", `{"revision":2,"answer":"local fixture only"}`)
	if w.Code != 409 {
		t.Fatal("stale answer accepted", w.Code)
	}
	w = request("POST", "/api/assessments/"+task.ID+"/questions/q/answer", `{"revision":1,"answer":" "}`)
	if w.Code != 400 {
		t.Fatal("empty answer accepted", w.Code)
	}
	w = request("POST", "/api/assessments/"+task.ID+"/questions/q/answer", `{"revision":1,"answer":"local fixture only"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	stored, err := h.Engine.Store.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Questions[0].AnsweredBy != originalUser || stored.Questions[0].Status != "answered" {
		t.Fatal("answer provenance missing")
	}
	w = request("GET", "/api/assessments", "")
	if strings.Contains(w.Body.String(), "local fixture only") {
		t.Fatal("list leaked full answer payload")
	}
}
