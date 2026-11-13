package taskflow

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
	if _, err = tx.Exec(`INSERT INTO long_tasks(id,owner_user_id,project_id,name,status,payload,updated_at) VALUES(?,?,?,?,?,?,?)`, t.ID, t.OwnerID, t.ProjectID, t.Name, t.Status, string(b), t.UpdatedAt); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO rbac_resource_assignments(id,user_id,resource_type,resource_id,created_at) VALUES(?,?,?,?,?)`, uuid.NewString(), t.OwnerID, "long_task", t.ID, t.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Get(id string) (*Task, error) {
	var b string
	if err := s.DB.QueryRow(`SELECT payload FROM long_tasks WHERE id=?`, id).Scan(&b); err != nil {
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
	if err = tx.QueryRow(`SELECT payload FROM long_tasks WHERE id=?`, id).Scan(&b); err != nil {
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
	if _, err = tx.Exec(`UPDATE long_tasks SET name=?,status=?,payload=?,updated_at=? WHERE id=?`, t.Name, t.Status, string(out), t.UpdatedAt, id); err != nil {
		return err
	}
	if kind != "" {
		if _, err = tx.Exec(`INSERT INTO long_task_events(task_id,step_id,kind,message,created_at) VALUES(?,?,?,?,?)`, id, step, kind, Clip(message, 16000), t.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) List(user, scope, status, query string, limit, offset int) ([]*Task, error) {
	q := `SELECT json_remove(payload,'$.steps','$.guidance','$.scope','$.criteria','$.objective','$.strategy','$.question') FROM long_tasks t WHERE 1=1`
	args := []any{}
	if scope != database.RBACScopeAll {
		assignment := `EXISTS(SELECT 1 FROM rbac_resource_assignments r WHERE r.user_id=? AND r.resource_type='long_task' AND r.resource_id=t.id)`
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
	rows, err := s.DB.Query(`SELECT id,step_id,kind,message,created_at FROM long_task_events WHERE task_id=? AND id>? ORDER BY id LIMIT 100`, id, after)
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
	r, err := tx.Exec(`DELETE FROM long_tasks WHERE id=? AND status NOT IN ('running','pausing','cancelling')`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrConflict
	}
	if _, err = tx.Exec(`DELETE FROM rbac_resource_assignments WHERE resource_type='long_task' AND resource_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// A restarted process cannot know whether an in-flight tool took effect.
// Preserve the attempt and require a deliberate retry or alternative plan.
func (s *Store) Recover() error {
	rows, err := s.DB.Query(`SELECT id FROM long_tasks WHERE status IN ('running','pausing','cancelling')`)
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
		if err = s.Change(id, "", "recovered", "服务重启，已保存进度；中断步骤需检查后重试。", func(t *Task) error {
			accountTime(t)
			if t.Status == "cancelling" {
				t.Status = "cancelled"
			} else {
				t.Status = "paused"
			}
			t.Summary = "服务重启后已暂停，未自动重放未确认的工具操作。"
			for i := range t.Steps {
				if t.Steps[i].Status == "running" {
					t.Steps[i].Status = "interrupted"
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("recover task %s: %w", id, err)
		}
	}
	return nil
}

func accountTime(t *Task) {
	if t.RunStartedAt != nil {
		elapsed := int64(time.Since(*t.RunStartedAt).Seconds())
		if elapsed > 0 {
			t.RemainingSeconds -= elapsed
		}
		if t.RemainingSeconds < 0 {
			t.RemainingSeconds = 0
		}
		t.RunStartedAt = nil
	}
}
