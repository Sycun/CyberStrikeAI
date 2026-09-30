package handler

import (
	"path/filepath"
	"sort"
	"testing"

	"cyberstrike-ai/internal/sse"
)

// The third producer/consumer pair in this product, and the one nobody gated: after a page
// refresh the timeline is rebuilt from persisted `process_details` rows, and the renderer
// branches on `eventType`. The producer is the store, not the SSE writer, so the stream gates
// said nothing about it.

// historyBranchesNothingCanProduce was measured at 1: chat.js's history ladder branches on
// `workflow_agent_start`, and neither the persisted sinks nor the stream can ever produce that
// name - the workflow engine writes `workflow_start`. The branch is dead.
const historyBranchesNothingCanProduce = 1

// persistedRowsThePageIgnores was measured at 1: `finalization_check` is written into
// process_details, and the history renderer has no branch for it, so the row is stored and
// then invisible after a refresh (the live stream does render it, which is what makes this
// easy to miss).
const persistedRowsThePageIgnores = 1

func persistedInventory(t *testing.T) sse.Inventory {
	t.Helper()
	root := moduleRoot(t)
	inv, err := sse.ScanPersistedDetails(filepath.Join(root, "internal"), filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatalf("persisted inventory: %v", err)
	}
	names := inv.NamesIn(sse.StreamDetail)
	if len(names) < 8 {
		t.Fatalf("the persisted scan found %d event types; the sink table is stale or broken", len(names))
	}
	return inv
}

func TestPersistedDetailTierMatchesThePage(t *testing.T) {
	persisted := nameSet(persistedInventory(t).NamesIn(sse.StreamDetail))
	stream := nameSet(scanInventory(t).NamesIn(sse.StreamAgent))
	page := sse.WebNames(scanWeb(t), sse.TierDetail)
	if len(page) < 25 {
		t.Fatalf("the page branches on only %d persisted event types; the frontend detail matcher is broken", len(page))
	}
	t.Logf("persisted=%d stream=%d pageHistory=%d", len(persisted), len(stream), len(page))

	// Probes on both sides of the union, and one that must be absent from both.
	for _, probe := range []struct {
		name        string
		inPersisted bool
		inStream    bool
		inPage      bool
		why         string
	}{
		{"knowledge_retrieval", true, false, true, "written by the retrieval path, read back by history"},
		{"tool_call", false, true, true, "carried by the progress callback, so not in the precise persisted set"},
		{"finalization_check", true, true, false, "persisted but unrendered - the drift this gate exists for"},
		{"workflow_agent_start", false, false, true, "a branch the backend cannot produce"},
		{"definitely_not_a_detail_type", false, false, false, "must not be invented by either matcher"},
	} {
		pageSet := nameSet(page)
		if got := persisted[probe.name]; got != probe.inPersisted {
			t.Fatalf("probe %s: persisted=%v, want %v (%s)", probe.name, got, probe.inPersisted, probe.why)
		}
		if got := stream[probe.name]; got != probe.inStream {
			t.Fatalf("probe %s: stream=%v, want %v (%s)", probe.name, got, probe.inStream, probe.why)
		}
		if got := pageSet[probe.name]; got != probe.inPage {
			t.Fatalf("probe %s: page history=%v, want %v (%s)", probe.name, got, probe.inPage, probe.why)
		}
	}

	// A history branch nothing can feed is dead code. Compared against the union of both
	// producers, which over-approximates what can reach a row, so this direction cannot
	// report a false positive.
	producible := map[string]bool{}
	for name := range persisted {
		producible[name] = true
	}
	for name := range stream {
		producible[name] = true
	}
	dead := difference(nameSet(page), producible)
	if len(dead) > historyBranchesNothingCanProduce {
		t.Errorf("the page has %d history branches nothing can produce (baseline %d): %v. "+
			"Either the name was renamed on the backend, or the branch should go.",
			len(dead), historyBranchesNothingCanProduce, missingFrom(page, producible))
	}
	if len(dead) < historyBranchesNothingCanProduce {
		t.Logf("dead history branches dropped to %d; tighten historyBranchesNothingCanProduce", len(dead))
	}

	// A row the renderer ignores is stored and then invisible. This direction is precise and
	// therefore the weaker of the two: names that reach a row through the progress callback are
	// not in the precise persisted set (the value graph keys parameter positions by declared
	// function names, and a callback is a variable), so an unrendered callback-carried row
	// would not be caught here. Recorded rather than papered over.
	unrendered := difference(persisted, nameSet(page))
	if len(unrendered) > persistedRowsThePageIgnores {
		t.Errorf("the store can persist %d event types the history renderer ignores (baseline %d): %v. "+
			"The row is written and then never shown after a refresh.",
			len(unrendered), persistedRowsThePageIgnores, unrendered)
	}
	if len(unrendered) < persistedRowsThePageIgnores {
		t.Logf("unrendered persisted rows dropped to %d; tighten persistedRowsThePageIgnores", len(unrendered))
	}
}

// TestGoldenRecordsThePersistedTier keeps the third inventory in the same committed artifact
// as the other two, so CI's regenerate-and-diff covers it.
func TestGoldenRecordsThePersistedTier(t *testing.T) {
	golden := readSSEGolden(t)
	live := persistedInventory(t).NamesIn(sse.StreamDetail)
	if len(golden.Persisted) == 0 {
		t.Fatal("the committed inventory records no persisted event types; regenerate it")
	}
	got, want := nameSet(live), nameSet(golden.Persisted)
	for _, name := range live {
		if !want[name] {
			t.Errorf("%q can be persisted but the committed inventory omits it - regenerate the golden", name)
		}
	}
	for _, name := range golden.Persisted {
		if !got[name] {
			t.Errorf("the committed inventory lists persisted type %q that nothing writes any more", name)
		}
	}
}

// missingFrom lists the names of `from` that are absent from `against`, sorted for the error.
func missingFrom(from []string, against map[string]bool) []string {
	out := []string{}
	for _, name := range from {
		if !against[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
