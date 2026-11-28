package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrPluginOperationConflict = errors.New("plugin operation conflicts with existing state")
var ErrPluginPlanUnavailable = errors.New("plugin plan expired or already consumed")

type PluginRuntimeInstall struct {
	ID, PluginID, Source, Title, Desired, Observed, Version, Driver string
	Endpoint, ErrorCode                                             string
	Port                                                            int
	Generation                                                      int64
}

type PluginVersion struct {
	ID, InstallID, Version, Driver, Manifest, ManifestDigest string
	Artifact, ArtifactDigest, Policy, Plan                   string
}

type PluginOperation struct {
	ID          string    `json:"id"`
	InstallID   string    `json:"install_id"`
	Actor       string    `json:"-"`
	Kind        string    `json:"kind"`
	Key         string    `json:"-"`
	PlanDigest  string    `json:"plan_digest,omitempty"`
	VersionID   string    `json:"-"`
	RequestHash string    `json:"-"`
	PurgeData   bool      `json:"-"`
	Status      string    `json:"status"`
	ErrorCode   string    `json:"error_code,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type pluginQueryRow interface{ Scan(...any) error }

const runtimeInstallColumns = "id, plugin_id, source_identity, title, desired_state, observed_state, COALESCE(active_version_id,''), driver, COALESCE(endpoint_ref,''), COALESCE(last_error_code,''), COALESCE(observed_port,0), generation"
const pluginVersionColumns = "id, install_id, version, driver, manifest_json, manifest_digest, artifact_identity, artifact_digest, approval_json, plan_json"
const pluginOperationColumns = "id, install_id, actor_user_id, kind, idempotency_key, plan_digest, version_id, request_digest, purge_data, status, error_code, created_at, updated_at"

func scanRuntimeInstall(row pluginQueryRow) (v PluginRuntimeInstall, err error) {
	err = row.Scan(&v.ID, &v.PluginID, &v.Source, &v.Title, &v.Desired, &v.Observed, &v.Version, &v.Driver, &v.Endpoint, &v.ErrorCode, &v.Port, &v.Generation)
	return v, err
}

func scanPluginVersion(row pluginQueryRow) (v PluginVersion, err error) {
	err = row.Scan(&v.ID, &v.InstallID, &v.Version, &v.Driver, &v.Manifest, &v.ManifestDigest, &v.Artifact, &v.ArtifactDigest, &v.Policy, &v.Plan)
	return v, err
}

func scanPluginOperation(row pluginQueryRow) (v PluginOperation, err error) {
	err = row.Scan(&v.ID, &v.InstallID, &v.Actor, &v.Kind, &v.Key, &v.PlanDigest, &v.VersionID, &v.RequestHash, &v.PurgeData,
		&v.Status, &v.ErrorCode, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (db *DB) RuntimePluginInstall(ctx context.Context, id string) (PluginRuntimeInstall, error) {
	return scanRuntimeInstall(db.QueryRowContext(ctx, "SELECT "+runtimeInstallColumns+" FROM plugin_installs WHERE id = ?", id))
}

func (db *DB) RecoverablePluginInstalls(ctx context.Context) ([]PluginRuntimeInstall, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+runtimeInstallColumns+" FROM plugin_installs WHERE observed_state NOT IN ('stopped','removed') OR desired_state = 'running'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []PluginRuntimeInstall
	for rows.Next() {
		item, err := scanRuntimeInstall(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, rows.Err()
}

func (db *DB) PluginVersion(ctx context.Context, installID, versionID string) (PluginVersion, error) {
	return scanPluginVersion(db.QueryRowContext(ctx, "SELECT "+pluginVersionColumns+" FROM plugin_versions WHERE install_id = ? AND id = ?", installID, versionID))
}

func (db *DB) PluginVersions(ctx context.Context, installID string) ([]PluginVersion, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+pluginVersionColumns+" FROM plugin_versions WHERE install_id = ? ORDER BY created_at DESC", installID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []PluginVersion
	for rows.Next() {
		value, err := scanPluginVersion(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) LastPluginOperation(ctx context.Context, installID string) (PluginOperation, error) {
	return scanPluginOperation(db.QueryRowContext(ctx, "SELECT "+pluginOperationColumns+
		" FROM plugin_operations WHERE install_id = ? ORDER BY updated_at DESC, id DESC LIMIT 1", installID))
}

func (db *DB) GetPluginOperation(ctx context.Context, id string) (PluginOperation, error) {
	return scanPluginOperation(db.QueryRowContext(ctx, "SELECT "+pluginOperationColumns+" FROM plugin_operations WHERE id = ?", id))
}

func (db *DB) PluginOperationByKey(ctx context.Context, installID, key string) (PluginOperation, error) {
	return scanPluginOperation(db.QueryRowContext(ctx, "SELECT "+pluginOperationColumns+" FROM plugin_operations WHERE install_id = ? AND idempotency_key = ?", installID, key))
}

func (db *DB) GetPluginPlanForConfirmation(ctx context.Context, id, actor string) (PluginPlanRecord, error) {
	var result PluginPlanRecord
	err := db.QueryRowContext(ctx, `SELECT id,install_id,inspection_id,actor_user_id,manifest_json,manifest_digest,policy_json,plan_json,plan_digest,expires_at
		FROM plugin_install_plans WHERE id=? AND actor_user_id=?`, id, actor).Scan(&result.ID, &result.InstallID, &result.InspectionID,
		&result.ActorUserID, &result.ManifestJSON, &result.ManifestDigest, &result.PolicyJSON, &result.PlanJSON, &result.PlanDigest, &result.ExpiresAt)
	return result, err
}

func (db *DB) PluginPlanApproved(ctx context.Context, id, install, actor, digest string) (bool, error) {
	var approved bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM plugin_install_plans
		WHERE id=? AND install_id=? AND actor_user_id=? AND plan_digest=?
		AND approved_at IS NOT NULL AND consumed_at IS NOT NULL)`, id, install, actor, digest).Scan(&approved)
	return approved, err
}

type PluginQueueRequest struct {
	Operation PluginOperation
	Install   PluginRuntimeInstall
	Version   PluginVersion
	PlanID    string
	CatalogID string
}

func (db *DB) QueuePluginOperation(ctx context.Context, request PluginQueueRequest, now time.Time) (PluginOperation, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PluginOperation{}, err
	}
	defer tx.Rollback()
	op := request.Operation
	previous, err := scanPluginOperation(tx.QueryRowContext(ctx, "SELECT "+pluginOperationColumns+" FROM plugin_operations WHERE install_id=? AND idempotency_key=?", op.InstallID, op.Key))
	if err == nil {
		if previous.Actor == op.Actor && previous.RequestHash == op.RequestHash {
			return previous, nil
		}
		return PluginOperation{}, ErrPluginOperationConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PluginOperation{}, err
	}
	current, err := scanRuntimeInstall(tx.QueryRowContext(ctx, "SELECT "+runtimeInstallColumns+" FROM plugin_installs WHERE id = ?", op.InstallID))
	if op.Kind == "install" {
		if !errors.Is(err, sql.ErrNoRows) {
			if err != nil {
				return PluginOperation{}, err
			}
			return PluginOperation{}, ErrPluginOperationConflict
		}
	} else if err != nil || current.Observed == "removed" {
		if err == nil {
			err = sql.ErrNoRows
		}
		return PluginOperation{}, err
	}
	if op.Kind == "upgrade" && (current.Source != request.Install.Source || current.PluginID != request.Install.PluginID || current.Version == "") {
		return PluginOperation{}, ErrPluginOperationConflict
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_operations WHERE install_id=? AND status IN ('queued','running'))`, op.InstallID).Scan(&busy); err != nil {
		return PluginOperation{}, err
	}
	if busy {
		return PluginOperation{}, ErrPluginOperationConflict
	}
	if request.PlanID != "" {
		result, err := tx.ExecContext(ctx, `UPDATE plugin_install_plans SET approved_at=?,consumed_at=?
			WHERE id=? AND install_id=? AND actor_user_id=? AND plan_digest=? AND expires_at>? AND consumed_at IS NULL`,
			now, now, request.PlanID, op.InstallID, op.Actor, op.PlanDigest, now)
		if err := changedExactlyOne(result, err, ErrPluginPlanUnavailable); err != nil {
			return PluginOperation{}, err
		}
	}
	if op.Kind == "install" {
		record := request.Install
		var duplicate bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_installs WHERE source_identity=? AND plugin_id=? AND observed_state<>'removed')`, record.Source, record.PluginID).Scan(&duplicate); err != nil {
			return PluginOperation{}, err
		}
		if duplicate {
			return PluginOperation{}, ErrPluginOperationConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO plugin_installs
			(id,catalog_id,plugin_id,source_identity,title,owner_user_id,desired_state,observed_state,driver,created_at,updated_at)
			VALUES(?,?,?,?,?,?,'running','installing',?,?,?)`, op.InstallID, request.CatalogID, record.PluginID, record.Source, record.Title,
			op.Actor, request.Version.Driver, now, now)
		if err != nil {
			return PluginOperation{}, err
		}
	}
	if v := request.Version; v.ID != "" {
		var storedID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM plugin_versions WHERE install_id=? AND manifest_digest=? AND artifact_identity=? AND artifact_digest=? AND driver=?`,
			op.InstallID, v.ManifestDigest, v.Artifact, v.ArtifactDigest, v.Driver).Scan(&storedID)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `INSERT INTO plugin_versions
				(id,install_id,version,manifest_json,manifest_digest,artifact_identity,artifact_digest,driver,storage_key,approval_json,plan_json,created_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, op.InstallID, v.Version, v.Manifest, v.ManifestDigest,
				v.Artifact, v.ArtifactDigest, v.Driver, v.ID, v.Policy, v.Plan, now)
			storedID = v.ID
		}
		if err != nil {
			return PluginOperation{}, err
		}
		op.VersionID = storedID
	}
	op.Status, op.CreatedAt, op.UpdatedAt = "queued", now, now
	_, err = tx.ExecContext(ctx, `INSERT INTO plugin_operations
		(id,install_id,actor_user_id,kind,idempotency_key,plan_digest,version_id,request_digest,purge_data,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, op.ID, op.InstallID, op.Actor, op.Kind, op.Key, op.PlanDigest, op.VersionID,
		op.RequestHash, op.PurgeData, op.Status, now, now)
	if err != nil {
		return PluginOperation{}, err
	}
	return op, tx.Commit()
}

func (db *DB) ClaimPluginOperation(ctx context.Context, owner string, now time.Time) (PluginOperation, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PluginOperation{}, err
	}
	defer tx.Rollback()
	var ownsLease bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_runtime_leases WHERE id=1 AND owner=? AND expires_at>?)`, owner, now).Scan(&ownsLease)
	if err != nil || !ownsLease {
		return PluginOperation{}, sql.ErrNoRows
	}
	op, err := scanPluginOperation(tx.QueryRowContext(ctx, "SELECT "+pluginOperationColumns+" FROM plugin_operations WHERE status='queued' ORDER BY created_at,id LIMIT 1"))
	if err != nil {
		return PluginOperation{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE plugin_operations SET status='running',lease_owner=?,lease_until=(SELECT expires_at FROM plugin_runtime_leases WHERE id=1),updated_at=? WHERE id=? AND status='queued'`, owner, now, op.ID)
	if err := changedExactlyOne(result, err, ErrPluginOperationConflict); err != nil {
		return PluginOperation{}, err
	}
	op.Status, op.UpdatedAt = "running", now
	return op, tx.Commit()
}

type PluginObservation struct {
	Desired, Observed, Version, Driver, Endpoint, ErrorCode string
	Port                                                    int
}

func (db *DB) FinishPluginOperation(ctx context.Context, owner string, op PluginOperation, observation PluginObservation, failed bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status := "succeeded"
	if failed {
		status = "failed"
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE plugin_operations SET status=?, error_code=?, updated_at=?, lease_owner=NULL, lease_until=NULL
		WHERE id=? AND status='running' AND lease_owner=? AND EXISTS(SELECT 1 FROM plugin_runtime_leases WHERE id=1 AND owner=? AND expires_at>?)`,
		status, observation.ErrorCode, now, op.ID, owner, owner, now)
	if err := changedExactlyOne(result, err, ErrPluginOperationConflict); err != nil {
		return err
	}
	if err := setPluginObservation(ctx, tx, op.InstallID, observation, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) SetPluginObservation(ctx context.Context, owner, id string, observation PluginObservation) error {
	now := time.Now().UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ownsLease bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plugin_runtime_leases WHERE id=1 AND owner=? AND expires_at>?)`, owner, now).Scan(&ownsLease)
	if err != nil || !ownsLease {
		return ErrPluginOperationConflict
	}
	if err := setPluginObservation(ctx, tx, id, observation, now); err != nil {
		return err
	}
	return tx.Commit()
}

func setPluginObservation(ctx context.Context, tx *sql.Tx, id string, o PluginObservation, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE plugin_installs SET desired_state=?, observed_state=?, active_version_id=NULLIF(?, ''),
		driver=?, endpoint_ref=NULLIF(?, ''), observed_port=NULLIF(?, 0), last_error_code=NULLIF(?, ''),
		updated_at=? WHERE id=?`,
		o.Desired, o.Observed, o.Version, o.Driver, o.Endpoint, o.Port, o.ErrorCode, now, id)
	return err
}

func (db *DB) InterruptedPluginOperations(ctx context.Context) ([]PluginOperation, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+pluginOperationColumns+" FROM plugin_operations WHERE status='running'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []PluginOperation
	for rows.Next() {
		op, err := scanPluginOperation(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, op)
	}
	return values, rows.Err()
}

func (db *DB) InterruptPluginOperation(ctx context.Context, owner, id string) error {
	now := time.Now().UTC()
	result, err := db.ExecContext(ctx, `UPDATE plugin_operations SET status='failed', error_code='plugin_operation_interrupted',
		updated_at=?, lease_owner=NULL, lease_until=NULL WHERE id=? AND status='running'
		AND EXISTS(SELECT 1 FROM plugin_runtime_leases WHERE id=1 AND owner=? AND expires_at>?)`, now, id, owner, now)
	return changedExactlyOne(result, err, ErrPluginOperationConflict)
}

func (db *DB) RenewPluginLease(ctx context.Context, owner string, now, expires time.Time) (bool, error) {
	result, err := db.ExecContext(ctx, `INSERT INTO plugin_runtime_leases(id,owner,expires_at) VALUES(1,?,?)
		ON CONFLICT(id) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at
		WHERE plugin_runtime_leases.owner=excluded.owner OR plugin_runtime_leases.expires_at<=?`, owner, expires, now)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (db *DB) ReleasePluginLease(ctx context.Context, owner string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM plugin_runtime_leases WHERE owner=?", owner)
	return err
}

func changedExactlyOne(result sql.Result, err, conflict error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return conflict
	}
	return nil
}

type PluginLogEntry struct {
	ID        int64     `json:"id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

func (db *DB) AppendPluginLog(ctx context.Context, id, message string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO plugin_runtime_logs(install_id,message,created_at) VALUES(?,?,?)`, id, message, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM plugin_runtime_logs WHERE install_id=? AND id <= COALESCE(
		(SELECT id FROM plugin_runtime_logs WHERE install_id=? ORDER BY id DESC LIMIT 1 OFFSET 2000),-1)`, id, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) ReadPluginLogs(ctx context.Context, id string, after int64, limit int) ([]PluginLogEntry, error) {
	if after < 0 || limit < 1 || limit > 200 {
		return nil, fmt.Errorf("invalid plugin log cursor")
	}
	rows, err := db.QueryContext(ctx, `SELECT id,message,created_at FROM plugin_runtime_logs
		WHERE install_id=? AND id>? ORDER BY id LIMIT ?`, id, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []PluginLogEntry{}
	for rows.Next() {
		var entry PluginLogEntry
		if err := rows.Scan(&entry.ID, &entry.Message, &entry.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
