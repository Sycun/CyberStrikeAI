package database

import (
	"context"
	"time"
)

type PluginCatalogRecord struct {
	ID           string
	Title        string
	Repository   string
	Revision     string
	Summary      string
	ManifestJSON string
	Source       string // "custom" | "builtin_hidden"
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (db *DB) ListPluginCatalogRecords(ctx context.Context) ([]PluginCatalogRecord, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, title, repository, revision, summary, manifest_json, source, created_at, updated_at
		FROM plugin_catalog_entries ORDER BY updated_at DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PluginCatalogRecord{}
	for rows.Next() {
		var r PluginCatalogRecord
		if err := rows.Scan(&r.ID, &r.Title, &r.Repository, &r.Revision, &r.Summary, &r.ManifestJSON, &r.Source, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) GetPluginCatalogRecord(ctx context.Context, id string) (PluginCatalogRecord, error) {
	var r PluginCatalogRecord
	err := db.QueryRowContext(ctx, `SELECT id, title, repository, revision, summary, manifest_json, source, created_at, updated_at
		FROM plugin_catalog_entries WHERE id = ?`, id).
		Scan(&r.ID, &r.Title, &r.Repository, &r.Revision, &r.Summary, &r.ManifestJSON, &r.Source, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (db *DB) UpsertPluginCatalogRecord(ctx context.Context, r PluginCatalogRecord) error {
	now := time.Now().UTC()
	if r.Source == "" {
		r.Source = "custom"
	}
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_catalog_entries
		(id, title, repository, revision, summary, manifest_json, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title,
			repository=excluded.repository,
			revision=excluded.revision,
			summary=excluded.summary,
			manifest_json=excluded.manifest_json,
			source=excluded.source,
			updated_at=excluded.updated_at`,
		r.ID, r.Title, r.Repository, r.Revision, r.Summary, r.ManifestJSON, r.Source, now, now)
	return err
}

func (db *DB) DeletePluginCatalogRecord(ctx context.Context, id string) (bool, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM plugin_catalog_entries WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (db *DB) RestoreHiddenBuiltinCatalog(ctx context.Context) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM plugin_catalog_entries WHERE source = 'builtin_hidden'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
