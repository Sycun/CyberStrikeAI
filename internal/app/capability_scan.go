package app

import (
	"errors"
	"fmt"
	"path/filepath"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

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
