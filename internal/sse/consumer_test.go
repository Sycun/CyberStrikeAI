package sse

import (
	"os"
	"path/filepath"
	"testing"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test working directory")
		}
		dir = parent
	}
}

// TestScanWebTiers pins what the frontend scan claims, including the two things it must
// not claim: a same-named field on an unrelated object (the fact graph's node types) and a
// switch over a C2 task type are not event consumers.
func TestScanWebTiers(t *testing.T) {
	sources, err := ScanWeb(moduleRoot(t))
	if err != nil {
		t.Fatalf("ScanWeb: %v", err)
	}
	stream := WebNames(sources, TierStream)
	detail := WebNames(sources, TierDetail)
	c2 := WebNames(sources, TierC2)
	t.Logf("stream=%d detail=%d c2=%d", len(stream), len(detail), len(c2))

	contains := func(list []string, name string) bool {
		for _, item := range list {
			if item == name {
				return true
			}
		}
		return false
	}

	for _, probe := range []struct {
		tier string
		name string
		list []string
		want bool
		why  string
	}{
		{TierStream, "tool_call", stream, true, "handled by the frame switch"},
		{TierStream, "thinking_stream_end", stream, true, "only the webshell reader branches on it"},
		{TierStream, "message_saved", stream, true, "an if, not a case"},
		{TierStream, "vulnerability", stream, false, "a fact-graph node type, not a frame"},
		{TierStream, "shell", stream, false, "a C2 task type"},
		{TierDetail, "knowledge_retrieval", detail, true, "a persisted detail type"},
		{TierC2, "session", c2, true, "the C2 pane branches on it"},
		{TierC2, "task", c2, true, "the C2 pane branches on it"},
		{TierC2, "shell", c2, false, "a task type switch, not a category"},
	} {
		if got := contains(probe.list, probe.name); got != probe.want {
			t.Errorf("%s tier: %q present=%v, want %v (%s)", probe.tier, probe.name, got, probe.want, probe.why)
		}
	}
	if len(c2) != 2 {
		t.Errorf("c2 tier has %d names (%v), want exactly session and task", len(c2), c2)
	}
}
