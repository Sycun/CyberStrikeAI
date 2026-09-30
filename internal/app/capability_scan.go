package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/handler"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// scanBuiltInCapabilities loads the shipped capability directories into the plug-in table.
//
// This is not bookkeeping. The Eino skill middleware now reads its skills *from the table*
// (internal/einoskill), so if the built-in skills/ directory were not scanned here, switching to
// the table backend would serve zero skills and every agent run would lose them. Scanning makes
// "shipped" and "installed later" the same kind of object, which is what lets one code path serve
// both.
//
// Roles are deliberately absent: RoleHandler.Reload() owns that directory so the scan and the
// published configuration snapshot stay in one place.
func scanBuiltInCapabilities(table *plugin.Table, cfg *config.Config, configPath string, logger *zap.Logger) error {
	if table == nil {
		return errors.New("no capability table installed")
	}
	if cfg == nil {
		return errors.New("no configuration to resolve capability directories from")
	}
	configDir := filepath.Dir(configPath)

	for _, src := range builtInCapabilitySources(cfg, configDir, configPath) {
		if src.dir == "" {
			continue // this kind is not configured in this installation
		}
		units, err := plugin.ScanDir(src.kind, src.dir, src.namer)
		if err != nil {
			return fmt.Errorf("scan %s directory %s: %w", src.kind, src.dir, err)
		}
		scanned := 0
		for _, u := range units {
			if err := table.PutLocal(u); err != nil {
				var conflict *plugin.ErrConflict
				if errors.As(err, &conflict) {
					// An installed bundle owns this identity: the bundle wins, and the next
					// publish would otherwise silently replace a pack the operator chose.
					continue
				}
				return fmt.Errorf("record %s: %w", u.ID, err)
			}
			scanned++
		}
		if logger != nil {
			logger.Info("内置能力已登记到能力表",
				zap.String("kind", string(src.kind)),
				zap.String("dir", src.dir),
				zap.Int("units", scanned))
		}
	}
	return nil
}

type builtInSource struct {
	kind  plugin.Kind
	dir   string
	namer plugin.Namer
}

// builtInCapabilitySources resolves each directory the same way config.Load does, so a relative
// path means the same thing to the scan and to the loader that reads these files today.
func builtInCapabilitySources(cfg *config.Config, configDir, configPath string) []builtInSource {
	toolNamer := func(kind plugin.Kind, path string) (string, error) {
		if kind != plugin.KindTool {
			return "", nil
		}
		tool, err := config.LoadToolFromFile(path)
		if err != nil {
			return "", err
		}
		return tool.Name, nil
	}
	return []builtInSource{
		// No default for skills: an empty skills_dir means "this installation has no skill
		// directory", and inventing one would load skills a config explicitly turned off.
		{kind: plugin.KindSkill, dir: resolveUnderConfig(cfg.SkillsDir, configDir, "")},
		{kind: plugin.KindAgent, dir: resolveUnderConfig(cfg.AgentsDir, configDir, "agents")},
		{kind: plugin.KindTool, dir: config.ResolveToolsDir(cfg.Security.ToolsDir, configPath), namer: toolNamer},
	}
}

func resolveUnderConfig(dir, configDir, fallback string) string {
	name := dir
	if name == "" {
		name = fallback
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(configDir, name)
}

// installBundlesFromDisk re-installs the packs that live under <configDir>/bundles.
//
// Without this an installed capability lasts until the next restart, which is not what
// "install" means to the person who clicked it: every run path reads the table, and the table is
// rebuilt from disk at start-up. A pack on disk is therefore part of the installation, not a
// session.
//
// The built-in scan runs first on purpose. Identity is what makes the merge safe, so a pack that
// shadows a shipped capability must be refused here exactly as the install endpoint refuses it,
// rather than winning because it happened to load before the built-in scan could object.
//
// A pack that cannot be read is reported and skipped: one half-written bundle.yaml must not stop
// the server from booting, and must not be able to take the *other* packs' capabilities away.
func installBundlesFromDisk(table *plugin.Table, root string, logger *zap.Logger) (int, []string) {
	if table == nil || strings.TrimSpace(root) == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, nil // no bundles directory is a normal state, not a failure
	}
	var installed int
	var refused []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		dir := filepath.Join(root, name)
		bundle, err := loadBundleForScan(dir)
		if err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if err := table.InstallBundle(bundle); err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		installed++
	}
	if logger != nil && (installed > 0 || len(refused) > 0) {
		logger.Info("能力包已从磁盘重新装入能力表",
			zap.String("root", root), zap.Int("installed", installed), zap.Int("refused", len(refused)))
		for _, r := range refused {
			logger.Warn("能力包未能装入，其余包与内置能力不受影响", zap.String("bundle", r))
		}
	}
	return installed, refused
}

func loadBundleForScan(dir string) (*plugin.Bundle, error) {
	m, err := plugin.LoadManifestDir(dir)
	if err != nil {
		return nil, err
	}
	return m.Resolve()
}

// bundleOwnedToolUnits counts the recipes that came from a pack rather than from tools_dir.
//
// The built-in ones are already in the live tool list, because config.Load scanned tools_dir
// before the table existed; only a pack's recipe needs the table-driven rebuild at start-up.
func bundleOwnedToolUnits(table *plugin.Table) int {
	if table == nil {
		return 0
	}
	var n int
	for _, u := range table.Units(plugin.KindTool) {
		if u.Bundle != "" {
			n++
		}
	}
	return n
}

// provisionDeclaredServers re-declares the MCP servers that packs own into the live external-MCP
// manager at start-up. The manager reads its servers from config.yaml, which a pack cannot edit,
// so without this a declaration would exist only in the table until somebody reinstalled the pack.
//
// Every one of them comes back switched off, and the capability unit is flipped off to match. A
// pack's file saying `enabled: true` is the pack author's intent, not the operator's consent to
// spawn a process, and the table has no persisted switch state to honour - so the same rule that
// holds for install holds here: declaring is not starting. A server the operator wants to live
// across restarts belongs in config.yaml, where their own declaration is the consent.
func provisionDeclaredServers(mgr *mcp.ExternalMCPManager, table *plugin.Table) (int, string) {
	if mgr == nil || table == nil {
		return 0, "未启用外部 MCP 管理器，包声明的服务器只留在能力表里"
	}
	var declared int
	var note string
	for _, u := range table.Units(plugin.KindMCP) {
		if u.Enabled {
			off, err := table.SetEnabled(u.ID, false)
			if err != nil {
				note = fmt.Sprintf("%s: %v", u.ID, err)
				continue
			}
			u = off
		}
		cfg, err := handler.LoadMCPDeclaration(u.Path)
		if err != nil {
			note = fmt.Sprintf("%s: %v", u.ID, err)
			continue
		}
		cfg.Disabled = !u.Enabled
		cfg.ExternalMCPEnable = u.Enabled
		if err := mgr.DeclarePackServer(u.Name, u.Bundle, cfg); err != nil {
			note = fmt.Sprintf("%s: %v", u.ID, err)
			continue
		}
		declared++
	}
	return declared, note
}

// applyPersistedSwitches re-applies the operator's own on/off decisions after the packs have been
// rebuilt from disk, and prunes the rows that no longer describe anything.
//
// Only a saved "off" is acted on. A row that said "on" cannot turn a unit whose source file says
// `enabled: false` back on: the runtime state stays `file enabled AND table enabled`, which is the
// same rule the tool layer runs, and it is what keeps a stale row from widening what may execute.
// A row whose unit is gone, or whose source slot no longer matches, is forgotten - otherwise
// shipping a different capability under an identity somebody switched off would inherit that
// switch, and the console would show a unit as unavailable with no reason anyone could name.
// The comparison is on the slot (`roles/x.yaml`), not the whole path, because the same
// installation reaches different absolute paths depending on how config.yaml was named.
func applyPersistedSwitches(table *plugin.Table, switches *store.CapabilitySwitches, logger *zap.Logger) (int, []string) {
	if table == nil || switches == nil {
		return 0, nil
	}
	rows, err := switches.All()
	if err != nil {
		if logger != nil {
			logger.Warn("读取能力单元开关失败，本次启动只按文件状态服务", zap.Error(err))
		}
		return 0, nil
	}
	var applied int
	var notes []string
	for _, sw := range rows {
		unit, ok := table.Unit(sw.UnitID)
		if !ok || store.SwitchPathKey(unit.Path) != store.SwitchPathKey(sw.Path) {
			if err := switches.Forget(sw.UnitID); err != nil {
				notes = append(notes, fmt.Sprintf("%s: %v", sw.UnitID, err))
			}
			continue
		}
		if sw.Enabled || !unit.Enabled {
			continue // already in the state the operator chose
		}
		if _, err := table.SetEnabled(sw.UnitID, false); err != nil {
			notes = append(notes, fmt.Sprintf("%s: %v", sw.UnitID, err))
			continue
		}
		applied++
	}
	if logger != nil && applied > 0 {
		logger.Info("已按保存的开关重新停用能力单元", zap.Int("units", applied))
	}
	return applied, notes
}
