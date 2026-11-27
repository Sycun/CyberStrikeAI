// Package taxonomy provides a small, offline classification reference.
package taxonomy

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed wstg.json cwe.json attack.json
var data embed.FS

type Entry struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

type Pack struct {
	entries map[string][]Entry
}

var Default = sync.OnceValues(load)

func load() (*Pack, error) {
	p := &Pack{entries: make(map[string][]Entry)}
	for _, source := range []string{"wstg", "cwe", "attack"} {
		raw, err := data.ReadFile(source + ".json")
		if err != nil {
			return nil, err
		}
		var entries []Entry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("decode %s taxonomy: %w", source, err)
		}
		p.entries[source] = entries
	}
	return p, nil
}

func (p *Pack) Lookup(source, id string) (Entry, bool) {
	source = strings.ToLower(strings.TrimSpace(source))
	id = strings.TrimSpace(id)
	for _, entry := range p.entries[source] {
		if strings.EqualFold(entry.ID, id) {
			return entry, true
		}
	}
	return Entry{}, false
}

func (p *Pack) Search(source, query string, limit int) []Entry {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	limit = min(limit, 20)
	source = strings.ToLower(strings.TrimSpace(source))
	results := make([]Entry, 0, limit)
	searchWSTG42 := source == "wstg" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(query)), "wstg-v42-")
	exact, found := p.Lookup(source, query)
	if found {
		results = append(results, exact)
	}
	for _, entry := range p.entries[source] {
		if len(results) == limit {
			break
		}
		if found && entry.ID == exact.ID {
			continue
		}
		if !searchWSTG42 && strings.HasPrefix(entry.ID, "WSTG-v42-") {
			continue
		}
		text := strings.ToLower(entry.ID + " " + entry.Title + " " + entry.Summary)
		if containsAll(text, terms) {
			results = append(results, entry)
		}
	}
	return results
}

func containsAll(text string, terms []string) bool {
	for _, term := range terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}
