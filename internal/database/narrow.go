package database

import (
	"fmt"
	"reflect"
)

// Narrow hands the process's *DB to one consumer as that consumer's own store interface.
//
// The nil handling is the whole reason this exists instead of a plain assignment.
// `var store AssetStore = (*DB)(nil)` yields a *non-nil* interface, and the transport layer's
// `if h.db == nil` guards - how a handler reports "database unavailable" rather than panicking when
// the deployment runs without a database - would then take the wrong branch. That drift compiles,
// and no test that only exercises the enabled path can see it.
//
// The interface check below is a runtime assertion, but it is not the only line of defence: every
// store interface in this package carries `var _ XStore = (*DB)(nil)`, so a mismatch is already a
// compile error next to its declaration. This panic covers an interface added without that
// assertion - Go cannot express "the pointer to T implements I" as a constraint
// (`interface{ *T; I }` is rejected with "term cannot be a type parameter").
func Narrow[I any](db *DB) I {
	if db == nil {
		var none I
		return none
	}
	value, ok := any(db).(I)
	if !ok {
		panic(fmt.Sprintf("database: *DB does not implement %s; keep the compile-time assertion in stores.go beside the interface",
			reflect.TypeOf((*I)(nil)).Elem()))
	}
	return value
}
