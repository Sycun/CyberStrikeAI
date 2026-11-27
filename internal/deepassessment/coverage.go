package deepassessment

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const maxCoverageKeys = 2000

const (
	coverageReported      = "reported"
	coverageNoIssueFound  = "no_issue_found"
	coverageRuledOut      = "ruled_out"
	coverageNotApplicable = "not_applicable"
	coverageNeedsFollowUp = "needs_follow_up"
)

type CoverageRecord struct {
	Surface   string    `json:"surface"`
	RiskArea  string    `json:"risk_area"`
	Outcome   string    `json:"outcome"`
	Evidence  string    `json:"evidence,omitempty"` // Original receipt ID or observed fact ID.
	Reason    string    `json:"reason,omitempty"`
	StepID    string    `json:"step_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CoverageGap struct {
	Kind     string `json:"kind"`
	Surface  string `json:"surface,omitempty"`
	RiskArea string `json:"risk_area"`
	GoalID   string `json:"goal_id,omitempty"`
}

type AssessmentGoal struct {
	ID        string `json:"goal_id"`
	Text      string `json:"text"`
	RiskClass string `json:"risk_class,omitempty"`
}

type AssessmentConstraint struct {
	ID     string `json:"constraint_id"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Origin string `json:"origin"`
}

type CoverageLedger struct {
	Records     []CoverageRecord       `json:"records"`
	Gaps        []CoverageGap          `json:"gaps"`
	Counts      map[string]int         `json:"counts"`
	Goals       []AssessmentGoal       `json:"goals"`
	Constraints []AssessmentConstraint `json:"constraints"`
}

// SaveCoverage records an audit annotation and its history in a single transaction.
func (s *Store) SaveCoverage(task string, entry CoverageRecord) error {
	for _, field := range []struct {
		name  string
		value *string
		limit int
	}{
		{"surface", &entry.Surface, 240},
		{"risk_area", &entry.RiskArea, 128},
		{"evidence", &entry.Evidence, 240},
		{"reason", &entry.Reason, 1000},
		{"step_id", &entry.StepID, 240},
	} {
		*field.value = strings.Join(strings.Fields(*field.value), " ")
		if utf8.RuneCountInString(*field.value) > field.limit {
			return fmt.Errorf("%s exceeds %d characters", field.name, field.limit)
		}
	}
	if entry.Surface == "" || entry.RiskArea == "" {
		return fmt.Errorf("surface and risk_area are required")
	}
	switch entry.Outcome {
	case coverageReported, coverageNoIssueFound, coverageRuledOut, coverageNotApplicable, coverageNeedsFollowUp:
	default:
		return fmt.Errorf("invalid coverage outcome")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	entry.UpdatedAt = time.Now().UTC()
	args := []any{task, entry.Surface, entry.RiskArea, entry.Outcome, entry.Evidence, entry.Reason, entry.StepID, entry.UpdatedAt}
	result, err := tx.Exec(`INSERT INTO deep_assessment_coverage(task_id,surface,risk_area,outcome,evidence,reason,step_id,updated_at)
		SELECT ?,?,?,?,?,?,?,? WHERE EXISTS (
			SELECT 1 FROM deep_assessment_coverage WHERE task_id=? AND surface=? AND risk_area=?
		) OR (SELECT COUNT(*) FROM deep_assessment_coverage WHERE task_id=?) < ?
		ON CONFLICT(task_id,surface,risk_area) DO UPDATE SET outcome=excluded.outcome,
			evidence=excluded.evidence,reason=excluded.reason,step_id=excluded.step_id,updated_at=excluded.updated_at`,
		append(args, task, entry.Surface, entry.RiskArea, task, maxCoverageKeys)...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("coverage limit of %d keys reached", maxCoverageKeys)
	}
	if _, err = tx.Exec(`INSERT INTO deep_assessment_coverage_history(task_id,surface,risk_area,outcome,evidence,reason,step_id,updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, args...); err != nil {
		return err
	}
	if err = Signal(tx, task); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Coverage(task string) (CoverageLedger, error) {
	ledger := CoverageLedger{
		Records: []CoverageRecord{}, Gaps: []CoverageGap{}, Counts: map[string]int{},
		Goals: []AssessmentGoal{}, Constraints: []AssessmentConstraint{},
	}
	if err := s.readCoverageDefinitions(task, &ledger); err != nil {
		return CoverageLedger{}, err
	}
	rows, err := s.DB.Query(`SELECT l.surface,l.risk_area,l.outcome,l.evidence,l.reason,l.step_id,l.updated_at,
		l.outcome='no_issue_found' AND NOT (
			EXISTS(SELECT 1 FROM deep_assessment_calls c WHERE c.id=l.evidence AND c.task_id=l.task_id
				AND c.status='completed' AND c.`+originalEvidenceTool+`) OR
			EXISTS(SELECT 1 FROM deep_assessment_facts f WHERE f.id=l.evidence AND f.task_id=l.task_id
				AND `+trustedObservation+`)
		) FROM deep_assessment_coverage l WHERE l.task_id=? ORDER BY l.surface,l.risk_area`, task)
	if err != nil {
		return CoverageLedger{}, err
	}
	defer rows.Close()
	covered := make(map[string]bool)
	for rows.Next() {
		var entry CoverageRecord
		var missingEvidence bool
		if err = rows.Scan(&entry.Surface, &entry.RiskArea, &entry.Outcome, &entry.Evidence, &entry.Reason, &entry.StepID, &entry.UpdatedAt, &missingEvidence); err != nil {
			return CoverageLedger{}, err
		}
		ledger.Records = append(ledger.Records, entry)
		ledger.Counts[entry.Outcome]++
		covered[strings.ToLower(entry.RiskArea)] = true
		gap := CoverageGap{Surface: entry.Surface, RiskArea: entry.RiskArea}
		switch {
		case entry.Outcome == coverageNeedsFollowUp:
			gap.Kind = coverageNeedsFollowUp
		case missingEvidence:
			gap.Kind = "missing_evidence"
		case (entry.Outcome == coverageRuledOut || entry.Outcome == coverageNotApplicable) && strings.TrimSpace(entry.Reason) == "":
			gap.Kind = "missing_reason"
		default:
			continue
		}
		ledger.Gaps = append(ledger.Gaps, gap)
	}
	if err = rows.Err(); err != nil {
		return CoverageLedger{}, err
	}
	for _, goal := range ledger.Goals {
		risk := strings.TrimSpace(goal.RiskClass)
		if risk != "" && !covered[strings.ToLower(risk)] {
			ledger.Gaps = append(ledger.Gaps, CoverageGap{Kind: "missing_risk_class", RiskArea: risk, GoalID: goal.ID})
		}
	}
	return ledger, nil
}

func (s *Store) readCoverageDefinitions(task string, ledger *CoverageLedger) error {
	// Read declarations together so the overview uses the same stored definitions.
	rows, err := s.DB.Query(`SELECT 'goal',goal_id,text,risk_class,'' FROM deep_assessment_goals WHERE task_id=?
		UNION ALL SELECT 'constraint',constraint_id,text,kind,origin FROM deep_assessment_constraints WHERE task_id=?
		ORDER BY 1,2`, task, task)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var recordType, id, description, kind, origin string
		if err = rows.Scan(&recordType, &id, &description, &kind, &origin); err != nil {
			return err
		}
		if recordType == "goal" {
			ledger.Goals = append(ledger.Goals, AssessmentGoal{ID: id, Text: description, RiskClass: kind})
		} else {
			ledger.Constraints = append(ledger.Constraints, AssessmentConstraint{ID: id, Text: description, Kind: kind, Origin: origin})
		}
	}
	return rows.Err()
}

// FormatGap renders a single-line human/model-readable gap description.
func (g CoverageGap) FormatGap() string {
	switch g.Kind {
	case "missing_risk_class":
		if g.GoalID != "" {
			return fmt.Sprintf("goal=%s risk_area=%s: 必测风险类别尚无覆盖记录", g.GoalID, g.RiskArea)
		}
		return fmt.Sprintf("risk_area=%s: 必测风险类别尚无覆盖记录", g.RiskArea)
	case "missing_evidence":
		return fmt.Sprintf("surface=%s risk_area=%s: no_issue_found 缺少本任务成功原始回执或 observed 事实证据", g.Surface, g.RiskArea)
	case "missing_reason":
		return fmt.Sprintf("surface=%s risk_area=%s: ruled_out/not_applicable 缺少原因说明", g.Surface, g.RiskArea)
	case coverageNeedsFollowUp:
		return fmt.Sprintf("surface=%s risk_area=%s: 标记为 needs_follow_up 待跟进", g.Surface, g.RiskArea)
	default:
		return fmt.Sprintf("surface=%s risk_area=%s: %s", g.Surface, g.RiskArea, g.Kind)
	}
}

// SummaryBlock formats compact counts and up to maxGaps gaps for prompt context.
func (l CoverageLedger) SummaryBlock(maxGaps int) string {
	if len(l.Records) == 0 && len(l.Gaps) == 0 {
		return ""
	}
	var parts []string
	for _, k := range []string{coverageReported, coverageNoIssueFound, coverageRuledOut, coverageNotApplicable, coverageNeedsFollowUp} {
		if n := l.Counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, n))
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "覆盖账本记录 %d 条", len(l.Records))
	if len(parts) > 0 {
		b.WriteString("（" + strings.Join(parts, ", ") + "）")
	}
	fmt.Fprintf(&b, "；待补缺口 %d 项。\n", len(l.Gaps))
	if maxGaps > 0 && len(l.Gaps) > 0 {
		limit := min(maxGaps, len(l.Gaps))
		for i := 0; i < limit; i++ {
			b.WriteString("- " + l.Gaps[i].FormatGap() + "\n")
		}
		if extra := len(l.Gaps) - limit; extra > 0 {
			fmt.Fprintf(&b, "- 其余 %d 项缺口可通过 assessment_coverage_ledger 查看。\n", extra)
		}
	}
	return strings.TrimSpace(b.String())
}
