package surfaceingest

import (
	"strings"
	"sync"
)

// applied records execution receipts that have already been parsed and written.
// An empty execution ID is a local tool result with no shared receipt, so each
// caller owns that ingest. Deep assessment keeps handling those results and the
// planner wake that follows a real asset write.
var (
	appliedMu sync.Mutex
	applied   = map[string]struct{}{}
)

const appliedLimit = 8192

// Claim returns true when this caller should parse and write the receipt.
// A second caller for the same execution ID must skip both the parse and the write.
func Claim(executionID string) bool {
	id := strings.TrimSpace(executionID)
	if id == "" {
		return true
	}
	appliedMu.Lock()
	defer appliedMu.Unlock()
	if _, ok := applied[id]; ok {
		return false
	}
	if len(applied) >= appliedLimit {
		applied = map[string]struct{}{}
	}
	applied[id] = struct{}{}
	return true
}

// ResetClaims clears the receipt set. Tests use it so cases do not share state.
func ResetClaims() {
	appliedMu.Lock()
	applied = map[string]struct{}{}
	appliedMu.Unlock()
}
