package settings

import (
	"strconv"
	"sync"
	"testing"

	"cyberstrike-ai/internal/config"
)

// TestConcurrentUpdateAndReadIsRaceFree is the S4 gate. Before this package
// existed the same shape of test failed under -race, because ApplyConfig wrote
// HITL fields in place while agents read them through the shared pointer.
func TestConcurrentUpdateAndReadIsRaceFree(t *testing.T) {
	store := New(&config.Config{Hitl: config.HitlConfig{
		DefaultMode:   "approval",
		ToolWhitelist: []string{"read_file"},
	}})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				store.UpdateHitl(func(hitl *config.HitlConfig) {
					hitl.DefaultMode = "review_edit"
					hitl.DefaultReviewer = strconv.Itoa(n)
					hitl.ToolWhitelist = append([]string{"read_file", "glob"}, strconv.Itoa(j))
				})
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 400; j++ {
				snapshot := store.Hitl()
				if len(snapshot.ToolWhitelist) == 0 {
					t.Error("a published snapshot lost its whitelist")
					return
				}
				_ = store.Current().Hitl.DefaultReviewer
			}
		}()
	}
	wg.Wait()
}

// TestSnapshotIsolation proves an in-flight reader cannot observe a later write:
// the writer replaces the snapshot instead of editing the one being read.
func TestSnapshotIsolation(t *testing.T) {
	store := New(&config.Config{Hitl: config.HitlConfig{ToolWhitelist: []string{"one"}}})

	before := store.Hitl()
	store.UpdateHitl(func(hitl *config.HitlConfig) {
		hitl.ToolWhitelist = append(hitl.ToolWhitelist, "two")
		hitl.DefaultMode = "off"
	})

	if len(before.ToolWhitelist) != 1 {
		t.Fatalf("held snapshot changed underneath the reader: %v", before.ToolWhitelist)
	}
	if after := store.Hitl(); len(after.ToolWhitelist) != 2 {
		t.Fatalf("new snapshot = %v, want the appended pair", after.ToolWhitelist)
	}
}

// TestReplacePublishesWholeSnapshot covers the reload path: ApplyConfig swaps the
// entire config after a tools-dir reload.
func TestReplacePublishesWholeSnapshot(t *testing.T) {
	store := New(&config.Config{})
	next := &config.Config{Hitl: config.HitlConfig{DefaultMode: "human"}}
	next.Security.Tools = []config.ToolConfig{{Name: "nmap_scan"}}

	store.Replace(next)
	if got := store.Current().Hitl.DefaultMode; got != "human" {
		t.Fatalf("mode = %q", got)
	}
	if len(store.Current().Security.Tools) != 1 {
		t.Fatal("tools were not published with the snapshot")
	}

	// A nil publish must not hand readers a nil pointer.
	store.Replace(nil)
	if store.Current() == nil {
		t.Fatal("nil Replace produced a nil snapshot")
	}
}
