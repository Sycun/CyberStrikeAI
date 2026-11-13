package deepassessment

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/database"
	"go.uber.org/zap"
)

type fakeRuntime struct {
	plan   func(context.Context, *Task) (Proposal, error)
	run    func(context.Context, *Task, Step, func(string, string) error) (Outcome, error)
	review func(context.Context, *Task) (Verdict, error)
}

func (f fakeRuntime) Plan(c context.Context, t *Task) (Proposal, error) { return f.plan(c, t) }
func (f fakeRuntime) Run(c context.Context, t *Task, s Step, p func(string, string) error) (Outcome, error) {
	return f.run(c, t, s, p)
}
func (f fakeRuntime) Review(c context.Context, t *Task) (Verdict, error) {
	if f.review != nil {
		return f.review(c, t)
	}
	ids := []string{}
	for _, s := range t.Steps {
		if s.Status == "completed" {
			ids = append(ids, s.ID)
		}
	}
	return Verdict{Complete: true, Summary: "verified", Evidence: ids}, nil
}
func fixture(t *testing.T) (*Store, *Task) {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "tasks.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := db.CreateRBACUser("owner", "Owner", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := &Task{ID: "task-one", OwnerID: user.ID, ExecutorID: user.ID, Name: "fixture", Objective: "Complete twelve dependent stages", Scope: "local fixtures", Criteria: "All results independently checked", Concurrency: 2, MaxRounds: 40, RemainingSeconds: 120, Status: "draft", CreatedAt: now, UpdatedAt: now, Steps: []Step{}, Guidance: []string{}}
	store := &Store{DB: db}
	if err = store.Create(task); err != nil {
		t.Fatal(err)
	}
	return store, task
}
func await(t *testing.T, s *Store, id string, predicate func(*Task) bool) *Task {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		task, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(task) {
			return task
		}
		time.Sleep(5 * time.Millisecond)
	}
	task, _ := s.Get(id)
	t.Fatalf("timed out: %+v", task)
	return nil
}
func successful(_ context.Context, _ *Task, s Step, checkpoint func(string, string) error) (Outcome, error) {
	id := "conversation-" + s.ID
	if err := checkpoint(id, "observed "+s.ID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Status: "completed", Summary: "verified " + s.ID, ConversationID: id}, nil
}
func start(t *testing.T, e *Engine, task *Task) {
	t.Helper()
	if err := e.Start(task.ID, task.OwnerID, task.Revision); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
}

func TestTwelveStageObjectivePersistsAndCompletes(t *testing.T) {
	s, task := fixture(t)
	var mu sync.Mutex
	runs := map[string]int{}
	runtime := fakeRuntime{plan: func(_ context.Context, current *Task) (Proposal, error) {
		if len(current.Steps) == 12 {
			return Proposal{Strategy: "verify results"}, nil
		}
		if n := len(current.Steps); n > 0 && current.Steps[n-1].Status != "completed" {
			return Proposal{}, nil
		}
		n := len(current.Steps) + 1
		step := Step{ID: fmt.Sprintf("s%d", n), Title: fmt.Sprint(n), Instruction: fmt.Sprintf("stage %d", n)}
		if n > 1 {
			step.DependsOn = []string{fmt.Sprintf("s%d", n-1)}
		}
		return Proposal{Strategy: "persistent chain", Steps: []Step{step}}, nil
	}, run: func(c context.Context, t *Task, step Step, p func(string, string) error) (Outcome, error) {
		mu.Lock()
		runs[step.ID]++
		mu.Unlock()
		for _, id := range step.DependsOn {
			if t.Step(id).Status != "completed" {
				return Outcome{}, errors.New("dependency ran early")
			}
		}
		return successful(c, t, step, p)
	}}
	e := NewEngine(s, runtime)
	start(t, e, task)
	done := await(t, s, task.ID, func(t *Task) bool { return t.Status == "completed" && t.RunStartedAt == nil })
	if len(done.Steps) != 12 {
		t.Fatal(done.Steps)
	}
	mu.Lock()
	defer mu.Unlock()
	for id, n := range runs {
		if n != 1 {
			t.Fatalf("%s ran %d times", id, n)
		}
	}
	events, err := s.Events(task.ID, 0)
	if err != nil || len(events) < 24 {
		t.Fatalf("missing durable events: %d %v", len(events), err)
	}
}

func TestReadyDependencyRunsWhilePlannerAndOtherWorkerAreSlow(t *testing.T) {
	s, task := fixture(t)
	slow := make(chan struct{})
	planner := make(chan struct{})
	dependent := make(chan struct{}, 1)
	runtime := fakeRuntime{plan: func(ctx context.Context, t *Task) (Proposal, error) {
		if len(t.Steps) == 0 {
			return Proposal{Steps: []Step{{ID: "a", Title: "a", Instruction: "fast"}, {ID: "b", Title: "b", Instruction: "slow"}, {ID: "c", Title: "c", Instruction: "dependent", DependsOn: []string{"a"}}}}, nil
		}
		select {
		case <-planner:
			return Proposal{}, nil
		case <-ctx.Done():
			return Proposal{}, ctx.Err()
		}
	}, run: func(ctx context.Context, t *Task, s Step, p func(string, string) error) (Outcome, error) {
		if s.ID == "b" {
			select {
			case <-slow:
			case <-ctx.Done():
				return Outcome{}, ctx.Err()
			}
		}
		if s.ID == "c" {
			dependent <- struct{}{}
		}
		return successful(ctx, t, s, p)
	}}
	e := NewEngine(s, runtime)
	start(t, e, task)
	select {
	case <-dependent:
	case <-time.After(3 * time.Second):
		t.Fatal("ready worker was blocked by planner or sibling")
	}
	close(planner)
	close(slow)
	await(t, s, task.ID, func(t *Task) bool { return t.Status == "completed" })
}

func TestPauseDrainsAndNeverClaimsInterruptedWorkCompleted(t *testing.T) {
	s, task := fixture(t)
	started := make(chan struct{})
	exited := make(chan struct{})
	runtime := fakeRuntime{plan: func(context.Context, *Task) (Proposal, error) {
		return Proposal{Steps: []Step{{ID: "a", Title: "a", Instruction: "interruptible"}, {ID: "b", Title: "b", Instruction: "dependent", DependsOn: []string{"a"}}}}, nil
	}, run: func(ctx context.Context, t *Task, step Step, p func(string, string) error) (Outcome, error) {
		close(started)
		p("conv", "partial evidence")
		<-ctx.Done()
		close(exited)
		return Outcome{Status: "completed", Summary: "late completion", ConversationID: "conv"}, nil
	}}
	e := NewEngine(s, runtime)
	start(t, e, task)
	<-started
	for {
		current, _ := s.Get(task.ID)
		err := e.Stop(task.ID, current.Revision, false)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	done := await(t, s, task.ID, func(t *Task) bool { return t.Status == "paused" && t.RunStartedAt == nil })
	<-exited
	if done.Step("a").Status != "pending" || done.Step("b").Status != "pending" {
		t.Fatal(done.Steps)
	}
	if err := e.Start(task.ID, task.OwnerID, task.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale start: %v", err)
	}
}

func TestRecoveryRetainsCompletedAndInterruptedAttempts(t *testing.T) {
	s, task := fixture(t)
	past := time.Now().Add(-10 * time.Second)
	if err := s.Change(task.ID, "", "", "", func(t *Task) error {
		t.Status = "running"
		t.RunStartedAt = &past
		t.Steps = []Step{{ID: "a", Status: "completed", Summary: "evidence", ConversationID: "a"}, {ID: "b", Status: "running", Summary: "checkpoint", ConversationID: "b"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A new store instance models a new service process; nothing is held in RAM.
	reloaded := &Store{DB: s.DB}
	if err := reloaded.Recover(); err != nil {
		t.Fatal(err)
	}
	got, _ := reloaded.Get(task.ID)
	if got.Status != "paused" || got.Step("a").Status != "completed" || got.Step("b").Status != "pending" || got.Step("b").Summary != "checkpoint" || got.RemainingSeconds > 110 {
		t.Fatalf("bad recovery: %+v", got)
	}
}

func TestFailedBranchIsReplacedAndReviewGapDrivesMoreWork(t *testing.T) {
	s, task := fixture(t)
	reviews := 0
	runtime := fakeRuntime{plan: func(_ context.Context, t *Task) (Proposal, error) {
		switch len(t.Steps) {
		case 0:
			return Proposal{Steps: []Step{{ID: "a", Title: "a", Instruction: "initial route"}}}, nil
		case 1:
			if t.Step("a").Status == "failed" {
				return Proposal{Retire: []string{"a"}, Steps: []Step{{ID: "b", Title: "b", Instruction: "alternative route"}}}, nil
			}
		case 2:
			if t.Summary == "验收缺口: verify final artifact" {
				return Proposal{Steps: []Step{{ID: "c", Title: "c", Instruction: "check artifact", DependsOn: []string{"b"}}}}, nil
			}
		}
		return Proposal{}, nil
	}, run: func(c context.Context, t *Task, s Step, p func(string, string) error) (Outcome, error) {
		if s.ID == "a" {
			return Outcome{}, errors.New("route failed")
		}
		return successful(c, t, s, p)
	}, review: func(_ context.Context, t *Task) (Verdict, error) {
		reviews++
		if reviews == 1 {
			return Verdict{Missing: "verify final artifact"}, nil
		}
		return Verdict{Complete: true, Summary: "all verified", Evidence: []string{"b", "c"}}, nil
	}}
	e := NewEngine(s, runtime)
	start(t, e, task)
	done := await(t, s, task.ID, func(t *Task) bool { return t.Status == "completed" })
	if done.Step("a").Status != "retired" || done.Step("a").Summary != "route failed" || reviews != 2 {
		t.Fatal(done)
	}
}

func TestEmptyPlanQuestionAndRoundBudgetAreNotSuccess(t *testing.T) {
	for _, scenario := range []string{"empty", "question", "budget"} {
		t.Run(scenario, func(t *testing.T) {
			s, task := fixture(t)
			if scenario == "budget" {
				task.MaxRounds = 1
				s.Change(task.ID, "", "", "", func(t *Task) error { t.MaxRounds = 1; return nil })
				task, _ = s.Get(task.ID)
			}
			runtime := fakeRuntime{plan: func(context.Context, *Task) (Proposal, error) {
				if scenario == "question" {
					return Proposal{Question: "Which local input file?"}, nil
				}
				if scenario == "budget" {
					return Proposal{Steps: []Step{{ID: "a", Title: "a", Instruction: "one step"}}}, nil
				}
				return Proposal{}, nil
			}, run: successful, review: func(context.Context, *Task) (Verdict, error) { return Verdict{Missing: "more evidence needed"}, nil }}
			e := NewEngine(s, runtime)
			start(t, e, task)
			expected := map[string]string{"empty": "blocked", "question": "waiting_input", "budget": "budget_exhausted"}[scenario]
			await(t, s, task.ID, func(t *Task) bool { return t.Status == expected })
		})
	}
}

func TestProposalValidationAndAcceptance(t *testing.T) {
	base := Task{Steps: []Step{{ID: "done", Status: "completed", Instruction: "already did", Summary: "result", ConversationID: "conv"}, {ID: "active", Status: "running", Instruction: "running"}}}
	for name, p := range map[string]Proposal{"duplicate": {Steps: []Step{{ID: "new", Title: "renamed", Instruction: "already DID"}}}, "cycle": {Steps: []Step{{ID: "a", Title: "a", Instruction: "a", DependsOn: []string{"b"}}, {ID: "b", Title: "b", Instruction: "b", DependsOn: []string{"a"}}}}, "missing": {Steps: []Step{{ID: "a", Title: "a", Instruction: "a", DependsOn: []string{"missing"}}}}, "retire-running": {Retire: []string{"active"}}, "retire-done": {Retire: []string{"done"}}} {
		t.Run(name, func(t *testing.T) {
			copy := base
			if err := ApplyProposal(&copy, p); err == nil {
				t.Fatal("invalid proposal accepted")
			}
		})
	}
	if AcceptVerdict(&base, Verdict{Complete: true, Summary: "done", Evidence: []string{"done"}}) {
		t.Fatal("accepted with live worker")
	}
	base.Steps = base.Steps[:1]
	if AcceptVerdict(&base, Verdict{Complete: true, Summary: "done", Evidence: []string{"invented"}}) {
		t.Fatal("invented evidence accepted")
	}
	if AcceptVerdict(&base, Verdict{Complete: true, Summary: "done", Missing: "unchecked", Evidence: []string{"done"}}) {
		t.Fatal("missing checks accepted")
	}
}

func TestTimeBudgetStopsWithoutSuccess(t *testing.T) {
	s, task := fixture(t)
	s.Change(task.ID, "", "", "", func(t *Task) error { t.RemainingSeconds = 1; return nil })
	task, _ = s.Get(task.ID)
	runtime := fakeRuntime{plan: func(context.Context, *Task) (Proposal, error) {
		return Proposal{Steps: []Step{{ID: "s", Title: "wait", Instruction: "bounded wait"}}}, nil
	}, run: func(ctx context.Context, _ *Task, _ Step, _ func(string, string) error) (Outcome, error) {
		<-ctx.Done()
		return Outcome{}, ctx.Err()
	}}
	e := NewEngine(s, runtime)
	start(t, e, task)
	done := await(t, s, task.ID, func(t *Task) bool { return t.Status == "budget_exhausted" && t.RunStartedAt == nil })
	if done.Step("s").Status != "pending" {
		t.Fatal(done.Steps)
	}
}
func TestStartDoesNotExecuteWhenEventCommitFails(t *testing.T) {
	s, task := fixture(t)
	if _, err := s.DB.Exec(`CREATE TRIGGER fail_task_event BEFORE INSERT ON deep_assessment_events BEGIN SELECT RAISE(FAIL,'disk error fixture'); END`); err != nil {
		t.Fatal(err)
	}
	called := false
	e := NewEngine(s, fakeRuntime{plan: func(context.Context, *Task) (Proposal, error) { called = true; return Proposal{}, nil }})
	if err := e.Start(task.ID, task.OwnerID, 0); err == nil {
		t.Fatal("start accepted without durable state")
	}
	e.Close()
	got, _ := s.Get(task.ID)
	if called || got.Status != "draft" || got.Revision != 0 {
		t.Fatal("uncommitted work executed")
	}
}
