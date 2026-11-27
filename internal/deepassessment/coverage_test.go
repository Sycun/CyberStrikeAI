package deepassessment

import (
	"strings"
	"testing"
)

func TestCoverageKeepsHistoryAndRollsBackFailedWrites(t *testing.T) {
	s, task := fixture(t)
	entry := CoverageRecord{Surface: " Example\r\n Site ", RiskArea: " CWE-20 ", Outcome: coverageNeedsFollowUp,
		Reason: " Inspect\u2028the review. ", Evidence: " receipt ", StepID: " step "}
	if err := s.SaveCoverage(task.ID, entry); err != nil {
		t.Fatal(err)
	}
	ledger, err := s.Coverage(task.ID)
	if err != nil || len(ledger.Records) != 1 {
		t.Fatalf("records=%+v err=%v", ledger.Records, err)
	}
	row := ledger.Records[0]
	if row.Surface != "Example Site" || row.RiskArea != "CWE-20" || row.Reason != "Inspect the review." ||
		row.Evidence != "receipt" || row.StepID != "step" || row.UpdatedAt.IsZero() {
		t.Fatalf("normalization: %+v", row)
	}
	for _, invalid := range []CoverageRecord{
		{Surface: "Site", RiskArea: "CWE-20", Outcome: "complete"},
		{Surface: "Site", RiskArea: "CWE-20", Outcome: coverageReported, Reason: strings.Repeat("证", 1001)},
		{Surface: "\r\n", RiskArea: "CWE-20", Outcome: coverageReported},
	} {
		if err = s.SaveCoverage(task.ID, invalid); err == nil {
			t.Fatalf("accepted invalid entry: %+v", invalid)
		}
	}
	entry.Outcome = coverageReported
	if err = s.SaveCoverage(task.ID, entry); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`CREATE TRIGGER reject_new_history BEFORE INSERT ON deep_assessment_coverage_history
		BEGIN SELECT RAISE(ABORT, 'fixture history failure'); END`); err != nil {
		t.Fatal(err)
	}
	entry.Outcome = coverageNoIssueFound
	if err = s.SaveCoverage(task.ID, entry); err == nil {
		t.Fatal("history failure was ignored")
	}
	var latest, first, last string
	var history int
	if err = s.DB.QueryRow(`SELECT c.outcome,
		(SELECT outcome FROM deep_assessment_coverage_history WHERE task_id=c.task_id ORDER BY id LIMIT 1),
		(SELECT outcome FROM deep_assessment_coverage_history WHERE task_id=c.task_id ORDER BY id DESC LIMIT 1),
		(SELECT COUNT(*) FROM deep_assessment_coverage_history WHERE task_id=c.task_id)
		FROM deep_assessment_coverage c WHERE c.task_id=?`, task.ID).Scan(&latest, &first, &last, &history); err != nil ||
		latest != coverageReported || first != coverageNeedsFollowUp || last != coverageReported || history != 2 {
		t.Fatalf("current=%q history=%q…%q (%d), err=%v", latest, first, last, history, err)
	}
}

func TestCoverageLimitAllowsUpdates(t *testing.T) {
	s, task := fixture(t)
	if _, err := s.DB.Exec(`WITH RECURSIVE keys(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM keys WHERE n<?)
		INSERT INTO deep_assessment_coverage(task_id,surface,risk_area,outcome,updated_at)
		SELECT ?,n,'CWE-20','reported',CURRENT_TIMESTAMP FROM keys`, maxCoverageKeys, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCoverage(task.ID, CoverageRecord{Surface: "new", RiskArea: "CWE-20", Outcome: coverageReported}); err == nil {
		t.Fatal("accepted a new key beyond the limit")
	}
	if err := s.SaveCoverage(task.ID, CoverageRecord{Surface: "1", RiskArea: "CWE-20", Outcome: coverageNeedsFollowUp}); err != nil {
		t.Fatal(err)
	}
	ledger, err := s.Coverage(task.ID)
	if err != nil || len(ledger.Records) != maxCoverageKeys || ledger.Counts[coverageNeedsFollowUp] != 1 || len(ledger.Gaps) != 1 {
		t.Fatalf("records=%d counts=%v gaps=%d err=%v", len(ledger.Records), ledger.Counts, len(ledger.Gaps), err)
	}
}

func TestCoverageGapsUseTaskScopedOriginalEvidence(t *testing.T) {
	s, task := fixture(t)
	foreign := *task
	foreign.ID = "foreign-task"
	if err := s.Create(&foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO deep_assessment_goals(task_id,goal_id,text,risk_class) VALUES
		(?,'a','Uncovered goal','CWE-999'),(?,'b','Covered goal','cwe-20'),(?,'c','Unclassified goal',''),(?,'x','Foreign','CWE-89');
		INSERT INTO deep_assessment_constraints(task_id,constraint_id,kind,text,origin) VALUES
		(?,'scope','deny','Excluded fixture','user');`, task.ID, task.ID, task.ID, foreign.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO deep_assessment_calls(id,task_id,step_id,tool,arguments,status,updated_at) VALUES
		('original',?,'step','fixture-read','{}','completed',CURRENT_TIMESTAMP),
		('failed',?,'step','fixture-read','{}','failed',CURRENT_TIMESTAMP),
		('annotation',?,'step','assessment_fact','{}','completed',CURRENT_TIMESTAMP),
		('foreign',?,'step','fixture-read','{}','completed',CURRENT_TIMESTAMP);
		INSERT INTO deep_assessment_facts(id,task_id,step_id,confidence,summary,body,evidence_json,assets_json,sources_json,created_at) VALUES
		('observed',?,'step','observed','fixture','','["original"]','[]','[]',CURRENT_TIMESTAMP),
		('inferred',?,'step','inferred','fixture','','["original"]','[]','[]',CURRENT_TIMESTAMP),
		('unverified',?,'step','observed','fixture','','["foreign"]','[]','[]',CURRENT_TIMESTAMP);`,
		task.ID, task.ID, task.ID, foreign.ID, task.ID, task.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []CoverageRecord{
		{Surface: "raw", Outcome: coverageNoIssueFound, Evidence: "original"},
		{Surface: "fact", Outcome: coverageNoIssueFound, Evidence: "observed"},
		{Surface: "none", Outcome: coverageNoIssueFound},
		{Surface: "foreign", Outcome: coverageNoIssueFound, Evidence: "foreign"},
		{Surface: "failed", Outcome: coverageNoIssueFound, Evidence: "failed"},
		{Surface: "annotation", Outcome: coverageNoIssueFound, Evidence: "annotation"},
		{Surface: "inference", Outcome: coverageNoIssueFound, Evidence: "inferred"},
		{Surface: "unverified", Outcome: coverageNoIssueFound, Evidence: "unverified"},
		{Surface: "pending", Outcome: coverageNeedsFollowUp},
		{Surface: "excluded", Outcome: coverageRuledOut},
		{Surface: "out-of-scope", Outcome: coverageNotApplicable, Reason: " "},
		{Surface: "documented", Outcome: coverageNotApplicable, Reason: "Outside agreed scope"},
	} {
		entry.RiskArea = "CWE-20"
		if err := s.SaveCoverage(task.ID, entry); err != nil {
			t.Fatal(err)
		}
	}
	ledger, err := s.Coverage(task.ID)
	if err != nil || len(ledger.Records) != 12 || len(ledger.Goals) != 3 || len(ledger.Constraints) != 1 || len(ledger.Gaps) != 10 {
		t.Fatalf("unexpected ledger: %+v, %v", ledger, err)
	}
	want := map[string]string{"annotation": "missing_evidence", "failed": "missing_evidence", "foreign": "missing_evidence",
		"inference": "missing_evidence", "none": "missing_evidence", "unverified": "missing_evidence", "excluded": "missing_reason",
		"out-of-scope": "missing_reason", "pending": "needs_follow_up", "a": "missing_risk_class"}
	for _, gap := range ledger.Gaps {
		key := gap.Surface
		if gap.GoalID != "" {
			key = gap.GoalID
		}
		if gap.Kind != want[key] {
			t.Fatalf("unexpected gap: %+v", gap)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing gaps: %+v", want)
	}
}
