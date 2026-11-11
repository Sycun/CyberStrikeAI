package assessment

import "encoding/json"

type EvaluationMetrics struct {
	Intent            string            `json:"intent,omitempty"`
	OwnerQuestions    int               `json:"owner_questions"`
	OwnerAnswers      int               `json:"owner_answers"`
	PendingQuestions  int               `json:"pending_questions"`
	RetainedReports   int               `json:"retained_reports"`
	Name              string            `json:"name"`
	ModelChannel      string            `json:"model_channel"`
	ReviewerChannel   string            `json:"reviewer_channel"`
	WorkerChannels    map[string]string `json:"worker_channels"`
	TokenBudget       int               `json:"token_budget"`
	MaxRounds         int               `json:"max_rounds"`
	TaskID            string            `json:"task_id"`
	Status            string            `json:"status"`
	CriteriaTotal     int               `json:"criteria_total"`
	CriteriaSatisfied int               `json:"criteria_satisfied"`
	Steps             int               `json:"steps"`
	Completed         int               `json:"completed_steps"`
	Blocked           int               `json:"blocked_steps"`
	Retired           int               `json:"retired_steps"`
	VerificationSteps int               `json:"verification_steps"`
	Findings          int               `json:"findings"`
	Confirmed         int               `json:"confirmed_findings"`
	NeedsRetest       int               `json:"needs_retest"`
	ToolCalls         int               `json:"tool_calls"`
	UniqueActions     int               `json:"unique_actions"`
	Suppressed        int               `json:"suppressed_repeats"`
	ModelCalls        int               `json:"model_calls"`
	ModelRetries      int               `json:"model_retries"`
	Tokens            int               `json:"tokens"`
	Usage             map[string]int    `json:"tokens_by_phase"`
}

func Metrics(t *Task) EvaluationMetrics {
	m := EvaluationMetrics{Name: t.Name, ModelChannel: t.Model, ReviewerChannel: t.ReviewerModel, WorkerChannels: t.WorkerModels, TokenBudget: t.TokenBudget, MaxRounds: t.MaxRounds, TaskID: t.ID, Status: t.Status, CriteriaTotal: len(criteria(t)), Steps: len(t.Steps), ModelCalls: t.ModelCalls, ModelRetries: t.ModelRetries, Tokens: t.Tokens, Usage: t.Usage}
	if t.Intake != nil {
		m.Intent = t.Intake.Intent
	}
	m.OwnerQuestions = len(t.Questions)
	m.RetainedReports = len(t.Reports)
	for _, q := range t.Questions {
		if q.Status == "answered" {
			m.OwnerAnswers++
		}
		if q.Status == "pending" {
			m.PendingQuestions++
		}
	}
	actions := map[string]bool{}
	for _, s := range t.Steps {
		switch s.Status {
		case "completed":
			m.Completed++
		case "blocked":
			m.Blocked++
		case "skipped":
			m.Retired++
		}
		if s.VerifyOf != "" {
			m.VerificationSteps++
		}
		if s.ToolCalls > 0 {
			m.ToolCalls += s.ToolCalls
		} else {
			m.ToolCalls += len(s.ExecutionIDs)
		}
		m.Suppressed += s.Suppressed
		for _, a := range s.Attempts {
			actions[a.Action] = true
		}
		for i, v := range s.Evidence {
			if v.Kind == "finding" {
				m.Findings++
				status := findingReviewStatus(t, s.ID, i)
				if status == "confirmed" {
					m.Confirmed++
				} else if status == "needs_retest" {
					m.NeedsRetest++
				}
			}
		}
	}
	if t.Review != nil {
		for _, v := range t.Review.Criteria {
			if v.Status == "satisfied" {
				m.CriteriaSatisfied++
			}
		}
	}
	m.UniqueActions = len(actions)
	return m
}
func MetricsJSON(t *Task) []byte { b, _ := json.MarshalIndent(Metrics(t), "", "  "); return b }
