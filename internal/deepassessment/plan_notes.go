package deepassessment

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"cyberstrike-ai/internal/promptguard"
)

const maxPlanNotesRunes = 8000

type PlanNotes struct {
	Revision  int64     `json:"revision"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) GetPlanNotes(taskID string) (PlanNotes, error) {
	var n PlanNotes
	err := s.DB.QueryRow(`SELECT revision, body, updated_at FROM deep_assessment_plan_notes WHERE task_id=?`, taskID).
		Scan(&n.Revision, &n.Body, &n.UpdatedAt)
	if err == sql.ErrNoRows {
		return PlanNotes{}, nil
	}
	return n, err
}

func (s *Store) SavePlanNotes(taskID string, expectedRevision int64, body string) (PlanNotes, error) {
	if expectedRevision < 0 {
		return PlanNotes{}, fmt.Errorf("revision must be >= 0")
	}
	body = strings.TrimSpace(promptguard.Neutralize(body))
	if utf8.RuneCountInString(body) > maxPlanNotesRunes {
		return PlanNotes{}, fmt.Errorf("plan notes exceed %d characters", maxPlanNotesRunes)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return PlanNotes{}, err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRow(`SELECT COALESCE((SELECT revision FROM deep_assessment_plan_notes WHERE task_id=?),0)`, taskID).Scan(&current)
	if err != nil {
		return PlanNotes{}, err
	}
	if current != expectedRevision {
		return PlanNotes{}, ErrConflict
	}
	now := time.Now().UTC()
	next := current + 1
	if _, err = tx.Exec(`INSERT INTO deep_assessment_plan_notes(task_id,revision,body,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(task_id) DO UPDATE SET revision=excluded.revision,body=excluded.body,updated_at=excluded.updated_at`,
		taskID, next, body, now); err != nil {
		return PlanNotes{}, err
	}
	if err = tx.Commit(); err != nil {
		return PlanNotes{}, err
	}
	return PlanNotes{Revision: next, Body: body, UpdatedAt: now}, nil
}
