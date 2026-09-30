package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/handler"
)

// Every handler that writes platform audit records receives its *audit.Service through a
// SetAudit injection, and New() has to remember to call each one. Forgetting an injection is
// invisible: the endpoint keeps working and simply produces no audit trail. bindAudit gives
// those injections one recognizable shape so completeness can be checked against the handler
// package instead of against the wiring author's memory.

// auditableTypesAndConstructors parses internal/handler and returns the handler types that
// declare a SetAudit injection, plus every New*-style constructor that returns one of them.
//
// The truth source is the source text, not a registry in the app: a list maintained next to
// the wiring would be exactly as forgetful as the wiring it is supposed to guard.
func auditableTypesAndConstructors(t *testing.T, root string) (map[string]bool, map[string]string) {
	t.Helper()
	types := map[string]bool{}
	ctors := map[string]string{}
	returned := map[string][]string{}
	fset := token.NewFileSet()
	handlerDir := filepath.Join(root, "internal", "handler")
	entries, err := os.ReadDir(handlerDir)
	if err != nil {
		t.Fatalf("read %s: %v", handlerDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(handlerDir, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil {
				continue
			}
			if fn.Recv != nil && len(fn.Recv.List) == 1 && fn.Name.Name == "SetAudit" {
				if recv := receiverTypeName(fn.Recv.List[0].Type); recv != "" {
					types[recv] = true
				}
				continue
			}
			if fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "New") || fn.Type.Results == nil {
				continue
			}
			for _, result := range fn.Type.Results.List {
				if typeName, ok := pointerResultType(result.Type); ok {
					returned[fn.Name.Name] = append(returned[fn.Name.Name], typeName)
				}
			}
		}
	}
	// Constructors are filtered after the walk: SetAudit declarations and constructors do not
	// live in the same files, so a single pass would let file order decide which constructors
	// are recognised.
	for ctor, results := range returned {
		matched := ""
		for _, typeName := range results {
			if !types[typeName] {
				continue
			}
			if matched != "" && matched != typeName {
				t.Fatalf("constructor %s returns two auditable types (%s, %s), the scan needs updating",
					ctor, matched, typeName)
			}
			matched = typeName
		}
		if matched != "" {
			ctors[ctor] = matched
		}
	}
	return types, ctors
}

func receiverTypeName(expr ast.Expr) string {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return ""
	}
	ident, ok := star.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

func pointerResultType(expr ast.Expr) (string, bool) {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	pkg, ok := star.X.(*ast.Ident)
	if !ok || pkg.Name == "" {
		return "", false
	}
	return pkg.Name, true
}

// unboundAuditableHandlers walks internal/app and returns every handler constructed there
// that never reaches bindAudit inside the function that built it.
func unboundAuditableHandlers(t *testing.T, root string, ctors map[string]string) ([]string, int, int) {
	t.Helper()
	fset := token.NewFileSet()
	appDir := filepath.Join(root, "internal", "app")
	entries, err := os.ReadDir(appDir)
	if err != nil {
		t.Fatalf("read %s: %v", appDir, err)
	}
	var violations []string
	constructions := 0
	bindings := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(appDir, name)
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			bound := map[string]bool{}
			var built [][2]string // variable name -> constructor
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch stmt := n.(type) {
				case *ast.AssignStmt:
					call, ok := singleHandlerConstructor(stmt.Rhs)
					if !ok {
						return true
					}
					if _, known := ctors[call]; !known {
						return true
					}
					for _, lhs := range stmt.Lhs {
						if ident, ok := lhs.(*ast.Ident); ok && ident.Name != "_" {
							built = append(built, [2]string{ident.Name, call})
							constructions++
						}
					}
				case *ast.CallExpr:
					if isBindAudit(stmt) {
						if ident, ok := stmt.Args[0].(*ast.Ident); ok {
							bound[ident.Name] = true
							bindings++
						}
					}
				}
				return true
			})
			for _, pair := range built {
				if bound[pair[0]] {
					continue
				}
				violations = append(violations, name+": "+pair[1]+" is assigned to "+pair[0]+
					" but that variable is never passed to bindAudit - its endpoints would write no audit records")
			}
		}
	}
	sort.Strings(violations)
	return violations, constructions, bindings
}

func singleHandlerConstructor(rhs []ast.Expr) (string, bool) {
	if len(rhs) != 1 {
		return "", false
	}
	call, ok := rhs[0].(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "handler" {
		return "", false
	}
	return sel.Sel.Name, true
}

func isBindAudit(call *ast.CallExpr) bool {
	fn, ok := call.Fun.(*ast.Ident)
	return ok && fn.Name == "bindAudit" && len(call.Args) == 2
}

func auditWiringRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test working directory")
		}
		dir = parent
	}
}

// TestEveryAuditableHandlerIsAuditBound is the gate: a handler that declares SetAudit must be
// given the service in New(), or the build fails.
func TestEveryAuditableHandlerIsAuditBound(t *testing.T) {
	root := auditWiringRoot(t)
	types, ctors := auditableTypesAndConstructors(t, root)
	if len(types) < 18 {
		t.Fatalf("only %d handler types declare SetAudit; the scan is broken (measured 18)", len(types))
	}
	if len(ctors) < 18 {
		t.Fatalf("only %d constructors return an auditable handler type; the scan is broken (measured 18+)", len(ctors))
	}
	violations, constructions, bindings := unboundAuditableHandlers(t, root, ctors)
	if constructions < 18 {
		t.Fatalf("internal/app constructs only %d auditable handlers, the constructor scan is broken", constructions)
	}
	// No floor on the bindAudit call count on purpose: if that scan degrades, every
	// constructed handler below turns into a violation, which is louder and better located
	// than a count mismatch blaming the scanner for somebody else's missing injection.
	t.Logf("auditable handler types: %d | constructed in internal/app: %d | bindAudit calls: %d",
		len(types), constructions, bindings)
	if len(violations) > 0 {
		t.Fatalf("%d auditable handlers are constructed without an audit binding:\n%s", len(violations),
			strings.Join(violations, "\n"))
	}
}

// recordingAuditable proves bindAudit is not a no-op: the indirection has to reach the setter,
// otherwise the gate would be guarding a call that wires nothing.
type recordingAuditable struct {
	calls int
	svc   *audit.Service
}

func (r *recordingAuditable) SetAudit(svc *audit.Service) {
	r.calls++
	r.svc = svc
}

var _ handler.Auditable = (*recordingAuditable)(nil)

func TestBindAuditReachesTheSetter(t *testing.T) {
	rec := &recordingAuditable{}
	bindAudit(rec, nil)
	if rec.calls != 1 {
		t.Fatalf("bindAudit called SetAudit %d times, want exactly 1", rec.calls)
	}
}
