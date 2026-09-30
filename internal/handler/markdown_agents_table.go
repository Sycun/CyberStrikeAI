package handler

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cyberstrike-ai/internal/plugin"
)

// Markdown agent files reach the run path through the capability table now, so the admin API
// resolves them the same way: a definition that lives inside an installed bundle is readable but
// not editable here, exactly like a bundled role or skill.

// markdownAgentUnit looks up the capability unit behind one "<name>.md" filename.
func markdownAgentUnit(filename string) (plugin.Unit, bool) {
	table := plugin.Global()
	if table == nil {
		return plugin.Unit{}, false
	}
	name := strings.TrimSpace(filename)
	if name == "" {
		return plugin.Unit{}, false
	}
	// The table stores the identity without the extension ("agent/recon" for recon.md),
	// matching how markdown agents are addressed everywhere else.
	u, ok := table.Unit(plugin.UnitIDFor(plugin.KindAgent, strings.TrimSuffix(name, filepath.Ext(name))))
	return u, ok
}

// markdownAgentPath resolves a filename to the file the table says provides it, falling back to
// the handler's own directory. The second return value reports a bundle-owned file.
func markdownAgentPath(dir, filename string) (path string, fromBundle string, err error) {
	if u, ok := markdownAgentUnit(filename); ok {
		if u.Bundle != "" {
			return u.Path, u.Bundle, nil
		}
		return u.Path, "", nil
	}
	if strings.Contains(filepath.Base(filename), "..") || filepath.Clean(filename) != filename {
		return "", "", fmt.Errorf("非法文件名")
	}
	return filepath.Join(dir, filename), "", nil
}

// refuseBundleMarkdownAgent answers a write against a bundle-provided definition.
func refuseBundleMarkdownAgent(filename string) (string, bool) {
	u, ok := markdownAgentUnit(filename)
	if !ok || u.Bundle == "" {
		return "", false
	}
	return fmt.Sprintf("%s 由能力包 %q 提供，请改该包的内容或卸载它，不要在此覆盖", filename, u.Bundle), true
}

// putMarkdownAgentUnit registers a markdown agent the admin API just created, so the run path
// that reads the table sees it. A file the table does not track yet stays invisible otherwise.
func putMarkdownAgentUnit(dir, filename string) error {
	table := plugin.Global()
	if table == nil {
		return nil
	}
	path := filepath.Join(dir, filename)
	digest, err := plugin.Digest(path)
	if err != nil {
		return fmt.Errorf("digest %s: %w", path, err)
	}
	u, err := plugin.NewUnit(plugin.KindAgent, strings.TrimSuffix(filename, filepath.Ext(filename)), path)
	if err != nil {
		return err
	}
	u.Digest = digest
	if err := table.PutLocal(u); err != nil {
		var conflict *plugin.ErrConflict
		if errors.As(err, &conflict) {
			return fmt.Errorf("%s 由能力包 %q 提供，不能重复创建", filename, conflict.Owner)
		}
		return err
	}
	return nil
}

func removeMarkdownAgentUnit(filename string) error {
	table := plugin.Global()
	if table == nil {
		return nil
	}
	err := table.RemoveLocal(plugin.UnitIDFor(plugin.KindAgent, strings.TrimSuffix(filename, filepath.Ext(filename))))
	var conflict *plugin.ErrConflict
	if errors.As(err, &conflict) {
		return fmt.Errorf("%s 由能力包 %q 提供，请卸载该包", filename, conflict.Owner)
	}
	return err
}
