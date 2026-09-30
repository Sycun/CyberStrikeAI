package project

import "cyberstrike-ai/internal/database"

// Store is the persistence surface this package needs: the project row plus the fact index and
// fact-edge ledger that the blackboard, stats and graph builders read and write.
//
// Every function here used to take the 361-method *database.DB, which is how "project" code ended
// able to touch any table - and it is why the HTTP layer could not narrow its own storage field:
// the handle escaped into these signatures.
//
// The method list itself lives in database.ProjectFactStore because this package imports database
// and the reverse would be a cycle; the alias keeps it a single list to edit, and database asserts
// *DB satisfies it at compile time.
type Store = database.ProjectFactStore
