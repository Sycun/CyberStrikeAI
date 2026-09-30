package layering

import (
	"sort"
	"strings"
	"testing"
)

// concreteDBFields is the per-file count of transport-layer structs still holding the process's
// god object. Each entry is a domain that has not been narrowed yet, and the number beside it is
// what that file holds today.
//
// Before this ratchet existed the handler layer carried 19 of these. Narrowing one means swapping
// the field for that handler's own interface from internal/database/stores.go and constructing it
// with database.Narrow - see internal/handler/narrow_db_test.go for why the helper is mandatory
// rather than a plain assignment. 19 of these existed when the ratchet started and 3 remain:
// fifteen were narrowed to a consumer interface, and knowledge.go's was deleted outright - a dead
// field that only ever held the god object (its handler never read it, so the honest fix was to
// drop the field and the constructor parameter, not to invent a store for it). The comment
// beside each remaining entry is the
// reason it cannot go today.
//
// agent.go is the biggest block because its handle escapes into multiagent.RunDeepAgent /
// RunEinoSingleChatModelAgent and into agentfinalizer; those signatures have to take consumer
// interfaces before the field can narrow. audit.go was the first one unlocked this way: the escape
// was audit.ApplyResourceAvailability, which now declares its own ResourceExistenceSource and took
// eight members out of AuditStore's blind spot. project.go (six escapes into internal/project and
// internal/attackchain), workflow.go (into a workflow runner struct literal) and attackchain.go are
// the same story one level down.
var concreteDBFields = map[string]int{
	"internal/handler/agent.go":    1,
	"internal/handler/project.go":  1,
	"internal/handler/workflow.go": 1,
}

// narrowedStoreFloor is how many handler structs hold a consumer-shaped store interface today.
// It only ever goes up; a drop means a domain was widened back to *database.DB.
const narrowedStoreFloor = 15

func TestConcreteDBFieldsOnlyShrink(t *testing.T) {
	byFile, err := FieldTypesByFile(moduleRoot(t), "internal/handler")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	total := 0
	for _, types := range byFile {
		total += types["*database.DB"]
	}
	if total == 0 {
		t.Fatal("no struct holds *database.DB at all: either the narrowing finished or the scan is broken")
	}
	narrowed := 0
	var narrowedFiles []string
	for file, types := range byFile {
		for text, count := range types {
			if strings.HasPrefix(text, "database.") && strings.HasSuffix(text, "Store") {
				narrowed += count
				narrowedFiles = append(narrowedFiles, file+"="+text)
			}
		}
	}
	if narrowed < narrowedStoreFloor {
		t.Fatalf("only %d structs hold a database.*Store interface (floor %d): a handler was widened "+
			"back to the god object", narrowed, narrowedStoreFloor)
	}
	sort.Strings(narrowedFiles)
	t.Logf("transport layer: %d structs hold *database.DB, %d hold their own store interface (started 19/0)",
		total, narrowed)

	var regressions, newLeaks []string
	for file, types := range byFile {
		count := types["*database.DB"]
		if count == 0 {
			continue
		}
		baseline, known := concreteDBFields[file]
		if !known {
			newLeaks = append(newLeaks, file+" ("+intStr(count)+")")
			continue
		}
		if count > baseline {
			regressions = append(regressions, file+": "+intStr(count)+" > baseline "+intStr(baseline))
		}
	}
	if len(newLeaks) > 0 {
		t.Fatalf("new structs now hold the whole *database.DB: %v. The target is one consumer interface "+
			"per domain: declare the surface in internal/database/stores.go with `var _ XStore = (*DB)(nil)`, "+
			"then narrow the field with database.Narrow so a nil database stays nil.", newLeaks)
	}
	if len(regressions) > 0 {
		t.Fatalf("*database.DB fields grew inside existing files: %v", regressions)
	}
	for file, baseline := range concreteDBFields {
		got := byFile[file]["*database.DB"]
		if got < baseline {
			// Reported, never punished: a gate that goes red when debt shrinks gets relaxed
			// by whoever hits it first.
			t.Logf("%s dropped to %d (baseline %d): tighten concreteDBFields", file, got, baseline)
		}
		if got == 0 {
			t.Logf("%s is fully narrowed: remove it from concreteDBFields once its peers keep the floor honest", file)
		}
	}
}
