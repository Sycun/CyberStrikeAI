package pluginhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub/manifest"
	"cyberstrike-ai/internal/pluginhub/runner"
	"cyberstrike-ai/internal/pluginhub/source"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type testLocker struct{}

func (testLocker) LockRelease(context.Context, manifest.Distribution, manifest.Platform) (manifest.Artifact, error) {
	return manifest.Artifact{}, source.ErrArtifactUnavailable
}

func (testLocker) LockImage(_ context.Context, s manifest.Service, os, arch string) (manifest.LockedService, error) {
	repo, _, _, err := manifest.ParseImageReference(s.Image)
	return manifest.LockedService{Name: s.Name, RequestedImage: s.Image, Repository: repo,
		ManifestDigest: "sha256:" + manifest.Digest([]byte(s.Image+"/"+os+"/"+arch))}, err
}

type fixtureDriver struct {
	mu      sync.Mutex
	running map[string]*httptest.Server
	block   chan struct{}
}

func (f *fixtureDriver) Probe(context.Context, string) runner.Probe {
	return runner.Probe{Available: true}
}

func (f *fixtureDriver) Prepare(ctx context.Context, spec runner.Spec) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return os.MkdirAll(spec.VersionDir(), 0700)
}

func (f *fixtureDriver) Start(_ context.Context, spec runner.Spec) (runner.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := filepath.Join(spec.Root, "data")
	if err := os.MkdirAll(path, 0700); err != nil {
		return runner.State{}, err
	}
	if spec.Declaration.Metadata.Version == "2.0.0" {
		_ = os.WriteFile(filepath.Join(path, "observations"), []byte("corrupted replacement"), 0600)
		return runner.State{}, errors.New("replacement fixture fails health")
	}
	if prior := f.running[spec.VersionID]; prior != nil {
		return fixtureState(prior), nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	f.running[spec.VersionID] = srv
	return fixtureState(srv), nil
}

func fixtureState(srv *httptest.Server) runner.State {
	port, _ := strconv.Atoi(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	return runner.State{Running: true, Endpoint: srv.URL, Port: port}
}

func (f *fixtureDriver) Inspect(_ context.Context, spec runner.Spec) (runner.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if server := f.running[spec.VersionID]; server != nil {
		return fixtureState(server), nil
	}
	return runner.State{}, nil
}

func (f *fixtureDriver) Stop(_ context.Context, spec runner.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if server := f.running[spec.VersionID]; server != nil {
		server.Close()
		delete(f.running, spec.VersionID)
	}
	return nil
}

func (f *fixtureDriver) Remove(ctx context.Context, spec runner.Spec) error {
	if err := f.Stop(ctx, spec); err != nil {
		return err
	}
	return os.RemoveAll(spec.VersionDir())
}

const fixtureRepo = "https://github.com/example/static"

func fixtureDeclaration(version, tag string) []byte {
	return []byte(`apiVersion: plugins.csai.io/v1
kind: WebPlugin
metadata:
  id: test-static
  title: Static test
  repository: https://github.com/example/static
  version: "` + version + `"
distribution:
  - type: container
    mode: docker
    services:
      - name: web
        role: web
        image: example.org/static:` + tag + `
        containerPort: 8080
        resources: {memoryMiB: 128, pids: 64}
web: {route: root, entryPath: "/", healthPath: "/"}
access: {mode: platform}
permissions: {persistentData: true, modelGateway: false, egress: []}
`)
}

func TestPluginQueueIdempotencyRollbackAndRecovery(t *testing.T) {
	root := t.TempDir()
	db, err := database.NewDB(filepath.Join(root, "fixture.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := config.PluginsConfig{Directory: filepath.Join(root, "plugins")}
	firstDoc := fixtureDeclaration("1.0.0", "first")
	secondDoc := fixtureDeclaration("2.0.0", "second")
	newManager := func(driver *fixtureDriver) *Manager {
		manager, err := New(db, cfg, root, zap.NewNop())
		if err != nil {
			t.Fatal(err)
		}
		manager.locker = testLocker{}
		manager.drivers["container"] = driver
		if err := manager.Start(ctx); err != nil {
			t.Fatal(err)
		}
		waitPlugin(t, ctx, manager.Ready)
		return manager
	}
	releaseBlock := make(chan struct{})
	driver := &fixtureDriver{running: map[string]*httptest.Server{}, block: releaseBlock}
	manager := newManager(driver)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	plan := func(declaration []byte, installID string) PlanResult {
		id := uuid.NewString()
		parsed, err := manifest.DecodeDeclaration(declaration)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := json.Marshal(source.Inspection{Repository: fixtureRepo, Manifest: parsed.Canonical})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if err := db.SavePluginInspection(ctx, id, "actor", string(snapshot), now, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		result, err := manager.CreatePlan(ctx, PlanRequest{Actor: "actor", InspectionID: id, InstallID: installID})
		if err != nil || result.HostApproved || !manager.CanConfirm(result.Record.ManifestDigest, result.ArtifactDigest) {
			t.Fatalf("expected a confirmable plan without a host allowlist: %+v %v", result, err)
		}
		return result
	}
	first := plan(firstDoc, "")
	request := OperationRequest{Actor: "actor", Kind: "install", PlanID: first.Record.ID, PlanDigest: first.Record.PlanDigest, Key: "same-install"}
	incorrect := request
	incorrect.PlanDigest = strings.Repeat("0", 64)
	if _, err := manager.Queue(ctx, incorrect); !errors.Is(err, ErrStalePlan) {
		t.Fatalf("confirmation accepted a wrong digest: %v", err)
	}
	approved, err := db.PluginPlanApproved(ctx, first.Record.ID, first.Record.InstallID, "actor", first.Record.PlanDigest)
	if err != nil || approved {
		t.Fatalf("mismatched confirmation changed approval status: %t %v", approved, err)
	}
	var group sync.WaitGroup
	results := make(chan database.PluginOperation, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			op, err := manager.Queue(ctx, request)
			if err != nil {
				failures <- err
				return
			}
			results <- op
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	var operation database.PluginOperation
	for op := range results {
		if operation.ID != "" && operation.ID != op.ID {
			t.Fatal("one key accepted multiple operations")
		}
		operation = op
	}
	if operation.ID == "" {
		t.Fatal("installation was not queued")
	}
	if _, err := manager.Queue(ctx, OperationRequest{Actor: "actor", Kind: "stop", InstallID: operation.InstallID, Key: "parallel-stop"}); !errors.Is(err, database.ErrPluginOperationConflict) {
		t.Fatalf("overlapping operation not rejected: %v", err)
	}
	close(releaseBlock)
	waitOperation(t, ctx, db, operation.ID, "succeeded")
	install, err := db.RuntimePluginInstall(ctx, operation.InstallID)
	if err != nil || install.Observed != "healthy" || install.Port < 1 {
		t.Fatalf("install %+v %v", install, err)
	}
	persisted, err := db.GetPluginPlan(ctx, first.Record.ID, "actor", time.Now())
	if err != nil || persisted.ApprovedAt == nil {
		t.Fatalf("confirmation did not persist the plan approval: %v", err)
	}
	version, err := db.PluginVersion(ctx, install.ID, install.Version)
	if err != nil {
		t.Fatal(err)
	}
	var approval versionApproval
	if err := json.Unmarshal([]byte(version.Policy), &approval); err != nil ||
		approval.ApprovedBy != "actor" || approval.PlanDigest != first.Record.PlanDigest ||
		!approval.ApprovedAt.Equal(*persisted.ApprovedAt) {
		t.Fatalf("version approval does not match the confirmed plan: %+v %v", approval, err)
	}
	marker := filepath.Join(cfg.Directory, install.ID, "data", "observations")
	if err := os.WriteFile(marker, []byte("initial data"), 0600); err != nil {
		t.Fatal(err)
	}

	second := plan(secondDoc, install.ID)
	upgrade, err := manager.Queue(ctx, OperationRequest{Actor: "actor", Kind: "upgrade", InstallID: install.ID, Key: "upgrade-once",
		PlanID: second.Record.ID, PlanDigest: second.Record.PlanDigest})
	if err != nil {
		t.Fatal(err)
	}
	waitOperation(t, ctx, db, upgrade.ID, "failed")
	rolled, err := db.RuntimePluginInstall(ctx, install.ID)
	restored, readErr := os.ReadFile(marker)
	if err != nil || readErr != nil || rolled.Version != install.Version || rolled.Observed != "healthy" || string(restored) != "initial data" {
		t.Fatalf("rollback did not restore original state: %+v %q %v %v", rolled, restored, err, readErr)
	}

	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(cfg.Directory, install.ID, ".rollback-"+upgrade.VersionID)
	if err := os.MkdirAll(journal, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journal, "observations"), []byte("initial data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("interrupted migration"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE plugin_operations SET status='running',lease_owner='retired-owner' WHERE id=?", upgrade.ID); err != nil {
		t.Fatal(err)
	}
	manager = newManager(&fixtureDriver{running: map[string]*httptest.Server{}})
	restored, readErr = os.ReadFile(marker)
	interrupted, lookupErr := db.GetPluginOperation(ctx, upgrade.ID)
	if string(restored) != "initial data" || readErr != nil || lookupErr != nil || interrupted.ErrorCode != "plugin_operation_interrupted" {
		t.Fatalf("startup did not reconcile interrupted upgrade: %+v %q %v %v", interrupted, restored, readErr, lookupErr)
	}
	waitPlugin(t, ctx, func() bool {
		value, err := db.RuntimePluginInstall(ctx, install.ID)
		return err == nil && value.Observed == "healthy"
	})
	uninstall, err := manager.Queue(ctx, OperationRequest{Actor: "actor", Kind: "uninstall", InstallID: install.ID, Key: "uninstall", Purge: true})
	if err != nil {
		t.Fatal(err)
	}
	waitOperation(t, ctx, db, uninstall.ID, "succeeded")
	if _, err := os.Stat(filepath.Dir(marker)); !os.IsNotExist(err) {
		t.Fatalf("data retained after approved purge: %v", err)
	}
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitOperation(t *testing.T, ctx context.Context, db *database.DB, id, status string) {
	t.Helper()
	waitPlugin(t, ctx, func() bool { op, err := db.GetPluginOperation(ctx, id); return err == nil && op.Status == status })
}

func waitPlugin(t *testing.T, ctx context.Context, check func() bool) {
	t.Helper()
	for !check() {
		select {
		case <-ctx.Done():
			t.Fatal("plugin state did not converge")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestPluginCatalogPlanUsesPlatformLock(t *testing.T) {
	root := t.TempDir()
	db, err := database.NewDB(filepath.Join(root, "catalog.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := New(db, config.PluginsConfig{}, root, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	m.locker = testLocker{}
	m.drivers["container"] = &fixtureDriver{}
	result, err := m.CreatePlan(context.Background(), PlanRequest{Actor: "tester", CatalogID: "artex", Isolation: "strict"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := manifest.DecodeInstallPlan([]byte(result.Record.PlanJSON))
	if err != nil || plan.Platform.OS != "linux" || plan.Artifact.Driver != "container" ||
		len(plan.Artifact.Services) != 2 || result.HostApproved {
		t.Fatalf("unexpected catalog execution plan: %+v %v", plan, err)
	}
}

func TestPluginHostConfigOnlyPreservesStaticApprovals(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.NewDB(filepath.Join(root, "host-approval.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.PluginsConfig{ApprovalPolicy: config.PluginApprovalHostConfigOnly}
	m, err := New(db, cfg, root, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	m.locker = testLocker{}
	m.drivers["container"] = &fixtureDriver{}
	result, err := m.CreatePlan(ctx, PlanRequest{Actor: "operator", CatalogID: "artex", Isolation: "strict"})
	if err != nil {
		t.Fatal(err)
	}
	var request database.PluginQueueRequest
	if err := m.validateConfirmation(result.Record, &request, time.Now()); !errors.Is(err, ErrUnapprovedArtifact) {
		t.Fatalf("host policy accepted a plan absent from its allowlist: %v", err)
	}
	cfg.ApprovedArtifacts = map[string][]string{result.Record.ManifestDigest: {result.ArtifactDigest}}
	approved, err := New(db, cfg, root, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := approved.validateConfirmation(result.Record, &request, time.Now()); err != nil {
		t.Fatalf("host policy rejected an approved exact artifact: %v", err)
	}
}
