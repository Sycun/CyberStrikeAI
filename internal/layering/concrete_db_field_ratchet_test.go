package layering

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// assignmentKinds reports, for one module-relative file, how each narrowed storage field is
// assigned: "Narrow" when the value goes through database.Narrow, otherwise the offending text.
//
// The judgement is line-scoped on purpose. Every assignment in this codebase fits on one line, and
// a regexp over text is the right tool for a shape rule - the AST walker would not know which
// composite-literal key belongs to which struct without type information this package deliberately
// does not have. It is NOT a semantic analysis: if a multi-line assignment is ever introduced here,
// internal/handler/narrow_db_test.go (which builds each handler with a nil *DB and inspects the field
// by reflection) is the gate that still catches the leak.
func assignmentKinds(root, rel string) map[string][]string {
	out := map[string][]string{}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return out
	}
	keyRe := regexp.MustCompile(`(^|[\s{,])db:\s*([^,\n]+)`)
	// `\b([a-z]+)\.db = ` followed by a character that is not another `=`: RE2 has no negative
	// lookahead, and without this the ten `if h.db == nil` guards in this package read as
	// assignments whose "value" is "= nil {" - a false positive that fails the gate on a clean tree.
	setterRe := regexp.MustCompile(`\b([a-z]+)\.db = ([^=\n][^\n]*)`)
	for _, line := range strings.Split(string(data), "\n") {
		if m := keyRe.FindStringSubmatch(line); m != nil {
			out[rel] = append(out[rel], classifyAssignment(m[2]))
		}
		if m := setterRe.FindStringSubmatch(line); m != nil {
			out[rel] = append(out[rel], classifyAssignment(m[2]))
		}
	}
	return out
}

func classifyAssignment(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.Contains(trimmed, "Narrow[") {
		return "Narrow"
	}
	return trimmed
}

// The transport layer must not hold the process's persistence god object.
//
// This started as a ratchet: internal/handler had 19 structs with a `*database.DB` field, and each
// slice lowered one file's ceiling. It is now a hard zero, so the invariant is "no struct in the
// HTTP layer can reach any table or any of the 361 methods", not "fewer than yesterday".
//
// Getting the last three (AgentHandler, ProjectHandler, WorkflowHandler) required declaring the
// consumer interface at the far end of each chain rather than faking it here:
//   - multiagent never touches the database itself; it forwards the handle to internal/project, so
//     the surface is database.ProjectFactStore and multiagent takes project.Store.
//   - agentfinalizer needed two methods (read/save a tool execution) - database.ToolExecutionLedger.
//   - attackchain needed the chain rows plus the fact ledger - database.AttackChainLedger.
//   - the workflow engine needs its own run ledger plus project facts - workflow.Store.
//
// Surfaces that more than one package needs are declared in internal/database (the consumers import
// it, so declaring them in the consumer would cycle) and aliased back: project.Store,
// agentfinalizer.Store, attackchain.Store. One list to edit, and `var _ X = (*DB)(nil)` beside each.
//
// knowledge.go is the counter-example worth keeping: its `db` field was never read, so the honest
// fix was deleting the field and the constructor parameter rather than inventing a store for it.

// narrowedStoreFloor is how many handler structs hold a consumer-shaped store interface today
// (measured 18). It only goes up; a drop means a domain was widened back to the god object.
const narrowedStoreFloor = 18

// scannedFieldFloor keeps a broken scan from producing a green zero: the handler package declares
// ~990 struct fields, so a walk that sees a tenth of that is not reporting the truth.
const scannedFieldFloor = 500

func TestHandlerLayerHoldsNoGodObject(t *testing.T) {
	byFile, err := FieldTypesByFile(moduleRoot(t), "internal/handler")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	fields := 0
	concrete := []string{}
	for file, types := range byFile {
		for text, count := range types {
			fields += count
			if text == "*database.DB" || text == "*sql.DB" || text == "sql.DB" {
				concrete = append(concrete, file+": "+intStr(count)+" field(s) of type "+text)
			}
		}
	}
	if fields < scannedFieldFloor {
		t.Fatalf("the scan saw only %d struct fields in internal/handler (floor %d): the walker is broken, "+
			"a zero here would be meaningless", fields, scannedFieldFloor)
	}

	narrowed := 0
	var narrowedList []string
	for file, types := range byFile {
		for text, count := range types {
			if strings.HasPrefix(text, "database.") && strings.HasSuffix(text, "Store") {
				narrowed += count
				narrowedList = append(narrowedList, file+"="+text)
			}
		}
	}
	if narrowed < narrowedStoreFloor {
		t.Fatalf("only %d structs hold a database.*Store interface (floor %d): a handler was widened back "+
			"to *database.DB", narrowed, narrowedStoreFloor)
	}
	sort.Strings(narrowedList)
	t.Logf("transport layer: %d structs hold *database.DB, %d hold their own store interface, "+
		"%d struct fields scanned (started 19/0)", len(concrete), narrowed, fields)

	if len(concrete) > 0 {
		sort.Strings(concrete)
		t.Fatalf("%d handler struct fields still hold the whole database handle:\n%s\n"+
			"The destination is the consumer's own surface: declare what it needs in "+
			"internal/database/surfaces.go (or stores.go) with `var _ X = (*DB)(nil)`, alias it in the "+
			"consuming package, then narrow the field with database.Narrow - a plain assignment of a "+
			"possibly-nil *DB leaks a non-nil interface and silently inverts every h.db == nil guard.",
			len(concrete), strings.Join(concrete, "\n"))
	}
}

// TestNarrowedFieldsAreOnlyAssignedThroughNarrow keeps the typed-nil contract honest at source level:
// an interface field must never be assigned a bare variable. internal/handler/narrow_db_test.go
// proves the behaviour; this proves the shape, so a "simplification" back to `db: db` is caught even
// where no test exercises the nil path.
func TestNarrowedFieldsAreOnlyAssignedThroughNarrow(t *testing.T) {
	root := moduleRoot(t)
	byFile, err := FieldTypesByFile(root, "internal/handler")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	narrowedFiles := map[string]bool{}
	for file, types := range byFile {
		for text := range types {
			if strings.HasPrefix(text, "database.") && strings.HasSuffix(text, "Store") {
				narrowedFiles[file] = true
			}
		}
	}
	if len(narrowedFiles) < narrowedStoreFloor {
		t.Fatalf("%d files declare a narrowed field, floor is %d", len(narrowedFiles), narrowedStoreFloor)
	}

	violations := []string{}
	seen := 0
	for rel := range narrowedFiles {
		kinds := assignmentKinds(root, rel)
		if len(kinds[rel]) == 0 {
			// A file the scanner named must be readable and contain at least one assignment to the
			// field it declared. Without this the gate silently inspects nothing and stays green -
			// which is exactly how the first version of this helper failed its own probe.
			t.Fatalf("%s declares a narrowed storage field but no assignment to it was found: the "+
				"shape scan is reading the wrong path or matching the wrong syntax", rel)
		}
		for _, kind := range kinds[rel] {
			seen++
			if kind != "Narrow" {
				violations = append(violations, rel+" assigns a narrowed storage field via "+kind)
			}
		}
	}
	if seen < narrowedStoreFloor {
		t.Fatalf("only %d narrowed assignments inspected across the package (floor %d): the scan misses them",
			seen, narrowedStoreFloor)
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("narrowed fields assigned without database.Narrow (a nil *DB becomes a non-nil interface):\n%s",
			strings.Join(violations, "\n"))
	}
	t.Logf("%d files hold narrowed fields, %d assignments inspected, every one goes through database.Narrow",
		len(narrowedFiles), seen)
}
