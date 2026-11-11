package assessment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberstrike-ai/internal/agent"
)

type delegationRuntime struct{ *fakeRuntime }

func (*delegationRuntime) RequiresApproval(*Task, string) bool { return false }
func testQuestion() *QuestionProposal {
	return &QuestionProposal{Kind: "clarification", Question: "只分析提供的本地材料吗？", Reason: "目标与范围尚不清楚", Options: []string{"仅本地材料", "补充其他范围"}}
}
func readyIntake() intakeOutput {
	return intakeOutput{Intent: "work", Name: "Review fixture", Goal: "Review local fixture and report observations", Scope: "Only the local fixture; no network access", Criteria: []string{"Report observations from the fixture"}, Deliverables: []string{"Evidence report"}}
}
func pendingQuestion(t *testing.T, task *Task) OwnerQuestion {
	t.Helper()
	for _, q := range task.Questions {
		if q.Status == "pending" {
			return q
		}
	}
	t.Fatal("pending question missing")
	return OwnerQuestion{}
}
func waitIdle(t *testing.T, e *Engine, id string) {
	t.Helper()
	limit := time.Now().Add(5 * time.Second)
	for time.Now().Before(limit) {
		e.mu.Lock()
		active := e.active[id] != nil
		e.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not release worker while awaiting input")
}

func TestDelegationVagueIntakeIndependentWorkRestartAnswerAndCompletion(t *testing.T) {
	store, task := fixture(t)
	if err := store.Change(task.ID, "", "", "", func(t *Task) error {
		t.Intake = &IntakeDraft{Message: "帮我看看这份材料，做好后告诉我", State: "pending"}
		t.Goal = ""
		t.Scope = ""
		t.Criteria = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var serial atomic.Int32
	base := &fakeRuntime{}
	base.execute = func(_ context.Context, _ *Task, sid string, _ agent.ToolCall) (*agent.ToolExecutionResult, error) {
		return &agent.ToolExecutionResult{ExecutionID: fmt.Sprintf("exec-%s-%d", sid, serial.Add(1)), Result: "fixture observation"}, nil
	}
	base.complete = func(ctx context.Context, t *Task, msgs []agent.ChatMessage, tools []agent.Tool) (*ModelReply, error) {
		if requestPurpose(ctx).Phase == "intake" {
			if len(tools) != 0 {
				return nil, fmt.Errorf("intake received execution tools")
			}
			if len(t.Questions) == 0 {
				return reply(intakeOutput{Intent: "work", MissingFields: []string{"scope"}, Question: testQuestion()}), nil
			}
			return reply(readyIntake()), nil
		}
		if strings.Contains(msgs[0].Content, "You plan an authorized") {
			if len(t.Steps) == 0 {
				return reply(map[string]any{"status": "continue", "summary": "Two independent steps", "steps": []map[string]any{{"key": "a", "title": "Clarify after observation", "instruction": "branch-a"}, {"key": "b", "title": "Independent observation", "instruction": "branch-b"}}}), nil
			}
			return reply(map[string]any{"status": "completed", "summary": "All requested observations recorded"}), nil
		}
		key := "a"
		if strings.Contains(msgs[1].Content, "YOUR TASK: Independent observation") {
			key = "b"
		}
		var st *Step
		for _, s := range t.Steps {
			if s.Key == key {
				st = s
			}
		}
		if st == nil {
			return nil, fmt.Errorf("step missing")
		}
		toolCall := func(name, id string, args map[string]interface{}) *ModelReply {
			return &ModelReply{Message: agent.ChatMessage{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: id, Type: "function", Function: agent.FunctionCall{Name: name, Arguments: args}}}}, Tokens: 100}
		}
		if len(st.SuccessfulIDs) == 0 {
			return toolCall("inspect", key+"-first", map[string]interface{}{"fixture": key}), nil
		}
		if key == "a" {
			answered := false
			for _, q := range t.Questions {
				if q.StepID == st.ID && q.Status == "answered" {
					answered = true
				}
			}
			if !answered {
				return toolCall(ownerQuestionTool, "a-question", map[string]interface{}{"kind": "decision", "question": "报告需要按模块还是按严重程度组织？", "reason": "交付形式需要您决定", "options": []string{"按模块", "按严重程度"}}), nil
			}
			if len(st.SuccessfulIDs) == 1 {
				found := false
				for _, m := range msgs {
					if m.Role == "tool" && m.ToolCallID == "a-question" && strings.Contains(m.Content, "按模块") {
						found = true
					}
				}
				if !found {
					return nil, fmt.Errorf("answer missing from resumed tool exchange")
				}
				return toolCall("inspect", "a-second", map[string]interface{}{"fixture": "final"}), nil
			}
		}
		return reply(workerOutput{Status: "completed", Summary: "Recorded observation", Evidence: []Evidence{{Kind: "fact", Title: "Fixture evidence", ExecutionIDs: st.SuccessfulIDs}}}), nil
	}
	runtime := &delegationRuntime{base}
	e := NewEngine(store, runtime, nil)
	if err := e.Start(task.ID); err != nil {
		t.Fatal(err)
	}
	waiting := waitTask(t, store, task.ID, func(t *Task) bool { return t.Status == "awaiting_input" })
	waitIdle(t, e, task.ID)
	if base.calls.Load() != 0 || len(waiting.Steps) != 0 || waiting.Deadline != 0 {
		t.Fatal("vague intake executed work or retained deadline")
	}
	q := pendingQuestion(t, waiting)
	if _, err := e.AnswerAs(task.ID, q.ID, "owner", OwnerAnswer{Revision: q.Revision + 1, Answer: "only local"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if _, err := e.AnswerAs(task.ID, q.ID, "owner", OwnerAnswer{Revision: q.Revision, Answer: "仅本地材料，报告可验证的观察结果"}); err != nil {
		t.Fatal(err)
	}
	waiting = waitTask(t, store, task.ID, func(t *Task) bool {
		return t.Status == "awaiting_input" && len(t.Steps) == 2 && t.Steps[1].Status == "completed"
	})
	waitIdle(t, e, task.ID)
	if base.calls.Load() != 2 {
		t.Fatalf("independent work should complete while asking: %d", base.calls.Load())
	}
	if _, err := e.AnswerAs(task.ID, q.ID, "owner", OwnerAnswer{Revision: q.Revision, Answer: "仅本地材料，报告可验证的观察结果"}); err != nil {
		t.Fatalf("duplicate intake answer after revision change: %v", err)
	}
	q = pendingQuestion(t, waiting)
	e.Close()
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	e = NewEngine(store, runtime, nil)
	defer e.Close()
	if _, err := e.AnswerAs(task.ID, q.ID, "owner", OwnerAnswer{Revision: q.Revision, Answer: "按模块"}); err != nil {
		t.Fatal(err)
	}
	done := waitTask(t, store, task.ID, func(t *Task) bool { return t.Status == "completed" })
	waitIdle(t, e, task.ID)
	if base.calls.Load() != 3 {
		t.Fatalf("old operation replayed or continuation missing: %d", base.calls.Load())
	}
	if len(done.Reports) == 0 || len(done.Reports[len(done.Reports)-1].Completed) != 2 {
		t.Fatal("factual milestone report missing")
	}
	m := Metrics(done)
	if m.OwnerAnswers != 2 || m.PendingQuestions != 0 || m.ToolCalls != 3 {
		t.Fatalf("delegation metrics: %+v", m)
	}
	before := done.ModelCalls
	if _, err := e.AnswerAs(task.ID, q.ID, "owner", OwnerAnswer{Revision: q.Revision, Answer: "按模块"}); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(task.ID)
	if after.ModelCalls != before {
		t.Fatal("duplicate answer restarted completed work")
	}
	if _, err := e.AnswerAs(task.ID, q.ID, "owner", OwnerAnswer{Revision: q.Revision, Answer: "different"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting answer accepted: %v", err)
	}
	if !strings.Contains(Report(done), "Owner question") {
		t.Fatal("export lost questions")
	}
}

func TestDelegationInformationalRequestNeverExecutes(t *testing.T) {
	for _, intent := range []string{"question", "status"} {
		t.Run(intent, func(t *testing.T) {
			store, task := fixture(t)
			_ = store.Change(task.ID, "", "", "", func(t *Task) error {
				t.Intake = &IntakeDraft{Message: "这个功能是什么意思？", State: "pending"}
				return nil
			})
			r := &fakeRuntime{complete: func(_ context.Context, _ *Task, _ []agent.ChatMessage, tools []agent.Tool) (*ModelReply, error) {
				if len(tools) != 0 {
					t.Fatal("tools provided to intake")
				}
				return reply(intakeOutput{Intent: intent, Reply: "功能说明；未执行任何任务。"}), nil
			}}
			e := NewEngine(store, r, nil)
			defer e.Close()
			if err := e.Start(task.ID); err != nil {
				t.Fatal(err)
			}
			out := waitTask(t, store, task.ID, func(t *Task) bool { return t.Status == "answered" })
			if r.calls.Load() != 0 || len(out.Steps) != 0 {
				t.Fatal("informational request became execution")
			}
		})
	}
}
func TestDelegationMissingRequiredFieldsOverridesModelReadiness(t *testing.T) {
	store, task := fixture(t)
	_ = store.Change(task.ID, "", "", "", func(t *Task) error { t.Intake = &IntakeDraft{Message: "帮我查一下", State: "pending"}; return nil })
	r := &fakeRuntime{complete: func(context.Context, *Task, []agent.ChatMessage, []agent.Tool) (*ModelReply, error) {
		return reply(intakeOutput{Intent: "work", Goal: "Investigate", Criteria: []string{"report"}}), nil
	}}
	e := NewEngine(store, r, nil)
	defer e.Close()
	if err := e.Start(task.ID); err != nil {
		t.Fatal(err)
	}
	out := waitTask(t, store, task.ID, func(t *Task) bool { return t.Status == "awaiting_input" })
	if pendingQuestion(t, out).Source != "intake" || r.calls.Load() != 0 {
		t.Fatal("missing scope bypassed intake")
	}
}
func TestDelegationPlannerQuestionSuspendsUnstartedWork(t *testing.T) {
	store, task := fixture(t)
	_ = store.Change(task.ID, "", "", "", func(t *Task) error {
		t.Status = "running"
		t.Deadline = time.Now().Unix() + 90
		t.Steps = []*Step{{ID: "s", Key: "s", Title: "pending", Status: "pending"}}
		return nil
	})
	e := NewEngine(store, &fakeRuntime{}, nil)
	defer e.Close()
	call := agent.ToolCall{ID: "q", Function: agent.FunctionCall{Name: ownerQuestionTool, Arguments: map[string]interface{}{"question": "Which deliverable?", "reason": "Required decision"}}}
	if err := e.askOwner(task.ID, "", call); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.run(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	out, _ := store.Get(task.ID)
	if out.Status != "awaiting_input" || out.Steps[0].Status != "pending" {
		t.Fatalf("planner wait lost: %+v", out)
	}
}

func TestDelegationQueuedAnswerResumesAfterRestart(t *testing.T) {
	store, task := fixture(t)
	if err := store.Change(task.ID, "", "", "", func(t *Task) error {
		t.Status = "paused"
		t.Intake = &IntakeDraft{Message: "解释这个功能", State: "pending"}
		t.ResumeRequested = true
		t.ExecutorID = "answering-owner"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := &fakeRuntime{complete: func(_ context.Context, t *Task, _ []agent.ChatMessage, _ []agent.Tool) (*ModelReply, error) {
		if t.ExecutorID != "answering-owner" {
			return nil, fmt.Errorf("resume changed actor")
		}
		return reply(intakeOutput{Intent: "question", Reply: "An explanation."}), nil
	}}
	e := NewEngine(store, r, nil)
	defer e.Close()
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	if err := e.ResumeAnsweredTasks(); err != nil {
		t.Fatal(err)
	}
	done := waitTask(t, store, task.ID, func(t *Task) bool { return t.Status == "answered" })
	if done.ResumeRequested || r.calls.Load() != 0 {
		t.Fatal("queued answer not consumed safely")
	}
}
func TestOwnerQuestionCannotAuthorizeTools(t *testing.T) {
	p := QuestionProposal{Kind: "approval", Question: "Execute?", Reason: "approve tool"}
	if p.validate() == nil {
		t.Fatal("owner question bypasses HITL")
	}
}
func TestIntakePreservesExplicitScopeAndCriteria(t *testing.T) {
	store, task := fixture(t)
	if err := store.Change(task.ID, "", "", "", func(t *Task) error {
		t.Intake = &IntakeDraft{Message: "看看材料", State: "pending", ProvidedScope: "Only fixture A; exclude B", ProvidedCriteria: []string{"Report existing evidence only"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := &fakeRuntime{complete: func(_ context.Context, _ *Task, m []agent.ChatMessage, _ []agent.Tool) (*ModelReply, error) {
		if !strings.Contains(m[1].Content, "exclude B") {
			return nil, fmt.Errorf("scope omitted from model input")
		}
		out := readyIntake()
		out.Scope = "A and B"
		out.Criteria = []string{"Run new tests"}
		return reply(out), nil
	}}
	e := NewEngine(store, r, nil)
	defer e.Close()
	_ = store.Change(task.ID, "", "", "", func(t *Task) error { t.Status = "running"; return nil })
	if err := e.understand(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	out, _ := store.Get(task.ID)
	if out.Scope != "Only fixture A; exclude B" || out.Criteria[0] != "Report existing evidence only" {
		t.Fatal("model expanded explicit owner scope/criteria")
	}
}
