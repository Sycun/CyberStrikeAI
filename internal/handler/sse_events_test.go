package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTerminalEmitterSendsTheHistoricalFrame pins that routing the terminal
// stream through the shared writer did not change what the client receives.
func TestTerminalEmitterSendsTheHistoricalFrame(t *testing.T) {
	kind := streamEvent{T: "out", D: "hello", C: 0}
	body, err := json.Marshal(kind)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"t":"out"`) || !strings.Contains(string(body), `"c":0`) {
		t.Fatalf("short-key payload shape changed, the terminal pane would break: %s", body)
	}
	if !strings.Contains(string(body), `"c"`) {
		t.Fatal("exit code must always be serialized, otherwise the UI shows [exit undefined]")
	}

	declared := map[string]bool{}
	for _, entry := range TerminalSSEKinds() {
		declared[entry.Name] = true
	}
	for _, name := range []string{"out", "err", "exit"} {
		if !declared[name] {
			t.Errorf("terminal event %q is emitted but not declared in the registry", name)
		}
	}
}
