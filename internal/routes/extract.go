// Package routes extracts the HTTP route table from Go source without starting the
// application, so a wiring refactor can be proven against a golden file. A change to
// which paths are registered must fail a test, not a user.
package routes

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var registrationMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true,
	"PATCH": true, "HEAD": true, "OPTIONS": true, "Any": true,
}

// Entry is one route registration.
type Entry struct {
	Method string
	Path   string
	File   string
	Line   int
	// Receiver is the group variable the route was registered on, so a caller can
	// audit one group (for example everything behind the authenticated group) without
	// re-deriving the wiring structure.
	Receiver string
	// GinPath is the registration exactly as written, with :param and *wildcard
	// segments intact, for callers that match on the Gin form.
	GinPath string
}

func (e Entry) String() string { return e.Method + " " + e.Path }

// Table is a set of registrations.
type Table []Entry

// Sorted returns a de-duplicated, deterministic view of the table.
func (t Table) Sorted() Table {
	out := make(Table, 0, len(t))
	seen := map[string]bool{}
	for _, entry := range t {
		key := entry.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Method != out[j].Method {
			return out[i].Method < out[j].Method
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Lines renders the table for a golden file.
func (t Table) Lines() []string {
	out := make([]string, 0, len(t))
	for _, entry := range t {
		out = append(out, entry.String())
	}
	return out
}

// GroupNames lists the receiver identifiers that were bound to a route group.
type GroupNames map[string]string

// Extract parses every non-test Go file in dir and returns the route table.
//
// Group bindings are resolved across files with a shared prefix map and repeated to a
// fixed point: the registrars take `protected *gin.RouterGroup` as a parameter while
// the group itself is created in the wiring function, so a per-file-only pass would
// lose the /api prefix and report a table that does not match the running server.
func Extract(dir string) (Table, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	var parsed []*ast.File
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("routes: parse %s: %w", name, err)
		}
		parsed = append(parsed, file)
	}

	shared := map[string]string{"router": "", "engine": "", "r": "", "api": "/api"}
	var table Table
	for pass := 0; pass < 3; pass++ {
		table = Table{}
		for index, file := range parsed {
			entries := collect(fset, filepath.Base(files[index]), file, shared)
			table = append(table, entries...)
		}
		table = table.Sorted()
	}
	return table, nil
}

func collect(fset *token.FileSet, name string, file *ast.File, prefixes map[string]string) Table {
	var table Table

	var walk func([]ast.Stmt)
	walk = func(stmts []ast.Stmt) {
		for _, stmt := range stmts {
			switch node := stmt.(type) {
			case *ast.AssignStmt:
				if group, prefix, ok := groupBinding(node, prefixes); ok {
					prefixes[group] = prefix
				}
			case *ast.ExprStmt:
				if entry, ok := registration(node.X, prefixes); ok {
					entry.File = name
					entry.Line = fset.Position(node.Pos()).Line
					table = append(table, entry)
				}
			case *ast.BlockStmt:
				walk(node.List)
			case *ast.IfStmt:
				if node.Body != nil {
					walk(node.Body.List)
				}
				if block, ok := node.Else.(*ast.BlockStmt); ok {
					walk(block.List)
				}
			case *ast.RangeStmt:
				if node.Body != nil {
					walk(node.Body.List)
				}
			}
		}
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		walk(fn.Body.List)
	}
	return table
}

// groupBinding recognises `child := parent.Group("prefix")`.
func groupBinding(node *ast.AssignStmt, prefixes map[string]string) (string, string, bool) {
	if len(node.Lhs) != 1 || len(node.Rhs) != 1 {
		return "", "", false
	}
	ident, ok := node.Lhs[0].(*ast.Ident)
	if !ok {
		return "", "", false
	}
	call, ok := node.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Group" {
		return "", "", false
	}
	parent, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", "", false
	}
	base, known := prefixes[parent.Name]
	if !known {
		base = ""
	}
	return ident.Name, joinPath(base, unquote(lit.Value)), true
}

func joinPath(parent, group string) string {
	group = strings.Trim(unquote(group), "/")
	parent = strings.TrimRight(parent, "/")
	if group == "" {
		return parent
	}
	return parent + "/" + group
}

func registration(expr ast.Expr, prefixes map[string]string) (Entry, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return Entry{}, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !registrationMethods[sel.Sel.Name] {
		return Entry{}, false
	}
	receiver, ok := sel.X.(*ast.Ident)
	if !ok {
		return Entry{}, false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return Entry{}, false
	}
	path := unquote(lit.Value)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	full := prefixes[receiver.Name] + path
	return Entry{
		Method:   strings.ToUpper(sel.Sel.Name),
		Path:     ginPathToTemplate(full),
		GinPath:  full,
		Receiver: receiver.Name,
	}, true
}

// ginPathToTemplate rewrites :param and *wildcard segments into OpenAPI form so the
// table is comparable against a published spec.
func ginPathToTemplate(path string) string {
	segments := strings.Split(path, "/")
	for index, segment := range segments {
		switch {
		case strings.HasPrefix(segment, ":"):
			segments[index] = "{" + strings.TrimPrefix(segment, ":") + "}"
		case strings.HasPrefix(segment, "*"):
			segments[index] = "{" + strings.TrimPrefix(segment, "*") + "}"
		}
	}
	return strings.Join(segments, "/")
}

func unquote(raw string) string {
	if raw == "" {
		return ""
	}
	if raw[0] == '`' {
		return strings.Trim(raw, "`")
	}
	if out, err := strconv.Unquote(raw); err == nil {
		return out
	}
	return strings.Trim(raw, "\"")
}
