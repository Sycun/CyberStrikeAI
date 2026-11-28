package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPluginServicesSchemaUpgradeAndGenerationFence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "previous.db")
	db, err := NewDB(file, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = db.Exec(`DROP TABLE plugin_web_origins; DROP TABLE plugin_gateway_keys; DROP TABLE plugin_model_usage; DROP TABLE plugin_llm_bindings;
		INSERT INTO plugin_installs(id,plugin_id,source_identity,title,driver,observed_state,created_at,updated_at)
		VALUES('fixture','old','old','retained','release','healthy',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = NewDB(file, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	i, err := db.RuntimePluginInstall(ctx, "fixture")
	if err != nil || i.Title != "retained" {
		t.Fatalf("existing install lost: %+v %v", i, err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('plugin_web_origins','plugin_llm_bindings','plugin_gateway_keys','plugin_model_usage')`).Scan(&tables); err != nil || tables != 4 {
		t.Fatalf("plugin service schema incomplete: %d %v", tables, err)
	}
	now := time.Now().UTC()
	if ok, err := db.RenewPluginLease(ctx, "test-owner", now, now.Add(time.Minute)); err != nil || !ok {
		t.Fatal(err)
	}
	gen, err := db.BeginPluginLaunch(ctx, "test-owner", "fixture")
	if err != nil || gen != 1 {
		t.Fatalf("generation %d %v", gen, err)
	}
	b := PluginLLMBinding{Protocol: "openai_chat", Mode: "channel", ChannelID: "fixture", Concurrency: 1, RPM: 2, TokenBudget: 500}
	if err := db.SetPluginLLMBindings(ctx, "fixture", []PluginLLMBinding{b}); err != nil {
		t.Fatal(err)
	}
	if err := db.SavePluginGatewayKey(ctx, "fixture", gen, b.Protocol, "fixture-digest"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PluginGatewayKey(ctx, "fixture-digest"); err != nil {
		t.Fatal(err)
	}
	if err := db.BlockPluginTraffic(ctx, "test-owner", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PluginGatewayKey(ctx, "fixture-digest"); err == nil {
		t.Fatal("stopping instance still admitted model traffic")
	}
	if err := db.SavePluginWebOrigin(ctx, PluginWebOrigin{InstallID: "fixture", Mode: "port", Port: 43220}); err == nil {
		t.Fatal("stopping instance allocated a browser port")
	}
}
