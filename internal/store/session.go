package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Session owns the durable conversation rows an interrupted run leaves behind:
// the assistant placeholder in `messages` and the terminal event recorded in
// `process_details`. The HITL domain decides *why* a run ended; this store is the
// only place allowed to rewrite the placeholder and append its evidence.
type Session struct {
	db *sql.DB
}

// NewSession binds the store to a connection.
func NewSession(db *sql.DB) *Session {
	return &Session{db: db}
}

func (s *Session) requireDB() error {
	if s == nil || s.db == nil {
		return errors.New("store: session requires a database")
	}
	return nil
}

// InterruptedPlaceholder is an assistant message still showing a "working"
// placeholder while the evidence says its run is over.
type InterruptedPlaceholder struct {
	MessageID       string
	ConversationID  string
	TerminalEvent   string
	HITLStatus      string
	HITLDecision    string
	DecisionComment string
	InterruptedAt   string
}

// interruptedPlaceholderQuery finds placeholders with explicit proof of being over:
// a terminal HITL row, a terminal process event, or a later message in the same
// conversation. The evidence requirement is what keeps this from rewriting a
// placeholder another runtime could still recover.
const interruptedPlaceholderQuery = `
SELECT msg.id, msg.conversation_id,
       COALESCE((
           SELECT pd.event_type
           FROM process_details pd
           WHERE pd.message_id = msg.id
             AND pd.event_type IN ('cancelled', 'timeout', 'error')
           ORDER BY pd.created_at DESC LIMIT 1
       ), '') AS terminal_event,
       COALESCE((
           SELECT hi.status
           FROM hitl_interrupts hi
           WHERE hi.message_id = msg.id
           ORDER BY COALESCE(hi.decided_at, hi.created_at) DESC LIMIT 1
       ), '') AS hitl_status,
       COALESCE((
           SELECT hi.decision
           FROM hitl_interrupts hi
           WHERE hi.message_id = msg.id
           ORDER BY COALESCE(hi.decided_at, hi.created_at) DESC LIMIT 1
       ), '') AS hitl_decision,
       COALESCE((
           SELECT hi.decision_comment
           FROM hitl_interrupts hi
           WHERE hi.message_id = msg.id
           ORDER BY COALESCE(hi.decided_at, hi.created_at) DESC LIMIT 1
       ), '') AS decision_comment,
       COALESCE((
           SELECT MAX(COALESCE(hi.decided_at, hi.created_at))
           FROM hitl_interrupts hi
           WHERE hi.message_id = msg.id
       ), (
           SELECT MIN(later.created_at)
           FROM messages later
           WHERE later.conversation_id = msg.conversation_id
             AND later.created_at > msg.created_at
       ), (
           SELECT MAX(pd.created_at)
           FROM process_details pd
           WHERE pd.message_id = msg.id
       ), msg.updated_at, msg.created_at) AS interrupted_at
FROM messages msg
WHERE msg.role = 'assistant'
  AND TRIM(msg.content) IN ('处理中...', 'Processing...')
  AND (
      EXISTS (
          SELECT 1 FROM hitl_interrupts hi
          WHERE hi.message_id = msg.id
            AND (hi.status IN ('cancelled', 'timeout')
                 OR (hi.status = 'decided' AND hi.decision = 'reject'))
      )
      OR EXISTS (
          SELECT 1 FROM process_details pd
          WHERE pd.message_id = msg.id
            AND pd.event_type IN ('cancelled', 'timeout', 'error')
      )
      OR EXISTS (
          SELECT 1 FROM messages later
          WHERE later.conversation_id = msg.conversation_id
            AND later.created_at > msg.created_at
      )
  )`

// InterruptedPlaceholders lists the rows a restart has to finish.
func (s *Session) InterruptedPlaceholders() ([]InterruptedPlaceholder, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(interruptedPlaceholderQuery)
	if err != nil {
		return nil, fmt.Errorf("scan interrupted assistant placeholders: %w", err)
	}
	defer rows.Close()

	items := make([]InterruptedPlaceholder, 0)
	for rows.Next() {
		var item InterruptedPlaceholder
		if err := rows.Scan(&item.MessageID, &item.ConversationID, &item.TerminalEvent,
			&item.HITLStatus, &item.HITLDecision, &item.DecisionComment, &item.InterruptedAt); err != nil {
			return nil, fmt.Errorf("scan interrupted assistant placeholder: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// InterruptedUpdate is the terminal state to write for one placeholder: the notice
// the user sees, the event that explains it, and the timestamp the interruption is
// pinned to.
type InterruptedUpdate struct {
	MessageID      string
	ConversationID string
	EventType      string
	Notice         string
	Reason         string
	InterruptedAt  string
}

// FinalizeInterruptedPlaceholders rewrites each placeholder and records its terminal
// event in one transaction, and reports how many placeholders were actually still
// untouched. A placeholder that someone else updated in the meantime is skipped by
// the content guard, so a restart cannot overwrite a finished answer.
func (s *Session) FinalizeInterruptedPlaceholders(updates []InterruptedUpdate) (int, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	if len(updates) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin interruption finalize: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	finished := 0
	for _, u := range updates {
		detail, err := json.Marshal(map[string]string{"reason": u.Reason, "status": u.EventType})
		if err != nil {
			return 0, fmt.Errorf("encode interruption detail for %s: %w", u.MessageID, err)
		}
		res, err := tx.Exec(`
UPDATE messages
SET content = ?, updated_at = ?
WHERE id = ? AND TRIM(content) IN ('处理中...', 'Processing...')`,
			u.Notice, u.InterruptedAt, u.MessageID)
		if err != nil {
			return 0, fmt.Errorf("finalize message %s: %w", u.MessageID, err)
		}
		updated, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count finalized messages: %w", err)
		}
		if updated == 0 {
			continue
		}
		finished++
		if _, err := tx.Exec(`
INSERT INTO process_details (id, message_id, conversation_id, event_type, message, data, created_at)
SELECT ?, ?, ?, ?, ?, ?, ?
WHERE NOT EXISTS (
    SELECT 1 FROM process_details
    WHERE message_id = ? AND event_type IN ('cancelled', 'timeout', 'error')
)`, uuid.NewString(), u.MessageID, u.ConversationID, u.EventType, u.Notice, string(detail),
			u.InterruptedAt, u.MessageID); err != nil {
			return 0, fmt.Errorf("record interruption event for %s: %w", u.MessageID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit interruption finalize: %w", err)
	}
	return finished, nil
}

// SetMessageContent rewrites one message's body and stamps updated_at. It is the single
// statement the HTTP layer used to repeat at fifteen sites: every path that ends a run
// abnormally (task already running, wait timeout, client failure, batch failure, finalizer
// refusal) overwrites the assistant placeholder it had already inserted.
//
// It returns the rows it touched so callers can tell "no such message" from success; the
// run paths deliberately ignore that, since the message is a nicety and the SSE error frame
// is the answer.
func (s *Session) SetMessageContent(id, content string) (int64, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	res, err := s.db.Exec("UPDATE messages SET content = ?, updated_at = ? WHERE id = ?", content, time.Now(), id)
	if err != nil {
		return 0, fmt.Errorf("update message content %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count message content update: %w", err)
	}
	return n, nil
}

// appendFragmentSQL is one CASE expression with two callers in the HTTP layer that used to
// carry their own copy of it. They differed by exactly one clause - whether an untouched
// placeholder counts as empty - and were otherwise character-for-character the same
// statement, which is the shape this refactor is meant to collapse.
const appendFragmentSQL = `UPDATE messages
 SET content = CASE
  WHEN content IS NULL OR TRIM(content) = ''%s THEN ?
  WHEN INSTR(content, ?) > 0 THEN content
  ELSE content || '\n\n' || ?
 END,
 updated_at = ?
 WHERE id = ?`

// emptyWhenPlaceholder is the clause that also treats an untouched "working" placeholder as
// empty, so a fragment replaces it instead of being appended under the spinner.
const emptyWhenPlaceholder = ` OR TRIM(content) = '处理中...'`

// AppendNotice adds a notice to the end of a message: a fresh message takes the notice as
// its content, a message that already contains it is left alone, otherwise the notice is
// appended after a blank line. An untouched placeholder is *not* treated as empty here,
// because a notice is commentary and the body that follows it still matters.
func (s *Session) AppendNotice(id, notice string) (int64, error) {
	return s.appendFragment(id, notice, "")
}

// AppendPartialOnCancel is AppendNotice for the case where the run was cancelled and the
// fragment is the only answer that will ever arrive: an untouched placeholder is replaced
// rather than annotated.
func (s *Session) AppendPartialOnCancel(id, partial string) (int64, error) {
	return s.appendFragment(id, partial, emptyWhenPlaceholder)
}

func (s *Session) appendFragment(id, fragment, emptyClause string) (int64, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(fmt.Sprintf(appendFragmentSQL, emptyClause),
		fragment, fragment, fragment, time.Now(), id)
	if err != nil {
		return 0, fmt.Errorf("append fragment to message %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count message fragment append: %w", err)
	}
	return n, nil
}
