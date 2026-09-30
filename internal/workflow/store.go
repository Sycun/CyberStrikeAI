package workflow

import (
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/project"
)

// Store is the persistence surface a workflow run needs: its own run/node-run ledger plus the
// definition it executes and the HITL handshake it waits on - and, because a workflow node can run
// a deep agent that maintains the project fact index, everything internal/project needs.
//
// Declared here because this package is the consumer. Passing the 361-method *database.DB into the
// Eino runtime meant every graph node could reach any table; with this interface the runtime can
// only write workflow run state and project facts, and the compiler enforces it. The method lists
// live in database (WorkflowRunLedger, ProjectFactStore) to avoid an import cycle.
type Store interface {
	project.Store
	database.WorkflowRunLedger
}
