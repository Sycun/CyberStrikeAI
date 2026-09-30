package handler

import (
	"reflect"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"

	"go.uber.org/zap"
)

// Each entry below is a handler whose storage field was narrowed from *database.DB to that
// handler's own consumer interface.
//
// The invariant is the nil path, not the happy path. Go converts `var store AssetStore = (*DB)(nil)`
// into a *non-nil* interface, so a plain assignment of a possibly-nil pointer would keep every
// `if h.db == nil` guard - the code path that answers "database unavailable" when a module is
// disabled - permanently wrong. That compiles, and the enabled path never notices. database.Narrow
// is what preserves the distinction; this test is what keeps somebody from "simplifying" it back
// to an assignment.
var narrowedHandlers = []struct {
	name  string
	build func(db *database.DB) interface{}
}{
	{"AssetHandler", func(db *database.DB) interface{} { return NewAssetHandler(db, zap.NewNop()) }},
	{"AuditHandler", func(db *database.DB) interface{} { return NewAuditHandler(db, nil, zap.NewNop()) }},
	{"RBACHandler", func(db *database.DB) interface{} { return NewRBACHandler(db, zap.NewNop()) }},
	{"VulnerabilityHandler", func(db *database.DB) interface{} { return NewVulnerabilityHandler(db, zap.NewNop()) }},
	{"ConversationHandler", func(db *database.DB) interface{} { return NewConversationHandler(db, zap.NewNop()) }},
	{"MonitorHandler", func(db *database.DB) interface{} { return NewMonitorHandler(nil, nil, db, zap.NewNop()) }},
	{"NotificationHandler", func(db *database.DB) interface{} { return NewNotificationHandler(db, nil, zap.NewNop()) }},
	{"OpenAPIHandler", func(db *database.DB) interface{} { return NewOpenAPIHandler(db, zap.NewNop(), nil, nil) }},
	{"RobotHandler", func(db *database.DB) interface{} { return NewRobotHandler(&config.Config{}, db, nil, zap.NewNop()) }},
	{"WebShellHandler", func(db *database.DB) interface{} { return NewWebShellHandler(zap.NewNop(), db) }},
	{"ChatUploadsHandler", func(db *database.DB) interface{} { return NewChatUploadsHandler(zap.NewNop(), db) }},
	{"SkillsHandler", func(db *database.DB) interface{} {
		h := NewSkillsHandler(&config.Config{}, "", zap.NewNop())
		h.SetDB(db)
		return h
	}},
	{"ConfigHandler", func(db *database.DB) interface{} {
		h := &ConfigHandler{logger: zap.NewNop()}
		h.SetDB(db)
		return h
	}},
}

// storageField returns the handler's `db` field, failing when the field is not an interface -
// which is the same as saying the domain was never narrowed.
func storageField(t *testing.T, built interface{}) reflect.Value {
	t.Helper()
	value := reflect.ValueOf(built)
	if value.Kind() != reflect.Ptr || value.Elem().Kind() != reflect.Struct {
		t.Fatalf("%T is not a pointer to a struct", built)
	}
	field := value.Elem().FieldByName("db")
	if !field.IsValid() {
		t.Fatalf("%T has no db field: the narrowing moved, update this list", value.Type())
	}
	if field.Kind() != reflect.Interface {
		t.Fatalf("%T.db is %s, not an interface: this domain was widened back to *database.DB",
			value.Type(), field.Type())
	}
	return field
}

func TestNarrowedStorageStaysNilWithoutADatabase(t *testing.T) {
	if len(narrowedHandlers) < 13 {
		t.Fatalf("only %d narrowed handlers listed, the inventory is stale", len(narrowedHandlers))
	}
	for _, entry := range narrowedHandlers {
		field := storageField(t, entry.build(nil))
		if !field.IsNil() {
			t.Errorf("%s built with a nil *database.DB holds a non-nil %s - the typed-nil leak: every "+
				"h.db == nil guard in that handler would take the wrong branch (use database.Narrow)",
				entry.name, field.Type())
		}
	}
}

func TestNarrowedStorageKeepsALiveDatabase(t *testing.T) {
	db := &database.DB{}
	for _, entry := range narrowedHandlers {
		field := storageField(t, entry.build(db))
		if field.IsNil() {
			t.Errorf("%s dropped a live *database.DB to nil: the handler would answer 'database unavailable' "+
				"for a working deployment", entry.name)
		}
	}
}

// TestNarrowRejectsAnInterfaceDBDoesNotImplement covers the panic path in database.Narrow: an
// store interface declared without its `var _ XStore = (*DB)(nil)` assertion must fail loudly at
// assembly time instead of producing a handler that panics on the first request.
func TestNarrowRejectsAnInterfaceDBDoesNotImplement(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("database.Narrow returned for an interface *DB does not implement: the assertion in stores.go is missing")
		}
	}()
	_ = database.Narrow[interface {
		NoSuchMethodAnywhere() error
	}](&database.DB{})
}
