package handler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"cyberstrike-ai/internal/sse"
)

// The second half of the SSE contract: the page. The registry gate proves the server can
// only put declared names on the wire; these gates prove the wire and the code that reads
// it still describe the same set, in both directions, and that the published enum the
// browser loads is the same list.

// unhandledByPageBaseline was measured at 2 when this gate landed: the server can emit
// `model_output_rejected` and `eino_context_overflow_retry`, and no branch in the page
// reads them, so those frames arrive and are dropped. Lowering it means a renderer was
// added; raising it means a new event shipped without anything showing it.
const unhandledByPageBaseline = 2

// pageBranchesOnUnknownBaseline was measured at 1: monitor.js still has a `case 'warning'`
// and webshell.js an `_et === 'warning'` branch, for an event nothing in the backend ever
// emits. It is dead code rather than a bug - the writer refuses unregistered kinds, so the
// branch cannot be reached - and the fix is to delete the branch (or declare and emit the
// kind), not to widen the registry.
const pageBranchesOnUnknownBaseline = 1

func scanWeb(t *testing.T) []sse.WebSource {
	t.Helper()
	sources, err := sse.ScanWeb(moduleRoot(t))
	if err != nil {
		t.Fatalf("frontend scan: %v", err)
	}
	return sources
}

func nameSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		out[name] = true
	}
	return out
}

func difference(from map[string]bool, against map[string]bool) []string {
	out := []string{}
	for name := range from {
		if !against[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func TestSSEPageAndRegistryAgreeOnEventNames(t *testing.T) {
	inv := scanInventory(t)
	sources := scanWeb(t)

	emitted := nameSet(inv.NamesIn(sse.StreamAgent))
	handled := nameSet(sse.WebNames(sources, sse.TierStream))
	if len(handled) < 55 {
		t.Fatalf("the page branches on only %d frame types; the frontend scan is broken", len(handled))
	}
	t.Logf("emitted=%d handled=%d", len(emitted), len(handled))

	// Probes both ways, one per tier of the page scan.
	for _, probe := range []struct {
		name      string
		inEmitted bool
		inHandled bool
	}{
		{"tool_call", true, true}, // emitted and rendered
		{"heartbeat", true, true}, // transport kind the page ignores on purpose
		{"definitely_not_an_event", false, false},
	} {
		if got := emitted[probe.name]; got != probe.inEmitted {
			t.Fatalf("probe %s: emitted=%v, want %v", probe.name, got, probe.inEmitted)
		}
		if got := handled[probe.name]; got != probe.inHandled {
			t.Fatalf("probe %s: handled=%v, want %v", probe.name, got, probe.inHandled)
		}
	}

	dropped := difference(emitted, handled)
	if len(dropped) > unhandledByPageBaseline {
		t.Fatalf("the server can emit %d names the page ignores (baseline %d): %v. Either render them or stop emitting them.",
			len(dropped), unhandledByPageBaseline, dropped)
	}
	if len(dropped) < unhandledByPageBaseline {
		t.Logf("frames the page ignores dropped to %d; tighten unhandledByPageBaseline", len(dropped))
	}

	phantom := difference(handled, emitted)
	if len(phantom) > pageBranchesOnUnknownBaseline {
		t.Fatalf("the page branches on %d names the server never emits (baseline %d): %v. "+
			"The writer refuses undeclared kinds, so a new branch here is code that cannot run.",
			len(phantom), pageBranchesOnUnknownBaseline, phantom)
	}
	if len(phantom) < pageBranchesOnUnknownBaseline {
		t.Logf("dead page branches dropped to %d; tighten pageBranchesOnUnknownBaseline", len(phantom))
	}

	// The tier split is load-bearing: the history timeline reads persisted
	// process-detail types, and folding them into the stream tier would make a name that
	// only ever appears in history look like a live consumer - which would pass this test
	// while the frame itself stayed unrendered.
	if names := sse.WebNames(sources, sse.TierDetail); len(names) < 25 {
		t.Fatalf("the page reads only %d persisted detail types; the detail matcher is broken", len(names))
	} else {
		t.Logf("detail tier (not compared against the stream): %d", len(names))
	}
}

// TestSSEPageConsumesTheCommittedInventory keeps the frontend half in the same golden as
// the server half, so `make generate` and this gate disagree loudly rather than silently.
func TestSSEPageConsumesTheCommittedInventory(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), sseGoldenPath))
	if err != nil {
		t.Fatalf("golden inventory missing (regenerate with `go run ./internal/sse/gen -root .`): %v", err)
	}
	var golden struct {
		Consumed []string `json:"consumed"`
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("decode golden inventory: %v", err)
	}
	if len(golden.Consumed) == 0 {
		t.Fatal("the committed inventory records no frontend consumers; regenerate it")
	}

	handled := sse.WebNames(scanWeb(t), sse.TierStream)
	goldenSet := nameSet(golden.Consumed)
	handledSet := nameSet(handled)
	for _, name := range handled {
		if !goldenSet[name] {
			t.Errorf("the page branches on %q but the committed inventory omits it - regenerate the golden", name)
		}
	}
	for _, name := range golden.Consumed {
		if !handledSet[name] {
			t.Errorf("the committed inventory lists %q as page-consumed but no branch reads it any more", name)
		}
	}
}

// TestGeneratedSSEEnumMatchesTheRegistry pins the artifact the browser actually loads:
// an enum derived from the server but shipped stale would be worse than none, because the
// page would then warn about live events and stay quiet about the ones that vanished.
func TestGeneratedSSEEnumMatchesTheRegistry(t *testing.T) {
	root := moduleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "web/static/js/generated/sse-events.js"))
	if err != nil {
		t.Fatalf("generated enum missing (run `make generate`): %v", err)
	}
	agent := enumList(t, string(data), "agent")
	terminal := enumList(t, string(data), "terminal")

	inv := scanInventory(t)
	for name, pair := range map[string][2][]string{
		"agent":    {agent, inv.NamesIn(sse.StreamAgent)},
		"terminal": {terminal, inv.NamesIn(sse.StreamTerminal)},
	} {
		got, want := pair[0], pair[1]
		if len(got) != len(want) {
			t.Fatalf("%s enum has %d names, the registry emits %d - regenerate the enum", name, len(got), len(want))
		}
		gotSet, wantSet := nameSet(got), nameSet(want)
		for _, n := range got {
			if !wantSet[n] {
				t.Errorf("%s enum lists %q that the registry no longer emits", name, n)
			}
		}
		for _, n := range want {
			if !gotSet[n] {
				t.Errorf("%s registry emits %q that the enum does not list", name, n)
			}
		}
	}
}

// TestPageLoadsTheGeneratedEnum is the wiring check: an artifact nobody loads makes every
// content test above pass while the browser still runs with no contract at all.
func TestPageLoadsTheGeneratedEnum(t *testing.T) {
	root := moduleRoot(t)
	page, err := os.ReadFile(filepath.Join(root, "web", "templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := filepath.ToSlash(string(page))
	tag := strings.Index(html, "generated/sse-events.js")
	handler := strings.Index(html, "js/monitor.js")
	if tag < 0 {
		t.Fatal("index.html does not load the generated SSE enum")
	}
	if handler >= 0 && tag > handler {
		t.Fatalf("the enum must load before monitor.js, which calls isSSEEvent (enum at %d, monitor at %d)", tag, handler)
	}

	monitor, err := os.ReadFile(filepath.Join(root, "web", "static", "js", "monitor.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(monitor), "CSAI.isSSEEvent") {
		t.Fatal("monitor.js never calls the published enum, so the loaded script is dead weight")
	}
}

var enumItem = regexp.MustCompile(`^\s*"([a-z0-9_]+)",\s*$`)

// enumList reads one array out of the generated file rather than evaluating it, so the
// check runs in the Go suite without a JS runtime.
func enumList(t *testing.T, src, key string) []string {
	t.Helper()
	start := strings.Index(src, "\n  "+key+": [")
	if start < 0 {
		t.Fatalf("generated enum has no %q array", key)
	}
	rest := src[start:]
	end := strings.Index(rest, "\n  ],")
	if end < 0 {
		t.Fatalf("generated enum array %q is unterminated", key)
	}
	out := []string{}
	for _, line := range strings.Split(rest[:end], "\n")[1:] {
		if m := enumItem.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatalf("generated enum array %q parsed empty", key)
	}
	return out
}
