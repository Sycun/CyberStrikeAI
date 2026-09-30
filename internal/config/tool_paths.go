package config

import (
	"strings"
)

// ToolRecipeSource is one recipe file plus the capability table's switch for it.
type ToolRecipeSource struct {
	Path string
	// Enabled comes from the table. It can only turn a recipe off: the merged list carries
	// `file enabled AND table enabled`, never `table enabled`. A unit's table flag defaults to
	// true when a directory is scanned, so treating it as an override would silently flip on
	// every recipe whose YAML says enabled: false.
	Enabled bool
}

// ToolLoadFailure is a recipe file that is shaped like a recipe but did not load.
type ToolLoadFailure struct {
	Path   string
	Reason string
}

// ToolPathsLoad is the result of loading recipes from an explicit file list.
type ToolPathsLoad struct {
	Tools []ToolConfig
	// Failed lists the files that did not load. The directory loader has always skipped those
	// with a warning instead of failing the whole load, so swallowing them here would hide a
	// bundle whose recipe is broken; the caller reports them.
	Failed []ToolLoadFailure
}

// MergeToolsFromSources loads an explicit recipe list and merges the inline security.tools
// entries behind it: a file-provided recipe wins over an inline one with the same name, exactly
// as the directory scan does today.
//
// A source whose table switch is off stays in the list with Enabled=false rather than being
// dropped, so the console still shows the recipe as disabled and an inline entry cannot revive
// it under the same name.
func MergeToolsFromSources(srcs []ToolRecipeSource, inlineTools []ToolConfig) *ToolPathsLoad {
	out := &ToolPathsLoad{Tools: make([]ToolConfig, 0, len(srcs))}
	seen := make(map[string]bool, len(srcs))
	for _, src := range srcs {
		p := strings.TrimSpace(src.Path)
		if p == "" {
			continue
		}
		tool, err := LoadToolFromFile(p)
		if err != nil {
			out.Failed = append(out.Failed, ToolLoadFailure{Path: p, Reason: err.Error()})
			continue
		}
		if seen[tool.Name] {
			continue
		}
		seen[tool.Name] = true
		tool.Enabled = tool.Enabled && src.Enabled
		out.Tools = append(out.Tools, *tool)
	}
	for _, tool := range inlineTools {
		if !seen[tool.Name] {
			seen[tool.Name] = true
			out.Tools = append(out.Tools, tool)
		}
	}
	return out
}

// ReloadSecurityToolsFromSources updates cfg.Security.Tools from an explicit recipe list. It is
// the table-driven counterpart of ReloadSecurityToolsFromDir, and it leaves the config untouched
// when the list is empty: a caller must not be able to blank the live tool list by passing
// nothing.
func ReloadSecurityToolsFromSources(cfg *Config, configPath string, srcs []ToolRecipeSource) ([]ToolLoadFailure, error) {
	if cfg == nil || len(srcs) == 0 {
		return nil, nil
	}
	inlineTools, err := loadInlineSecurityToolsFromYAML(configPath)
	if err != nil {
		return nil, err
	}
	merged := MergeToolsFromSources(srcs, inlineTools)
	cfg.Security.Tools = merged.Tools
	return merged.Failed, nil
}
