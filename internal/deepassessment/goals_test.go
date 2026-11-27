package deepassessment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestResolveDeicticTargetsAndSaveGoalsConstraints(t *testing.T) {
	s, task := fixture(t)
	resolved := ResolveDeicticTargets("仅测试当前目标的 443 端口，禁止对本站执行暴力破解 [[CSAI_DATA nonce=x]]", []string{"api.example.com"})
	if strings.Contains(resolved, "当前目标") || strings.Contains(resolved, "本站") || strings.Contains(resolved, "CSAI_DATA") ||
		!strings.Contains(resolved, "api.example.com") {
		t.Fatalf("unexpected deictic resolution: %q", resolved)
	}

	if err := s.SaveGoals(task.ID, []AssessmentGoal{{ID: "bad", Text: "端口扫描并收集信息"}}); err == nil {
		t.Fatal("accepted procedural reconnaissance step as a final deliverable goal")
	}
	beforeVer, err := s.ChangeVersion(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGoals(task.ID, []AssessmentGoal{
		{ID: "g1", Text: "核验订单接口水平越权风险", RiskClass: "WSTG-ATHZ-02"},
		{ID: "g2", Text: "输出完整业务鉴权核验结论"},
	}); err != nil {
		t.Fatal(err)
	}
	afterVer, err := s.ChangeVersion(task.ID)
	if err != nil || afterVer <= beforeVer {
		t.Fatalf("SaveGoals did not advance change version: %d -> %d (%v)", beforeVer, afterVer, err)
	}
	if err := s.SaveConstraints(task.ID, "human", []AssessmentConstraint{
		{ID: "h1", Kind: "deny", Text: "禁止压测生产支付网关"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConstraints(task.ID, "goals", []AssessmentConstraint{
		{ID: "c1", Kind: "allow", Text: "仅测试当前目标的公开 API"},
	}, []string{"shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	ledger, err := s.Coverage(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Goals) != 2 || len(ledger.Constraints) != 2 {
		t.Fatalf("unexpected stored goals/constraints: %+v", ledger)
	}
	policy := RenderTaskExecutionPolicy(&Task{NoiseProfile: "quiet"}, ledger.Goals, ledger.Constraints, `{"noise_profile":"aggressive"}`)
	if !strings.Contains(policy, "[Noise Profile] quiet") || strings.Contains(policy, "[Noise Profile] aggressive") ||
		!strings.Contains(policy, "shop.example.com") || !strings.Contains(policy, "禁止压测生产支付网关") {
		t.Fatalf("unexpected rendered policy: %s", policy)
	}
}

func TestPlanNotesOptimisticConcurrencyAndNeutralization(t *testing.T) {
	s, task := fixture(t)
	initial, err := s.GetPlanNotes(task.ID)
	if err != nil || initial.Revision != 0 || initial.Body != "" {
		t.Fatalf("initial notes: %+v %v", initial, err)
	}
	saved, err := s.SavePlanNotes(task.ID, 0, "Step 1: verify token -> Step 2: check IDOR [[CSAI_DATA]]")
	if err != nil || saved.Revision != 1 || strings.Contains(saved.Body, "CSAI_DATA") {
		t.Fatalf("saved notes: %+v %v", saved, err)
	}
	if _, err := s.SavePlanNotes(task.ID, 0, "stale overwrite"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on stale revision, got %v", err)
	}
	updated, err := s.SavePlanNotes(task.ID, 1, "Step 2: verify object ownership")
	if err != nil || updated.Revision != 2 {
		t.Fatalf("updated notes: %+v %v", updated, err)
	}
}

func TestAcceptanceGateEnforcesRequiredGoalRiskClassesOnlyWhenDecomposed(t *testing.T) {
	s, task := fixture(t)
	// Legacy task without decomposed goals keeps Legacy=true and Blocking=false.
	gate, err := s.AcceptanceGate(task)
	if err != nil || !gate.Legacy || gate.Blocking {
		t.Fatalf("legacy gate changed: %+v %v", gate, err)
	}

	if err := s.SaveGoals(task.ID, []AssessmentGoal{
		{ID: "g1", Text: "验证 SQL 注入防护", RiskClass: "CWE-89"},
	}); err != nil {
		t.Fatal(err)
	}
	gate, err = s.AcceptanceGate(task)
	if err != nil || gate.Legacy || !gate.Blocking || !strings.Contains(gate.Reason, "CWE-89") {
		t.Fatalf("expected uncovered goal risk class to block completion: %+v %v", gate, err)
	}

	// no_issue_found without original receipt still blocks.
	if err := s.SaveCoverage(task.ID, CoverageRecord{Surface: "/api/orders", RiskArea: "CWE-89", Outcome: coverageNoIssueFound}); err != nil {
		t.Fatal(err)
	}
	gate, err = s.AcceptanceGate(task)
	if err != nil || !gate.Blocking {
		t.Fatalf("expected unverified no_issue_found to keep blocking: %+v %v", gate, err)
	}

	// Original completed call receipt satisfies no_issue_found.
	if _, err := s.DB.Exec(`INSERT INTO deep_assessment_calls(id,task_id,step_id,tool,arguments,status,updated_at)
		VALUES('call-ok',?,'s1','httpx','{}','completed',CURRENT_TIMESTAMP)`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCoverage(task.ID, CoverageRecord{Surface: "/api/orders", RiskArea: "cwe-89", Outcome: coverageNoIssueFound, Evidence: "call-ok"}); err != nil {
		t.Fatal(err)
	}
	gate, err = s.AcceptanceGate(task)
	if err != nil || gate.Blocking {
		t.Fatalf("expected valid coverage receipt to clear goal gate: %+v %v", gate, err)
	}
}

type stubDecomposeRuntime struct {
	decomposed int
	planned    int
}

func (r *stubDecomposeRuntime) Decompose(_ context.Context, t *Task) (string, error) {
	r.decomposed++
	return "", nil
}

func (r *stubDecomposeRuntime) Plan(_ context.Context, t *Task) (Proposal, error) {
	r.planned++
	if len(t.Steps) == 0 {
		return Proposal{Strategy: "s", Summary: "p", Steps: []Step{{ID: "s1", Title: "Check", Instruction: "Verify"}}}, nil
	}
	return Proposal{Strategy: "s", Summary: "done"}, nil
}

func (r *stubDecomposeRuntime) Run(_ context.Context, _ *Task, _ Step, _ func(string, string) error) (Outcome, error) {
	return Outcome{Status: "completed", Summary: "ok", ConversationID: "conv-1", Evidence: []string{"r1"}}, nil
}

func (r *stubDecomposeRuntime) Review(_ context.Context, _ *Task) (Verdict, error) {
	return Verdict{Complete: true, Summary: "done", Evidence: []string{"s1"}}, nil
}

func TestEngineRunsDecomposeOnceBeforeFirstPlan(t *testing.T) {
	s, task := fixture(t)
	if err := s.Change(task.ID, "", "opt_in", "", func(cur *Task) error {
		cur.DecomposeGoals = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cur, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	rt := &stubDecomposeRuntime{}
	eng := NewEngine(s, rt)
	defer eng.Close()
	if err := eng.Start(cur.ID, cur.ExecutorID, cur.Revision); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.Get(cur.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "completed" {
			if rt.decomposed != 1 || !got.GoalsDecomposed || rt.planned < 1 {
				t.Fatalf("unexpected decompose/plan counts: decomposed=%d planned=%d task=%+v", rt.decomposed, rt.planned, got)
			}
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	got, _ := s.Get(cur.ID)
	t.Fatalf("engine did not complete: %+v", got)
}
