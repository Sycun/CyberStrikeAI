package agents

import (
	"cyberstrike-ai/internal/plugin"
)

// LoadMarkdownAgents reads the agent definitions the capability table lists when a table is
// installed, and falls back to scanning one directory when it is not.
//
// Both paths matter. The table is what lets a bundle contribute a sub-agent without a restart;
// the directory is what a process with no installed table (unit tests, or an assembly whose
// capability scan failed) must keep working on, exactly as before.
//
// Identity makes the merge safe rather than merely ordered: a unit is `agent/<basename>`, so a
// bundle cannot ship a second orchestrator.md or a same-named sub-agent - the install is
// refused with 409 by the table long before the loader's "at most one orchestrator" rule
// could be provoked into failing every run.
func LoadMarkdownAgents(agentsDir string) (*MarkdownDirLoad, error) {
	table := plugin.Global()
	if table == nil {
		return LoadMarkdownAgentsDir(agentsDir)
	}
	units := table.Units(plugin.KindAgent)
	paths := make([]string, 0, len(units))
	for _, u := range units {
		if !u.Enabled {
			continue
		}
		paths = append(paths, u.Path)
	}
	if len(paths) == 0 {
		// A table that holds no agents is not a statement that the installation has none -
		// the boot scan may simply not have run. Loading nothing here would silently remove
		// every markdown agent from every run.
		return LoadMarkdownAgentsDir(agentsDir)
	}
	return LoadMarkdownAgentPaths(paths)
}

// MarkdownAgentFiles is the admin-facing list: one row per agent definition the table knows,
// including which bundle provided it.
type MarkdownAgentFiles struct {
	Entries []FileAgent
	// BundleByFilename maps "recon.md" to the bundle that installed it; built-in files are absent.
	BundleByFilename map[string]string
}

func MarkdownAgentFilesFromTable(agentsDir string) (*MarkdownAgentFiles, error) {
	load, err := LoadMarkdownAgents(agentsDir)
	if err != nil {
		return nil, err
	}
	out := &MarkdownAgentFiles{
		Entries:          load.FileEntries,
		BundleByFilename: map[string]string{},
	}
	table := plugin.Global()
	if table == nil {
		return out, nil
	}
	for _, u := range table.Units(plugin.KindAgent) {
		if u.Bundle != "" {
			out.BundleByFilename[u.Name+".md"] = u.Bundle
		}
	}
	return out, nil
}
