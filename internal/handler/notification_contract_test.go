package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The notification bell is a second producer/consumer pair, and it drifts the same way the
// SSE stream does. Checking the *name sets* alone was not enough: `task_failed` appears on
// both sides and still cannot reach the page, because its only producer has no caller and
// the digest reports `"failedExecutions": 0` unconditionally. So this gate follows calls from
// the HTTP entry points and compares what is actually reachable.

// producerTypes maps a function name to the notification item types it builds. Membership is
// decided by the AST: only a composite literal of NotificationSummaryItem counts, with its
// `Type` key set to a string literal. A text pattern over the whole body would also match
// the unrelated Type fields in this package (MCP content blocks, robot messages) and report
// a notification surface that does not exist.
func producerTypes(t *testing.T, files []*ast.File) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	record := func(fn, typ string) {
		if out[fn] == nil {
			out[fn] = map[string]bool{}
		}
		out[fn][typ] = true
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Body == nil {
				continue
			}
			visit := func(lit *ast.CompositeLit) {
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok || key.Name != "Type" {
						continue
					}
					value, ok := kv.Value.(*ast.BasicLit)
					if !ok || value.Kind != token.STRING {
						continue
					}
					typ, err := strconv.Unquote(value.Value)
					if err != nil {
						t.Fatalf("unquote %s: %v", value.Value, err)
					}
					record(fn.Name.Name, typ)
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				switch name := typeIdentName(lit.Type); {
				case lit.Type == nil:
					// An elided literal is only resolvable through its container, handled
					// by the slice case below.
					return true
				case name == "NotificationSummaryItem":
					visit(lit)
				case name == "":
					// `[]NotificationSummaryItem{{...}}`: the elements carry no type of
					// their own, so resolve them from the element type here - otherwise a
					// producer written this way escapes the gate entirely.
					if arr, ok := lit.Type.(*ast.ArrayType); ok && typeIdentName(arr.Elt) == "NotificationSummaryItem" {
						for _, elt := range lit.Elts {
							if inner, ok := elt.(*ast.CompositeLit); ok && inner.Type == nil {
								visit(inner)
							}
						}
					}
				}
				return true
			})
		}
	}
	return out
}

func typeIdentName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.StarExpr:
		// `&NotificationSummaryItem{...}` is the same contract as the bare literal.
		return typeIdentName(t.X)
	default:
		return ""
	}
}

// reachable returns the function names reachable from the HTTP entry points. A callee is
// recorded by its identifier (so `h.loadVulnerabilityItems(...)` counts as a call to
// loadVulnerabilityItems): this over-approximates across same-named methods on other types,
// which is the safe direction - an over-approximation hides dead code rather than
// inventing it, and the entry points themselves are exact.
func reachable(t *testing.T, files []*ast.File) map[string]bool {
	t.Helper()
	calls := map[string][]string{}
	entries := map[string]bool{}

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if takesGinContext(fn) {
				entries[name] = true
			}
			var callees []string
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch callee := call.Fun.(type) {
				case *ast.Ident:
					callees = append(callees, callee.Name)
				case *ast.SelectorExpr:
					callees = append(callees, callee.Sel.Name)
				}
				return true
			})
			calls[name] = append(calls[name], callees...)
		}
	}

	if len(entries) < 5 {
		t.Fatalf("only %d HTTP entry points found; the reachability scan is broken", len(entries))
	}
	seen := map[string]bool{}
	queue := make([]string, 0, len(entries))
	for name := range entries {
		queue = append(queue, name)
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		queue = append(queue, calls[name]...)
	}
	return seen
}

func takesGinContext(fn *ast.FuncDecl) bool {
	if fn.Type == nil || fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		if exprString(field.Type) == "gin.Context" {
			return true
		}
	}
	return false
}

func exprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return exprString(t.X)
	case *ast.SelectorExpr:
		// A qualified type keeps its package: the entry points are declared as
		// `*gin.Context`, and matching only `Context` would also accept some other
		// package's same-named type.
		if pkg, ok := t.X.(*ast.Ident); ok {
			return pkg.Name + "." + t.Sel.Name
		}
		return t.Sel.Name
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

// orphanNotificationProducers is the ratchet: a producer of notification item types that no
// HTTP entry point can reach. Each entry is a feature the page still handles and the server
// has stopped delivering.
var orphanNotificationProducers = map[string]string{
	"loadFailedExecutionItems": "the digest reports a hard-coded `\"failedExecutions\": 0` and never calls this " +
		"loader, while notifications.js still branches on item.type === 'task_failed' and uses item.executionId. " +
		"Either wire it back into GetSummary or delete the loader and the page branch - do not leave it orphaned.",
}

func TestNotificationProducersAreReachableFromTheDigest(t *testing.T) {
	files := handlerFiles(t)
	producers := producerTypes(t, files)
	if len(producers) < 4 {
		t.Fatalf("only %d functions build notification items; the scan is broken", len(producers))
	}
	reach := reachable(t, files)

	var orphans []string
	for name := range producers {
		if !reach[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)

	if len(orphans) > len(orphanNotificationProducers) {
		var detail []string
		for _, name := range orphans {
			note := "no known reason"
			if documented, ok := orphanNotificationProducers[name]; ok {
				note = documented
			}
			detail = append(detail, name+": "+note)
		}
		t.Fatalf("%d notification producers are unreachable from any HTTP entry point (baseline %d): %v. "+
			"Their item types can never reach the page.",
			len(orphans), len(orphanNotificationProducers), detail)
	}
	for name := range orphanNotificationProducers {
		found := false
		for _, orphan := range orphans {
			if orphan == name {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is no longer orphaned: drop it from orphanNotificationProducers, "+
				"and check whether the digest now reports the count it used to hard-code", name)
		}
	}
	t.Logf("unreachable notification producers: %v", orphans)
}

// TestNotificationTypesMatchThePage compares the types the digest can actually deliver with
// the types the page branches on. Both directions are ratchets, because a type on one side
// alone is either a frame nobody renders or a branch nobody can reach.
func TestNotificationTypesMatchThePage(t *testing.T) {
	files := handlerFiles(t)
	producers := producerTypes(t, files)
	reach := reachable(t, files)

	delivered := map[string]bool{}
	for name, types := range producers {
		if !reach[name] {
			continue
		}
		for typ := range types {
			delivered[typ] = true
		}
	}
	if len(delivered) < 4 {
		t.Fatalf("only %d notification types are reachable; the reachability scan is broken", len(delivered))
	}

	page := pageNotificationTypes(t)
	if len(page) < 5 {
		t.Fatalf("the page branches on only %d notification types; the scan is broken", len(page))
	}
	t.Logf("delivered=%d handled=%d", len(delivered), len(page))

	unrendered := keysWithout(page, delivered)
	if len(unrendered) > 1 {
		t.Fatalf("the page handles %d notification types the digest cannot deliver (baseline 1): %v. "+
			"One of them is the orphaned task_failed producer - see orphanNotificationProducers.",
			len(unrendered), unrendered)
	}
	// The other direction: a delivered type with no branch on the page arrives and is dropped.
	unhandled := keysWithout(delivered, page)
	if len(unhandled) > 0 {
		t.Fatalf("the digest delivers %d notification types the page ignores: %v", len(unhandled), unhandled)
	}
}

func pageNotificationTypes(t *testing.T) map[string]bool {
	t.Helper()
	root := moduleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "web", "static", "js", "notifications.js"))
	if err != nil {
		t.Fatalf("read the notification page script: %v", err)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`\.type\s*===?\s*'([a-z][a-z0-9_]{2,})'`).FindAllStringSubmatch(string(data), -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("no notification types found in notifications.js; the matcher is broken")
	}
	return out
}

func keysWithout(from map[string]bool, against map[string]bool) []string {
	out := []string{}
	for key := range from {
		if !against[key] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func handlerFiles(t *testing.T) []*ast.File {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	if len(files) < 50 {
		t.Fatalf("parsed only %d handler sources; the scan is broken", len(files))
	}
	return files
}
