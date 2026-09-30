package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// sessionTestSchema is the smallest shape the restart reconciliation reads and
// writes: an assistant placeholder, its process events, and the interrupt that
// ended it.
const sessionTestSchema = `
CREATE TABLE messages (
	id TEXT PRIMARY KEY,
	conversation_id TEXT NOT NULL,
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE TABLE process_details (
	id TEXT PRIMARY KEY,
	message_id TEXT,
	conversation_id TEXT,
	event_type TEXT NOT NULL,
	message TEXT,
	data TEXT,
	created_at DATETIME NOT NULL
);
CREATE TABLE hitl_interrupts (
	id TEXT PRIMARY KEY,
	conversation_id TEXT NOT NULL,
	message_id TEXT,
	mode TEXT NOT NULL,
	tool_name TEXT NOT NULL,
	tool_call_id TEXT,
	payload TEXT,
	status TEXT NOT NULL,
	reviewer TEXT NOT NULL DEFAULT 'human',
	decision TEXT,
	decision_comment TEXT,
	decided_by TEXT NOT NULL DEFAULT 'human',
	created_at DATETIME NOT NULL,
	decided_at DATETIME
);
`

func openSessionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(sessionTestSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func seedMessage(t *testing.T, db *sql.DB, id, conversationID, role, content, createdAt string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO messages (id, conversation_id, role, content, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, conversationID, role, content, createdAt, createdAt)
	if err != nil {
		t.Fatalf("insert message %s: %v", id, err)
	}
}

func seedProcessEvent(t *testing.T, db *sql.DB, messageID, event, createdAt string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO process_details (id, message_id, conversation_id, event_type, message, created_at)
		VALUES (?, ?, 'c1', ?, ?, ?)`, event+"-"+messageID, messageID, event, event+" notice", createdAt)
	if err != nil {
		t.Fatalf("insert process event: %v", err)
	}
}

func seedInterruptForMessage(t *testing.T, db *sql.DB, id, messageID, status, decision, comment, decidedAt string) {
	t.Helper()
	var decided any
	if decidedAt != "" {
		decided = decidedAt
	}
	_, err := db.Exec(`INSERT INTO hitl_interrupts
		(id, conversation_id, message_id, mode, tool_name, status, reviewer, decision, decision_comment, decided_by, created_at, decided_at)
		VALUES (?, 'c1', ?, 'approval', 'exec', ?, 'human', ?, ?, 'human', ?, ?)`,
		id, messageID, status, nullableString(decision), nullableString(comment), decidedAt, decided)
	if err != nil {
		t.Fatalf("insert interrupt %s: %v", id, err)
	}
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func placeholderIDs(items []InterruptedPlaceholder) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.MessageID)
	}
	return out
}

func messageContent(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var content string
	if err := db.QueryRow(`SELECT content FROM messages WHERE id = ?`, id).Scan(&content); err != nil {
		t.Fatalf("read message %s: %v", id, err)
	}
	return content
}

func eventCount(t *testing.T, db *sql.DB, messageID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM process_details WHERE message_id = ?`, messageID).Scan(&n); err != nil {
		t.Fatalf("count events for %s: %v", messageID, err)
	}
	return n
}

// The evidence rule is the whole point: a placeholder is only rewritten when
// something proves the run is over, so a turn that another runtime could still
// finish is left alone.
func TestInterruptedPlaceholdersOnlyWithEvidence(t *testing.T) {
	db := openSessionTestDB(t)
	seedMessage(t, db, "m-hitl-cancelled", "c1", "assistant", "处理中...", "2026-09-01 10:00:00")
	seedMessage(t, db, "m-hitl-reject", "c1", "assistant", "Processing...", "2026-09-02 10:00:00")
	seedMessage(t, db, "m-process-error", "c1", "assistant", "处理中...", "2026-09-03 10:00:00")
	seedMessage(t, db, "m-later-message", "c1", "assistant", "处理中...", "2026-09-04 10:00:00")
	// Its own conversation: any later message in the conversation is evidence that a
	// run is over, so a placeholder that should stay untouched must be the newest row.
	seedMessage(t, db, "m-still-running", "c-live", "assistant", "处理中...", "2026-09-05 10:00:00")
	seedMessage(t, db, "m-finished", "c1", "assistant", "done already", "2026-09-06 10:00:00")
	seedMessage(t, db, "m-user", "c1", "user", "处理中...", "2026-08-01 10:00:00")

	seedInterruptForMessage(t, db, "i-cancel", "m-hitl-cancelled", "cancelled", "reject", "task cancelled", "2026-09-01 11:00:00")
	seedInterruptForMessage(t, db, "i-reject", "m-hitl-reject", "decided", "reject", "out of scope", "2026-09-02 11:00:00")
	seedInterruptForMessage(t, db, "i-approve", "m-still-running", "pending", "", "", "2026-09-05 10:00:00")
	seedProcessEvent(t, db, "m-process-error", "error", "2026-09-03 11:00:00")
	seedMessage(t, db, "m-follows", "c1", "assistant", "the next turn", "2026-09-04 12:00:00")

	s := NewSession(db)
	items, err := s.InterruptedPlaceholders()
	if err != nil {
		t.Fatalf("interrupted placeholders: %v", err)
	}
	got := map[string]bool{}
	for _, id := range placeholderIDs(items) {
		got[id] = true
	}
	for id, want := range map[string]bool{
		"m-hitl-cancelled": true, "m-hitl-reject": true, "m-process-error": true,
		"m-later-message": true, "m-still-running": false, "m-finished": false, "m-user": false,
	} {
		if got[id] != want {
			t.Errorf("%s matched = %v, want %v (all: %v)", id, got[id], want, placeholderIDs(items))
		}
	}
}

// interrupted_at decides the timestamp the user is told the run stopped at, so the
// preference order is behaviour, not an implementation detail.
func TestInterruptedPlaceholdersTimestampPreference(t *testing.T) {
	db := openSessionTestDB(t)
	seedMessage(t, db, "m-a", "c1", "assistant", "处理中...", "2026-09-01 09:00:00")
	seedInterruptForMessage(t, db, "i-a", "m-a", "cancelled", "reject", "task cancelled", "2026-09-01 10:00:00")
	seedProcessEvent(t, db, "m-a", "error", "2026-09-01 09:30:00")

	seedMessage(t, db, "m-b", "c1", "assistant", "处理中...", "2026-09-02 09:00:00")
	seedMessage(t, db, "m-b-later", "c1", "assistant", "next", "2026-09-02 09:40:00")

	seedMessage(t, db, "m-c", "c1", "assistant", "处理中...", "2026-09-03 09:00:00")
	seedProcessEvent(t, db, "m-c", "timeout", "2026-09-03 09:20:00")

	s := NewSession(db)
	items, err := s.InterruptedPlaceholders()
	if err != nil {
		t.Fatalf("interrupted placeholders: %v", err)
	}
	want := map[string]string{
		// newest interrupt time wins over the earlier process event
		"m-a": "2026-09-01 10:00:00",
		// no interrupt: the later message's time
		"m-b": "2026-09-02 09:40:00",
		// no interrupt, no later message: the process event
		"m-c": "2026-09-03 09:20:00",
	}
	if len(items) != len(want) {
		t.Fatalf("items = %v, want %d rows", placeholderIDs(items), len(want))
	}
	for _, it := range items {
		if want[it.MessageID] != it.InterruptedAt {
			t.Errorf("%s interrupted_at = %q, want %q", it.MessageID, it.InterruptedAt, want[it.MessageID])
		}
	}
}

func TestFinalizeInterruptedPlaceholders(t *testing.T) {
	db := openSessionTestDB(t)
	seedMessage(t, db, "m-1", "c1", "assistant", "处理中...", "2026-09-01 09:00:00")
	seedMessage(t, db, "m-2", "c1", "assistant", "Processing...", "2026-09-01 09:00:00")
	s := NewSession(db)

	finished, err := s.FinalizeInterruptedPlaceholders([]InterruptedUpdate{
		{MessageID: "m-1", ConversationID: "c1", EventType: "cancelled", Notice: "任务因服务重启已中断。", Reason: "process_restarted", InterruptedAt: "2026-09-01 10:00:00"},
		{MessageID: "m-2", ConversationID: "c1", EventType: "timeout", Notice: "任务等待审批超时，已自动拒绝。", Reason: "hitl_timeout", InterruptedAt: "2026-09-01 10:00:00"},
		{MessageID: "m-gone", ConversationID: "c1", EventType: "cancelled", Notice: "x", Reason: "y", InterruptedAt: "2026-09-01 10:00:00"},
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if finished != 2 {
		t.Fatalf("finished = %d, want 2 (the unknown message must not count)", finished)
	}
	if content := messageContent(t, db, "m-1"); content != "任务因服务重启已中断。" {
		t.Fatalf("m-1 content = %q, want the notice", content)
	}
	if n := eventCount(t, db, "m-1"); n != 1 {
		t.Fatalf("m-1 events = %d, want one", n)
	}
	if n := eventCount(t, db, "m-gone"); n != 0 {
		t.Fatalf("unknown message got %d events", n)
	}

	// Re-running the finalize must not rewrite the notice or duplicate the event:
	// the content guard excludes rows that are no longer placeholders, and the
	// event insert is guarded by an existing terminal event.
	again, err := s.FinalizeInterruptedPlaceholders([]InterruptedUpdate{
		{MessageID: "m-1", ConversationID: "c1", EventType: "error", Notice: "重写", Reason: "other", InterruptedAt: "2026-09-02 10:00:00"},
	})
	if err != nil {
		t.Fatalf("second finalize: %v", err)
	}
	if again != 0 {
		t.Fatalf("second finalize reported %d, want 0", again)
	}
	if content := messageContent(t, db, "m-1"); content != "任务因服务重启已中断。" {
		t.Fatalf("m-1 was rewritten to %q", content)
	}
	if n := eventCount(t, db, "m-1"); n != 1 {
		t.Fatalf("m-1 events = %d after a retry, want still one", n)
	}

	// An existing terminal event is not duplicated even when the placeholder is
	// somehow still untouched.
	seedMessage(t, db, "m-3", "c1", "assistant", "处理中...", "2026-09-03 09:00:00")
	seedProcessEvent(t, db, "m-3", "cancelled", "2026-09-03 09:10:00")
	if _, err := s.FinalizeInterruptedPlaceholders([]InterruptedUpdate{
		{MessageID: "m-3", ConversationID: "c1", EventType: "cancelled", Notice: "已中断", Reason: "process_restarted", InterruptedAt: "2026-09-03 10:00:00"},
	}); err != nil {
		t.Fatalf("finalize with a pre-existing event: %v", err)
	}
	if n := eventCount(t, db, "m-3"); n != 1 {
		t.Fatalf("m-3 events = %d, want the pre-existing one only", n)
	}

	empty, err := s.FinalizeInterruptedPlaceholders(nil)
	if err != nil || empty != 0 {
		t.Fatalf("empty batch = (%d, %v), want (0, nil)", empty, err)
	}
}

// The end-to-end shape the restart path uses: read the evidence, then write back
// what the store reported rather than what the caller assumed.
func TestSessionRestartReconciliationRoundTrip(t *testing.T) {
	db := openSessionTestDB(t)
	seedMessage(t, db, "m-live", "c1", "assistant", "处理中...", "2026-09-01 09:00:00")
	seedInterruptForMessage(t, db, "i-live", "m-live", "timeout", "reject", "HITL timeout auto-reject for safety", "2026-09-01 09:30:00")

	s := NewSession(db)
	items, err := s.InterruptedPlaceholders()
	if err != nil {
		t.Fatalf("read placeholders: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %v, want the one placeholder with evidence", placeholderIDs(items))
	}
	it := items[0]
	if it.HITLStatus != "timeout" || it.HITLDecision != "reject" || it.TerminalEvent != "" {
		t.Fatalf("evidence = %+v, want the interrupt's status and decision carried out", it)
	}
	finished, err := s.FinalizeInterruptedPlaceholders([]InterruptedUpdate{{
		MessageID: it.MessageID, ConversationID: it.ConversationID, EventType: "timeout",
		Notice: "任务等待审批超时，已自动拒绝。", Reason: "hitl_timeout", InterruptedAt: it.InterruptedAt,
	}})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if finished != 1 {
		t.Fatalf("finished = %d, want 1", finished)
	}

	// After the write the placeholder no longer qualifies, so a second restart is inert.
	remaining, err := s.InterruptedPlaceholders()
	if err != nil {
		t.Fatalf("re-read placeholders: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining = %v, want nothing left to finalize", placeholderIDs(remaining))
	}
	var stored sql.NullString
	if err := db.QueryRow(`SELECT data FROM process_details WHERE message_id = 'm-live'`).Scan(&stored); err != nil {
		t.Fatalf("read event data: %v", err)
	}
	if stored.String == "" {
		t.Fatal("the process event must record why the run ended")
	}
}

func TestSessionRefusesAConnectionlessStore(t *testing.T) {
	s := NewSession(nil)
	if _, err := s.InterruptedPlaceholders(); err == nil {
		t.Error("InterruptedPlaceholders should fail without a database")
	}
	if _, err := s.FinalizeInterruptedPlaceholders([]InterruptedUpdate{{MessageID: "m"}}); err == nil {
		t.Error("FinalizeInterruptedPlaceholders should fail without a database")
	}
	var nilStore *Session
	if _, err := nilStore.InterruptedPlaceholders(); err == nil {
		t.Error("a nil store should fail instead of panicking")
	}
}
