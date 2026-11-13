package deepassessment

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/agentruntime"
)

func (s *Store) RetryStep(id, step string, revision int64, reconciled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRow(`SELECT payload FROM deep_assessments WHERE id=?`, id).Scan(&raw); err != nil {
		return err
	}
	var t Task
	if err = json.Unmarshal([]byte(raw), &t); err != nil {
		return err
	}
	if t.Revision != revision || Active(t.Status) || t.Status == "completed" || t.Status == "cancelled" {
		return ErrConflict
	}
	st := t.Step(step)
	if st == nil {
		return fmt.Errorf("unknown step")
	}
	if st.Status != "failed" && st.Status != "blocked" && st.Status != "interrupted" && st.Status != "uncertain" && st.Status != "cleanup_unconfirmed" {
		return ErrConflict
	}
	if (st.Status == "uncertain" || st.Status == "cleanup_unconfirmed") && !reconciled {
		return fmt.Errorf("review the uncertain execution before retrying")
	}
	if _, err = tx.Exec(`UPDATE deep_assessment_calls SET status='reconciled' WHERE task_id=? AND step_id=? AND status IN ('running','uncertain')`, id, step); err != nil {
		return err
	}
	st.Status = "pending"
	st.StalledSegments = 0
	t.Status = "paused"
	t.RetryCount = 0
	t.Revision++
	t.UpdatedAt = time.Now().UTC()
	body, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE deep_assessments SET status=?,payload=?,updated_at=? WHERE id=?`, t.Status, string(body), t.UpdatedAt, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO deep_assessment_events(task_id,step_id,kind,message,created_at) VALUES(?,?,'retry','用户核对后恢复原会话执行。',?)`, id, step, t.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ValidateEvidence(task string, ids []string) error {
	if len(ids) == 0 || len(ids) > 32 {
		return fmt.Errorf("completed requires 1–32 real evidence receipt IDs")
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("empty evidence reference")
		}
		var n int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM deep_assessment_calls WHERE task_id=? AND (id=? OR execution_id=?) AND status='completed'`, task, id, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("receipt %s is not a successful operation in this assessment", id)
		}
	}
	return nil
}

// Intent is committed before dispatching a tool. The receipt is committed before
// its result goes back to Pi. A process loss between the two remains visible.
func (s *Store) BeginCall(task, step string, call agentruntime.ToolCall) error {
	b, err := json.Marshal(call.Arguments)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO deep_assessment_calls(id,task_id,step_id,tool,arguments,status,updated_at,session_entry_id,provider_id) VALUES(?,?,?,?,?,'running',?,?,?)`, call.ID, task, step, call.Name, string(b), time.Now().UTC(), call.SessionEntryID, call.ProviderID)
	return err
}
func (s *Store) FinishCall(call agentruntime.ToolCall, result agentruntime.ToolResult, uncertain bool) error {
	state := "completed"
	if result.IsError {
		state = "failed"
	}
	if uncertain {
		state = "uncertain"
	}
	_, err := s.DB.Exec(`UPDATE deep_assessment_calls SET status=?,execution_id=?,result=?,updated_at=? WHERE id=?`, state, result.ExecutionID, Clip(result.Content, 16000), time.Now().UTC(), call.ID)
	return err
}
func (s *Store) UncertainSteps(id string) (map[string]bool, error) {
	rows, err := s.DB.Query(`SELECT DISTINCT step_id FROM deep_assessment_calls WHERE task_id=? AND status IN ('running','uncertain')`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var step string
		if err = rows.Scan(&step); err != nil {
			return nil, err
		}
		out[step] = true
	}
	return out, rows.Err()
}
func (s *Store) CallCount(id, step string) (int, error) {
	var count int
	err := s.DB.QueryRow(`SELECT COUNT(DISTINCT json_array(tool,arguments,result)) FROM deep_assessment_calls WHERE task_id=? AND step_id=? AND status='completed'`, id, step).Scan(&count)
	return count, err
}
func (s *Store) RecordMetrics(id, step string, data map[string]any) error {
	session, _ := data["sessionId"].(string)
	if session == "" {
		return fmt.Errorf("missing Pi session identity")
	}
	number := func(key string) int64 { v, _ := data[key].(float64); return int64(v) }
	m := SessionMetrics{StepID: step, Tokens: number("tokens"), Compactions: int(number("compactions")), ToolCount: int(number("toolCount"))}
	m.ToolDefinitionBytes = number("toolDefinitionBytes")
	if context, ok := data["context"].(map[string]any); ok {
		if n, ok := context["tokens"].(float64); ok {
			value := int64(n)
			m.ContextTokens = &value
		}
		if n, ok := context["contextWindow"].(float64); ok {
			m.ContextWindow = int64(n)
		}
	}
	return s.Change(id, "", "", "", func(t *Task) error {
		if t.Metrics == nil {
			t.Metrics = map[string]SessionMetrics{}
		}
		old := t.Metrics[session]
		if m.Tokens < old.Tokens {
			return nil
		}
		t.TokensUsed += m.Tokens - old.Tokens
		t.Metrics[session] = m
		return nil
	})
}

// Only known results may repair a missing Pi tool result. Unknown calls remain
// gated by Recover; identity includes the native entry, not just a provider ID.
func (s *Store) NativeReceipts(task, step string) ([]agentruntime.NativeReceipt, error) {
	rows, err := s.DB.Query(`SELECT session_entry_id,provider_id,tool,result,execution_id,status FROM deep_assessment_calls WHERE task_id=? AND step_id=? AND session_entry_id<>'' AND status IN ('completed','failed') ORDER BY rowid DESC LIMIT 128`, task, step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []agentruntime.NativeReceipt{}
	for rows.Next() {
		var receipt agentruntime.NativeReceipt
		var state string
		if err = rows.Scan(&receipt.SessionEntryID, &receipt.ProviderID, &receipt.Name, &receipt.Content, &receipt.ExecutionID, &state); err != nil {
			return nil, err
		}
		receipt.IsError = state != "completed"
		out = append(out, receipt)
	}
	return out, rows.Err()
}
