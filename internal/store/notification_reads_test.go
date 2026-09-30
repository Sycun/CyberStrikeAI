package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestReadStatesAreScopedPerUser(t *testing.T) {
	reads := NewNotificationReads(openDB(t))
	if err := reads.EnsureSchema(); err != nil {
		t.Fatal(err)
	}

	ids := []string{"vuln:v1", "vuln:v2", "exec_failed:e1"}
	if n, err := reads.MarkRead("u1", ids); err != nil {
		t.Fatal(err)
	} else if n != 3 {
		t.Fatalf("marked %d, want 3", n)
	}

	u1, err := reads.ReadStates("u1", ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(u1) != 3 {
		t.Fatalf("u1 read states = %v", u1)
	}

	// The same events must not look read to another user: the primary key is
	// (user_id, event_id) and a leak here would hide notifications from someone who
	// never saw them.
	u2, err := reads.ReadStates("u2", ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(u2) != 0 {
		t.Fatalf("read state leaked across users: %v", u2)
	}

	if got, err := reads.ReadStates("u1", nil); err != nil || len(got) != 0 {
		t.Fatalf("empty id list should be inert: %v %v", got, err)
	}
	if got, err := reads.ReadStates("  ", ids); err != nil || len(got) != 0 {
		t.Fatalf("blank user should be inert: %v %v", got, err)
	}
}

// TestOnlyInformationalEventsAreMarkable locks the policy that actionable
// notifications cannot be dismissed by marking them read.
func TestOnlyInformationalEventsAreMarkable(t *testing.T) {
	reads := NewNotificationReads(openDB(t))
	if err := reads.EnsureSchema(); err != nil {
		t.Fatal(err)
	}

	mixed := []string{
		"vuln:v1",
		"exec_failed:e1",
		"task_completed:t1",
		"c2evt:c1",
		"hitl:i1",           // actionable: pending approval must not be hideable
		"anything_else:x",   // unknown shape
		"",                  // blank
		"  ",                // whitespace
		"vuln:not-yet-read", // markable, unusual suffix
	}
	marked, err := reads.MarkRead("u1", mixed)
	if err != nil {
		t.Fatal(err)
	}
	if marked != 5 {
		t.Fatalf("marked %d, want the 5 informational ids", marked)
	}
	states, err := reads.ReadStates("u1", mixed)
	if err != nil {
		t.Fatal(err)
	}
	if states["hitl:i1"] {
		t.Error("an actionable event was marked read, hiding work that still needs a decision")
	}
	if states["anything_else:x"] {
		t.Error("an unknown event shape was accepted")
	}
	if !states["vuln:v1"] || !states["c2evt:c1"] {
		t.Error("a markable id did not persist")
	}
}

func TestMarkReadIsIdempotent(t *testing.T) {
	reads := NewNotificationReads(openDB(t))
	if err := reads.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	ids := []string{"vuln:v1"}
	for i := 0; i < 3; i++ {
		if _, err := reads.MarkRead("u1", ids); err != nil {
			t.Fatalf("retry %d failed: %v", i, err)
		}
	}
	db := reads.db
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_reads_by_user WHERE user_id = 'u1'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("re-marking created %d rows, want the upsert to keep 1", rows)
	}
}

func TestPruneKeepsTheMostRecentRows(t *testing.T) {
	reads := NewNotificationReads(openDB(t))
	if err := reads.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 12)
	for i := 0; i < 10; i++ {
		ids = append(ids, "vuln:v"+string(rune('a'+i)))
	}
	if _, err := reads.MarkRead("u1", ids); err != nil {
		t.Fatal(err)
	}
	if _, err := reads.MarkRead("u2", []string{"vuln:other"}); err != nil {
		t.Fatal(err)
	}

	if err := reads.Prune("u1", 4); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := reads.db.QueryRow(`SELECT COUNT(*) FROM notification_reads_by_user WHERE user_id = 'u1'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 4 {
		t.Fatalf("u1 kept %d rows, want 4", remaining)
	}
	// Pruning one user must not touch another's history.
	var other int
	if err := reads.db.QueryRow(`SELECT COUNT(*) FROM notification_reads_by_user WHERE user_id = 'u2'`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if other != 1 {
		t.Fatalf("pruning u1 affected u2 (%d rows)", other)
	}
	if err := reads.Prune("u1", 0); err != nil {
		t.Fatalf("a zero limit should be inert: %v", err)
	}
	if err := reads.Prune("", 5); err != nil {
		t.Fatalf("a blank user should be inert: %v", err)
	}
}

func TestStoreRefusesADisconnectedDatabase(t *testing.T) {
	empty := NewNotificationReads(nil)
	if err := empty.EnsureSchema(); err == nil {
		t.Fatal("EnsureSchema succeeded with no database")
	}
	if _, err := empty.MarkRead("u1", []string{"vuln:v1"}); err == nil {
		t.Fatal("MarkRead succeeded with no database")
	}
	if err := empty.Prune("u1", 10); err == nil {
		t.Fatal("Prune succeeded with no database")
	}
	if states, err := empty.ReadStates("u1", []string{"vuln:v1"}); err != nil || len(states) != 0 {
		t.Fatalf("ReadStates should be inert without a database: %v %v", states, err)
	}
	if _, err := NewNotificationReads(nil).MarkRead("", nil); err == nil {
		t.Fatal("a nil store accepted a write")
	}
}

func TestMarkReadRequiresAUser(t *testing.T) {
	reads := NewNotificationReads(openDB(t))
	if err := reads.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	if _, err := reads.MarkRead("   ", []string{"vuln:v1"}); err == nil {
		t.Fatal("read marks were accepted without an owner")
	}
}
