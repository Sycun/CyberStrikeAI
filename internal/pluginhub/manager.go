package pluginhub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub/llmgateway"
	"cyberstrike-ai/internal/pluginhub/manifest"
	"cyberstrike-ai/internal/pluginhub/runner"
	"cyberstrike-ai/internal/pluginhub/source"
	"cyberstrike-ai/internal/pluginhub/webproxy"
	"cyberstrike-ai/internal/security"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const leaseTTL = 30 * time.Second

type Manager struct {
	web            *webproxy.Service
	gateway        *llmgateway.Service
	webAuth        *security.AuthManager
	db             *database.DB
	root           string
	logger         *zap.Logger
	approvalPolicy string
	approvals      map[string][]string
	proxyMode      string
	locker         ArtifactLocker
	inspector      *source.GitHubInspector
	drivers        map[string]runner.Driver
	owner          string
	wake           chan struct{}
	ready          atomic.Bool
	mu             sync.Mutex
	skipAutoBind   map[string]bool
	cancel         context.CancelFunc
	done           chan struct{}
}

func New(db *database.DB, cfg config.PluginsConfig, configDir string, logger *zap.Logger) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	network, err := source.NewEgress(cfg.Egress)
	if err != nil {
		return nil, err
	}
	root := cfg.Directory
	if root == "" {
		root = filepath.Join("data", "plugins")
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(configDir, root)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		db: db, root: root, logger: logger, approvalPolicy: cfg.EffectiveApprovalPolicy(), approvals: maps.Clone(cfg.ApprovedArtifacts), proxyMode: cfg.Proxy.Effective().Mode, locker: network,
		inspector: source.NewGitHubInspectorWithTransport(network.Transport(), os.Getenv(cfg.Egress.GitHubTokenEnv)),
		drivers: map[string]runner.Driver{
			"release":   runner.NewNative("release", network, cfg.Egress.ProxyMode, cfg.Egress.ProxyURL),
			"git":       runner.NewNative("git", network, cfg.Egress.ProxyMode, cfg.Egress.ProxyURL),
			"container": runner.NewDocker(),
		},
		owner: uuid.NewString(), wake: make(chan struct{}, 1), skipAutoBind: map[string]bool{},
	}
	for key, digests := range m.approvals {
		m.approvals[key] = slices.Clone(digests)
	}
	m.web = webproxy.New(cfg.Proxy, db, m.webTarget, m.webAuthorization)
	m.gateway = llmgateway.New(db, cfg.LLMGateway, cfg.Egress)
	return m, nil
}

func (m *Manager) Inspector() *source.GitHubInspector { return m.inspector }

func (m *Manager) Probes(ctx context.Context, isolation string) map[string]runner.Probe {
	probes := map[string]runner.Probe{}
	for name, driver := range m.drivers {
		probes[name] = driver.Probe(ctx, isolation)
	}
	if docker, ok := m.drivers["container"].(interface {
		ProbeCompose(context.Context, string) runner.Probe
	}); ok {
		probes["compose"] = docker.ProbeCompose(ctx, isolation)
	}
	return probes
}

func (m *Manager) Ready() bool { return m.ready.Load() }

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done != nil {
		return nil
	}
	now := time.Now().UTC()
	acquired, err := m.db.RenewPluginLease(ctx, m.owner, now, now.Add(leaseTTL))
	if err != nil || !acquired {
		return ErrRuntimeUnavailable
	}
	worker, cancel := context.WithCancel(ctx)
	m.cancel, m.done = cancel, make(chan struct{})
	go m.run(worker, m.done, cancel)
	return nil
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) run(ctx context.Context, done chan struct{}, cancel context.CancelFunc) {
	defer close(done)
	leaseCtx, stopHeartbeat := context.WithCancel(context.Background())
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case now := <-tick.C:
				probe, finish := context.WithTimeout(leaseCtx, 3*time.Second)
				ok, err := m.db.RenewPluginLease(probe, m.owner, now.UTC(), now.UTC().Add(leaseTTL))
				finish()
				if !ok || err != nil {
					if leaseCtx.Err() == nil {
						m.logger.Warn("Plugin runtime lost its database lease")
						cancel()
					}
					return
				}
			}
		}
	}()
	defer func() {
		m.ready.Store(false)
		cleanup, finish := context.WithTimeout(context.Background(), 25*time.Second)
		now := time.Now().UTC()
		owned, err := m.db.RenewPluginLease(cleanup, m.owner, now, now.Add(leaseTTL))
		if err == nil && owned {
			m.reconcile(cleanup, false)
		}
		finish()
		stopHeartbeat()
		<-heartbeatDone
		release, finish := context.WithTimeout(context.Background(), 3*time.Second)
		defer finish()
		_ = m.db.ReleasePluginLease(release, m.owner)
	}()
	m.reconcile(ctx, true)
	if ctx.Err() != nil {
		return
	}
	m.ready.Store(true)
	poll := time.NewTicker(350 * time.Millisecond)
	defer poll.Stop()
	observe := time.NewTicker(10 * time.Second)
	defer observe.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-observe.C:
			m.observe(ctx)
		case <-m.wake:
		case <-poll.C:
		}
		for ctx.Err() == nil {
			op, err := m.db.ClaimPluginOperation(ctx, m.owner, time.Now().UTC())
			if err != nil {
				break
			}
			m.runOperation(ctx, op)
		}
	}
}

type OperationRequest struct {
	Actor, Kind, InstallID, PlanID, PlanDigest, Key string
	Purge                                           bool
	AutoBindLLM                                     *bool
}

func (m *Manager) consumeSkipAutoBind(installID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.skipAutoBind[installID] {
		return false
	}
	delete(m.skipAutoBind, installID)
	return true
}

var idempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func (m *Manager) Queue(ctx context.Context, req OperationRequest) (database.PluginOperation, error) {
	if !slices.Contains([]string{"install", "start", "stop", "restart", "upgrade", "uninstall"}, req.Kind) ||
		!idempotencyKey.MatchString(req.Key) || req.Actor == "" || (req.Purge && req.Kind != "uninstall") {
		return database.PluginOperation{}, ErrInvalidRequest
	}
	var record database.PluginPlanRecord
	if req.Kind == "install" || req.Kind == "upgrade" {
		if req.PlanID == "" || req.PlanDigest == "" {
			return database.PluginOperation{}, ErrInvalidRequest
		}
		var err error
		record, err = m.db.GetPluginPlanForConfirmation(ctx, req.PlanID, req.Actor)
		if err != nil {
			return database.PluginOperation{}, err
		}
		if req.Kind == "install" {
			req.InstallID = record.InstallID
		}
		if req.InstallID != record.InstallID || req.PlanDigest != record.PlanDigest {
			return database.PluginOperation{}, ErrStalePlan
		}
	} else if req.PlanID != "" || req.PlanDigest != "" {
		return database.PluginOperation{}, ErrInvalidRequest
	}
	instance, err := uuid.Parse(req.InstallID)
	if err != nil || instance.String() != req.InstallID || instance == uuid.Nil {
		return database.PluginOperation{}, ErrInvalidRequest
	}
	raw, _ := json.Marshal(req)
	hash := manifest.Digest(raw)
	previous, err := m.db.PluginOperationByKey(ctx, req.InstallID, req.Key)
	if err == nil {
		if previous.Actor == req.Actor && previous.RequestHash == hash {
			return previous, nil
		}
		return database.PluginOperation{}, database.ErrPluginOperationConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return database.PluginOperation{}, err
	}
	if !m.Ready() {
		return database.PluginOperation{}, ErrRuntimeUnavailable
	}
	request := database.PluginQueueRequest{
		Operation: database.PluginOperation{ID: uuid.NewString(), InstallID: req.InstallID, Actor: req.Actor,
			Kind: req.Kind, Key: req.Key, PlanDigest: req.PlanDigest, RequestHash: hash, PurgeData: req.Purge},
		PlanID: req.PlanID,
	}
	now := time.Now().UTC()
	if req.PlanID != "" {
		if !record.ExpiresAt.After(now) {
			return database.PluginOperation{}, database.ErrPluginPlanUnavailable
		}
		if err := m.validateConfirmation(record, &request, now); err != nil {
			return database.PluginOperation{}, err
		}
	}
	if req.Kind == "install" && req.AutoBindLLM != nil && !*req.AutoBindLLM {
		m.mu.Lock()
		m.skipAutoBind[req.InstallID] = true
		m.mu.Unlock()
	}
	operation, err := m.db.QueuePluginOperation(ctx, request, now)
	if err != nil && req.Kind == "install" && req.AutoBindLLM != nil && !*req.AutoBindLLM {
		m.consumeSkipAutoBind(req.InstallID)
	}
	if err == nil {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
	return operation, err
}

func (m *Manager) validateConfirmation(record database.PluginPlanRecord, request *database.PluginQueueRequest, now time.Time) error {
	plan, err := manifest.DecodeInstallPlan([]byte(record.PlanJSON))
	if err != nil || plan.ID != record.ID || plan.ActorID != record.ActorUserID || plan.InstallID != record.InstallID ||
		manifest.Digest(plan.Canonical) != record.PlanDigest || manifest.Digest([]byte(record.PolicyJSON)) != plan.PolicySHA256 ||
		plan.ManifestSHA256 != record.ManifestDigest {
		return ErrStalePlan
	}
	doc, err := manifest.DecodeDeclaration([]byte(record.ManifestJSON))
	if err != nil || manifest.Digest(doc.Canonical) != plan.ManifestSHA256 {
		return ErrStalePlan
	}
	var policy planPolicy
	artifactDigest := ArtifactDigest(plan.Artifact)
	if err := json.Unmarshal([]byte(record.PolicyJSON), &policy); err != nil || policy.ArtifactDigest != artifactDigest || policy.Isolation != plan.Isolation {
		return ErrStalePlan
	}
	if !m.CanConfirm(plan.ManifestSHA256, artifactDigest) {
		return ErrUnapprovedArtifact
	}
	canonical, err := source.NormalizeGitHubRepository(doc.Metadata.Repository)
	if err != nil {
		return ErrStalePlan
	}
	approval, err := json.Marshal(versionApproval{
		Policy: json.RawMessage(record.PolicyJSON), PlanID: record.ID, PlanDigest: record.PlanDigest,
		ApprovedBy: record.ActorUserID, ApprovedAt: now,
	})
	if err != nil {
		return err
	}
	request.CatalogID = policy.CatalogID
	request.Install = database.PluginRuntimeInstall{ID: plan.InstallID, PluginID: doc.Metadata.ID, Source: canonical, Title: doc.Metadata.Title}
	request.Version = database.PluginVersion{
		ID: uuid.NewString(), InstallID: plan.InstallID, Version: doc.Metadata.Version, Driver: plan.Artifact.Driver,
		Manifest: record.ManifestJSON, ManifestDigest: plan.ManifestSHA256,
		Artifact:       plan.Artifact.Driver + ":" + plan.Platform.OS + ":" + plan.Platform.Arch + ":" + plan.PolicySHA256,
		ArtifactDigest: artifactDigest, Policy: string(approval), Plan: record.PlanJSON,
	}
	return nil
}

func (m *Manager) spec(ctx context.Context, id, versionID string) (runner.Spec, runner.Driver, error) {
	record, err := m.db.PluginVersion(ctx, id, versionID)
	if err != nil {
		return runner.Spec{}, nil, err
	}
	doc, err := manifest.DecodeDeclaration([]byte(record.Manifest))
	if err != nil || manifest.Digest(doc.Canonical) != record.ManifestDigest {
		return runner.Spec{}, nil, ErrStalePlan
	}
	plan, err := manifest.DecodeInstallPlan([]byte(record.Plan))
	if err != nil || plan.InstallID != id || plan.ManifestSHA256 != record.ManifestDigest ||
		plan.Artifact.Driver != record.Driver || ArtifactDigest(plan.Artifact) != record.ArtifactDigest ||
		!versionPolicyMatches(record.Policy, plan) {
		return runner.Spec{}, nil, ErrStalePlan
	}
	driver := m.drivers[record.Driver]
	if driver == nil {
		return runner.Spec{}, nil, ErrRuntimeUnavailable
	}
	for _, value := range []string{id, versionID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.String() != value {
			return runner.Spec{}, nil, ErrStalePlan
		}
	}
	return runner.Spec{InstallID: id, VersionID: versionID, Root: filepath.Join(m.root, id), Declaration: doc,
		Plan: plan, Log: m.logSink(id)}, driver, nil
}

func (m *Manager) logSink(id string) func(string) {
	var lock sync.Mutex
	var start time.Time
	var count int
	return func(line string) {
		lock.Lock()
		now := time.Now()
		if now.Sub(start) >= time.Second {
			start, count = now, 0
		}
		count++
		allowed := count <= 30
		lock.Unlock()
		if !allowed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.db.AppendPluginLog(ctx, id, runner.SanitizeLog(line, nil))
	}
}

type versionApproval struct {
	Policy     json.RawMessage `json:"policy"`
	PlanID     string          `json:"plan_id"`
	PlanDigest string          `json:"plan_digest"`
	ApprovedBy string          `json:"approved_by"`
	ApprovedAt time.Time       `json:"approved_at"`
}

func versionPolicyMatches(raw string, plan *manifest.InstallPlan) bool {
	if manifest.Digest([]byte(raw)) == plan.PolicySHA256 {
		// Earlier versions stored the policy itself. Their approval timestamp
		// already lives in plugin_install_plans.
		return true
	}
	var approved versionApproval
	return json.Unmarshal([]byte(raw), &approved) == nil &&
		approved.PlanID == plan.ID && approved.ApprovedBy == plan.ActorID &&
		approved.PlanDigest == manifest.Digest(plan.Canonical) && !approved.ApprovedAt.IsZero() &&
		manifest.Digest(approved.Policy) == plan.PolicySHA256
}

func (m *Manager) assertApproved(ctx context.Context, spec runner.Spec) error {
	if m.approvalPolicy == config.PluginApprovalHostConfigOnly {
		if m.approved(spec.Plan.ManifestSHA256, ArtifactDigest(spec.Plan.Artifact)) {
			return nil
		}
		return ErrUnapprovedArtifact
	}
	approved, err := m.db.PluginPlanApproved(ctx, spec.Plan.ID, spec.InstallID, spec.Plan.ActorID, manifest.Digest(spec.Plan.Canonical))
	if err != nil {
		return err
	}
	if !approved {
		return ErrUnapprovedArtifact
	}
	return nil
}

func observation(i database.PluginRuntimeInstall) database.PluginObservation {
	return database.PluginObservation{Desired: i.Desired, Observed: i.Observed, Version: i.Version, Driver: i.Driver, Endpoint: i.Endpoint, Port: i.Port, ErrorCode: i.ErrorCode}
}

func markRunning(old database.PluginObservation, state runner.State, spec runner.Spec) database.PluginObservation {
	old.Desired, old.Observed, old.Endpoint, old.Port = "running", "healthy", state.Endpoint, state.Port
	old.Version, old.Driver, old.ErrorCode = spec.VersionID, spec.Plan.Artifact.Driver, ""
	return old
}

func (m *Manager) runOperation(parent context.Context, op database.PluginOperation) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Minute)
	defer cancel()
	current, err := m.db.RuntimePluginInstall(ctx, op.InstallID)
	result := observation(current)
	if err == nil {
		log := m.logSink(op.InstallID)
		log("Operation started: " + op.Kind)
		result, err = m.execute(ctx, current, op)
		if err != nil {
			switch {
			case errors.Is(err, ErrUnapprovedArtifact):
				result.ErrorCode = "plugin_artifact_not_approved"
			case result.Observed == "cleanup_pending":
				result.ErrorCode = "plugin_cleanup_pending"
			default:
				result.ErrorCode = "plugin_" + op.Kind + "_failed"
			}
			log(fmt.Sprintf("Operation failed: %s", result.ErrorCode))
		} else {
			log("Operation finished: " + op.Kind)
		}
	}
	if result.Observed == "" {
		result.Observed = "failed"
	}
	if result.Desired == "" {
		result.Desired = "stopped"
	}
	if result.Observed != "healthy" {
		result.Endpoint, result.Port = "", 0
	}
	if err == nil {
		result.ErrorCode = ""
	}
	commit, finish := context.WithTimeout(context.Background(), 4*time.Second)
	defer finish()
	if commitErr := m.db.FinishPluginOperation(commit, m.owner, op, result, err != nil); commitErr != nil {
		m.logger.Warn("Plugin operation outcome awaits reconciliation", zap.String("operation", op.ID), zap.Error(commitErr))
	} else if err == nil && op.Kind == "upgrade" {
		_ = os.RemoveAll(snapshotPath(runner.Spec{Root: filepath.Join(m.root, op.InstallID), VersionID: op.VersionID}))
	}
}
