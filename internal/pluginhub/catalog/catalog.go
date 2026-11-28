// Package catalog contains curated metadata for the read-only discovery UI.
package catalog

import _ "embed"

//go:embed artex.yaml
var artex []byte

type Entry struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Repository   string `json:"repository"`
	Revision     string `json:"revision"`
	Summary      string `json:"summary"`
	Availability string `json:"availability"`
	Builtin      bool   `json:"builtin"`
}

func Entries() []Entry {
	return []Entry{
		{
			ID:           "artex",
			Title:        "ARTEX",
			Repository:   "https://github.com/RuoJi6/ARTEX",
			Summary:      "AI autonomous penetration testing platform (Go + Next.js + PostgreSQL) with CSAI LLM gateway reuse.",
			Availability: "plan_available",
			Builtin:      true,
		},
	}
}

func HasBuiltin(id string) bool {
	for _, entry := range Entries() {
		if entry.ID == id {
			return true
		}
	}
	return false
}

func Manifest(id string) ([]byte, bool) {
	switch id {
	case "artex":
		return append([]byte(nil), artex...), true
	default:
		return nil, false
	}
}
