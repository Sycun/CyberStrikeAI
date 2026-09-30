package plugin

import "sync"

// The table is process-wide because the code that serves capabilities reaches it from deep
// inside a run (the Eino skill middleware is built per run, five calls below the assembly), and
// threading a new parameter through RunDeepAgent's twenty-five arguments would cost more than
// the indirection is worth. This mirrors how internal/capability and internal/pluginhost already
// expose one assembly-owned object.
//
// It is installed exactly once, and a gate in internal/app fails if assembly stops doing so -
// without that, readers would silently fall back to the single-directory behaviour and a bundle
// would look installed while nothing served it.
var (
	globalMu sync.RWMutex
	global   *Table
)

// Install makes t the process-wide table. Pass nil only from tests that want it cleared.
func Install(t *Table) {
	globalMu.Lock()
	defer globalMu.Unlock()
	global = t
}

// Global returns the installed table, or nil when none is.
func Global() *Table {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}
