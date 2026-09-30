package sse

import (
	"fmt"
	"path/filepath"
)

// The traversal domain of the SSE event contract, in one place on purpose: the
// generator that writes the catalogue and the gates that compare a registry against it
// must scan the same tree with the same rules. Two copies of a domain drift, and a gate
// that reads a narrower tree than the generator certifies a contract the catalogue never
// claimed.

// The domain has two halves.
//
// Emitter directories are where frames are assembled - the HTTP layer. internal/sse
// itself is excluded: it defines the envelope, not the events.
//
// Conduit directories are where progress callbacks are *called*. The callback is created
// in the HTTP layer and handed to the agent runtime, the workflow engine and the Eino
// observers, so the names a client can receive are not all spelled out inside the emitter
// package. Restricting the scan to the emitters produced an 18-name registry that refused
// live events such as tool_call and workflow_start.
var (
	emitterDomain = []string{"internal/handler"}
	conduitDomain = []string{"internal"}
)

// BuildInventory returns every event name that can reach a frame under root. An emit
// site whose name cannot be traced is an error rather than a dropped entry: a silently
// short catalogue would let every downstream gate pass.
func BuildInventory(root string) (Inventory, error) {
	inv, err := Scan(joinDomain(root, emitterDomain)...)
	if err != nil {
		return Inventory{}, err
	}
	if len(inv.Unresolved) > 0 {
		var sites []string
		for _, u := range inv.Unresolved {
			sites = append(sites, fmt.Sprintf("%s:%d (%s)", u.File, u.Line, u.Name))
		}
		return Inventory{}, fmt.Errorf("event names reaching a frame without a traceable source: %v - "+
			"the emitter must pass a name the inventory can see", sites)
	}
	conduit, err := ConduitNames(joinDomain(root, conduitDomain)...)
	if err != nil {
		return Inventory{}, err
	}
	return Merge(inv, conduit), nil
}

// Merge unions an inventory with additional provenance sites, deduplicated by the triple
// that identifies a site.
func Merge(inv Inventory, extra []KindSource) Inventory {
	seen := map[string]bool{}
	key := func(s KindSource) string { return s.Name + "|" + s.File + "|" + s.Mode }
	for _, s := range inv.Sources {
		seen[key(s)] = true
	}
	sources := append([]KindSource{}, inv.Sources...)
	for _, s := range extra {
		if !seen[key(s)] {
			seen[key(s)] = true
			sources = append(sources, s)
		}
	}
	return Inventory{Sources: sources}
}

// InventoryFloors are the cardinality guards for BuildInventory, measured on this tree
// (64 names: 61 agent, 3 terminal). They sit below the current count so a legitimate
// removal does not trip them, while an extractor that silently stopped following names
// would produce a catalogue short enough to fail.
func InventoryFloors() (total, agent, terminal int) { return 55, 52, 3 }

func joinDomain(root string, dirs []string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, filepath.Join(root, d))
	}
	return out
}
