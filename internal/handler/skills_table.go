package handler

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/skillpackage"

	"go.uber.org/zap"
)

// The skill endpoints used to resolve every path against one directory, h.config.SkillsDir.
// That stopped being true the moment skills could arrive from a bundle: the run path serves
// them (internal/einoskill reads the capability table), while this API still looked only in the
// built-in directory. Verified against the running server - a bundled skill was used by agents
// and was absent from GET /api/skills.
//
// Resolution therefore goes through the table too, and writes to a bundle's skill are refused
// rather than silently creating a same-named copy in the built-in directory.

// putSkillUnit / removeSkillUnit keep the built-in directory and the table in step when this API
// creates or deletes a skill. Assembly seeds the table through scanBuiltInCapabilities; without
// these, "create a skill" would write files that the new table-driven listing no longer shows.
func (h *SkillsHandler) putSkillUnit(name string) error {
	table := plugin.Global()
	if table == nil {
		return nil
	}
	path := filepath.Join(h.skillsRootAbs(), name)
	digest, err := plugin.Digest(path)
	if err != nil {
		return fmt.Errorf("digest %s: %w", path, err)
	}
	u, err := plugin.NewUnit(plugin.KindSkill, name, path)
	if err != nil {
		return err
	}
	u.Digest = digest
	if err := table.PutLocal(u); err != nil {
		var conflict *plugin.ErrConflict
		if errors.As(err, &conflict) {
			return fmt.Errorf("skill %q 由能力包 %q 提供，不能重复创建", name, conflict.Owner)
		}
		return err
	}
	return nil
}

func (h *SkillsHandler) removeSkillUnit(name string) error {
	table := plugin.Global()
	if table == nil {
		return nil
	}
	err := table.RemoveLocal(plugin.UnitIDFor(plugin.KindSkill, name))
	var conflict *plugin.ErrConflict
	if errors.As(err, &conflict) {
		return fmt.Errorf("skill %q 由能力包 %q 提供，请卸载该包", name, conflict.Owner)
	}
	return err
}

// skillLocation resolves a skill name to the (root, directory) pair the skillpackage helpers
// expect. Without an installed table this is exactly the old behaviour.
func (h *SkillsHandler) skillLocation(name string) (root, dir string) {
	name = strings.TrimSpace(name)
	if table := plugin.Global(); table != nil && name != "" {
		if u, ok := table.Unit(plugin.UnitIDFor(plugin.KindSkill, name)); ok {
			return filepath.Dir(u.Path), filepath.Base(u.Path)
		}
	}
	return h.skillsRootAbs(), name
}

// bundleOwnedSkill reports which installed bundle provides this skill.
func (h *SkillsHandler) bundleOwnedSkill(name string) (string, bool) {
	table := plugin.Global()
	if table == nil {
		return "", false
	}
	u, ok := table.Unit(plugin.UnitIDFor(plugin.KindSkill, name))
	if !ok || u.Bundle == "" {
		return "", false
	}
	return u.Bundle, true
}

// refuseBundleSkill answers a write against a bundle-owned skill.
func (h *SkillsHandler) refuseBundleSkill(name string) (string, bool) {
	bundle, owned := h.bundleOwnedSkill(name)
	if !owned {
		return "", false
	}
	return fmt.Sprintf("skill %q 由能力包 %q 提供，请改该包的内容或卸载它，不要在此覆盖", name, bundle), true
}

// installedSkillSummaries lists the skills the table reports, built-in plus bundled.
//
// The summariser is reused per parent directory rather than re-implemented, so "what counts as a
// skill" stays a single definition shared with the built-in path. Anything the table claims but
// the summariser could not read is logged instead of vanishing quietly: the run path would fail
// on it too, and a listing that silently drops it would hide the problem.
func (h *SkillsHandler) installedSkillSummaries() ([]skillpackage.SkillSummary, error) {
	table := plugin.Global()
	if table == nil {
		return skillpackage.ListSkillSummaries(h.skillsRootAbs())
	}
	units := table.Units(plugin.KindSkill)
	if len(units) == 0 {
		return skillpackage.ListSkillSummaries(h.skillsRootAbs())
	}

	wantedByRoot := map[string]map[string]bool{}
	for _, u := range units {
		root := filepath.Dir(u.Path)
		if wantedByRoot[root] == nil {
			wantedByRoot[root] = map[string]bool{}
		}
		wantedByRoot[root][filepath.Base(u.Path)] = true
	}

	out := make([]skillpackage.SkillSummary, 0, len(units))
	found := map[string]bool{}
	for root, wanted := range wantedByRoot {
		summaries, err := skillpackage.ListSkillSummaries(root)
		if err != nil {
			return nil, err
		}
		for _, s := range summaries {
			if !wanted[s.DirName] {
				continue
			}
			found[s.DirName] = true
			out = append(out, s)
		}
	}
	if len(found) < len(units) {
		missing := make([]string, 0, len(units)-len(found))
		for _, u := range units {
			if !found[u.Name] {
				missing = append(missing, u.ID)
			}
		}
		h.logger.Warn("部分能力单元无法作为 skill 读取", zap.Strings("units", missing))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DirName < out[j].DirName })
	return out, nil
}

// installedSkillDirNames is the name-only form used by the refresh endpoint.
func (h *SkillsHandler) installedSkillDirNames() ([]string, error) {
	summaries, err := h.installedSkillSummaries()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, s.DirName)
	}
	return out, nil
}
