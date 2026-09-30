package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// legacyInterruptSchema is the table as it stood before the audit-reviewer columns:
// hitl_interrupts without decided_by, hitl_conversation_configs without reviewer.
// EnsureSchema has to bring exactly this shape forward, because live installs have it.
const legacyInterruptSchema = `
CREATE TABLE hitl_interrupts (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    message_id TEXT,
    mode TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    tool_call_id TEXT,
    payload TEXT,
    status TEXT NOT NULL,
    decision TEXT,
    decision_comment TEXT,
    created_at DATETIME NOT NULL,
    decided_at DATETIME
);
CREATE TABLE hitl_conversation_configs (
    conversation_id TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL DEFAULT 0,
    mode TEXT NOT NULL DEFAULT 'off',
    sensitive_tools TEXT NOT NULL DEFAULT '[]',
    timeout_seconds INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME NOT NULL
);
`

func openLegacyHITLDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "hitl-legacy.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(legacyInterruptSchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	return db
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("read columns of %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan column row: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}

func TestHITLEnsureSchemaCreatesAndIsIdempotent(t *testing.T) {
	s := NewHITL(openLegacyHITLDB(t))
	if _, err := s.EnsureSchema(); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := s.EnsureSchema(); err != nil {
		t.Fatalf("second ensure schema must tolerate existing columns: %v", err)
	}
	for _, tc := range []struct{ table, column string }{
		{"hitl_interrupts", "reviewer"},
		{"hitl_interrupts", "decided_by"},
		{"hitl_conversation_configs", "reviewer"},
	} {
		if !columnExists(t, s.db, tc.table, tc.column) {
			t.Errorf("%s.%s missing after migration", tc.table, tc.column)
		}
	}
}

// Rows left 'pending' by the previous process can never be answered: their channels
// died with it, so they must not keep showing up as an approval queue.
func TestHITLEnsureSchemaCancelsOrphansFromPreviousProcess(t *testing.T) {
	db := openLegacyHITLDB(t)
	if _, err := db.Exec(`INSERT INTO hitl_interrupts (id, conversation_id, mode, tool_name, status, created_at)
		VALUES ('orphan', 'c1', 'approval', 'exec', 'pending', CURRENT_TIMESTAMP),
		       ('orphan2', 'c1', 'approval', 'exec', 'pending', CURRENT_TIMESTAMP),
		       ('done', 'c1', 'approval', 'exec', 'decided', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := NewHITL(db)
	cancelled, err := s.EnsureSchema()
	if err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if cancelled != 2 {
		t.Fatalf("cancelled = %d, want the two orphaned rows", cancelled)
	}
	orphan, _, err := s.Get("orphan")
	if err != nil {
		t.Fatalf("read orphan: %v", err)
	}
	if orphan.Status != "cancelled" || orphan.Decision != "reject" ||
		orphan.Comment != "process restarted" || orphan.DecidedBy != "system" || orphan.DecidedAt == nil {
		t.Fatalf("orphan = %+v, want a recorded system rejection", orphan)
	}
	kept, _, err := s.Get("done")
	if err != nil || kept.Status != "decided" {
		t.Fatalf("decided row disturbed: %+v err=%v", kept, err)
	}

	// A second start with nothing pending must report zero, not re-cancel.
	again, err := s.EnsureSchema()
	if err != nil || again != 0 {
		t.Fatalf("second pass = (%d, %v), want (0, nil)", again, err)
	}
}

// The backfill runs after the column is added, so rows decided by the audit agent
// under the old schema must end up attributed to that reviewer rather than to a human.
func TestHITLEnsureSchemaBackfillsAgentReviewer(t *testing.T) {
	s := NewHITL(openLegacyHITLDB(t))
	if _, err := s.EnsureSchema(); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE hitl_interrupts SET reviewer='human'`); err != nil {
		t.Fatalf("reset reviewer: %v", err)
	}
	if err := s.CreateInterrupt(NewInterrupt{ID: "agent-row", ConversationID: "c1", Mode: "approval", ToolName: "exec", Reviewer: "audit_agent", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Resolve("agent-row", Resolution{Status: "decided", Decision: "approve", Comment: "ok", DecidedBy: "agent", At: time.Now()}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := s.EnsureSchema(); err != nil {
		t.Fatalf("re-run migration: %v", err)
	}
	it, _, err := s.Get("agent-row")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if it.Reviewer != "audit_agent" {
		t.Fatalf("reviewer = %q after backfill, want audit_agent (the alias decided_by must map to the reviewer)", it.Reviewer)
	}
}

func TestHITLCreateInterruptAndLatestPendingMode(t *testing.T) {
	s := NewHITL(openLegacyHITLDB(t))
	if _, err := s.EnsureSchema(); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	older := now.Add(-2 * time.Hour)
	for _, in := range []NewInterrupt{
		{ID: "p-old", ConversationID: "c1", Mode: "approval", ToolName: "exec", Reviewer: "human", CreatedAt: older},
		{ID: "p-new", ConversationID: "c1", Mode: "review_edit", ToolName: "http_get", Reviewer: "human", CreatedAt: now},
		{ID: "other", ConversationID: "c2", Mode: "feedback", ToolName: "exec", Reviewer: "human", CreatedAt: now},
	} {
		if err := s.CreateInterrupt(in); err != nil {
			t.Fatalf("create %s: %v", in.ID, err)
		}
	}

	mode, found, err := s.LatestPendingMode("c1")
	if err != nil || !found {
		t.Fatalf("latest pending mode = (%q, %v, %v)", mode, found, err)
	}
	if mode != "review_edit" {
		t.Fatalf("mode = %q, want the newest pending row's mode", mode)
	}
	if err := s.Resolve("p-new", Resolution{Status: "decided", Decision: "approve", DecidedBy: "human", At: time.Now()}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	mode, found, err = s.LatestPendingMode("c1")
	if err != nil || !found || mode != "approval" {
		t.Fatalf("after deciding the newest row = (%q, %v, %v), want the remaining pending row", mode, found, err)
	}

	if mode, found, err := s.LatestPendingMode("c-none"); err != nil || found || mode != "" {
		t.Fatalf("unknown conversation = (%q, %v, %v), want (\"\", false, nil)", mode, found, err)
	}
	if mode, found, err := s.LatestPendingMode("  "); err != nil || found || mode != "" {
		t.Fatalf("blank conversation = (%q, %v, %v), want (\"\", false, nil)", mode, found, err)
	}
	if err := s.CreateInterrupt(NewInterrupt{ConversationID: "c1"}); err == nil {
		t.Fatal("an interrupt without an id must be refused")
	}
}

func TestHITLResolveRecordsWhoDecided(t *testing.T) {
	s := NewHITL(openLegacyHITLDB(t))
	if _, err := s.EnsureSchema(); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := s.CreateInterrupt(NewInterrupt{ID: "i1", ConversationID: "c1", Mode: "approval", ToolName: "exec", Reviewer: "human", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create: %v", err)
	}
	at := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	for _, r := range []Resolution{
		{Status: "timeout", Decision: "reject", Comment: "HITL timeout auto-reject for safety", DecidedBy: "system", At: at},
		{Status: "cancelled", Decision: "reject", Comment: "task cancelled", DecidedBy: "system", At: at.Add(time.Minute)},
		{Status: "decided", Decision: "approve", Comment: "go ahead", DecidedBy: "human", At: at.Add(2 * time.Minute)},
	} {
		if err := s.Resolve("i1", r); err != nil {
			t.Fatalf("resolve %s: %v", r.Status, err)
		}
		it, _, err := s.Get("i1")
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if it.Status != r.Status || it.Decision != r.Decision || it.Comment != r.Comment || it.DecidedBy != r.DecidedBy {
			t.Fatalf("row = %+v, want status=%s decision=%s comment=%q decided_by=%s", it, r.Status, r.Decision, r.Comment, r.DecidedBy)
		}
		if it.DecidedAt == nil {
			t.Fatal("decided_at must be recorded")
		}
	}
	if err := s.Resolve("missing", Resolution{Status: "decided"}); err != nil {
		t.Fatalf("resolving an unknown id should not fail: %v", err)
	}
}

func TestHITLConversationConfigRoundTrip(t *testing.T) {
	s := NewHITL(openLegacyHITLDB(t))
	if _, err := s.EnsureSchema(); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	// No row yet is the "off" state, not an error.
	cfg, found, err := s.ConversationConfig("c-new")
	if err != nil || found || cfg.Mode != "" {
		t.Fatalf("absent config = (%+v, %v, %v), want an empty one and found=false", cfg, found, err)
	}
	if has, err := s.HasConversationConfig("c-new"); err != nil || has {
		t.Fatalf("HasConversationConfig = (%v, %v), want false", has, err)
	}

	in := ConversationConfig{Enabled: true, Mode: "review_edit", Reviewer: "audit_agent", SensitiveTools: []string{"exec", "http_get"}, TimeoutSeconds: 120}
	if err := s.SaveConversationConfig("c1", in); err != nil {
		t.Fatalf("save: %v", err)
	}
	stored, found, err := s.ConversationConfig("c1")
	if err != nil || !found {
		t.Fatalf("load = (%v, %v)", found, err)
	}
	if stored.Enabled != in.Enabled || stored.Mode != in.Mode || stored.Reviewer != in.Reviewer || stored.TimeoutSeconds != in.TimeoutSeconds {
		t.Fatalf("stored = %+v, want %+v", stored, in)
	}
	if strings.Join(stored.SensitiveTools, ",") != "exec,http_get" {
		t.Fatalf("sensitive tools = %v, want the round trip preserved", stored.SensitiveTools)
	}
	if has, err := s.HasConversationConfig("c1"); err != nil || !has {
		t.Fatalf("HasConversationConfig = (%v, %v), want true", has, err)
	}

	// Upsert, not a second row per conversation.
	if err := s.SaveConversationConfig("c1", ConversationConfig{Enabled: false, Mode: "off", TimeoutSeconds: 0}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	stored, _, err = s.ConversationConfig("c1")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Enabled || stored.Mode != "off" || len(stored.SensitiveTools) != 0 {
		t.Fatalf("after upsert = %+v, want the row replaced", stored)
	}

	// "Wait forever" must have one stored form, whichever way the caller spells it.
	if err := s.SaveConversationConfig("c2", ConversationConfig{Enabled: true, Mode: "approval", TimeoutSeconds: -5}); err != nil {
		t.Fatalf("save negative timeout: %v", err)
	}
	stored, _, err = s.ConversationConfig("c2")
	if err != nil || stored.TimeoutSeconds != 0 {
		t.Fatalf("timeout = %d err=%v, want 0", stored.TimeoutSeconds, err)
	}
	// An unset reviewer is a human review, not an empty string the UI cannot map.
	if stored.Reviewer != "human" {
		t.Fatalf("default reviewer = %q, want human", stored.Reviewer)
	}

	if err := s.SaveConversationConfig("  ", ConversationConfig{}); err == nil {
		t.Fatal("a config without a conversation must be refused")
	}
	if _, has, err := s.ConversationConfig(" "); err != nil || has {
		t.Fatalf("blank lookup = (%v, %v), want not found", has, err)
	}
}

func TestHITLLifecycleRefusesAConnectionlessStore(t *testing.T) {
	s := NewHITL(nil)
	if _, err := s.EnsureSchema(); err == nil {
		t.Error("EnsureSchema should fail without a database")
	}
	if err := s.CreateInterrupt(NewInterrupt{ID: "i"}); err == nil {
		t.Error("CreateInterrupt should fail without a database")
	}
	if err := s.Resolve("i", Resolution{}); err == nil {
		t.Error("Resolve should fail without a database")
	}
	if _, _, err := s.LatestPendingMode("c"); err == nil {
		t.Error("LatestPendingMode should fail without a database")
	}
	if err := s.SaveConversationConfig("c", ConversationConfig{}); err == nil {
		t.Error("SaveConversationConfig should fail without a database")
	}
	if _, _, err := s.ConversationConfig("c"); err == nil {
		t.Error("ConversationConfig should fail without a database")
	}
	if _, err := s.HasConversationConfig("c"); err == nil {
		t.Error("HasConversationConfig should fail without a database")
	}
}

func TestHITLCreateInterruptDefaultsTheReviewer(t *testing.T) {
	s := NewHITL(openLegacyHITLDB(t))
	if _, err := s.EnsureSchema(); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	// A caller that says nothing about the reviewer means a human review. Storing the
	// empty string would leave the row in neither the human queue nor the agent list.
	if err := s.CreateInterrupt(NewInterrupt{ID: "bare", ConversationID: "c1", Mode: "approval", ToolName: "exec", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create: %v", err)
	}
	it, _, err := s.Get("bare")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if it.Reviewer != "human" {
		t.Fatalf("reviewer = %q, want the human default", it.Reviewer)
	}
	pending, err := s.PendingApprovals(10, Access{Scope: ScopeAll})
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending approvals = %v err=%v, want the new row announced", pending, err)
	}
}

// The C2 bridge polls for an answer and gives up on timeout. Both halves need the
// table guarded: reading must distinguish "no row" from "no decision yet", and
// giving up must not overwrite a human decision that landed in the meantime.
func TestHITLDecisionAndResolvePending(t *testing.T) {
	s := NewHITL(openLegacyHITLDB(t))
	if _, err := s.EnsureSchema(); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := s.CreateInterrupt(NewInterrupt{ID: "c2-live", ConversationID: "c1", Mode: "approval", ToolName: "c2_task", Reviewer: "human", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create: %v", err)
	}

	answer, err := s.Decision("c2-live")
	if err != nil || !answer.Found {
		t.Fatalf("decision = %+v err=%v, want the row found", answer, err)
	}
	if answer.Status != "pending" || answer.Decision != "" {
		t.Fatalf("pending row reads as %+v, want status=pending and an empty decision", answer)
	}
	if missing, err := s.Decision("no-such-id"); err != nil || missing.Found {
		t.Fatalf("unknown id = %+v err=%v, want Found=false and no error", missing, err)
	}

	changed, err := s.ResolvePending("c2-live", Resolution{Status: "timeout", Decision: "reject", Comment: "auto", DecidedBy: "system", At: time.Now()})
	if err != nil || !changed {
		t.Fatalf("first resolve = (%v, %v), want it recorded", changed, err)
	}
	again, err := s.ResolvePending("c2-live", Resolution{Status: "cancelled", Decision: "reject", Comment: "later", DecidedBy: "system", At: time.Now()})
	if err != nil || again {
		t.Fatalf("second resolve = (%v, %v), want nothing overwritten", again, err)
	}
	it, _, err := s.Get("c2-live")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if it.Status != "timeout" || it.Comment != "auto" {
		t.Fatalf("row = %+v, want the first resolution kept", it)
	}

	// A human answer arriving first means the timeout path is inert.
	if err := s.CreateInterrupt(NewInterrupt{ID: "c2-human", ConversationID: "c1", Mode: "approval", ToolName: "c2_task", Reviewer: "human", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Resolve("c2-human", Resolution{Status: "decided", Decision: "approve", Comment: "ok", DecidedBy: "human", At: time.Now()}); err != nil {
		t.Fatalf("human decision: %v", err)
	}
	if changed, err := s.ResolvePending("c2-human", Resolution{Status: "timeout", Decision: "reject", DecidedBy: "system", At: time.Now()}); err != nil || changed {
		t.Fatalf("timeout over a human decision = (%v, %v), want refused", changed, err)
	}
	answer, err = s.Decision("c2-human")
	if err != nil || answer.Status != "decided" || answer.Decision != "approve" {
		t.Fatalf("decision = %+v err=%v, want the human approval preserved", answer, err)
	}
}

func TestHITLResolvePendingRefusesAConnectionlessStore(t *testing.T) {
	s := NewHITL(nil)
	if _, err := s.Decision("i"); err == nil {
		t.Error("Decision should fail without a database")
	}
	if _, err := s.ResolvePending("i", Resolution{}); err == nil {
		t.Error("ResolvePending should fail without a database")
	}
}
