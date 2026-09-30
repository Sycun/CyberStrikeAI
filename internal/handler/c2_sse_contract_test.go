package handler

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"cyberstrike-ai/internal/sse"
)

// The C2 event stream is the third wire shape: the frame body is a bare c2.Event and
// the page branches on its `category`. That field is this stream's event name, so it
// gets the same treatment as the agent streams - a declared set, checked against the
// code that publishes, and enforced by the writer.

const c2PublishArgIndex = 1

// publishEventCall describes one place the C2 manager reports an event.
type publishEventCall struct {
	file     string
	line     int
	category string
	dynamic  bool
}

func c2PublishCalls(t *testing.T) []publishEventCall {
	t.Helper()
	root := moduleRoot(t)
	dir := filepath.Join(root, "internal", "c2")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []publishEventCall
	fset := token.NewFileSet()
	scanFiles := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		scanFiles++
		rel, _ := filepath.Rel(root, name)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee := ""
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				callee = fn.Name
			case *ast.SelectorExpr:
				callee = fn.Sel.Name
			}
			if callee != "publishEvent" && callee != "PublishCustomEvent" {
				return true
			}
			if len(call.Args) <= c2PublishArgIndex {
				t.Errorf("%s:%d: a publishEvent call without a category argument", rel, fset.Position(call.Pos()).Line)
				return true
			}
			arg := call.Args[c2PublishArgIndex]
			if literal, ok := stringArg(arg); ok {
				out = append(out, publishEventCall{file: filepath.ToSlash(rel), line: fset.Position(call.Pos()).Line, category: literal})
				return true
			}
			out = append(out, publishEventCall{file: filepath.ToSlash(rel), line: fset.Position(call.Pos()).Line, dynamic: true})
			return true
		})
	}
	if scanFiles < 10 {
		t.Fatalf("only %d files read from internal/c2; the scan is broken", scanFiles)
	}
	return out
}

func stringArg(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// TestC2CategoriesMatchTheRegistry is the drift gate: every category the C2 manager
// can publish must be declared, and every declared category must still be published.
func TestC2CategoriesMatchTheRegistry(t *testing.T) {
	calls := c2PublishCalls(t)
	published := map[string]bool{}
	var dynamic []string
	for _, call := range calls {
		if call.dynamic {
			dynamic = append(dynamic, fmt.Sprintf("%s:%d", call.file, call.line))
			continue
		}
		published[call.category] = true
	}
	// The one pass-through is PublishCustomEvent's own parameter, which forwards
	// whatever a caller supplies. Today nothing in the tree calls it, so it adds no
	// name; if a caller appears, its category literal will show up in `published` and
	// this assertion stays meaningful.
	if len(dynamic) != 1 {
		t.Errorf("expected exactly one category pass-through, found %v", dynamic)
	}
	if len(published) < 3 {
		t.Fatalf("only %d C2 categories found (%v); the scan is broken", len(published), keysOf(published))
	}
	t.Logf("c2 categories published=%v pass-through=%v", keysOf(published), dynamic)

	declared := map[string]bool{}
	for _, kind := range C2EventSSEKinds() {
		declared[kind.Name] = true
		if strings.TrimSpace(kind.Description) == "" {
			t.Errorf("category %q is declared without a description", kind.Name)
		}
	}
	for name := range published {
		if !declared[name] {
			t.Errorf("the C2 manager publishes category %q but the stream does not declare it: the writer will drop the frame", name)
		}
	}
	for name := range declared {
		if !published[name] {
			t.Errorf("category %q is declared but the C2 manager never publishes it", name)
		}
	}

	// Probes both ways so neither an empty nor an everything scan can pass.
	for _, probe := range []struct {
		name string
		want bool
	}{{"task", true}, {"category_that_does_not_exist", false}} {
		if got := published[probe.name]; got != probe.want {
			t.Fatalf("probe %s: published=%v, want %v", probe.name, got, probe.want)
		}
	}
}

func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The frame bytes are the contract with c2.js, which parses the event object directly:
// moving onto the shared writer may not change one byte of them.
func TestC2EventStreamFrameBytes(t *testing.T) {
	sink := &streamSink{}
	var buf bytes.Buffer
	sink.use(&buf)
	writer := sse.NewWriter(c2EventSSE, sink, nil)

	body := `{"id":"e_1","level":"warn","category":"task","sessionId":"s1","message":"危险任务待审批"}`
	if err := writer.SendLegacy("task", []byte(body)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got, want := buf.String(), "data: "+body+"\n\n"; got != want {
		t.Fatalf("frame = %q, want %q", got, want)
	}

	buf.Reset()
	err := writer.SendLegacy("listener_that_nobody_declared", []byte(`{}`))
	if !errors.Is(err, sse.ErrUnregisteredKind) {
		t.Fatalf("undeclared category error = %v, want the registry refusal", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("an undeclared category reached the wire: %q", buf.String())
	}

	// The sink forwards to whatever writer gin handed the stream callback, so the
	// connection's own flush still happens.
	second := &bytes.Buffer{}
	sink.use(second)
	if err := writer.SendLegacy("session", []byte(`{"category":"session"}`)); err != nil {
		t.Fatalf("send after switch: %v", err)
	}
	if second.Len() == 0 || buf.Len() != 0 {
		t.Fatalf("frames went to the wrong connection: new=%d old=%d", second.Len(), buf.Len())
	}
}

var _ io.Writer = (*streamSink)(nil)

func TestC2EventStreamUsesTheSingleWriter(t *testing.T) {
	root := moduleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "internal", "handler", "c2.go"))
	if err != nil {
		t.Fatal(err)
	}
	// No comment or string in this file may assemble a frame by hand any more; the
	// gate over the whole package counts the same thing, this names the file so a
	// regression points at the line that reintroduced it.
	if strings.Contains(string(data), `"data: `) || strings.Contains(string(data), "`data: ") {
		t.Fatal("c2.go builds an SSE frame again - use the sse.Writer the category registry is bound to")
	}
}

// TestC2PageBranchesOnlyOnPublishedCategories closes the same loop on the C2 stream: the
// pane's switch is scanned (internal/sse.ScanWeb, c2 tier) and compared with the registry.
// Categories the page does not branch on are fine - listener events reach the generic
// renderer, which builds an i18n label from the category - but a branch on a category the
// manager never publishes is code that cannot run, and it fails here.
func TestC2PageBranchesOnlyOnPublishedCategories(t *testing.T) {
	sources, err := sse.ScanWeb(moduleRoot(t))
	if err != nil {
		t.Fatalf("frontend scan: %v", err)
	}
	categories := sse.WebNames(sources, sse.TierC2)
	if len(categories) != 2 {
		t.Fatalf("the C2 pane branches on %d categories (%v), want the two it used to: session and task", len(categories), categories)
	}
	published := map[string]bool{}
	for _, kind := range C2EventSSEKinds() {
		published[kind.Name] = true
	}
	for _, name := range categories {
		if !published[name] {
			t.Errorf("the C2 pane branches on %q, which the registry does not declare and the manager cannot publish", name)
		}
	}
}
