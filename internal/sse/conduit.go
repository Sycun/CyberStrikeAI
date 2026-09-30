package sse

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A progress event reaches the wire through a callback of the shape
// func(eventType, message string, data any). The HTTP layer creates it and hands it to
// the agent runtime, the workflow engine and the Eino observers, which call it with the
// frame's event type as their first argument. So the set of names a client can receive
// is not visible from the emitter package alone, and a registry built from only that
// package would refuse live events.
//
// Membership is decided by *type*, not by name: an identifier counts as a progress
// callback only where its declaration is a three-parameter function taking two strings
// and an untyped payload. The same rule identifies the parameters that receive an
// event name, which is how the names a consumer branches on are found.

// callbackPosition is one parameter position of a signature.
type callbackPosition struct {
	name string
	typ  string
}

func flattenParams(ft *ast.FuncType) []callbackPosition {
	if ft == nil || ft.Params == nil {
		return nil
	}
	var out []callbackPosition
	for _, field := range ft.Params.List {
		typ := exprTypeString(field.Type)
		if len(field.Names) == 0 {
			out = append(out, callbackPosition{typ: typ})
			continue
		}
		for _, name := range field.Names {
			out = append(out, callbackPosition{name: name.Name, typ: typ})
		}
	}
	return out
}

func exprTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return "any"
		}
		return "interface{}"
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprTypeString(t.Elt)
	case *ast.StarExpr:
		return "*" + exprTypeString(t.X)
	case *ast.MapType:
		return "map[" + exprTypeString(t.Key) + "]" + exprTypeString(t.Value)
	case *ast.FuncType:
		return "func"
	default:
		return "?"
	}
}

// isProgressCallbackShape reports the exact callback signature. Arity and the untyped
// third parameter matter: other callbacks in the tree take (string, string, string) or
// carry more parameters, and counting those would put robot channels and resource kinds
// into the event catalogue.
func isProgressCallbackShape(ft *ast.FuncType) bool {
	positions := flattenParams(ft)
	if len(positions) != 3 {
		return false
	}
	if positions[0].typ != "string" || positions[1].typ != "string" {
		return false
	}
	return positions[2].typ == "any" || positions[2].typ == "interface{}"
}

// isCallbackType reports whether a type expression denotes a progress callback: an
// inline signature, or a named type declared as one.
func isCallbackType(expr ast.Expr, named map[string]bool) bool {
	switch t := expr.(type) {
	case *ast.FuncType:
		return isProgressCallbackShape(t)
	case *ast.Ident:
		return named[t.Name]
	case *ast.SelectorExpr:
		return named[t.Sel.Name]
	case *ast.StarExpr:
		return isCallbackType(t.X, named)
	default:
		return false
	}
}

// namedCallbackTypes collects `type X func(string, string, any)` declarations, which is
// how the callback crosses package boundaries.
func namedCallbackTypes(files []*ast.File) map[string]bool {
	out := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			decl, ok := n.(*ast.GenDecl)
			if !ok || decl.Tok != token.TYPE {
				return true
			}
			for _, spec := range decl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if ft, ok := ts.Type.(*ast.FuncType); ok && isProgressCallbackShape(ft) {
					out[ts.Name.Name] = true
				}
			}
			return true
		})
	}
	return out
}

// callbackNames returns the identifiers that hold a progress callback, and the bodies
// of the functions whose own signature receives an event name - paired with the
// parameter that receives it. Scoping the variable to its function is what keeps
// unrelated `switch kind` / `switch mode` blocks out of the catalogue: only the
// parameter of a callback-shaped function names an event.
func callbackNames(files []*ast.File, named map[string]bool) (holders map[string]bool, eventScopes []eventScope) {
	holders = map[string]bool{}
	factories := map[string]bool{}

	// A factory is any function that hands back a progress callback; the variable it is
	// assigned to is therefore a callback too.
	var fields []*ast.Field
	var funcs []functionShape
	var assigns []*ast.AssignStmt

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Field:
				fields = append(fields, node)
			case *ast.FuncDecl:
				if node.Name == nil || node.Type == nil {
					return true
				}
				funcs = append(funcs, functionShape{name: node.Name.Name, ft: node.Type, body: node.Body})
				if node.Type.Results != nil {
					for _, result := range node.Type.Results.List {
						if isCallbackType(result.Type, named) {
							factories[node.Name.Name] = true
						}
					}
				}
			case *ast.FuncLit:
				if node.Type != nil {
					funcs = append(funcs, functionShape{ft: node.Type, body: node.Body})
				}
			case *ast.AssignStmt:
				assigns = append(assigns, node)
			}
			return true
		})
	}

	for _, field := range fields {
		if !isCallbackType(field.Type, named) {
			continue
		}
		for _, name := range field.Names {
			holders[name.Name] = true
		}
	}
	for _, fn := range funcs {
		if !isProgressCallbackShape(fn.ft) || fn.body == nil {
			continue
		}
		positions := flattenParams(fn.ft)
		if len(positions) == 0 || positions[0].name == "" {
			continue
		}
		eventScopes = append(eventScopes, eventScope{variable: positions[0].name, body: fn.body})
	}
	for _, stmt := range assigns {
		if len(stmt.Rhs) == 0 {
			continue
		}
		for _, rhs := range stmt.Rhs {
			call, ok := rhs.(*ast.CallExpr)
			if !ok {
				continue
			}
			callee := calleeName(call)
			if callee == "" || !factories[callee] {
				continue
			}
			for _, lhs := range stmt.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok {
					holders[ident.Name] = true
				}
			}
		}
	}
	return holders, eventScopes
}

// eventScope is a function body plus the parameter that carries its event name.
type eventScope struct {
	variable string
	body     *ast.BlockStmt
}

// functionShape is a declaration or literal, with the name a caller would use.
type functionShape struct {
	name string
	ft   *ast.FuncType
	body *ast.BlockStmt
}

func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	default:
		return ""
	}
}

// ConduitNames lists the event names that reach the wire through a progress callback,
// plus the names consumers branch on. Each entry keeps the file and line that proved
// it, so the catalogue can be reviewed rather than trusted.
func ConduitNames(dirs ...string) ([]KindSource, error) {
	fset := token.NewFileSet()
	var files []*ast.File
	var paths []string
	for _, dir := range dirs {
		err := walkGo(dir, func(path string) error {
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", path, parseErr)
			}
			files = append(files, file)
			paths = append(paths, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("sse: no Go files found under %v", dirs)
	}

	named := namedCallbackTypes(files)
	holders, eventScopes := callbackNames(files, named)
	if len(holders) == 0 || len(eventScopes) == 0 {
		return nil, fmt.Errorf("sse: found %d callback holders and %d event scopes under %v; the shape rule is broken",
			len(holders), len(eventScopes), dirs)
	}

	var out []KindSource
	for i, file := range files {
		display := displayPath(paths[i], dirs)
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				if source, ok := conduitCallSource(display, fset, node, holders); ok {
					out = append(out, source)
				}
			case *ast.BlockStmt:
				// Every function body (declarations and literals both get here) is a
				// scope for "literal assigned to a variable, variable passed to the
				// callback".
				out = append(out, assignedConduitSources(display, fset, node, holders)...)
			}
			return true
		})
		for _, scope := range eventScopesIn(file) {
			ast.Inspect(scope.body, func(n ast.Node) bool {
				for _, source := range handledNames(display, fset, n, scope.variable) {
					out = append(out, source)
				}
				return true
			})
		}
	}

	// A body nested inside another is visited twice by the walk above, so the same
	// (name, file, line, mode) can arrive more than once. The catalogue is a contract:
	// a duplicated row would read as two independent proofs.
	out = dedupeSources(out)
	sort.Slice(out, func(a, b int) bool {
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		if out[a].File != out[b].File {
			return out[a].File < out[b].File
		}
		return out[a].Line < out[b].Line
	})
	return out, nil
}

func conduitCallSource(display string, fset *token.FileSet, call *ast.CallExpr, holders map[string]bool) (KindSource, bool) {
	if len(call.Args) == 0 {
		return KindSource{}, false
	}
	if callee := calleeName(call); callee == "" || !holders[callee] {
		return KindSource{}, false
	}
	value, ok := stringLiteral(call.Args[0])
	if !ok || strings.TrimSpace(value) == "" {
		return KindSource{}, false
	}
	return KindSource{
		Name:   value,
		File:   display,
		Line:   fset.Position(call.Pos()).Line,
		Mode:   "conduit",
		Stream: StreamAgent,
	}, true
}

// assignedConduitSources covers the callback calls whose first argument is a *local*
// variable rather than a literal. The workflow engine reports its branch decisions this
// way:
//
//	eventType := "workflow_branch_skipped"
//	if allowed { eventType = "workflow_branch_taken" }
//	args.Progress(eventType, msg, ...)
//
// A call-site-only rule saw neither name, so the registry refused them at runtime and the
// branch vanishing was invisible: the frame was dropped rather than malformed. Scoping to
// one function body keeps a same-named variable in an unrelated function from contributing.
func assignedConduitSources(display string, fset *token.FileSet, body *ast.BlockStmt, holders map[string]bool) []KindSource {
	if body == nil {
		return nil
	}
	literals := map[string][]string{}
	collectAssignments(body, literals)

	var out []KindSource
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		callee := calleeName(call)
		if callee == "" || !holders[callee] {
			return true
		}
		ident, ok := call.Args[0].(*ast.Ident)
		if !ok {
			return true
		}
		for _, value := range literals[ident.Name] {
			out = append(out, KindSource{
				Name:   value,
				File:   display,
				Line:   fset.Position(call.Pos()).Line,
				Mode:   "assigned",
				Stream: StreamAgent,
			})
		}
		return true
	})
	return out
}

// collectAssignments records the string literals stored into local variables by `x := "a"`,
// `x = "a"` and `var x = "a"`, keyed by variable name.
func collectAssignments(body *ast.BlockStmt, literals map[string][]string) {
	record := func(name string, value string) {
		for _, seen := range literals[name] {
			if seen == value {
				return
			}
		}
		literals[name] = append(literals[name], value)
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if len(node.Rhs) != 1 {
				return true
			}
			value, ok := stringLiteral(node.Rhs[0])
			if !ok || !isEventName(value) {
				return true
			}
			for _, lhs := range node.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok {
					record(ident.Name, value)
				}
			}
		case *ast.DeclStmt:
			gen, ok := node.Decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				return true
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != 1 || len(vs.Names) != 1 {
					continue
				}
				value, ok := stringLiteral(vs.Values[0])
				if !ok || !isEventName(value) {
					continue
				}
				record(vs.Names[0].Name, value)
			}
		}
		return true
	})
}

// isEventName accepts the shapes actually used on the wire: lowercase with underscores,
// and dotted resource kinds. Plain prose ("是", "跳过分支") is what a branch label looks
// like, and it must not become an event name.
var eventNameShape = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

func isEventName(value string) bool {
	return eventNameShape.MatchString(strings.TrimSpace(value))
}

// `switch eventType { case "model_output_rejected" }`, `eventType == "thinking"`. A name
// the code handles this way is expected on the wire even where no call site spells it
// out, so it has to be declared or the writer would drop it.
// eventScopesIn repeats the scope discovery per file, which is what lets the scan look
// only inside functions that take an event name.
func eventScopesIn(file *ast.File) []eventScope {
	named := namedCallbackTypes([]*ast.File{file})
	_, scopes := callbackNames([]*ast.File{file}, named)
	return scopes
}

func handledNames(display string, fset *token.FileSet, n ast.Node, eventVar string) []KindSource {
	var out []KindSource
	add := func(name string, pos token.Pos) {
		if strings.TrimSpace(name) == "" {
			return
		}
		out = append(out, KindSource{Name: name, File: display, Line: fset.Position(pos).Line, Mode: "handled", Stream: StreamAgent})
	}

	switch node := n.(type) {
	case *ast.SwitchStmt:
		if node.Tag == nil || !isEventVariable(node.Tag, eventVar) {
			return nil
		}
		if node.Body == nil {
			return nil
		}
		for _, stmt := range node.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range clause.List {
				if name, ok := stringLiteral(expr); ok {
					add(name, expr.Pos())
				}
			}
		}
	case *ast.BinaryExpr:
		if node.Op != token.EQL && node.Op != token.NEQ {
			return nil
		}
		if isEventVariable(node.X, eventVar) {
			if name, ok := stringLiteral(node.Y); ok {
				add(name, node.Pos())
			}
		}
		if isEventVariable(node.Y, eventVar) {
			if name, ok := stringLiteral(node.X); ok {
				add(name, node.Pos())
			}
		}
	}
	return out
}

// isEventVariable reports an expression that reads an event-name variable, allowing the
// normalising wrappers the code uses (strings.TrimSpace, fmt.Sprint of the value).
func isEventVariable(expr ast.Expr, eventVar string) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == eventVar
	case *ast.CallExpr:
		if len(e.Args) == 0 {
			return false
		}
		return isEventVariable(e.Args[0], eventVar)
	case *ast.SelectorExpr:
		return e.Sel.Name == eventVar
	default:
		return false
	}
}

// displayPath renders a path relative to the module root, which is what makes the
// provenance in the catalogue readable.
func displayPath(path string, dirs []string) string {
	if parts := strings.Split(filepath.ToSlash(path), "/"); len(parts) > 0 {
		for i, part := range parts {
			if part == "internal" || part == "cmd" {
				return strings.Join(parts[i:], "/")
			}
		}
	}
	for _, dir := range dirs {
		if rel, err := filepath.Rel(dir, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}

// walkGo visits non-test Go files under dir, skipping fixtures and generated output.
func walkGo(dir string, visit func(path string) error) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch filepath.Base(path) {
			case "testdata", "generated", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		return visit(path)
	})
}

// dedupeSources removes identical provenance rows, keeping the order the scan produced.
func dedupeSources(in []KindSource) []KindSource {
	seen := map[KindSource]bool{}
	out := make([]KindSource, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
