package assessment

import (
	"fmt"
	"strings"
)

// Report is generated from persisted evidence, even for stopped/partial runs.
func Report(t *Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n- Assessment: `%s`\n- Status: %s\n- Reason: %s\n- Model channel: %s\n- Tokens charged: %d / %d (outstanding reservations: %d)\n- Planning rounds: %d / %d\n\n## Goal\n\n%s\n\n## Authorized scope\n\n%s\n\n## Summary and limitations\n\n%s\n\nFindings are candidates for human review. Completion describes this assessment's recorded outcome; it does not certify that the target has no vulnerabilities.\n", t.Name, t.ID, t.Status, t.Reason, t.Model, t.Tokens, t.TokenBudget, t.Reserved, t.Rounds, t.MaxRounds, t.Goal, t.Scope, t.Summary)
	if t.Intake != nil {
		fmt.Fprintf(&b, "\n## Original request\n\n%s\n\nIntent: %s\n", t.Intake.Message, t.Intake.Intent)
	}
	for _, q := range t.Questions {
		fmt.Fprintf(&b, "\n## Owner question [%s]\n\n%s\n\nReason: %s\n\nAnswer: %s\n", q.Status, q.Question, q.Reason, q.Answer)
	}
	for _, r := range t.Reports {
		fmt.Fprintf(&b, "\n## Stage report (%s)\n\nCompleted: %d; remaining: %d; next: %s\n", r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), len(r.Completed), len(r.Remaining), r.NextAction)
		for _, item := range r.Completed {
			fmt.Fprintf(&b, "\n- Done: %s (executions: %s)\n", item.Title, strings.Join(item.ExecutionIDs, ", "))
		}
		for _, item := range r.Remaining {
			fmt.Fprintf(&b, "\n- Remaining: %s [%s]\n", item.Title, item.Status)
		}
		for _, blocker := range r.Blockers {
			fmt.Fprintf(&b, "\n- Blocker: %s\n", blocker)
		}
	}
	fmt.Fprintf(&b, "\n## Acceptance criteria\n")
	for i, c := range criteria(t) {
		fmt.Fprintf(&b, "\n%d. %s\n", i+1, c)
	}
	if t.Review != nil {
		for _, v := range t.Review.Criteria {
			fmt.Fprintf(&b, "\n- Criterion %d: **%s** — %s (executions: %s)\n", v.Index+1, v.Status, v.Reason, strings.Join(v.ExecutionIDs, ", "))
		}
		for _, v := range t.Review.Findings {
			fmt.Fprintf(&b, "\n- Finding %s/%d: **%s** — %s (verification: %s)\n", v.StepID, v.EvidenceIndex, v.Status, v.Reason, strings.Join(v.ExecutionIDs, ", "))
		}
	}
	fmt.Fprintf(&b, "\n## Evaluation metrics\n\n```json\n%s\n```\n", MetricsJSON(t))
	if t.Strategy != "" {
		fmt.Fprintf(&b, "\n## Strategy\n\n%s\n", t.Strategy)
	}
	for _, s := range t.Steps {
		fmt.Fprintf(&b, "\n## %s [%s]\n\n- Step: `%s`\n- Key: `%s`\n- Depends on: %s\n\n%s\n\n%s\n", s.Title, s.Status, s.ID, s.Key, strings.Join(s.DependsOn, ", "), s.Instruction, s.Result)
		if s.Checkpoint != "" {
			fmt.Fprintf(&b, "\nSaved progress: %s\n", s.Checkpoint)
		}
		if s.RetiredReason != "" {
			fmt.Fprintf(&b, "\nRetired direction (coverage gap retained): %s\n", s.RetiredReason)
		}
		for _, v := range s.Evidence {
			fmt.Fprintf(&b, "\n### %s: %s\n\n%s\n\nExecution evidence: %s\n", v.Kind, v.Title, v.Detail, strings.Join(v.ExecutionIDs, ", "))
		}
		if s.Status != "completed" {
			fmt.Fprintf(&b, "\nUnresolved coverage: this step was %s.\n", s.Status)
		}
	}
	return b.String()
}
