package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const hitlTestSchema = `
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
CREATE TABLE conversations (
	id TEXT PRIMARY KEY,
	owner_user_id TEXT,
	project_id TEXT
);
CREATE TABLE projects (
	id TEXT PRIMARY KEY,
	owner_user_id TEXT
);
CREATE TABLE rbac_resource_assignments (
	user_id TEXT NOT NULL,
	resource_type TEXT NOT NULL,
	resource_id TEXT NOT NULL
);
`

func openHITLTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "hitl-store.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(hitlTestSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

type interruptSeed struct {
	id           string
	conversation string
	tool         string
	status       string
	reviewer     string
	decidedBy    string
	decision     string
	comment      string
	payload      string
	created      *time.Time
	decided      *time.Time
}

func seedInterrupt(t *testing.T, db *sql.DB, seed interruptSeed) {
	t.Helper()
	created := seed.created
	if created == nil {
		now := time.Now().UTC().Truncate(time.Second)
		created = &now
	}
	if seed.conversation == "" {
		seed.conversation = "conv-1"
	}
	if seed.tool == "" {
		seed.tool = "exec"
	}
	if seed.status == "" {
		seed.status = "pending"
	}
	// Unset columns are omitted instead of written as empty strings: that is what a
	// row created before reviewer/decided_by existed looks like once the migrator
	// backfilled them with their defaults.
	cols := []string{"id", "conversation_id", "mode", "tool_name", "tool_call_id", "status", "created_at"}
	args := []any{seed.id, seed.conversation, "approval", seed.tool, seed.id + "-call", seed.status,
		created.Format("2006-01-02 15:04:05")}
	optional := []struct {
		column string
		value  string
	}{
		{"payload", seed.payload},
		{"reviewer", seed.reviewer},
		{"decided_by", seed.decidedBy},
		{"decision", seed.decision},
		{"decision_comment", seed.comment},
	}
	for _, opt := range optional {
		if opt.value == "" {
			continue
		}
		cols = append(cols, opt.column)
		args = append(args, opt.value)
	}
	if seed.decided != nil {
		cols = append(cols, "decided_at")
		args = append(args, *seed.decided)
	}
	query := "INSERT INTO hitl_interrupts (" + strings.Join(cols, ", ") + ") VALUES (" +
		strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("insert interrupt %s: %v", seed.id, err)
	}
}

func seedConversation(t *testing.T, db *sql.DB, id, owner, project string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO conversations (id, owner_user_id, project_id) VALUES (?, ?, ?)`,
		id, nilIfEmpty(owner), nilIfEmpty(project))
	if err != nil {
		t.Fatalf("insert conversation %s: %v", id, err)
	}
}

func seedProject(t *testing.T, db *sql.DB, id, owner string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO projects (id, owner_user_id) VALUES (?, ?)`, id, nilIfEmpty(owner)); err != nil {
		t.Fatalf("insert project %s: %v", id, err)
	}
}

func seedAssignment(t *testing.T, db *sql.DB, user, resourceType, resourceID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO rbac_resource_assignments (user_id, resource_type, resource_id) VALUES (?, ?, ?)`,
		user, resourceType, resourceID); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
}

func nilIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func interruptIDs(items []Interrupt) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func mustTime(s string) *time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestHITLListSeparatesLogFromHumanQueue(t *testing.T) {
	db := openHITLTestDB(t)
	seedInterrupt(t, db, interruptSeed{id: "wait-human", status: "pending", reviewer: "human"})
	seedInterrupt(t, db, interruptSeed{id: "wait-agent", status: "pending", reviewer: "audit_agent"})
	seedInterrupt(t, db, interruptSeed{id: "decided", status: "decided", reviewer: "audit_agent", decision: "approve", decidedBy: "audit_agent", decided: mustTime("2026-09-01 10:00:00")})
	s := NewHITL(db)

	logs, logTotal, err := s.List(InterruptsLog, InterruptFilter{Access: Access{Scope: ScopeAll}})
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if logTotal != 1 || strings.Join(interruptIDs(logs), ",") != "decided" {
		t.Fatalf("log view = %v total %d, want [decided] 1", interruptIDs(logs), logTotal)
	}

	pending, pendingTotal, err := s.List(InterruptsAwaitingHuman, InterruptFilter{Access: Access{Scope: ScopeAll}})
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if pendingTotal != 1 || strings.Join(interruptIDs(pending), ",") != "wait-human" {
		t.Fatalf("human queue = %v total %d, want [wait-human] 1 - agent-reviewed work must not appear",
			interruptIDs(pending), pendingTotal)
	}
}

func TestHITLListFilters(t *testing.T) {
	db := openHITLTestDB(t)
	seedInterrupt(t, db, interruptSeed{id: "a", tool: "nmap_scan", status: "decided", decision: "approve", decidedBy: "human", comment: "ok", decided: mustTime("2026-09-01 10:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "b", tool: "http_get", status: "decided", decision: "reject", decidedBy: "audit_agent", payload: `{"argumentsObj":{"url":"https://target"}}`, decided: mustTime("2026-09-02 10:00:00")})
	s := NewHITL(db)
	all := Access{Scope: ScopeAll}

	cases := []struct {
		name  string
		f     InterruptFilter
		want  string
		total int
	}{
		{"tool substring", InterruptFilter{Access: all, ToolName: "nmap"}, "a", 1},
		{"decision equality", InterruptFilter{Access: all, Decision: "reject"}, "b", 1},
		{"decided_by equality", InterruptFilter{Access: all, DecidedBy: "audit_agent"}, "b", 1},
		{"status equality", InterruptFilter{Access: all, Status: "decided"}, "b,a", 2},
		{"all means no filter", InterruptFilter{Access: all, Decision: "all", DecidedBy: "all", Status: "all"}, "b,a", 2},
		{"search hits payload", InterruptFilter{Access: all, Search: "https://target"}, "b", 1},
		{"search hits comment", InterruptFilter{Access: all, Search: "ok"}, "a", 1},
		{"conversation equality", InterruptFilter{Access: all, ConversationID: "conv-1"}, "b,a", 2},
	}
	for _, tc := range cases {
		items, total, err := s.List(InterruptsLog, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := strings.Join(interruptIDs(items), ","); got != tc.want || total != tc.total {
			t.Errorf("%s = %v total %d, want %q total %d", tc.name, got, total, tc.want, tc.total)
		}
	}
}

func TestHITLListOrderingFollowsTheView(t *testing.T) {
	db := openHITLTestDB(t)
	// created newest first, decided longest-decided first: the two views must not
	// share one ORDER BY, because a log row is interesting when it was decided.
	seedInterrupt(t, db, interruptSeed{id: "late-created", status: "decided", decided: mustTime("2026-01-01 00:00:00"), created: mustTime("2026-09-09 00:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "late-decided", status: "decided", decided: mustTime("2026-09-09 00:00:00"), created: mustTime("2026-01-01 00:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "newer", status: "pending", reviewer: "human", created: mustTime("2026-05-05 00:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "older", status: "pending", reviewer: "human", created: mustTime("2026-02-02 00:00:00")})
	s := NewHITL(db)
	all := Access{Scope: ScopeAll}

	logs, _, err := s.List(InterruptsLog, InterruptFilter{Access: all})
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if got := strings.Join(interruptIDs(logs), ","); got != "late-decided,late-created" {
		t.Fatalf("log order = %q, want decided_at descending", got)
	}
	pending, _, err := s.List(InterruptsAwaitingHuman, InterruptFilter{Access: all})
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if got := strings.Join(interruptIDs(pending), ","); got != "newer,older" {
		t.Fatalf("queue order = %q, want created_at descending", got)
	}

	page, total, err := s.List(InterruptsLog, InterruptFilter{Access: all, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("paged list: %v", err)
	}
	if total != 2 || strings.Join(interruptIDs(page), ",") != "late-created" {
		t.Fatalf("page = %v total %d, want [late-created] with total 2", interruptIDs(page), total)
	}
}

func TestHITLListAccessScope(t *testing.T) {
	db := openHITLTestDB(t)
	seedProject(t, db, "proj-owner", "u1")
	seedProject(t, db, "proj-assigned", "u3")
	seedProject(t, db, "proj-foreign", "u2")

	seedConversation(t, db, "c-owned", "u1", "")
	seedConversation(t, db, "c-assigned", "u3", "")
	seedConversation(t, db, "c-project-owner", "u3", "proj-owner")
	seedConversation(t, db, "c-project-assigned", "u3", "proj-assigned")
	seedConversation(t, db, "c-foreign", "u2", "proj-foreign")

	seedAssignment(t, db, "u1", "conversation", "c-assigned")
	seedAssignment(t, db, "u1", "project", "proj-assigned")

	for _, cid := range []string{"c-owned", "c-assigned", "c-project-owner", "c-project-assigned", "c-foreign"} {
		seedInterrupt(t, db, interruptSeed{id: "i-" + cid, conversation: cid, status: "decided", decided: mustTime("2026-09-01 10:00:00")})
	}
	s := NewHITL(db)

	for _, scope := range []string{ScopeOwn, ScopeAssigned} {
		items, total, err := s.List(InterruptsLog, InterruptFilter{Access: Access{UserID: "u1", Scope: scope}})
		if err != nil {
			t.Fatalf("list with scope %q: %v", scope, err)
		}
		if total != 4 || len(items) != 4 {
			t.Fatalf("scope %q saw %d of 5 (%v), want 4: own, assigned, project owner, project assignment",
				scope, len(items), interruptIDs(items))
		}
	}

	other, otherTotal, err := s.List(InterruptsLog, InterruptFilter{Access: Access{UserID: "u2", Scope: ScopeOwn}})
	if err != nil {
		t.Fatalf("list for u2: %v", err)
	}
	if otherTotal != 1 || interruptIDs(other)[0] != "i-c-foreign" {
		t.Fatalf("u2 saw %v, want only its own conversation", interruptIDs(other))
	}

	// A listing without a session must not become a listing without a filter.
	anonymous, anonTotal, err := s.List(InterruptsLog, InterruptFilter{Access: Access{}})
	if err != nil {
		t.Fatalf("list anonymously: %v", err)
	}
	if anonTotal != 0 || len(anonymous) != 0 {
		t.Fatalf("anonymous listing saw %v, want nothing", interruptIDs(anonymous))
	}

	everyone, everyoneTotal, err := s.List(InterruptsLog, InterruptFilter{Access: Access{Scope: ScopeAll}})
	if err != nil {
		t.Fatalf("list as all: %v", err)
	}
	if everyoneTotal != 5 || len(everyone) != 5 {
		t.Fatalf("scope all saw %d, want 5", len(everyone))
	}
}

func TestHITLGetKeepsNullability(t *testing.T) {
	db := openHITLTestDB(t)
	created := mustTime("2026-09-01 10:00:00")
	seedInterrupt(t, db, interruptSeed{id: "pending-1", status: "pending", reviewer: "human", payload: `{"toolName":"exec"}`, created: created})
	seedInterrupt(t, db, interruptSeed{id: "decided-1", status: "decided", reviewer: "human", decision: "approve", comment: "go", decidedBy: "human", decided: mustTime("2026-09-01 11:00:00")})
	s := NewHITL(db)

	it, found, err := s.Get("pending-1")
	if err != nil || !found {
		t.Fatalf("get pending: found=%v err=%v", found, err)
	}
	if it.DecidedAt != nil {
		t.Fatalf("pending row has decided_at %v, want nil", it.DecidedAt)
	}
	if it.Decision != "" || it.Comment != "" || it.MessageID != "" {
		t.Fatalf("null columns should read as empty, got decision=%q comment=%q message=%q", it.Decision, it.Comment, it.MessageID)
	}
	if it.Reviewer != "human" || it.DecidedBy != "human" {
		t.Fatalf("row written before those columns existed should read as human work: reviewer=%q decided_by=%q", it.Reviewer, it.DecidedBy)
	}
	if !it.CreatedAt.Equal(*created) {
		t.Fatalf("created_at = %v, want %v", it.CreatedAt, *created)
	}

	decided, _, err := s.Get(" decided-1 ")
	if err != nil {
		t.Fatalf("get decided: %v", err)
	}
	if decided.DecidedAt == nil || decided.Decision != "approve" || decided.Comment != "go" {
		t.Fatalf("decided row = %+v, want decision/comment/decided_at present", decided)
	}

	if _, found, err := s.Get("nope"); err != nil || found {
		t.Fatalf("missing id: found=%v err=%v, want false and no error", found, err)
	}
	if _, found, err := s.Get("  "); err != nil || found {
		t.Fatalf("blank id: found=%v err=%v, want false and no error", found, err)
	}
}

func TestHITLConversationOwners(t *testing.T) {
	db := openHITLTestDB(t)
	seedInterrupt(t, db, interruptSeed{id: "i1", conversation: "c1"})
	seedInterrupt(t, db, interruptSeed{id: "i2", conversation: "c2"})
	// Beyond one chunk: a batch delete from the UI can carry more ids than SQLite
	// allows in a single statement.
	for n := 0; n < conversationOwnerChunk+5; n++ {
		seedInterrupt(t, db, interruptSeed{id: fmt.Sprintf("bulk-%d", n), conversation: "c-bulk"})
	}
	s := NewHITL(db)

	owners, err := s.ConversationOwners([]string{"i1", "i2", "gone", ""})
	if err != nil {
		t.Fatalf("owners: %v", err)
	}
	if len(owners) != 2 || owners["i1"] != "c1" || owners["i2"] != "c2" {
		t.Fatalf("owners = %v, want the two known ids only", owners)
	}

	bulk := make([]string, 0, conversationOwnerChunk+5)
	for n := 0; n < conversationOwnerChunk+5; n++ {
		bulk = append(bulk, fmt.Sprintf("bulk-%d", n))
	}
	all, err := s.ConversationOwners(bulk)
	if err != nil {
		t.Fatalf("chunked owners: %v", err)
	}
	if len(all) != len(bulk) {
		t.Fatalf("chunked owners returned %d of %d", len(all), len(bulk))
	}

	if got, err := s.ConversationOwners(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty batch = (%v, %v), want empty and no error", got, err)
	}
}

func TestHITLConversationOwner(t *testing.T) {
	db := openHITLTestDB(t)
	seedInterrupt(t, db, interruptSeed{id: "i1", conversation: "c1"})
	s := NewHITL(db)

	cid, found, err := s.ConversationOwner("i1")
	if err != nil || !found || cid != "c1" {
		t.Fatalf("owner = (%q, %v, %v), want (c1, true, nil)", cid, found, err)
	}
	if cid, found, err := s.ConversationOwner("nope"); err != nil || found || cid != "" {
		t.Fatalf("unknown id = (%q, %v, %v), want (\"\", false, nil)", cid, found, err)
	}
}

func TestHITLMutatePayload(t *testing.T) {
	db := openHITLTestDB(t)
	seedInterrupt(t, db, interruptSeed{id: "i1", payload: `{"toolName":"exec"}`})
	s := NewHITL(db)

	err := s.MutatePayload("i1", func(current string) (string, bool, error) {
		return current + `,"executionResult":{"success":true}`, true, nil
	})
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	it, _, err := s.Get("i1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(it.Payload, "executionResult") {
		t.Fatalf("payload = %q, want the merged value", it.Payload)
	}

	// Seeing the stored value is what makes a merge possible at all.
	var seen string
	if err := s.MutatePayload("i1", func(current string) (string, bool, error) {
		seen = current
		return current, false, nil
	}); err != nil {
		t.Fatalf("skipped mutation: %v", err)
	}
	if seen != it.Payload {
		t.Fatalf("merge saw %q, stored payload is %q", seen, it.Payload)
	}
	if after, _, err := s.Get("i1"); err != nil || after.Payload != it.Payload {
		t.Fatalf("returning false must leave the row alone: %q", after.Payload)
	}

	boom := errors.New("merge failed")
	if err := s.MutatePayload("i1", func(string) (string, bool, error) { return "", false, boom }); !errors.Is(err, boom) {
		t.Fatalf("merge error = %v, want it surfaced to the caller", err)
	}
	if after, _, err := s.Get("i1"); err != nil || after.Payload != it.Payload {
		t.Fatalf("failed merge must not write: %q", after.Payload)
	}

	if err := s.MutatePayload("missing", func(string) (string, bool, error) { return "{}", true, nil }); err != nil {
		t.Fatalf("mutate missing row: %v, want a no-op", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hitl_interrupts WHERE id = 'missing'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("mutate created a missing row")
	}
	if err := s.MutatePayload("  ", func(string) (string, bool, error) { return "{}", true, nil }); err == nil {
		t.Fatal("blank id should be refused")
	}
}

func TestHITLDismissOnlyCancelsPending(t *testing.T) {
	db := openHITLTestDB(t)
	seedInterrupt(t, db, interruptSeed{id: "i1", status: "pending", reviewer: "human"})
	seedInterrupt(t, db, interruptSeed{id: "decided", status: "decided", decision: "approve", decidedBy: "human", decided: mustTime("2026-09-01 10:00:00")})
	s := NewHITL(db)

	n, err := s.Dismiss("i1", "dismissed by user")
	if err != nil || n != 1 {
		t.Fatalf("dismiss = (%d, %v), want 1", n, err)
	}
	again, err := s.Dismiss("i1", "dismissed by user")
	if err != nil || again != 0 {
		t.Fatalf("second dismiss = (%d, %v), want 0 so the caller reports the race", again, err)
	}
	missed, err := s.Dismiss("decided", "dismissed by user")
	if err != nil || missed != 0 {
		t.Fatalf("dismissing a decided row = (%d, %v), want 0", missed, err)
	}
	if it, _, err := s.Get("decided"); err != nil || it.Decision != "approve" {
		t.Fatalf("decided row was overwritten: %+v", it)
	}
	if it, _, err := s.Get("i1"); err != nil || it.Status != "cancelled" || it.DecidedBy != "human" || it.DecidedAt == nil {
		t.Fatalf("dismissed row = %+v, want cancelled by human with a timestamp", it)
	}
}

func TestHITLRecordAgentDecision(t *testing.T) {
	db := openHITLTestDB(t)
	seedInterrupt(t, db, interruptSeed{id: "i1", status: "pending", reviewer: "audit_agent"})
	s := NewHITL(db)

	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := s.RecordAgentDecision("i1", "reject", "target out of scope", at); err != nil {
		t.Fatalf("record: %v", err)
	}
	it, _, err := s.Get("i1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if it.Status != "decided" || it.Decision != "reject" || it.Comment != "target out of scope" || it.DecidedBy != "audit_agent" {
		t.Fatalf("row = %+v, want the agent verdict recorded", it)
	}
	if it.DecidedAt == nil || !it.DecidedAt.Equal(at) {
		t.Fatalf("decided_at = %v, want %v", it.DecidedAt, at)
	}
	if err := s.RecordAgentDecision("missing", "approve", "", at); err != nil {
		t.Fatalf("recording for an unknown id should not fail: %v", err)
	}
}

func TestHITLDeleteLogsByIDsSkipsPending(t *testing.T) {
	db := openHITLTestDB(t)
	seedInterrupt(t, db, interruptSeed{id: "pending-1", status: "pending", reviewer: "human"})
	seedInterrupt(t, db, interruptSeed{id: "done-1", status: "decided", decision: "approve", decided: mustTime("2026-09-01 10:00:00")})
	s := NewHITL(db)

	deleted, err := s.DeleteLogsByIDs([]string{"pending-1", "done-1", " ", "gone"})
	if err != nil {
		t.Fatalf("delete by id: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, found, err := s.Get("pending-1"); err != nil || !found {
		t.Fatalf("pending row must survive an id batch delete: found=%v err=%v", found, err)
	}
	if _, found, _ := s.Get("done-1"); found {
		t.Fatal("decided row should be deleted")
	}
	if n, err := s.DeleteLogsByIDs(nil); err != nil || n != 0 {
		t.Fatalf("empty batch = (%d, %v), want (0, nil)", n, err)
	}
}

func TestHITLDeleteLogsMatchesFiltersAndAccess(t *testing.T) {
	db := openHITLTestDB(t)
	seedConversation(t, db, "c1", "u1", "")
	seedConversation(t, db, "c2", "u2", "")
	seedInterrupt(t, db, interruptSeed{id: "mine", conversation: "c1", status: "decided", tool: "nmap_scan", decided: mustTime("2026-09-01 10:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "mine-other-tool", conversation: "c1", status: "decided", tool: "http_get", decided: mustTime("2026-09-01 10:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "theirs", conversation: "c2", status: "decided", tool: "nmap_scan", decided: mustTime("2026-09-01 10:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "waiting", conversation: "c1", status: "pending", reviewer: "human", tool: "nmap_scan"})
	s := NewHITL(db)

	// The filter is the visible surface: a user clearing "all nmap logs" must not
	// reach another user's rows, and must never clear a pending interrupt.
	deleted, err := s.DeleteLogs(InterruptsLog, InterruptFilter{
		Access:   Access{UserID: "u1", Scope: ScopeOwn},
		ToolName: "nmap",
	})
	if err != nil {
		t.Fatalf("delete logs: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	for id, wantFound := range map[string]bool{"waiting": true, "theirs": true, "mine": false, "mine-other-tool": true} {
		_, found, err := s.Get(id)
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if found != wantFound {
			t.Errorf("%s found=%v, want %v", id, found, wantFound)
		}
	}

	if _, err := s.DeleteLogs(InterruptsAwaitingHuman, InterruptFilter{Access: Access{Scope: ScopeAll}}); err == nil {
		t.Fatal("deleting the approval queue as a group must be refused")
	}
}

func TestHITLPurgeDecidedBefore(t *testing.T) {
	db := openHITLTestDB(t)
	seedInterrupt(t, db, interruptSeed{id: "old-1", status: "decided", decision: "approve", created: mustTime("2026-01-01 00:00:00"), decided: mustTime("2026-01-01 00:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "new-1", status: "decided", decision: "approve", created: mustTime("2026-09-29 00:00:00"), decided: mustTime("2026-09-29 00:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "waiting", status: "pending", reviewer: "human", created: mustTime("2025-01-01 00:00:00")})
	s := NewHITL(db)

	deleted, err := s.PurgeDecidedBefore(time.Now().AddDate(0, 0, -90))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	for id, wantFound := range map[string]bool{"old-1": false, "new-1": true, "waiting": true} {
		_, found, err := s.Get(id)
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if found != wantFound {
			t.Errorf("%s found=%v, want %v", id, found, wantFound)
		}
	}
}

func TestHITLPendingApprovals(t *testing.T) {
	db := openHITLTestDB(t)
	seedConversation(t, db, "c1", "u1", "")
	seedConversation(t, db, "c2", "u2", "")
	seedInterrupt(t, db, interruptSeed{id: "older", conversation: "c1", status: "pending", reviewer: "human", tool: "exec", created: mustTime("2026-09-01 08:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "newer", conversation: "c1", status: "pending", reviewer: "human", tool: "http_get", created: mustTime("2026-09-02 08:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "theirs", conversation: "c2", status: "pending", reviewer: "human", created: mustTime("2026-09-03 08:00:00")})
	seedInterrupt(t, db, interruptSeed{id: "decided", conversation: "c1", status: "decided", decision: "approve", created: mustTime("2026-09-04 08:00:00")})
	s := NewHITL(db)

	items, err := s.PendingApprovals(10, Access{UserID: "u1", Scope: ScopeOwn})
	if err != nil {
		t.Fatalf("pending approvals: %v", err)
	}
	if len(items) != 2 || items[0].ID != "newer" || items[1].ID != "older" {
		t.Fatalf("items = %+v, want newer then older, scoped to u1", items)
	}
	if items[0].ToolName != "http_get" || items[0].ConversationID != "c1" {
		t.Fatalf("item = %+v, want the tool and conversation carried through", items[0])
	}
	if items[0].CreatedAtSec <= 0 {
		t.Fatalf("created_at came back as %d, want epoch seconds", items[0].CreatedAtSec)
	}

	limited, err := s.PendingApprovals(1, Access{Scope: ScopeAll})
	if err != nil {
		t.Fatalf("limited: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != "theirs" {
		t.Fatalf("limit 1 = %+v, want the newest row across all conversations", limited)
	}

	none, err := s.PendingApprovals(0, Access{Scope: ScopeAll})
	if err != nil || len(none) != 0 {
		t.Fatalf("limit 0 = (%v, %v), want empty and no error", none, err)
	}
	anonymous, err := s.PendingApprovals(10, Access{})
	if err != nil || len(anonymous) != 0 {
		t.Fatalf("no session = (%v, %v), want nothing announced", anonymous, err)
	}
}

func TestHITLRefusesAConnectionlessStore(t *testing.T) {
	s := NewHITL(nil)
	if _, _, err := s.List(InterruptsLog, InterruptFilter{}); err == nil {
		t.Error("List should fail without a database")
	}
	if _, _, err := s.Get("i1"); err == nil {
		t.Error("Get should fail without a database")
	}
	if _, _, err := s.ConversationOwner("i1"); err == nil {
		t.Error("ConversationOwner should fail without a database")
	}
	if _, err := s.ConversationOwners([]string{"i1"}); err == nil {
		t.Error("ConversationOwners should fail without a database")
	}
	if err := s.MutatePayload("i1", func(string) (string, bool, error) { return "", false, nil }); err == nil {
		t.Error("MutatePayload should fail without a database")
	}
	if err := s.RecordAgentDecision("i1", "approve", "", time.Now()); err == nil {
		t.Error("RecordAgentDecision should fail without a database")
	}
	if _, err := s.Dismiss("i1", "x"); err == nil {
		t.Error("Dismiss should fail without a database")
	}
	if _, err := s.DeleteLogsByIDs([]string{"i1"}); err == nil {
		t.Error("DeleteLogsByIDs should fail without a database")
	}
	if _, err := s.DeleteLogs(InterruptsLog, InterruptFilter{}); err == nil {
		t.Error("DeleteLogs should fail without a database")
	}
	if _, err := s.PurgeDecidedBefore(time.Now()); err == nil {
		t.Error("PurgeDecidedBefore should fail without a database")
	}
	if _, err := s.PendingApprovals(5, Access{Scope: ScopeAll}); err == nil {
		t.Error("PendingApprovals should fail without a database")
	}
	var nilStore *HITL
	if _, _, err := nilStore.List(InterruptsLog, InterruptFilter{}); err == nil {
		t.Error("a nil store should fail instead of panicking")
	}
}
