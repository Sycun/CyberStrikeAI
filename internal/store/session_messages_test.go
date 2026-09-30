package store

import (
	"database/sql"
	"testing"
	"time"
)

// These three statements are what the HTTP layer used to repeat: fifteen copies of one
// UPDATE across six files, plus a near-twin CASE append whose two copies differed by a
// single clause. The tests below pin that difference, because a refactor that merged the two
// arms would compile and pass every existing check while quietly appending a partial answer
// underneath a spinner instead of replacing it.

func seedContentMessage(t *testing.T, db *sql.DB, id, content string, updatedAt time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO messages (id, conversation_id, role, content, created_at, updated_at)
VALUES (?, 'c1', 'assistant', ?, ?, ?)`, id, content, updatedAt.Add(-time.Hour), updatedAt); err != nil {
		t.Fatalf("seed message: %v", err)
	}
}

func readContentMessage(t *testing.T, db *sql.DB, id string) (string, time.Time) {
	t.Helper()
	var content string
	var at time.Time
	if err := db.QueryRow("SELECT content, updated_at FROM messages WHERE id = ?", id).Scan(&content, &at); err != nil {
		t.Fatalf("read message: %v", err)
	}
	return content, at
}

func TestSetMessageContentReplacesAndStamps(t *testing.T) {
	db := openSessionTestDB(t)
	s := NewSession(db)
	before := time.Now()
	seedContentMessage(t, db, "m1", "处理中...", before.Add(-time.Hour))

	rows, err := s.SetMessageContent("m1", "执行失败: boom")
	if err != nil {
		t.Fatalf("SetMessageContent: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows affected = %d, want 1", rows)
	}
	content, at := readContentMessage(t, db, "m1")
	if content != "执行失败: boom" {
		t.Fatalf("content = %q, want the replacement", content)
	}
	if !at.After(before.Add(-time.Minute)) {
		t.Fatalf("updated_at = %v, want it touched", at)
	}
}

func TestSetMessageContentUnknownIDReportsZero(t *testing.T) {
	db := openSessionTestDB(t)
	s := NewSession(db)
	rows, err := s.SetMessageContent("no-such-message", "x")
	if err != nil {
		t.Fatalf("an absent message must not be an error: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rows affected = %d, want 0", rows)
	}
}

func TestSessionWithoutDatabaseFailsClosed(t *testing.T) {
	var s *Session
	if _, err := s.SetMessageContent("m1", "x"); err == nil {
		t.Fatal("a nil store accepted a write")
	}
	if _, err := (*Session)(nil).AppendNotice("m1", "x"); err == nil {
		t.Fatal("a nil store appended a notice")
	}
}

// TestAppendNoticeVersusAppendPartialOnCancel is the twin-pair contract. The two statements
// the HTTP layer carried differed by exactly one clause - whether an untouched placeholder
// counts as empty - and merging them wrongly would silently append a partial answer
// underneath a spinner instead of replacing it.
//
// The separator is the four characters `\n\n`, not a newline: SQLite applies no escape
// processing inside string literals, and this is what the pre-refactor statements stored, so
// the tests below assert it verbatim rather than what one might wish the SQL to do.
func TestAppendNoticeVersusAppendPartialOnCancel(t *testing.T) {
	const sep = `\n\n`
	db := openSessionTestDB(t)
	s := NewSession(db)
	now := time.Now()
	seedContentMessage(t, db, "empty", "", now)
	seedContentMessage(t, db, "placeholder", "处理中...", now)
	seedContentMessage(t, db, "body", "已经生成的正文", now)
	seedContentMessage(t, db, "cancel", "处理中...", now)

	append := func(id, fragment string) {
		t.Helper()
		if _, err := s.AppendNotice(id, fragment); err != nil {
			t.Fatalf("AppendNotice(%s): %v", id, err)
		}
	}

	const notice = "⚠️ 注意"
	append("empty", notice)
	append("placeholder", notice)
	append("body", notice)
	append("body", notice) // the INSTR arm: the same notice must not land twice

	if _, err := s.AppendPartialOnCancel("cancel", "半截回答"); err != nil {
		t.Fatalf("AppendPartialOnCancel: %v", err)
	}
	if _, err := s.AppendPartialOnCancel("body", "已经生成的正文"); err != nil {
		t.Fatalf("AppendPartialOnCancel into a body that contains it: %v", err)
	}

	for _, tc := range []struct {
		id   string
		want string
		why  string
	}{
		{"empty", notice, "an empty message takes the notice as its content"},
		{"placeholder", "处理中..." + sep + notice, "a notice is commentary, so the placeholder stays"},
		{"body", "已经生成的正文" + sep + notice, "generated content is never overwritten, nor appended twice"},
		{"cancel", "半截回答", "the cancel path owns the only answer that will arrive, so it replaces"},
	} {
		if got, _ := readContentMessage(t, db, tc.id); got != tc.want {
			t.Errorf("%s: content = %q, want %q (%s)", tc.id, got, tc.want, tc.why)
		}
	}
}

func TestAppendFragmentReportsMissingMessage(t *testing.T) {
	db := openSessionTestDB(t)
	s := NewSession(db)
	rows, err := s.AppendNotice("no-such-message", "note")
	if err != nil {
		t.Fatalf("AppendNotice: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rows affected = %d, want 0", rows)
	}
	if _, err := (*Session)(nil).AppendNotice("m", "n"); err == nil {
		t.Fatal("a store without a database must refuse the write")
	}
}
