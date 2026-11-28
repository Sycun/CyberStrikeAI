package database

import (
	"context"
	"errors"
	"time"
)

type PluginWebOrigin struct {
	InstallID string
	Mode      string
	Port      int
	Hostname  string
	Retired   *time.Time
}

func scanPluginOrigin(row pluginQueryRow) (v PluginWebOrigin, err error) {
	err = row.Scan(&v.InstallID, &v.Mode, &v.Port, &v.Hostname, &v.Retired)
	return v, err
}

func (db *DB) PluginWebOrigin(ctx context.Context, installID string) (PluginWebOrigin, error) {
	return scanPluginOrigin(db.QueryRowContext(ctx, `SELECT install_id,mode,listener_port,hostname,retired_at
		FROM plugin_web_origins WHERE install_id=?`, installID))
}

func (db *DB) PluginWebOrigins(ctx context.Context) ([]PluginWebOrigin, error) {
	rows, err := db.QueryContext(ctx, "SELECT install_id,mode,listener_port,hostname,retired_at FROM plugin_web_origins")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PluginWebOrigin
	for rows.Next() {
		item, err := scanPluginOrigin(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (db *DB) SavePluginWebOrigin(ctx context.Context, origin PluginWebOrigin) error {
	result, err := db.ExecContext(ctx, `INSERT INTO plugin_web_origins(install_id,mode,listener_port,hostname,created_at)
		SELECT id,?,?,?,? FROM plugin_installs WHERE id=? AND observed_state='healthy'`,
		origin.Mode, origin.Port, origin.Hostname, time.Now().UTC(), origin.InstallID)
	return changedExactlyOne(result, err, ErrPluginOperationConflict)
}

func (db *DB) RetirePluginWebOrigin(ctx context.Context, id string) error {
	_, err := db.ExecContext(ctx, "UPDATE plugin_web_origins SET retired_at=? WHERE install_id=? AND retired_at IS NULL", time.Now().UTC(), id)
	return err
}

func (db *DB) BlockPluginTraffic(ctx context.Context, owner, id string) error {
	result, err := db.ExecContext(ctx, `UPDATE plugin_installs SET observed_state='stopping',endpoint_ref=NULL,observed_port=NULL
		WHERE id=? AND observed_state<>'removed'
		AND EXISTS (SELECT 1 FROM plugin_runtime_leases WHERE id=1 AND owner=? AND expires_at>?)`, id, owner, time.Now().UTC())
	return changedExactlyOne(result, err, ErrPluginOperationConflict)
}

type PluginLLMBinding struct {
	Protocol    string `json:"protocol,omitempty"`
	Mode        string `json:"mode"`
	ChannelID   string `json:"channel_id,omitempty"`
	Concurrency int    `json:"concurrency"`
	RPM         int    `json:"rpm"`
	TokenBudget int64  `json:"token_budget"`
}

func (db *DB) PluginLLMBindings(ctx context.Context, installID string) ([]PluginLLMBinding, error) {
	rows, err := db.QueryContext(ctx, `SELECT protocol,mode,channel_id,concurrency,rpm,token_budget
		FROM plugin_llm_bindings WHERE install_id=? ORDER BY protocol`, installID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PluginLLMBinding
	for rows.Next() {
		var b PluginLLMBinding
		if err := rows.Scan(&b.Protocol, &b.Mode, &b.ChannelID, &b.Concurrency, &b.RPM, &b.TokenBudget); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func (db *DB) SetPluginLLMBindings(ctx context.Context, installID string, values []PluginLLMBinding) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM plugin_gateway_keys WHERE install_id=?", installID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM plugin_llm_bindings WHERE install_id=?", installID); err != nil {
		return err
	}
	for _, b := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_llm_bindings(install_id,protocol,mode,channel_id,concurrency,rpm,token_budget)
			VALUES(?,?,?,?,?,?,?)`, installID, b.Protocol, b.Mode, b.ChannelID, b.Concurrency, b.RPM, b.TokenBudget); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type PluginGatewayKey struct {
	InstallID  string
	Generation int64
	Binding    PluginLLMBinding
}

func (db *DB) SavePluginGatewayKey(ctx context.Context, id string, generation int64, protocol, digest string) error {
	result, err := db.ExecContext(ctx, `INSERT INTO plugin_gateway_keys(digest,install_id,generation,protocol,created_at)
		SELECT ?,i.id,i.generation,b.protocol,?
		FROM plugin_installs i JOIN plugin_llm_bindings b ON b.install_id=i.id
		WHERE i.id=? AND i.generation=? AND i.observed_state='starting' AND b.protocol=? AND b.mode<>'disabled'`,
		digest, time.Now().UTC(), id, generation, protocol)
	return changedExactlyOne(result, err, ErrPluginOperationConflict)
}

func (db *DB) PluginGatewayKey(ctx context.Context, digest string) (PluginGatewayKey, error) {
	var key PluginGatewayKey
	err := db.QueryRowContext(ctx, `SELECT k.install_id,k.generation,k.protocol,b.mode,b.channel_id,b.concurrency,b.rpm,b.token_budget
		FROM plugin_gateway_keys k JOIN plugin_installs i ON i.id=k.install_id AND i.generation=k.generation
		JOIN plugin_llm_bindings b ON b.install_id=k.install_id AND b.protocol=k.protocol
		WHERE k.digest=? AND i.observed_state IN ('starting','healthy') AND b.mode<>'disabled'`, digest).
		Scan(&key.InstallID, &key.Generation, &key.Binding.Protocol, &key.Binding.Mode, &key.Binding.ChannelID,
			&key.Binding.Concurrency, &key.Binding.RPM, &key.Binding.TokenBudget)
	return key, err
}

func (db *DB) RevokePluginGatewayKeys(ctx context.Context, id string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM plugin_gateway_keys WHERE install_id=?", id)
	return err
}

// BeginPluginLaunch reserves the next generation before credentials or a WebUI can
// be associated with it. The runtime lease fences out another supervisor.
func (db *DB) BeginPluginLaunch(ctx context.Context, owner, id string) (int64, error) {
	var generation int64
	err := db.QueryRowContext(ctx, `UPDATE plugin_installs
		SET generation=generation+1,observed_state='starting',endpoint_ref=NULL,observed_port=NULL
		WHERE id=? AND observed_state<>'removed'
		AND EXISTS (SELECT 1 FROM plugin_runtime_leases WHERE id=1 AND owner=? AND expires_at>?)
		RETURNING generation`, id, owner, time.Now().UTC()).Scan(&generation)
	return generation, err
}

var ErrPluginModelBudget = errors.New("plugin model token budget exhausted")

func (db *DB) ReservePluginModelUsage(ctx context.Context, id string, k PluginGatewayKey, channel string, charge int64) error {
	now := time.Now().UTC()
	day := now.Format(time.DateOnly)
	result, err := db.ExecContext(ctx, `INSERT INTO plugin_model_usage
		(id,install_id,generation,protocol,channel_id,day,charged_tokens,status,created_at)
		SELECT ?,?,?,?,?,?,?,'reserved',?
		WHERE COALESCE((SELECT SUM(charged_tokens) FROM plugin_model_usage WHERE install_id=? AND day=?),0)+? <= ?`,
		id, k.InstallID, k.Generation, k.Binding.Protocol, channel, day, charge, now, k.InstallID, day, charge, k.Binding.TokenBudget)
	return changedExactlyOne(result, err, ErrPluginModelBudget)
}

func (db *DB) FinishPluginModelUsage(ctx context.Context, id, status string, input, output int64, metered bool) error {
	if !metered {
		_, err := db.ExecContext(ctx, "UPDATE plugin_model_usage SET status=?,finished_at=? WHERE id=?",
			status, time.Now().UTC(), id)
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE plugin_model_usage SET status=?,finished_at=?,input_tokens=?,output_tokens=?,
		charged_tokens=? WHERE id=?`, status, time.Now().UTC(), input, output, input+output, id)
	return err
}
