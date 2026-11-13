package deepassessment

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"cyberstrike-ai/internal/database"
	"github.com/google/uuid"
)

var ErrConflict = errors.New("task changed; refresh and retry")

type Store struct {
	DB *database.DB
	mu sync.Mutex
}

func (s *Store) Create(t *Task) error {
	if err := t.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO deep_assessments(id,owner_user_id,project_id,name,status,payload,updated_at) VALUES(?,?,?,?,?,?,?)`, t.ID, t.OwnerID, t.ProjectID, t.Name, t.Status, string(b), t.UpdatedAt); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO rbac_resource_assignments(id,user_id,resource_type,resource_id,created_at) VALUES(?,?,?,?,?)`, uuid.NewString(), t.OwnerID, "deep_assessment", t.ID, t.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Get(id string) (*Task, error) {
	var b string
	if err := s.DB.QueryRow(`SELECT payload FROM deep_assessments WHERE id=?`, id).Scan(&b); err != nil {
		return nil, err
	}
	var t Task
	err := json.Unmarshal([]byte(b), &t)
	return &t, err
}

// State and lifecycle events commit together, before any dependent run starts.
func (s *Store) Change(id, step, kind, message string, fn func(*Task) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var b string
	if err = tx.QueryRow(`SELECT payload FROM deep_assessments WHERE id=?`, id).Scan(&b); err != nil {
		return err
	}
	var t Task
	if err = json.Unmarshal([]byte(b), &t); err != nil {
		return err
	}
	if err = fn(&t); err != nil {
		return err
	}
	t.Revision++
	t.UpdatedAt = time.Now().UTC()
	out, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE deep_assessments SET name=?,status=?,payload=?,updated_at=? WHERE id=?`, t.Name, t.Status, string(out), t.UpdatedAt, id); err != nil {
		return err
	}
	if kind != "" {
		if _, err = tx.Exec(`INSERT INTO deep_assessment_events(task_id,step_id,kind,message,created_at) VALUES(?,?,?,?,?)`, id, step, kind, Clip(message, 16000), t.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) List(user, scope, status, query string, limit, offset int) ([]*Task, error) {
	q := `SELECT json_remove(payload,'$.steps','$.guidance','$.scope','$.criteria','$.objective','$.strategy','$.question') FROM deep_assessments t WHERE 1=1`
	args := []any{}
	if scope != database.RBACScopeAll {
		assignment := `EXISTS(SELECT 1 FROM rbac_resource_assignments r WHERE r.user_id=? AND r.resource_type='deep_assessment' AND r.resource_id=t.id)`
		if scope == database.RBACScopeOwn {
			q += ` AND (t.owner_user_id=? OR ` + assignment + `)`
			args = append(args, user, user)
		} else {
			q += ` AND ` + assignment
			args = append(args, user)
		}
	}
	if status != "" {
		q += ` AND t.status=?`
		args = append(args, status)
	}
	if query != "" {
		q += ` AND (instr(lower(t.name),lower(?))>0 OR instr(t.id,?)>0)`
		args = append(args, query, query)
	}
	q += ` ORDER BY updated_at DESC,id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		var b string
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var t Task
		if err = json.Unmarshal([]byte(b), &t); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (s *Store) Events(id string, after int64) ([]Event, error) {
	rows, err := s.DB.Query(`SELECT id,step_id,kind,message,created_at FROM deep_assessment_events WHERE task_id=? AND id>? ORDER BY id LIMIT 100`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	size := 0
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.ID, &e.StepID, &e.Kind, &e.Message, &e.CreatedAt); err != nil {
			return nil, err
		}
		if size+len(e.Message) > 512<<10 && len(out) > 0 {
			break
		}
		size += len(e.Message)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.Exec(`DELETE FROM deep_assessments WHERE id=? AND status NOT IN ('running','pausing','cancelling')`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrConflict
	}
	if _, err = tx.Exec(`DELETE FROM rbac_resource_assignments WHERE resource_type='deep_assessment' AND resource_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Recover prepares safe segments for automatic continuation. A call with no
// committed receipt remains uncertain and is never automatically replayed.
func (s *Store) Recover() error {
	rows, err := s.DB.Query(`SELECT id FROM deep_assessments WHERE status IN ('running','pausing','cancelling','recovering','waiting_retry')`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		unknown, err := s.UncertainSteps(id)
		if err != nil {
			return err
		}
		if err = s.Change(id, "", "recovered", "已恢复任务记录和 Pi 会话引用，检查未完成调用。", func(t *Task) error {
			accountTime(t)
			prior := t.Status
			t.Status = "paused"
			if prior == "cancelling" {
				t.Status = "cancelled"
				return nil
			}
			for i := range t.Steps {
				st := &t.Steps[i]
				if unknown[st.ID] {
					st.Status = "uncertain"
				} else if st.Status == "running" || st.Status == "interrupted" {
					st.Status = "pending"
				}
			}
			if len(unknown) > 0 {
				t.Status = "needs_review"
				t.Summary = "存在执行结果不明的工具调用，请核对回执后继续。"
				return nil
			}
			if t.SecondsLeft() == 0 {
				t.Status = "budget_exhausted"
				return nil
			}
			if t.AutoResume && (prior != "pausing" || t.RestartPending) {
				t.Status = "recovering"
			}
			return nil
		}); err != nil {
			return fmt.Errorf("recover assessment %s: %w", id, err)
		}
	}
	return nil
}
func accountTime(t *Task) {
	if t.Deadline != nil {
		t.RemainingSeconds = t.SecondsLeft()
	} else if t.RunStartedAt != nil {
		t.RemainingSeconds -= int64(time.Since(*t.RunStartedAt).Seconds())
		if t.RemainingSeconds < 0 {
			t.RemainingSeconds = 0
		}
	}
	t.RunStartedAt = nil
}
