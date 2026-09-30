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
	"strconv"
	"strings"
)

// The inventory is what turns "the backend and the frontend each invented event
// names" into a checked contract: the name list must be derived from the code that
// emits, not maintained beside it.
//
// A name reaches a frame either as a literal at the emit site, or through a
// parameter of a helper that emits on someone's behalf. Literals are collected
// directly; for parameters the scan builds a small value graph - literal arguments
// seed a parameter position, and positions flow into each other where a variable is
// passed on - and runs it to a fixed point. A site whose position has no reachable
// name at all is reported instead of guessed, because an inventory that quietly
// misses a name cannot be the thing the frontend is validated against.

// KindSource records one event name together with the place that produced it.
type KindSource struct {
	Name string
	File string
	Line int
	Mode string
	// Stream is which envelope carries the name: "agent" for the conversational
	// {type,message,data} frame, "terminal" for the command stream's short-key frame.
	// They are separate contracts and are gated separately.
	Stream string
}

// Stream identifiers the inventory distinguishes.
const (
	StreamAgent    = "agent"
	StreamTerminal = "terminal"
	// StreamDetail is the persisted tier: `process_details.event_type` rows the page
	// rebuilds the timeline from after a refresh. It is a separate contract with a
	// separate producer, so it is inventoried separately.
	StreamDetail = "detail"
)

// Inventory is every event name the scan can prove, plus the sites it could not.
type Inventory struct {
	Sources    []KindSource
	Unresolved []KindSource
}

// Names returns the distinct event names, sorted.
func (inv Inventory) Names() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range inv.Sources {
		if s.Name == "" || seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// An analysis is one configured instance of the name-flow walk: which calls are sinks and
// which argument carries the name, which struct fields carry it, and which contract the
// results belong to. Two contracts run through it - the frames on the wire and the persisted
// rows the page reads back - so the difficult part (literals, locals, factory aliases,
// parameter flow iterated to a fixed point) exists once instead of twice, and a fix to the
// flow rules lands on both.
type analysis struct {
	// sinks maps a callee to the argument position holding the event name. A same-named
	// method elsewhere contributes nothing, because a position only ever learns names
	// from arguments at these calls.
	sinks map[string]int
	// envelopes maps a struct type to the field holding the event name.
	envelopes map[string]string
	// label names the contract for every source this analysis produces. The stream
	// analysis leaves it empty and classifies per file instead, because the terminal
	// command stream is a second wire format with its own registry.
	label string
	// handwrittenFrames recognises a `data: {"type":"x"}` string as an emit site: the
	// heartbeat used to be assembled that way, with no envelope to scan.
	handwrittenFrames bool
}

// streamAnalysis is the live wire. The handlers name their emitter closures this way.
var streamAnalysis = analysis{
	sinks: map[string]int{
		"sendEvent":  0,
		"emitHITL":   0,
		"emit":       0,
		"send":       0,
		"publish":    0,
		"sendLine":   0,
		"writeEvent": 0,
		"Send":       0,
	},
	envelopes: map[string]string{
		"StreamEvent": "Type",
		"streamEvent": "T",
		"Frame":       "Type",
	},
	handwrittenFrames: true,
}

// detailAnalysis is the persisted tier. The event type is the third argument of the two
// process-detail writers, and the field of the batch update the store applies.
var detailAnalysis = analysis{
	sinks: map[string]int{
		"AddProcessDetail":       2,
		"AddProcessDetailWithID": 2,
	},
	envelopes: map[string]string{
		"InterruptedUpdate": "EventType",
	},
	label: StreamDetail,
}

func positionKey(fn string, index int) string { return fn + "#" + strconv.Itoa(index) }

// flowSite is an emit point whose event name is a parameter of the enclosing
// function, so its value set has to come from that function's callers.
type flowSite struct {
	source string // "functionName#paramIndex"
	file   string
	line   int
	stream string
}

type collector struct {
	an      analysis
	fset    *token.FileSet
	names   map[string]map[string]bool // parameter position -> event names that can reach it
	flows   map[string][]string        // source position -> positions it flows into
	sites   []flowSite
	sources []KindSource
	gaps    []KindSource
}

// Scan walks the given package directories and collects every event name it can
// prove reaches the wire. The directories are explicit because the inventory is a
// contract: sweeping the whole tree would silently absorb an emitter that does not
// exist yet, and would pick up Send/Publish methods unrelated to SSE.
func Scan(roots ...string) (Inventory, error) {
	return streamAnalysis.run(roots)
}

// ScanPersistedDetails inventories the event types that can reach the process_details table.
// Same machinery, different sinks: the page's history renderer branches on these names, and a
// rename on either side leaves a branch that can never match.
func ScanPersistedDetails(roots ...string) (Inventory, error) {
	return detailAnalysis.run(roots)
}

func (an analysis) run(roots []string) (Inventory, error) {
	c := &collector{
		an:    an,
		fset:  token.NewFileSet(),
		names: map[string]map[string]bool{},
		flows: map[string][]string{},
	}

	var files []*ast.File
	var rels []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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
			file, parseErr := parser.ParseFile(c.fset, path, nil, 0)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", path, parseErr)
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			files = append(files, file)
			rels = append(rels, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return Inventory{}, err
		}
	}
	if len(files) == 0 {
		return Inventory{}, fmt.Errorf("sse: no Go files found under %v", roots)
	}

	// A variable holding an emitter closure returned by a factory
	// (`sendEvent = h.taskFinishingEventSender(sendEvent, ...)`) has to be known as
	// that factory's closure, or the wrapper's own emit site looks unresolvable. The
	// alias is per file: three different files each have their own `sendEvent`.
	for i, file := range files {
		c.scanFile(rels[i], file, fileAliases(file))
	}
	c.propagate()
	c.attributeFlowSites()

	sort.SliceStable(c.sources, func(a, b int) bool {
		if c.sources[a].Name != c.sources[b].Name {
			return c.sources[a].Name < c.sources[b].Name
		}
		if c.sources[a].File != c.sources[b].File {
			return c.sources[a].File < c.sources[b].File
		}
		return c.sources[a].Line < c.sources[b].Line
	})
	return Inventory{Sources: c.sources, Unresolved: c.gaps}, nil
}

func fileAliases(file *ast.File) map[string]string {
	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.AssignStmt)
		if !ok || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
			return true
		}
		lhs, ok := stmt.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := stmt.Rhs[0].(*ast.CallExpr)
		if !ok {
			// `sendEvent := stream.send` binds a local emitter name to a method. Without
			// this the method's own emit site looks like a dead end and every name that
			// only reaches the wire through a helper disappears from the inventory.
			if sel, isSel := stmt.Rhs[0].(*ast.SelectorExpr); isSel {
				out[lhs.Name] = sel.Sel.Name
			}
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			out[lhs.Name] = fn.Name
		case *ast.SelectorExpr:
			out[lhs.Name] = fn.Sel.Name
		}
		return true
	})
	return out
}

// forwarder is one function body that can receive an event name as a parameter,
// named the way its callers know it.
type forwarder struct {
	start, end token.Pos
	name       string
	params     []string
}

func (f forwarder) contains(pos token.Pos) bool { return f.start <= pos && pos <= f.end }

func (f forwarder) paramIndex(name string) (int, bool) {
	for i, param := range f.params {
		if param == name {
			return i, true
		}
	}
	return 0, false
}

func (c *collector) scanFile(rel string, file *ast.File, aliases map[string]string) {
	local := localStringLiterals(file)
	forwarders := forwardersIn(c.fset, file)

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			c.recordCall(rel, node, local, forwarders, aliases)
		case *ast.CompositeLit:
			c.recordEnvelope(rel, node, local, forwarders, aliases)
		case *ast.BasicLit:
			if c.an.handwrittenFrames {
				c.recordHandwrittenFrame(rel, node, c.fset.Position(node.Pos()).Line)
			}
		}
		return true
	})
}

// handwrittenFrame matches a frame assembled as a string instead of an envelope
// struct - `data: {"type":"heartbeat"}` is exactly that, and an inventory built only
// from struct literals would have missed the one event the transport itself emits.
var handwrittenFrame = regexp.MustCompile(`data: +\{[^{}]*"(?:type|t)" *:" *([a-z0-9_]+)"`)

func (c *collector) recordHandwrittenFrame(rel string, lit *ast.BasicLit, line int) {
	if lit.Kind != token.STRING {
		return
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return
	}
	for _, match := range handwrittenFrame.FindAllStringSubmatch(value, -1) {
		name := match[1]
		stream := c.streamOf(rel)
		if strings.Contains(value, `"t":`) || strings.Contains(value, `\"t\":`) {
			stream = StreamTerminal
		}
		c.sources = append(c.sources, KindSource{Name: name, File: rel, Line: line, Mode: "handwritten", Stream: stream})
	}
}

func forwardersIn(fset *token.FileSet, file *ast.File) []forwarder {
	out := []forwarder{}
	var enclosing string
	ast.Inspect(file, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			enclosing = fn.Name.Name
			if fn.Body != nil {
				out = append(out, forwarder{start: fn.Body.Pos(), end: fn.Body.End(), name: fn.Name.Name, params: paramNames(fn.Type)})
			}
		case *ast.FuncLit:
			if fn.Body == nil {
				return true
			}
			name := assignedTo(file, fn.Pos(), fn.Body.End())
			if name == "" {
				// A closure handed back by a factory is reached through that factory's
				// callers, so naming it after the enclosing function keeps the chain findable.
				name = enclosing
			}
			out = append(out, forwarder{start: fn.Body.Pos(), end: fn.Body.End(), name: name, params: paramNames(fn.Type)})
		}
		return true
	})
	return out
}

func assignedTo(file *ast.File, litPos, litEnd token.Pos) string {
	found := ""
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.AssignStmt)
		if !ok || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
			return true
		}
		lit, ok := stmt.Rhs[0].(*ast.FuncLit)
		if !ok || lit.Pos() != litPos || lit.Body.End() != litEnd {
			return true
		}
		if ident, ok := stmt.Lhs[0].(*ast.Ident); ok {
			found = ident.Name
		}
		return true
	})
	return found
}

// paramNames lists one entry per argument position, keeping the parameter name and
// falling back to the type so positions stay aligned for unnamed parameters.
func paramNames(ftype *ast.FuncType) []string {
	if ftype == nil || ftype.Params == nil {
		return nil
	}
	var out []string
	for _, field := range ftype.Params.List {
		if len(field.Names) == 0 {
			out = append(out, "~"+exprName(field.Type))
			continue
		}
		for _, name := range field.Names {
			out = append(out, name.Name)
		}
	}
	return out
}

func exprName(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return "?"
}

func innermost(forwarders []forwarder, pos token.Pos) (forwarder, bool) {
	best := forwarder{}
	found := false
	for _, f := range forwarders {
		if f.contains(pos) && (!found || f.start > best.start) {
			best, found = f, true
		}
	}
	return best, found
}

func calleeOf(call *ast.CallExpr, aliases map[string]string) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if factory, ok := aliases[fn.Name]; ok {
			return factory
		}
		return fn.Name
	case *ast.SelectorExpr:
		if factory, ok := aliases[fn.Sel.Name]; ok {
			return factory
		}
		return fn.Sel.Name
	default:
		return ""
	}
}

func (c *collector) recordCall(rel string, call *ast.CallExpr, local map[string][]string, forwarders []forwarder, aliases map[string]string) {
	callee := calleeOf(call, aliases)
	if callee == "" || len(call.Args) == 0 {
		return
	}
	line := c.fset.Position(call.Pos()).Line

	if index, ok := c.an.sinks[callee]; ok && len(call.Args) > index {
		c.noteNameArgument(rel, line, callee, index, call.Args[index], local, forwarders)
		return
	}
	// Any other call still teaches the graph: a literal in slot i is a name that can
	// reach that parameter, and a variable carries it from the enclosing position.
	for i, arg := range call.Args {
		key := positionKey(callee, i)
		if value, ok := stringLiteral(arg); ok {
			c.seed(key, value)
			continue
		}
		c.flowFromArgument(arg, key, forwarders)
	}
}

// noteNameArgument decides where the event name of one emit call comes from.
func (c *collector) noteNameArgument(rel string, line int, callee string, index int, arg ast.Expr, local map[string][]string, forwarders []forwarder) {
	if value, ok := stringLiteral(arg); ok {
		c.sources = append(c.sources, KindSource{Name: value, File: rel, Line: line, Mode: "literal", Stream: c.streamOf(rel)})
		c.seed(positionKey(callee, index), value)
		return
	}
	ident, ok := arg.(*ast.Ident)
	if !ok || ident.Name == "_" || ident.Name == "nil" {
		return
	}
	if names := local[ident.Name]; len(names) > 0 {
		for _, value := range names {
			c.sources = append(c.sources, KindSource{Name: value, File: rel, Line: line, Mode: "local", Stream: c.streamOf(rel)})
			c.seed(positionKey(callee, index), value)
		}
		return
	}
	fn, ok := innermost(forwarders, ident.Pos())
	if !ok || fn.name == "" {
		return
	}
	paramIndex, ok := fn.paramIndex(ident.Name)
	if !ok {
		return
	}
	source := positionKey(fn.name, paramIndex)
	c.sites = append(c.sites, flowSite{source: source, file: rel, line: line})
	c.flowInto(source, positionKey(callee, index))
}

// flowFromArgument records that the value of one argument position can reach `sink`.
func (c *collector) flowFromArgument(arg ast.Expr, sink string, forwarders []forwarder) {
	ident, ok := arg.(*ast.Ident)
	if !ok || ident.Name == "_" || ident.Name == "nil" {
		return
	}
	fn, ok := innermost(forwarders, ident.Pos())
	if !ok || fn.name == "" {
		return
	}
	paramIndex, ok := fn.paramIndex(ident.Name)
	if !ok {
		return
	}
	c.flowInto(positionKey(fn.name, paramIndex), sink)
}

func (c *collector) recordEnvelope(rel string, lit *ast.CompositeLit, local map[string][]string, forwarders []forwarder, aliases map[string]string) {
	name, ok := envelopeTypeName(lit.Type)
	if !ok {
		return
	}
	field, wants := c.an.envelopes[name]
	if !wants {
		return
	}
	line := c.fset.Position(lit.Pos()).Line
	stream := c.envelopeStream(name)
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != field {
			continue
		}
		if value, ok := stringLiteral(kv.Value); ok {
			c.sources = append(c.sources, KindSource{Name: value, File: rel, Line: line, Mode: "struct", Stream: stream})
			continue
		}
		ident, ok := kv.Value.(*ast.Ident)
		if !ok || ident.Name == "nil" {
			continue
		}
		if names := local[ident.Name]; len(names) > 0 {
			for _, value := range names {
				c.sources = append(c.sources, KindSource{Name: value, File: rel, Line: line, Mode: "local", Stream: stream})
			}
			continue
		}
		fn, ok := innermost(forwarders, ident.Pos())
		if !ok || fn.name == "" {
			c.gaps = append(c.gaps, KindSource{Name: ident.Name, File: rel, Line: line, Mode: "unresolved"})
			continue
		}
		paramIndex, ok := fn.paramIndex(ident.Name)
		if !ok {
			c.gaps = append(c.gaps, KindSource{Name: ident.Name, File: rel, Line: line, Mode: "unresolved"})
			continue
		}
		c.sites = append(c.sites, flowSite{source: positionKey(fn.name, paramIndex), file: rel, line: line, stream: stream})
	}
}

// envelopeStream keeps the terminal short-key envelope in its own contract while the
// persisted analysis labels everything with its own tier.
func (c *collector) envelopeStream(name string) string {
	if c.an.label != "" {
		return c.an.label
	}
	if name == "streamEvent" {
		return StreamTerminal
	}
	return StreamAgent
}

func envelopeTypeName(expr ast.Expr) (string, bool) {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name, true
	case *ast.SelectorExpr:
		return t.Sel.Name, true
	default:
		return "", false
	}
}

func (c *collector) seed(key, name string) {
	if c.names[key] == nil {
		c.names[key] = map[string]bool{}
	}
	c.names[key][name] = true
}

func (c *collector) flowInto(source, sink string) {
	c.flows[source] = append(c.flows[source], sink)
}

// propagate closes the graph over the flow edges: a parameter position knows every
// name that can be handed to it, however many hops away the literal was written.
func (c *collector) propagate() {
	for changed := true; changed; {
		changed = false
		for source, targets := range c.flows {
			if len(c.names[source]) == 0 {
				continue
			}
			for _, target := range targets {
				if c.names[target] == nil {
					c.names[target] = map[string]bool{}
				}
				for value := range c.names[source] {
					if !c.names[target][value] {
						c.names[target][value] = true
						changed = true
					}
				}
			}
		}
	}
}

// attributeFlowSites turns each parameter-driven emit site into the names it can
// actually send, and flags the ones nothing ever passes to it.
func (c *collector) attributeFlowSites() {
	seen := map[string]bool{}
	stillGap := []KindSource{}
	for _, site := range c.sites {
		values := sortedKeys(c.names[site.source])
		if len(values) == 0 {
			stillGap = append(stillGap, KindSource{Name: site.source, File: site.file, Line: site.line, Mode: "unresolved"})
			continue
		}
		for _, value := range values {
			key := value + "|" + site.file + "|" + strconv.Itoa(site.line)
			if seen[key] {
				continue
			}
			seen[key] = true
			stream := site.stream
			if stream == "" {
				stream = StreamAgent
			}
			c.sources = append(c.sources, KindSource{Name: value, File: site.file, Line: site.line, Mode: "forwarded", Stream: stream})
		}
	}
	c.gaps = append(c.gaps, stillGap...)
}

// streamOf classifies a call site by the file it lives in: the terminal command
// stream has its own short-key envelope and its own registry.
func (c *collector) streamOf(rel string) string {
	if c.an.label != "" {
		return c.an.label
	}
	if strings.HasPrefix(filepath.Base(rel), "terminal_stream") {
		return StreamTerminal
	}
	return StreamAgent
}

// NamesIn returns the names of one stream, sorted.
func (inv Inventory) NamesIn(stream string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range inv.Sources {
		if s.Stream != stream || s.Name == "" || seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// localStringLiterals collects `name := "literal"` and `var name = "literal"` per file.
func localStringLiterals(file *ast.File) map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	add := func(name, value string) {
		if seen[name+"|"+value] {
			return
		}
		seen[name+"|"+value] = true
		out[name] = append(out[name], value)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			if len(stmt.Rhs) != 1 || len(stmt.Lhs) != 1 {
				return true
			}
			lhs, ok := stmt.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			if value, ok := stringLiteral(stmt.Rhs[0]); ok {
				add(lhs.Name, value)
			}
		case *ast.ValueSpec:
			if len(stmt.Values) != 1 {
				return true
			}
			value, ok := stringLiteral(stmt.Values[0])
			if !ok {
				return true
			}
			for _, ident := range stmt.Names {
				add(ident.Name, value)
			}
		}
		return true
	})
	return out
}

// stringLiteral accepts the shapes event names appear in: an interpreted or raw
// string, and a concatenation of literals. Anything computed at runtime is not a
// name the inventory can claim, so it is left unresolved on purpose.
func stringLiteral(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false
		}
		return value, true
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := stringLiteral(e.X)
		if !ok {
			return "", false
		}
		right, ok := stringLiteral(e.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	default:
		return "", false
	}
}
