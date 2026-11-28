package pluginhub

import (
	"context"
	"database/sql"
	"regexp"
	"strings"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub/catalog"
	"cyberstrike-ai/internal/pluginhub/manifest"
	"cyberstrike-ai/internal/pluginhub/source"
)

var catalogIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,47}$`)

type CatalogInput struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Summary    string `json:"summary"`
	Manifest   string `json:"manifest"` // Optional YAML or JSON manifest for one-click install
}

func (m *Manager) ListCatalog(ctx context.Context) ([]catalog.Entry, int, error) {
	records, err := m.db.ListPluginCatalogRecords(ctx)
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[string]database.PluginCatalogRecord, len(records))
	hiddenBuiltins := 0
	for _, r := range records {
		byID[r.ID] = r
		if r.Source == "builtin_hidden" && catalog.HasBuiltin(r.ID) {
			hiddenBuiltins++
		}
	}
	result := make([]catalog.Entry, 0, len(catalog.Entries())+len(records))
	seen := map[string]bool{}
	for _, entry := range catalog.Entries() {
		seen[entry.ID] = true
		if override, ok := byID[entry.ID]; ok {
			if override.Source == "builtin_hidden" {
				continue
			}
			avail := "inspection_only"
			if strings.TrimSpace(override.ManifestJSON) != "" {
				avail = "plan_available"
			}
			result = append(result, catalog.Entry{
				ID: override.ID, Title: override.Title, Repository: override.Repository,
				Revision: override.Revision, Summary: override.Summary, Availability: avail, Builtin: false,
			})
			continue
		}
		result = append(result, entry)
	}
	for _, r := range records {
		if seen[r.ID] || r.Source != "custom" {
			continue
		}
		avail := "inspection_only"
		if strings.TrimSpace(r.ManifestJSON) != "" {
			avail = "plan_available"
		}
		result = append(result, catalog.Entry{
			ID: r.ID, Title: r.Title, Repository: r.Repository,
			Revision: r.Revision, Summary: r.Summary, Availability: avail, Builtin: false,
		})
	}
	return result, hiddenBuiltins, nil
}

func (m *Manager) CatalogManifest(ctx context.Context, id string) ([]byte, bool) {
	if r, err := m.db.GetPluginCatalogRecord(ctx, id); err == nil {
		if r.Source == "builtin_hidden" {
			return nil, false
		}
		if strings.TrimSpace(r.ManifestJSON) != "" {
			return []byte(r.ManifestJSON), true
		}
		return nil, false
	}
	return catalog.Manifest(id)
}

func (m *Manager) SaveCatalogEntry(ctx context.Context, input CatalogInput) (catalog.Entry, error) {
	id := strings.TrimSpace(input.ID)
	title := strings.TrimSpace(input.Title)
	repoRaw := strings.TrimSpace(input.Repository)
	rev := strings.TrimSpace(input.Revision)
	summary := strings.TrimSpace(input.Summary)
	manifestRaw := strings.TrimSpace(input.Manifest)

	var manifestCanonical string
	var manifestRepo string
	if manifestRaw != "" {
		decl, err := manifest.DecodeDeclaration([]byte(manifestRaw))
		if err != nil {
			return catalog.Entry{}, ErrInvalidManifest
		}
		manifestCanonical = string(decl.Canonical)
		manifestRepo = decl.Metadata.Repository
		if id == "" {
			id = decl.Metadata.ID
		} else if id != decl.Metadata.ID {
			return catalog.Entry{}, ErrInvalidManifest
		}
		if title == "" {
			title = decl.Metadata.Title
		}
		if repoRaw == "" {
			repoRaw = decl.Metadata.Repository
		}
		if summary == "" {
			summary = decl.Metadata.Summary
		}
	}

	if !catalogIDPattern.MatchString(id) || title == "" || len(title) > 100 || len(summary) > 500 {
		return catalog.Entry{}, ErrInvalidRequest
	}
	repo, err := source.NormalizeGitHubRepository(repoRaw)
	if err != nil {
		return catalog.Entry{}, ErrSourceMismatch
	}
	if manifestRepo != "" {
		if normManifestRepo, err := source.NormalizeGitHubRepository(manifestRepo); err == nil && normManifestRepo != repo {
			return catalog.Entry{}, ErrSourceMismatch
		}
	}
	if rev != "" && manifest.ValidateGitRef(rev) != nil {
		return catalog.Entry{}, ErrInvalidRequest
	}
	if err := m.db.UpsertPluginCatalogRecord(ctx, database.PluginCatalogRecord{
		ID: id, Title: title, Repository: repo, Revision: rev, Summary: summary,
		ManifestJSON: manifestCanonical, Source: "custom",
	}); err != nil {
		return catalog.Entry{}, err
	}
	avail := "inspection_only"
	if manifestCanonical != "" {
		avail = "plan_available"
	}
	return catalog.Entry{
		ID: id, Title: title, Repository: repo, Revision: rev,
		Summary: summary, Availability: avail, Builtin: false,
	}, nil
}

func (m *Manager) DeleteCatalogEntry(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if !catalogIDPattern.MatchString(id) {
		return ErrInvalidRequest
	}
	if catalog.HasBuiltin(id) {
		// Hide built-in catalog entries via tombstone so they disappear from the market
		// while remaining restorable if the operator chooses to restore defaults.
		return m.db.UpsertPluginCatalogRecord(ctx, database.PluginCatalogRecord{
			ID: id, Title: id, Repository: "", Source: "builtin_hidden",
		})
	}
	deleted, err := m.db.DeletePluginCatalogRecord(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return sql.ErrNoRows
	}
	return nil
}

func (m *Manager) RestoreBuiltinCatalog(ctx context.Context) (int64, error) {
	return m.db.RestoreHiddenBuiltinCatalog(ctx)
}
