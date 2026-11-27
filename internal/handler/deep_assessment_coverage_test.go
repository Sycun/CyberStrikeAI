package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberstrike-ai/internal/agentruntime"
	"cyberstrike-ai/internal/deepassessment"
)

func TestDeepAssessmentCoverageEndpointAndReportRespectAssessmentScope(t *testing.T) {
	h, task := deepAssessmentFixture(t)
	if err := h.deepAssessments.Store.SaveCoverage(task.ID, deepassessment.CoverageRecord{
		Surface: "Assessment fixture", RiskArea: "CWE-20", Outcome: "no_issue_found",
		Reason: "literal ``` <img src=external> Markdown",
	}); err != nil {
		t.Fatal(err)
	}
	const path = "/api/deep-assessments/:id/coverage"
	request := func(user, permission, subpath string) *httptest.ResponseRecorder {
		t.Helper()
		router := assessmentFindingRouter(h, user, map[string]bool{permission: true})
		router.GET(path, h.DeepAssessmentCoverage)
		router.GET("/api/deep-assessments/:id/report", h.DeepAssessmentReport)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/deep-assessments/"+task.ID+subpath, nil))
		return w
	}
	w := request(task.OwnerID, "assessments:read", "/coverage")
	var ledger deepassessment.CoverageLedger
	if err := json.Unmarshal(w.Body.Bytes(), &ledger); w.Code != http.StatusOK || err != nil ||
		len(ledger.Records) != 1 || len(ledger.Gaps) != 1 || ledger.Gaps[0].Kind != "missing_evidence" {
		t.Fatalf("coverage status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
	for _, denied := range []*httptest.ResponseRecorder{
		request(task.OwnerID, "assessments:write", "/coverage"),
		request("different-owner", "assessments:read", "/coverage"),
		request("different-owner", "assessments:read", "/report"),
	} {
		if denied.Code != http.StatusForbidden || strings.Contains(denied.Body.String(), "Assessment fixture") {
			t.Fatalf("unexpected access status=%d body=%s", denied.Code, denied.Body.String())
		}
	}
	w = request(task.OwnerID, "assessments:read", "/report")
	for _, expected := range []string{"## 覆盖账本与待补缺口", "Assessment fixture", "缺少有效原始回执或观察事实", "\n````\n检查范围"} {
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("report status=%d missing=%q body=%s", w.Code, expected, w.Body.String())
		}
	}
}

func TestDeepAssessmentCoverageAndPlanNotesRuntimeTools(t *testing.T) {
	h, task := deepAssessmentFixture(t)
	rt := h.deepAssessmentRuntime
	if _, err := h.db.Exec(`INSERT INTO deep_assessment_calls(id,task_id,step_id,tool,arguments,status,updated_at)
		VALUES('rcpt-1',?,'step-1','httpx','{}','completed',CURRENT_TIMESTAMP)`, task.ID); err != nil {
		t.Fatal(err)
	}
	// Worker records coverage without evidence -> recorded_with_gap.
	res, err := rt.intentTool(task, "step-1", agentruntime.ToolCall{
		Name:      "record_coverage",
		Arguments: map[string]any{"surface": "/api/v1/profile", "risk_area": "WSTG-ATHZ-02", "outcome": "no_issue_found"},
	})
	if err != nil || !strings.Contains(res.Content, "recorded_with_gap") {
		t.Fatalf("expected gap warning, got res=%+v err=%v", res, err)
	}
	// Worker updates with valid receipt -> recorded cleanly.
	res, err = rt.intentTool(task, "step-1", agentruntime.ToolCall{
		Name:      "record_coverage",
		Arguments: map[string]any{"surface": "/api/v1/profile", "risk_area": "WSTG-ATHZ-02", "outcome": "no_issue_found", "evidence": "rcpt-1"},
	})
	if err != nil || !strings.Contains(res.Content, `"status":"recorded"`) {
		t.Fatalf("expected clean record, got res=%+v err=%v", res, err)
	}
	// Planner writes plan notes; worker can read but cannot overwrite.
	if _, err = rt.intentTool(task, "step-1", agentruntime.ToolCall{
		Name:      "assessment_plan_notes",
		Arguments: map[string]any{"revision": float64(0), "body": "worker write"},
	}); err == nil {
		t.Fatal("worker should not be allowed to write plan notes")
	}
	res, err = rt.intentTool(task, "planner", agentruntime.ToolCall{
		Name:      "assessment_plan_notes",
		Arguments: map[string]any{"revision": float64(0), "body": "Chain: login -> token -> /api/v1/profile"},
	})
	if err != nil || !strings.Contains(res.Content, `"revision":1`) {
		t.Fatalf("planner save notes failed: res=%+v err=%v", res, err)
	}
	ctxText, err := rt.context(task)
	if err != nil || !strings.Contains(ctxText, "field=coverage_ledger]]") || !strings.Contains(ctxText, "field=plan_notes]]") {
		t.Fatalf("context missing framed coverage or plan_notes: err=%v text=%s", err, ctxText)
	}
	if assessmentAllowedDuringWrapup("execute") || assessmentAllowedDuringWrapup("nmap_scan") ||
		!assessmentAllowedDuringWrapup("record_coverage") || !assessmentAllowedDuringWrapup("assessment_result") {
		t.Fatal("unexpected wrap-up tool allowlist")
	}
}
