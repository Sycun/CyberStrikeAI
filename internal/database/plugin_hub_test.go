package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPluginSchemaReconcilesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE local_legacy (value TEXT); INSERT INTO local_legacy VALUES ('keep')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := NewDB(path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN
		('plugin_import_inspections','plugin_install_plans','plugin_installs','plugin_versions','plugin_operations')`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("missing tables: %d, %v", count, err)
	}
	var retained string
	if err := db.QueryRow(`SELECT value FROM local_legacy`).Scan(&retained); err != nil || retained != "keep" {
		t.Fatalf("legacy row changed: %q %v", retained, err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.SavePluginInspection(ctx, "inspection", "author", "{}", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	record := PluginPlanRecord{
		ID: "plan", InstallID: "instance", InspectionID: "inspection", ActorUserID: "other",
		ManifestJSON: "{}", PlanJSON: "{}", PolicyJSON: "{}", ExpiresAt: now.Add(30 * time.Second),
	}
	if err := db.SavePluginPlan(ctx, record, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign actor wrote a plan: %v", err)
	}
	record.ActorUserID = "author"
	if err := db.SavePluginPlan(ctx, record, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetPluginPlan(ctx, record.ID, "author", now.Add(time.Minute)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired plan returned: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewDB(path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.GetPluginPlan(ctx, record.ID, "author", now); err != nil || got.InspectionID != "inspection" {
		t.Fatalf("plan was lost after reopening: %+v %v", got, err)
	}
}
