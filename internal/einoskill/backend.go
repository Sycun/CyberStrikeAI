// Package einoskill adapts the plug-in capability table to Eino's skill middleware.
//
// Eino's own backend takes a single BaseDir and globs "*/SKILL.md" inside it. That shape cannot
// express "the built-in skills/ directory plus whatever a bundle installed", which is the whole
// point of a plug-in, so this package implements the two-method skill.Backend over table units
// instead - no symlink farm, no materialised copy of somebody else's files.
//
// It is an Eino-facing adapter on purpose: this is one of the packages allowed to import Eino
// types permanently (see internal/layering/eino_ratchet_test.go), so moving skill loading here
// also takes it out of the debt side of the convergence.
package einoskill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cyberstrike-ai/internal/plugin"

	"github.com/cloudwego/eino/adk/middlewares/skill"
	"gopkg.in/yaml.v3"
)

const skillFileName = "SKILL.md"

// Backend serves the enabled skill units of one capability table.
//
// Nothing is cached: the table hands out an immutable snapshot, so a bundle installed between two
// agent turns is visible on the next one, which is the behaviour the plug-in layer promises.
type Backend struct {
	table *plugin.Table
}

func NewBackend(table *plugin.Table) (*Backend, error) {
	if table == nil {
		return nil, fmt.Errorf("einoskill: nil capability table")
	}
	return &Backend{table: table}, nil
}

// List returns every enabled skill's front matter, ordered by unit name.
//
// One unreadable or malformed SKILL.md fails the whole call rather than being skipped, matching
// Eino's filesystem backend: a silently missing skill is a capability the model was never told
// about, and the run would look healthy while it happened.
func (b *Backend) List(ctx context.Context) ([]skill.FrontMatter, error) {
	units := b.table.Units(plugin.KindSkill)
	out := make([]skill.FrontMatter, 0, len(units))
	for _, u := range units {
		if !u.Enabled {
			continue
		}
		sk, err := load(u.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to load skill from %s: %w", filepath.Join(u.Path, skillFileName), err)
		}
		out = append(out, sk.FrontMatter)
	}
	return out, nil
}

// Get resolves a skill by the name in its own front matter, the same key Eino's middleware uses
// when the model asks for one. Matching on the *directory* name instead would break for any
// bundle whose directory differs from the declared skill name.
func (b *Backend) Get(ctx context.Context, name string) (skill.Skill, error) {
	for _, u := range b.table.Units(plugin.KindSkill) {
		if !u.Enabled {
			continue
		}
		sk, err := load(u.Path)
		if err != nil {
			return skill.Skill{}, fmt.Errorf("failed to load skill from %s: %w", filepath.Join(u.Path, skillFileName), err)
		}
		if sk.Name == name {
			return sk, nil
		}
	}
	return skill.Skill{}, fmt.Errorf("skill not found: %s", name)
}

// load parses one skill directory. The rules are copied from Eino's filesystem backend so that
// swapping backends cannot change what a skill renders as - see TestBackendMatchesEinoBackend.
func load(skillDir string) (skill.Skill, error) {
	path := filepath.Join(skillDir, skillFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return skill.Skill{}, fmt.Errorf("failed to read file: %w", err)
	}
	frontmatter, content, err := parseFrontmatter(string(data))
	if err != nil {
		return skill.Skill{}, fmt.Errorf("failed to parse frontmatter: %w", err)
	}
	var fm skill.FrontMatter
	if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return skill.Skill{}, fmt.Errorf("failed to unmarshal frontmatter: %w", err)
	}
	return skill.Skill{
		FrontMatter:   fm,
		Content:       strings.TrimSpace(content),
		BaseDirectory: filepath.Dir(path),
	}, nil
}

// parseFrontmatter splits "---\n<yaml>\n---\n<body>". Deliberately *not* run through Eino's
// stripLineNumbers: that helper exists because Eino's local filesystem backend prefixes each
// line with "N\t". These bytes come straight from disk, so stripping would truncate any skill
// body containing a real tab.
func parseFrontmatter(data string) (frontmatter string, content string, err error) {
	const delimiter = "---"
	data = strings.TrimSpace(data)
	if !strings.HasPrefix(data, delimiter) {
		return "", "", fmt.Errorf("file does not start with frontmatter delimiter")
	}
	rest := data[len(delimiter):]
	endIdx := strings.Index(rest, "\n"+delimiter)
	if endIdx == -1 {
		return "", "", fmt.Errorf("frontmatter closing delimiter not found")
	}
	frontmatter = strings.TrimSpace(rest[:endIdx])
	content = rest[endIdx+len("\n"+delimiter):]
	content = strings.TrimPrefix(content, "\n")
	return frontmatter, content, nil
}
