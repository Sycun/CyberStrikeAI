// Package catalog contains curated metadata for the read-only discovery UI.
package catalog

import _ "embed"

//go:embed static-web.yaml
var staticWeb []byte

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
		{ID: "static-web", Title: "Static Web Runtime Check", Repository: "https://github.com/nginx/docker-nginx-unprivileged",
			Summary: "Unprivileged static WebUI for lifecycle verification.", Availability: "plan_available", Builtin: true},
		{
			ID: "librechat", Title: "LibreChat", Repository: "https://github.com/LibreChat-AI/LibreChat",
			Revision:     "566e20b613389600a7975fd8f7c1bffbe54f06c8",
			Summary:      "Self-hosted chat WebUI used for plugin platform interoperability verification.",
			Availability: "inspection_only",
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
	case "static-web":
		return append([]byte(nil), staticWeb...), true
	default:
		return nil, false
	}
}
