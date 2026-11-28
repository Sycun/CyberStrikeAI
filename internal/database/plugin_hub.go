package database

import (
	"context"
	"database/sql"
	"time"
)

func (db *DB) SavePluginInspection(ctx context.Context, id, actor, payload string, now, expires time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM plugin_import_inspections WHERE expires_at <= ?", now.UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_import_inspections
		(id, actor_user_id, inspection_json, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		id, actor, payload, now.UTC(), expires.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) GetPluginInspection(ctx context.Context, id, actor string, now time.Time) (string, error) {
	var payload string
	err := db.QueryRowContext(ctx, `SELECT inspection_json FROM plugin_import_inspections
		WHERE id = ? AND actor_user_id = ? AND expires_at > ?`, id, actor, now.UTC()).Scan(&payload)
	return payload, err
}

type PluginPlanRecord struct {
	ID             string
	InstallID      string
	InspectionID   string
	ActorUserID    string
	ManifestJSON   string
	ManifestDigest string
	PolicyJSON     string
	PlanJSON       string
	PlanDigest     string
	ExpiresAt      time.Time
	ApprovedAt     *time.Time
}

func (db *DB) SavePluginPlan(ctx context.Context, record PluginPlanRecord, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_install_plans WHERE expires_at <= ? AND approved_at IS NULL`, now.UTC()); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO plugin_install_plans
		(id, install_id, inspection_id, actor_user_id, manifest_json, manifest_digest, policy_json, plan_json, plan_digest, expires_at)
		SELECT ?, ?, id, actor_user_id, ?, ?, ?, ?, ?, ? FROM plugin_import_inspections
		WHERE id = ? AND actor_user_id = ? AND expires_at > ?`,
		record.ID, record.InstallID, record.ManifestJSON, record.ManifestDigest, record.PolicyJSON, record.PlanJSON,
		record.PlanDigest, record.ExpiresAt.UTC(), record.InspectionID, record.ActorUserID, now.UTC())
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (db *DB) GetPluginPlan(ctx context.Context, id, actor string, now time.Time) (PluginPlanRecord, error) {
	var record PluginPlanRecord
	err := db.QueryRowContext(ctx, `SELECT id, install_id, inspection_id, actor_user_id, manifest_json, manifest_digest,
		policy_json, plan_json, plan_digest, expires_at, approved_at
		FROM plugin_install_plans WHERE id = ? AND actor_user_id = ? AND expires_at > ?`, id, actor, now.UTC()).
		Scan(&record.ID, &record.InstallID, &record.InspectionID, &record.ActorUserID, &record.ManifestJSON,
			&record.ManifestDigest, &record.PolicyJSON, &record.PlanJSON, &record.PlanDigest, &record.ExpiresAt, &record.ApprovedAt)
	return record, err
}

type PluginInstallSummary struct {
	ID        string    `json:"id"`
	PluginID  string    `json:"plugin_id"`
	Title     string    `json:"title"`
	Driver    string    `json:"driver"`
	Desired   string    `json:"desired_state"`
	Observed  string    `json:"observed_state"`
	ErrorCode string    `json:"last_error_code,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

const pluginInstallSummaryColumns = `id, plugin_id, title, driver, desired_state, observed_state, COALESCE(last_error_code, ''), updated_at`

func scanPluginInstall(row interface{ Scan(...any) error }) (PluginInstallSummary, error) {
	var install PluginInstallSummary
	err := row.Scan(&install.ID, &install.PluginID, &install.Title, &install.Driver, &install.Desired, &install.Observed, &install.ErrorCode, &install.UpdatedAt)
	return install, err
}

func (db *DB) ListPluginInstalls(ctx context.Context, actor, scope string) ([]PluginInstallSummary, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+pluginInstallSummaryColumns+` FROM plugin_installs
		WHERE observed_state <> 'removed' AND (
			? = 'all' OR (? = 'own' AND owner_user_id = ?) OR
			(? IN ('own', 'assigned') AND EXISTS (SELECT 1 FROM rbac_resource_assignments a
				WHERE a.user_id = ? AND a.resource_type = 'plugin_install' AND a.resource_id = plugin_installs.id)))
		ORDER BY updated_at DESC, id`, scope, scope, actor, scope, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []PluginInstallSummary{}
	for rows.Next() {
		install, err := scanPluginInstall(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, install)
	}
	return items, rows.Err()
}

func (db *DB) GetPluginInstall(ctx context.Context, id string) (PluginInstallSummary, error) {
	return scanPluginInstall(db.QueryRowContext(ctx, `SELECT `+pluginInstallSummaryColumns+` FROM plugin_installs
		WHERE id = ? AND observed_state <> 'removed'`, id))
}
