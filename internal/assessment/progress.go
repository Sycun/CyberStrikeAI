package assessment

import (
	"github.com/google/uuid"
	"time"
)

type ProgressItem struct {
	StepID       string   `json:"step_id"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Summary      string   `json:"summary,omitempty"`
	ExecutionIDs []string `json:"execution_ids,omitempty"`
}

// ProgressReport is a factual snapshot, never a model's claimed percentage.
type ProgressReport struct {
	ID          string         `json:"id"`
	Trigger     string         `json:"trigger"`
	Revision    int            `json:"revision"`
	Status      string         `json:"status"`
	Completed   []ProgressItem `json:"completed"`
	Remaining   []ProgressItem `json:"remaining"`
	Blockers    []string       `json:"blockers"`
	QuestionIDs []string       `json:"question_ids"`
	NextAction  string         `json:"next_action"`
	CreatedAt   time.Time      `json:"created_at"`
}

func progressSnapshot(t *Task, trigger string) *ProgressReport {
	if !contains([]string{"plan", "worker_result", "owner_question", "owner_answer", "intake_resolved", "settled", "recovery", "checkpoint"}, trigger) {
		return nil
	}
	r := &ProgressReport{ID: uuid.NewString(), Trigger: trigger, Revision: t.Revision, Status: t.Status, Completed: []ProgressItem{}, Remaining: []ProgressItem{}, Blockers: []string{}, QuestionIDs: []string{}, NextAction: "continue", CreatedAt: time.Now().UTC()}
	for _, s := range t.Steps {
		if s.Status == "skipped" {
			continue
		}
		item := ProgressItem{StepID: s.ID, Title: s.Title, Status: s.Status, Summary: clip(s.Result, 500), ExecutionIDs: append([]string(nil), s.SuccessfulIDs...)}
		if s.Status == "completed" {
			r.Completed = append(r.Completed, item)
		} else {
			r.Remaining = append(r.Remaining, item)
		}
		if s.Status == "blocked" || s.Status == "awaiting_approval" {
			r.Blockers = append(r.Blockers, s.Title+": "+clip(s.Result, 400))
		}
	}
	for _, q := range t.Questions {
		if q.Status == "pending" {
			r.QuestionIDs = append(r.QuestionIDs, q.ID)
			r.Blockers = append(r.Blockers, q.Question)
		}
	}
	switch {
	case t.Terminal() || t.Status == "partial":
		r.NextAction = "review_results"
	case t.Status == "awaiting_input" || taskQuestionPending(t):
		r.NextAction = "answer_owner_question"
	case t.Status == "awaiting_approval":
		r.NextAction = "review_tool_approval"
	case t.Status == "paused" || t.Status == "blocked":
		r.NextAction = "review_and_resume"
	case t.Intake != nil && t.Intake.State != "ready":
		r.NextAction = "understand_request"
	}
	if t.Reason != "" {
		r.Blockers = append(r.Blockers, t.Reason)
	}
	return r
}
