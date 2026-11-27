package database

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestProjectLessonReviewSummariesDedupeAndWithdraw(t *testing.T) {
	db := newRBACTestDB(t)
	p, err := db.CreateProject(&Project{Name: "Review", LessonsMode: ProjectLessonsShadow})
	if err != nil {
		t.Fatal(err)
	}
	create := func(vulnType string) *Vulnerability {
		t.Helper()
		finding, err := db.CreateVulnerability(&Vulnerability{
			ProjectID: p.ID, Status: "confirmed", Severity: "high", Type: vulnType,
			Title:       "internal record password=private-secret\nsecond line",
			Description: "private description", Evidence: strings.Repeat("private-evidence ", 300),
		})
		if err != nil {
			t.Fatal(err)
		}
		return finding
	}
	finding := create("SQL Injection")
	other := create("sqli")
	access := RBACListAccess{UserID: "reviewer", Scope: RBACScopeAll}
	items, err := db.ListProjectLessonsForReview(p.ID, access)
	if err != nil || len(items) != 1 {
		t.Fatalf("classified deduplication: %+v %v", items, err)
	}
	item := items[0]
	if item.VulnerabilityType != "SQL injection" || item.Summary != "Confirmed · SQL injection · high severity" ||
		item.Cue != "confirmed" || item.Support != 2 || strings.ContainsAny(item.Summary, "\r\n") || utf8.RuneCountInString(item.Summary) > 320 {
		t.Fatalf("review summary: %+v", item)
	}
	masked, err := db.ListProjectLessonsForReview(p.ID, RBACListAccess{UserID: "unassigned", Scope: RBACScopeAssigned})
	if err != nil || len(masked) != 1 || masked[0].Summary != "" || masked[0].VulnerabilityType != "" {
		t.Fatalf("scoped reader saw classified summary: %+v %v", masked, err)
	}
	var key string
	if err := db.QueryRow(`SELECT dedupe_key FROM project_lessons WHERE id=?`, item.ID).Scan(&key); err != nil ||
		key != projectLessonDedupeKey(p.ID, item.Kind, item.Summary, item.Action) {
		t.Fatalf("hashed lesson identity: %q %v", key, err)
	}
	for _, v := range []*Vulnerability{finding, other} {
		v.Status = "false_positive"
		if err := db.UpdateVulnerability(v.ID, v); err != nil {
			t.Fatal(err)
		}
	}
	assertLessonSupport(t, db, p.ID, "confirmed", "retired", 0)
	assertLessonSupport(t, db, p.ID, "false_positive", "active", 2)
	var withdrawn int
	if err := db.QueryRow(`SELECT count(*) FROM project_lesson_evidence WHERE lesson_id=? AND withdrawn_at IS NOT NULL`, item.ID).
		Scan(&withdrawn); err != nil || withdrawn != 2 {
		t.Fatalf("preserved withdrawn evidence: %d %v", withdrawn, err)
	}
}

func TestProjectLessonSummaryUsesControlledVocabulary(t *testing.T) {
	if class, summary := projectLessonSummary("SQL injection\npassword=private-secret", "high", "confirmed"); class != "" || summary != "" {
		t.Fatalf("unrecognized free text was used: %q %q", class, summary)
	}
	if class, summary := projectLessonSummary("CWE-863: private annotation", "unknown-token", "inconclusive"); class != "CWE-863" ||
		summary != "Inconclusive · CWE-863" {
		t.Fatalf("known identifier: %q %q", class, summary)
	}
	if class, summary := projectLessonSummary("CWE-0000", "low", "fixed"); class != "" || summary != "" {
		t.Fatalf("unknown identifier: %q %q", class, summary)
	}
}

func TestProjectLessonBackfillWithdrawalAndIsolation(t *testing.T) {
	db := newRBACTestDB(t)
	first, err := db.CreateProject(&Project{Name: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateProject(&Project{Name: "Second", LessonsMode: ProjectLessonsShadow})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := db.CreateConversation("First review", ConversationCreateMeta{ProjectID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	v, err := db.CreateVulnerability(&Vulnerability{ConversationID: conv.ID, Title: "Fixture", Severity: "low", Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	if items, err := db.ListProjectLessons(first.ID); err != nil || len(items) != 0 {
		t.Fatalf("default off recorded dispositions: %+v, %v", items, err)
	}
	if err := db.ExtractProjectLessons(first.ID); !errors.Is(err, ErrProjectLessonsOff) {
		t.Fatalf("off extraction: %v", err)
	}
	mode := ProjectLessonsShadow
	if _, err := db.UpdateProject(first.ID, ProjectUpdate{LessonsMode: &mode}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.ExtractProjectLessons(first.ID); err != nil {
			t.Fatal(err)
		}
	}
	assertLessonSupport(t, db, first.ID, "confirmed", "active", 1)
	if items, err := db.ListProjectLessons(second.ID); err != nil || len(items) != 0 {
		t.Fatalf("other project saw records: %+v, %v", items, err)
	}
	if err := db.RecordProjectLessonShadowUsage(first.ID, conv.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordProjectLessonShadowUsage(second.ID, conv.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM project_lesson_usage WHERE conversation_id=? AND mode='shadow'`, conv.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("shadow usage: %d, %v", count, err)
	}

	v.Status = "false_positive"
	if err := db.UpdateVulnerability(v.ID, v); err != nil {
		t.Fatal(err)
	}
	assertLessonSupport(t, db, first.ID, "confirmed", "retired", 0)
	assertLessonSupport(t, db, first.ID, "false_positive", "active", 1)
	if err := db.QueryRow(`SELECT count(*) FROM project_lesson_evidence WHERE vulnerability_id=? AND withdrawn_at IS NOT NULL`, v.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("withdrawn evidence missing: %d, %v", count, err)
	}
	if err := db.SetConversationProjectID(conv.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	assertLessonSupport(t, db, first.ID, "false_positive", "retired", 0)
	assertLessonSupport(t, db, second.ID, "false_positive", "active", 1)
	if err := db.DeleteVulnerability(v.ID); err != nil {
		t.Fatal(err)
	}
	assertLessonSupport(t, db, second.ID, "false_positive", "retired", 0)
}

func TestProjectLessonRetestProvenance(t *testing.T) {
	db := newRBACTestDB(t)
	p, err := db.CreateProject(&Project{Name: "Retest", LessonsMode: ProjectLessonsShadow})
	if err != nil {
		t.Fatal(err)
	}
	original, err := db.CreateConversation("Original", ConversationCreateMeta{ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	v, err := db.CreateVulnerability(&Vulnerability{ConversationID: original.ID, Title: "Fixture", Severity: "low", Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	retest, err := db.CreateVulnerabilityRetest(v, ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tool_executions(id,tool_name,arguments,status,start_time,conversation_id)
		VALUES('retest-evidence','fixture_review','{}','completed',CURRENT_TIMESTAMP,?)`, retest.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.UpdateVulnerabilityRetestResult(v.ID, "fixed", "Reviewed remediation", retest.ID); err != nil {
			t.Fatal(err)
		}
	}
	assertLessonSupport(t, db, p.ID, "confirmed", "retired", 0)
	assertLessonSupport(t, db, p.ID, "fixed", "active", 1)
	var sourceKind string
	if err := db.QueryRow(`SELECT source_kind FROM project_lesson_evidence WHERE vulnerability_id=? AND withdrawn_at IS NULL`, v.ID).Scan(&sourceKind); err != nil || sourceKind != "retest" {
		t.Fatalf("retest provenance: %q, %v", sourceKind, err)
	}
}

func TestProjectLessonAssessmentReviewReversal(t *testing.T) {
	db := newRBACTestDB(t)
	p, err := db.CreateProject(&Project{Name: "Review", LessonsMode: ProjectLessonsShadow})
	if err != nil {
		t.Fatal(err)
	}
	original, err := db.CreateConversation("Original", ConversationCreateMeta{ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := db.CreateConversation("Review", ConversationCreateMeta{ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	assessmentSourceFixture(t, db, "review-fixture", "Review", "", original.ID, reviewer.ID)
	if _, err := db.Exec(`UPDATE deep_assessments
		SET project_id=?,payload=json_set(payload,'$.steps[1].status','running') WHERE id='review-fixture'`, p.ID); err != nil {
		t.Fatal(err)
	}
	for id, step := range map[string]string{"original-fixture": "one", "review-proof": "two"} {
		if _, err := db.Exec(`INSERT INTO deep_assessment_calls(id,task_id,step_id,tool,arguments,status,result,updated_at)
			VALUES(?,'review-fixture',?,'fixture_review','{}','completed','receipt',?)`, id, step, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	v, err := db.CreateVulnerability(&Vulnerability{ConversationID: original.ID, Title: "Fixture", Severity: "low", EvidenceReceiptIDs: []string{"original-fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, verdict := range []string{"confirmed", "inconclusive", "refuted"} {
		if err := db.ReviewAssessmentFinding("review-fixture", "two", v.ID, verdict, "Independent fixture observation",
			[]string{"review-proof"}, verdict); err != nil {
			t.Fatal(err)
		}
		cue := verdict
		if verdict == "refuted" {
			cue = "false_positive"
		}
		assertLessonSupport(t, db, p.ID, cue, "active", 1)
		if verdict == "inconclusive" {
			current, err := db.GetVulnerability(v.ID)
			if err != nil {
				t.Fatal(err)
			}
			current.Description = "Edited display text"
			if err := db.UpdateVulnerability(v.ID, current); err != nil {
				t.Fatal(err)
			}
			if err := db.ExtractProjectLessons(p.ID); err != nil {
				t.Fatal(err)
			}
			assertLessonSupport(t, db, p.ID, "inconclusive", "active", 1)
		}
	}
	assertLessonSupport(t, db, p.ID, "confirmed", "retired", 0)
	assertLessonSupport(t, db, p.ID, "inconclusive", "retired", 0)
	var sourceKind string
	if err := db.QueryRow(`SELECT source_kind FROM project_lesson_evidence WHERE vulnerability_id=? AND withdrawn_at IS NULL`, v.ID).Scan(&sourceKind); err != nil || sourceKind != "review" {
		t.Fatalf("review provenance: %q, %v", sourceKind, err)
	}
}

func TestProjectLessonInjectModeRendersBoundedPromptAndLogsUsage(t *testing.T) {
	db := newRBACTestDB(t)
	p, err := db.CreateProject(&Project{Name: "InjectProj", LessonsMode: ProjectLessonsInject})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := db.CreateConversation("InjectConv", ConversationCreateMeta{ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateVulnerability(&Vulnerability{
		ConversationID: conv.ID, ProjectID: p.ID, Status: "false_positive", Severity: "medium", Type: "xss",
		Title: "secret-title-not-leaked",
	}); err != nil {
		t.Fatal(err)
	}
	block, err := db.BuildProjectLessonsPrompt(p.ID, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block, "False positive · Cross-site scripting · medium severity") ||
		strings.Contains(block, "secret-title-not-leaked") || utf8.RuneCountInString(block) > 1800 {
		t.Fatalf("unexpected inject block: %q", block)
	}
	var injectCount int
	if err := db.QueryRow(`SELECT count(*) FROM project_lesson_usage WHERE project_id=? AND conversation_id=? AND mode='inject'`, p.ID, conv.ID).Scan(&injectCount); err != nil || injectCount != 1 {
		t.Fatalf("inject usage count=%d err=%v", injectCount, err)
	}
}

func assertLessonSupport(t *testing.T, db *DB, projectID, cue, status string, support int) {
	t.Helper()
	items, err := db.ListProjectLessons(projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Cue == cue && item.Status == status && item.Support == support {
			return
		}
	}
	t.Fatalf("missing lesson %s (%s, support=%d) in %+v", cue, status, support, items)
}
