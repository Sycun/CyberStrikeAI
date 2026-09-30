package mcp

import (
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

// The inventory observer is called from code paths that already hold the manager's own mutex.
// sync.RWMutex is not reentrant, so a notify that reads the observer under that same lock
// deadlocks a server removal forever - which is exactly what the first version of this hook did,
// and it showed up only as a 10-minute test timeout on DELETE /api/external-mcp/:name.

func TestInventoryObserverDoesNotDeadlockRemovalPaths(t *testing.T) {
	manager := NewExternalMCPManager(zap.NewNop())

	var mu sync.Mutex
	var seen []string
	note := func(serverName string, tools []Tool) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, serverName)
	}
	manager.SetToolInventoryObserver(note)

	if err := manager.AddOrUpdateConfig("no-deadlock", config.ExternalMCPServerConfig{
		URL: "http://127.0.0.1:1/mcp", ExternalMCPEnable: true,
	}); err != nil {
		t.Fatalf("AddOrUpdateConfig: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- manager.RemoveConfig("no-deadlock")
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RemoveConfig: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("RemoveConfig deadlocked with an inventory observer installed")
	}

	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) == 0 || got[len(got)-1] != "no-deadlock" {
		t.Fatalf("the observer never learned about the removal: %v", got)
	}
}

func TestInventoryObserverSeesCacheWritesAndStop(t *testing.T) {
	manager := NewExternalMCPManager(zap.NewNop())

	type event struct {
		server string
		tools  int
	}
	var mu sync.Mutex
	var events []event
	manager.SetToolInventoryObserver(func(serverName string, tools []Tool) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event{server: serverName, tools: len(tools)})
	})

	manager.updateToolCache("githack", []Tool{{Name: "search"}, {Name: "issues"}})

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := manager.RemoveConfig("githack"); err != nil {
			t.Errorf("RemoveConfig: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("removing a server with an observer installed deadlocked")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("observer events = %+v, want a cache write and a removal", events)
	}
	if events[0].server != "githack" || events[0].tools != 2 {
		t.Fatalf("first event = %+v, want githack with two tools", events[0])
	}
	if events[1].server != "githack" || events[1].tools != 0 {
		t.Fatalf("second event = %+v, want githack with an empty inventory", events[1])
	}
}

// TestClearingAllCachesEmptiesEachInventory: an invalidation that left stale capabilities
// registered would be worse than no observer at all.
func TestClearingAllCachesEmptiesEachInventory(t *testing.T) {
	manager := NewExternalMCPManager(zap.NewNop())
	var mu sync.Mutex
	emptied := map[string]int{}
	manager.SetToolInventoryObserver(func(serverName string, tools []Tool) {
		mu.Lock()
		defer mu.Unlock()
		if len(tools) == 0 {
			emptied[serverName]++
		}
	})

	manager.updateToolCache("a", []Tool{{Name: "one"}})
	manager.updateToolCache("b", []Tool{{Name: "two"}})
	manager.InvalidateAllToolCaches()

	mu.Lock()
	defer mu.Unlock()
	if emptied["a"] != 1 || emptied["b"] != 1 {
		t.Fatalf("invalidation notified %v, want one empty event per server", emptied)
	}
}
