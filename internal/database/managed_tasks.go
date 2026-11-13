package database

// ManagedTaskRow is the shared task-list projection. Execution data stays with
// its existing owner so saved queues, schedules and conversations keep working.
type ManagedTaskRow struct {
	ID             string `json:"id"`
	Mode           string `json:"mode"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	ProjectID      string `json:"project_id"`
	Summary        string `json:"summary"`
	UpdatedAt      string `json:"updated_at"`
	CompletedSteps int    `json:"completed_steps"`
	TotalSteps     int    `json:"total_steps"`
	ScheduleMode   string `json:"schedule_mode"`
	NextRunAt      string `json:"next_run_at,omitempty"`
}

// ListManagedTasks merges before filtering/paging, avoiding gaps or duplicates
// caused by paginating the two sources independently. No execution traces enter
// the listing, and each source uses its own RBAC resource assignments.
func (db *DB) ListManagedTasks(user, scope, mode, status, query string, limit, offset int) ([]ManagedTaskRow, error) {
	access := func(alias, resource string) (string, []any) {
		if scope == RBACScopeAll {
			return "1=1", nil
		}
		assigned := `EXISTS(SELECT 1 FROM rbac_resource_assignments r WHERE r.user_id=? AND r.resource_type='` + resource + `' AND r.resource_id=` + alias + `.id)`
		if scope == RBACScopeOwn {
			return `(` + alias + `.owner_user_id=? OR ` + assigned + `)`, []any{user, user}
		}
		return assigned, []any{user}
	}
	goals, ga := access("g", "long_task")
	queues, qa := access("q", "batch_task")
	sql := `WITH entries AS (
  SELECT g.id,'goal' AS mode,g.name,g.status,g.project_id,
   substr(COALESCE(json_extract(g.payload,'$.summary'),''),1,2000) AS summary,
   COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ',g.updated_at),'') AS updated_at,
   (SELECT COUNT(*) FROM json_each(g.payload,'$.steps') WHERE json_extract(value,'$.status')='completed') AS completed_steps,
   (SELECT COUNT(*) FROM json_each(g.payload,'$.steps') WHERE json_extract(value,'$.status')<>'retired') AS total_steps,
   'manual' AS schedule_mode,'' AS next_run_at
  FROM long_tasks g WHERE ` + goals + `
  UNION ALL
  SELECT q.id,'list',COALESCE(NULLIF(q.title,''),q.id),
   CASE WHEN q.status='completed' AND EXISTS(SELECT 1 FROM batch_tasks b WHERE b.queue_id=q.id AND b.status='failed') THEN
    CASE WHEN EXISTS(SELECT 1 FROM batch_tasks b WHERE b.queue_id=q.id AND b.status='completed') THEN 'partial' ELSE 'failed' END
   WHEN q.status='completed' AND q.schedule_mode='cron' AND COALESCE(q.schedule_enabled,1)=1 THEN 'scheduled'
   ELSE q.status END,
   COALESCE(q.project_id,''),substr(COALESCE(NULLIF(q.last_run_error,''),q.last_schedule_error,''),1,2000),
   COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ',COALESCE(q.completed_at,q.started_at,q.created_at)),''),
   (SELECT COUNT(*) FROM batch_tasks b WHERE b.queue_id=q.id AND b.status='completed'),
   (SELECT COUNT(*) FROM batch_tasks b WHERE b.queue_id=q.id),
   COALESCE(q.schedule_mode,'manual'),COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ',q.next_run_at),'')
  FROM batch_task_queues q WHERE ` + queues + `
 ) SELECT id,mode,name,status,project_id,summary,updated_at,completed_steps,total_steps,schedule_mode,next_run_at FROM entries WHERE 1=1`
	args := append(ga, qa...)
	if mode != "" {
		sql += ` AND mode=?`
		args = append(args, mode)
	}
	if status != "" {
		sql += ` AND status=?`
		args = append(args, status)
	}
	if query != "" {
		sql += ` AND (instr(lower(name),lower(?))>0 OR instr(id,?)>0)`
		args = append(args, query, query)
	}
	sql += ` ORDER BY updated_at DESC,mode,id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManagedTaskRow{}
	for rows.Next() {
		var row ManagedTaskRow
		if err = rows.Scan(&row.ID, &row.Mode, &row.Name, &row.Status, &row.ProjectID, &row.Summary, &row.UpdatedAt, &row.CompletedSteps, &row.TotalSteps, &row.ScheduleMode, &row.NextRunAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
