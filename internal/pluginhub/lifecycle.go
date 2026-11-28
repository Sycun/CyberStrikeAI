package pluginhub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub/runner"

	"go.uber.org/zap"
)

func (m *Manager) execute(ctx context.Context, install database.PluginRuntimeInstall, op database.PluginOperation) (database.PluginObservation, error) {
	result := observation(install)
	switch op.Kind {
	case "install", "start", "restart":
		version := install.Version
		if op.Kind == "install" {
			version = op.VersionID
		}
		spec, driver, err := m.spec(ctx, install.ID, version)
		if err != nil {
			return result, err
		}
		if err := m.assertApproved(ctx, spec); err != nil {
			return result, err
		}
		result.Version = version
		if op.Kind == "restart" || install.Observed == "cleanup_pending" {
			if err := m.stopTraffic(ctx, install.ID); err != nil {
				return result, err
			}
			if err := driver.Stop(ctx, spec); err != nil {
				result.Observed = "cleanup_pending"
				return result, err
			}
		}
		if op.Kind == "start" && install.Observed == "healthy" {
			state, err := driver.Inspect(ctx, spec)
			if err == nil && state.Running {
				return markRunning(result, state, spec), nil
			}
		}
		if err := driver.Prepare(ctx, spec); err != nil {
			result.Observed = "failed"
			return result, err
		}
		if op.Kind == "install" && !m.consumeSkipAutoBind(install.ID) && spec.Declaration.LLM != nil {
			if bound, bindErr := m.gateway.EnsureDefaultBinding(ctx, install.ID, spec.Declaration.LLM); bindErr != nil {
				spec.Log("Default LLM channel auto-bind skipped: " + bindErr.Error())
			} else if bound {
				spec.Log("Auto-bound CSAI default LLM channel")
			}
		}
		state, err := m.startPlugin(ctx, driver, spec)
		if err != nil {
			result.Observed, result.Desired = "failed", "stopped"
			if err := stopAfterFailure(driver, spec); err != nil {
				result.Observed = "cleanup_pending"
			}
			return result, err
		}
		return markRunning(result, state, spec), nil
	case "stop":
		result.Desired = "stopped"
		if err := m.stopVersions(ctx, install.ID, false); err != nil {
			result.Observed = "cleanup_pending"
			return result, err
		}
		result.Observed = "stopped"
		return result, nil
	case "upgrade":
		return m.upgrade(ctx, install, op)
	case "uninstall":
		result.Desired = "stopped"
		if err := m.stopVersions(ctx, install.ID, true); err != nil {
			result.Observed = "cleanup_pending"
			return result, err
		}
		if op.PurgeData {
			if err := os.RemoveAll(filepath.Join(m.root, install.ID)); err != nil {
				result.Observed = "cleanup_pending"
				return result, err
			}
		}
		if err := m.web.Retire(ctx, install.ID); err != nil {
			result.Observed = "cleanup_pending"
			return result, err
		}
		result.Observed, result.Version = "removed", ""
		return result, nil
	default:
		return result, ErrInvalidRequest
	}
}

func (m *Manager) upgrade(ctx context.Context, install database.PluginRuntimeInstall, op database.PluginOperation) (database.PluginObservation, error) {
	result := observation(install)
	old, oldDriver, err := m.spec(ctx, install.ID, install.Version)
	if err != nil {
		return result, err
	}
	next, nextDriver, err := m.spec(ctx, install.ID, op.VersionID)
	if err != nil {
		return result, err
	}
	if err := m.assertApproved(ctx, next); err != nil {
		return result, err
	}
	if old.VersionID == next.VersionID {
		state, err := oldDriver.Inspect(ctx, old)
		if err == nil && state.Running {
			result = markRunning(result, state, old)
		}
		return result, err
	}
	// Stage the new artifact before stopping the current process. The old active
	// pointer stays committed until the replacement passes readiness.
	if err := nextDriver.Prepare(ctx, next); err != nil {
		state, inspectErr := oldDriver.Inspect(ctx, old)
		if inspectErr == nil && state.Running {
			result = markRunning(result, state, old)
		}
		return result, err
	}
	if err := m.stopTraffic(ctx, install.ID); err != nil {
		return result, err
	}
	if err := oldDriver.Stop(ctx, old); err != nil {
		result.Observed = "cleanup_pending"
		return result, err
	}
	if err := snapshotData(ctx, next); err != nil {
		return m.restorePrevious(result, old, oldDriver, err)
	}
	if install.Desired != "running" {
		result.Version, result.Driver, result.Observed = next.VersionID, next.Plan.Artifact.Driver, "stopped"
		return result, nil
	}
	state, err := m.startPlugin(ctx, nextDriver, next)
	if err == nil {
		return markRunning(result, state, next), nil
	}
	if cleanupErr := stopAfterFailure(nextDriver, next); cleanupErr != nil {
		result.Observed = "cleanup_pending"
		return result, cleanupErr
	}
	if restoreErr := restoreData(next); restoreErr != nil {
		result.Observed = "cleanup_pending"
		return result, restoreErr
	}
	return m.restorePrevious(result, old, oldDriver, err)
}

func (m *Manager) restorePrevious(result database.PluginObservation, old runner.Spec, driver runner.Driver, failure error) (database.PluginObservation, error) {
	result.Observed = "stopped"
	if result.Desired != "running" {
		return result, failure
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := m.assertApproved(ctx, old); err != nil {
		return result, failure
	}
	state, err := m.startPlugin(ctx, driver, old)
	if err != nil {
		result.Observed = "cleanup_pending"
		return result, failure
	}
	old.Log("Previous version restored")
	return markRunning(result, state, old), failure
}

func stopAfterFailure(driver runner.Driver, spec runner.Spec) error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return driver.Stop(ctx, spec)
}

func (m *Manager) stopVersions(ctx context.Context, id string, remove bool) error {
	if err := m.stopTraffic(ctx, id); err != nil {
		return err
	}
	versions, err := m.db.PluginVersions(ctx, id)
	if err != nil {
		return err
	}
	for _, version := range versions {
		spec, driver, err := m.spec(ctx, id, version.ID)
		if err != nil {
			return err
		}
		if remove {
			err = driver.Remove(ctx, spec)
		} else {
			err = driver.Stop(ctx, spec)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) reconcile(ctx context.Context, start bool) {
	interrupted, err := m.db.InterruptedPluginOperations(ctx)
	if err != nil {
		m.logger.Warn("Could not read interrupted plugin operations", zap.Error(err))
		return
	}
	byInstall := map[string]database.PluginOperation{}
	for _, op := range interrupted {
		byInstall[op.InstallID] = op
	}
	installs, err := m.db.RecoverablePluginInstalls(ctx)
	if err != nil {
		m.logger.Warn("Could not read plugin recovery state", zap.Error(err))
		return
	}
	seen := map[string]bool{}
	for _, install := range installs {
		seen[install.ID] = true
		m.reconcileInstance(ctx, install, byInstall[install.ID], start)
	}
	for _, op := range interrupted {
		if seen[op.InstallID] {
			continue
		}
		if install, err := m.db.RuntimePluginInstall(ctx, op.InstallID); err == nil {
			m.reconcileInstance(ctx, install, op, start)
		}
	}
}

func (m *Manager) reconcileInstance(ctx context.Context, install database.PluginRuntimeInstall, interrupted database.PluginOperation, start bool) {
	result := observation(install)
	if err := m.stopVersions(ctx, install.ID, false); err != nil {
		result.Observed, result.ErrorCode = "cleanup_pending", "plugin_cleanup_pending"
		result.Endpoint, result.Port = "", 0
		_ = m.db.SetPluginObservation(ctx, m.owner, install.ID, result)
		return
	}
	rollback := interrupted
	if rollback.ID == "" && install.Observed == "cleanup_pending" {
		prior, err := m.db.LastPluginOperation(ctx, install.ID)
		if err == nil && prior.Status == "failed" && prior.VersionID != install.Version {
			rollback = prior
		}
	}
	if rollback.Kind == "upgrade" {
		next, _, err := m.spec(ctx, install.ID, rollback.VersionID)
		if err != nil || restoreData(next) != nil {
			result.Observed, result.ErrorCode = "cleanup_pending", "plugin_cleanup_pending"
			result.Endpoint, result.Port = "", 0
			_ = m.db.SetPluginObservation(ctx, m.owner, install.ID, result)
			return
		}
	}
	if interrupted.ID != "" {
		_ = m.db.InterruptPluginOperation(ctx, m.owner, interrupted.ID)
	}
	result.Observed, result.Endpoint, result.Port = "stopped", "", 0
	if result.Version == "" {
		result.Desired = "stopped"
		if interrupted.Kind == "install" {
			// The approved version remains available for an explicit retry.
			result.Version = interrupted.VersionID
		}
	}
	if start && result.Desired == "running" && result.Version != "" {
		spec, driver, err := m.spec(ctx, install.ID, result.Version)
		if err == nil {
			err = m.assertApproved(ctx, spec)
		}
		if err == nil {
			err = driver.Prepare(ctx, spec)
		}
		if err == nil {
			var state runner.State
			state, err = m.startPlugin(ctx, driver, spec)
			if err == nil {
				result = markRunning(result, state, spec)
			} else {
				result.Observed, result.ErrorCode = "failed", "plugin_recovery_failed"
				if cleanup := stopAfterFailure(driver, spec); cleanup != nil {
					result.Observed = "cleanup_pending"
				}
			}
		} else {
			result.ErrorCode = "plugin_recovery_unavailable"
		}
	}
	if err := m.db.SetPluginObservation(ctx, m.owner, install.ID, result); err != nil && ctx.Err() == nil {
		m.logger.Warn("Could not persist plugin reconciliation", zap.Error(err))
	}
}

func (m *Manager) observe(ctx context.Context) {
	installs, err := m.db.RecoverablePluginInstalls(ctx)
	if err != nil {
		return
	}
	for _, i := range installs {
		if i.Observed == "cleanup_pending" {
			interrupted, err := m.db.InterruptedPluginOperations(ctx)
			if err != nil {
				continue
			}
			var active database.PluginOperation
			for _, op := range interrupted {
				if op.InstallID == i.ID {
					active = op
					break
				}
			}
			m.reconcileInstance(ctx, i, active, false)
			continue
		}
		if i.Observed != "healthy" {
			continue
		}
		spec, driver, err := m.spec(ctx, i.ID, i.Version)
		if err != nil {
			continue
		}
		state, err := driver.Inspect(ctx, spec)
		if err != nil || state.CheckHealth(ctx, spec.Declaration.Web) != nil {
			result := observation(i)
			result.Observed, result.ErrorCode = "degraded", "plugin_runtime_unhealthy"
			result.Endpoint, result.Port = "", 0
			_ = m.db.SetPluginObservation(ctx, m.owner, i.ID, result)
		}
	}
}

func validSnapshotVersion(spec runner.Spec) error {
	if spec.Root == "" || spec.VersionID == "" {
		return fmt.Errorf("plugin snapshot lacks owned instance identity")
	}
	return nil
}
