package sse

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The other half of the contract: what the shipped page actually branches on.
//
// The server-side inventory proves which names can reach a frame. It cannot say whether
// anyone is listening, and a rename on either side shows up as a frame the page ignores.
// So the page is scanned with the same "derive it from the code" rule and the two sets are
// compared in both directions.
//
// Tiers matter, because the page reads three different contracts: a live frame's `type`, a
// *persisted* process-detail `eventType` (what the timeline is rebuilt from after a
// refresh), and a C2 event's `category`. Their producers are different code, so folding
// them together would let a name handled only in history look like a live-stream consumer.
// Each comparison is done inside one tier.

// Tier identifiers for the frontend contract.
const (
	// TierStream reads `type` off a parsed SSE frame.
	TierStream = "stream"
	// TierDetail reads `eventType` off a persisted process-detail row.
	TierDetail = "detail"
	// TierC2 reads `category` off a C2 event.
	TierC2 = "c2"
)

// WebSource is one event name the page branches on, with the place that does it.
type WebSource struct {
	Name string
	File string
	Line int
	Tier string
}

// ScanWeb collects every event name the frontend branches on. The domain is the shipped
// scripts under web/static/js, excluding generated output and the node test fixtures:
// neither is what a browser loads, and counting them would make the catalogue claim a
// consumer that does not exist.
func ScanWeb(root string) ([]WebSource, error) {
	dir := filepath.Join(root, "web", "static", "js")
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir {
				switch filepath.Base(path) {
				case "generated", "node_modules":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".test.cjs") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) < 10 {
		return nil, fmt.Errorf("sse: only %d frontend scripts under %s; the scan is broken", len(files), dir)
	}
	sort.Strings(files)

	var out []WebSource
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		code := stripComments(string(data))
		display := filepath.ToSlash(strings.TrimPrefix(path, root+string(os.PathSeparator)))
		out = append(out, switchSources(display, code)...)
		out = append(out, comparisonSources(display, code, scopesOf(code))...)
	}
	out = dedupeWebSources(out)

	counts := map[string]int{
		TierStream: len(WebNames(out, TierStream)),
		TierDetail: len(WebNames(out, TierDetail)),
		TierC2:     len(WebNames(out, TierC2)),
	}
	// Cardinality floors measured on this tree: one switch over the frame type plus two
	// independent readers covers 60 names, the history renderer covers 34, and the C2 pane
	// branches on exactly two categories. A matcher that silently stopped working would
	// otherwise report an empty contract that every comparison below then passes.
	if counts[TierStream] < 55 || counts[TierDetail] < 25 || counts[TierC2] != 2 {
		return nil, fmt.Errorf("sse frontend scan found %d stream / %d detail / %d c2 branches under %s; expected at least 55/25 and exactly 2 - the matcher is broken",
			counts[TierStream], counts[TierDetail], counts[TierC2], dir)
	}
	return out, nil
}

// nameScopes are the identifiers per file that carry an event name, split by contract tier:
// frame variables (a parsed SSE frame, or the receiver of a switch over `.type`) and
// detail rows (anything bound from `.eventType`). Aliases inherit their tier, transitively,
// because that is how the readers are written.
type nameScopes struct {
	frames map[string]bool
	detail map[string]bool
}

func scopesOf(code string) nameScopes {
	scopes := nameScopes{frames: map[string]bool{}, detail: map[string]bool{}}
	for _, m := range jsonParseRe.FindAllStringSubmatch(code, -1) {
		scopes.frames[m[1]] = true
	}
	for _, m := range switchTypeRe.FindAllStringSubmatch(code, -1) {
		scopes.frames[cleanIdent(m[1])] = true
	}
	for grow := true; grow; {
		grow = false
		for _, m := range aliasRe.FindAllStringSubmatch(code, -1) {
			name, expr := m[1], m[2]
			if scopes.frames[name] || scopes.detail[name] {
				continue
			}
			if bindsFrame(expr, scopes.frames) {
				scopes.frames[name] = true
				grow = true
			} else if fieldDetailRe.MatchString(expr) {
				scopes.detail[name] = true
				grow = true
			}
		}
	}
	return scopes
}

// bindsFrame reports an expression that reads `.type` off a known frame variable.
func bindsFrame(expr string, frames map[string]bool) bool {
	for _, m := range fieldTypeRe.FindAllStringSubmatch(expr, -1) {
		if frames[cleanIdent(m[1])] {
			return true
		}
	}
	return false
}

// switchSources collects `case 'x':` labels from switches whose operand is an event-name
// field. Every other switch in the page (toast kinds, page ids, sort modes, workflow node
// types, C2 task types) is skipped: sweeping them would put `shell` and `vulnerability`
// into the event catalogue.
func switchSources(display, code string) []WebSource {
	var out []WebSource
	for _, loc := range switchRe.FindAllStringSubmatchIndex(code, -1) {
		operand := code[loc[2]:loc[3]]
		tier, ok := tierOfField(operand)
		if !ok {
			continue
		}
		body, found := blockFromBrace(code, loc[1]-1)
		if !found {
			continue
		}
		line := lineOf(code, loc[0])
		for _, m := range caseRe.FindAllStringSubmatch(body, -1) {
			name := m[2]
			if name == "" {
				name = m[3]
			}
			out = append(out, WebSource{Name: name, File: display, Line: line, Tier: tier})
		}
	}
	return out
}

// comparisonSources collects `x === 'name'` (and its negation and flipped form) where the
// compared expression is an event-name field, or a local alias of one.
func comparisonSources(display, code string, scopes nameScopes) []WebSource {
	var out []WebSource
	for _, loc := range compareRe.FindAllStringSubmatchIndex(code, -1) {
		var left, name string
		var pos int
		if loc[2] >= 0 {
			left, name, pos = code[loc[2]:loc[3]], code[loc[4]:loc[5]], loc[0]
		} else {
			left, name, pos = code[loc[8]:loc[9]], code[loc[6]:loc[7]], loc[6]
		}
		tier, ok := tierOfComparison(left, scopes)
		if !ok {
			continue
		}
		if isTypeofCheck(code, loc) {
			continue
		}
		out = append(out, WebSource{Name: name, File: display, Line: lineOf(code, pos), Tier: tier})
	}
	return out
}

// isTypeofCheck rejects `typeof event.type === 'object'`. The left side is a frame field,
// so the tier rule alone cannot tell it apart from a branch - but the compared value is a
// JavaScript type name, and counting it would invent an event called "object".
func isTypeofCheck(code string, loc []int) bool {
	start := loc[0]
	window := start
	if window > 24 {
		window = 24
	}
	return typeofRe.MatchString(code[start-window : start])
}

// typeofRe looks back over the characters that can sit between `typeof` and the operator:
// whitespace, identifier characters and member accesses.
var typeofRe = regexp.MustCompile(`typeof[\s\w.$]*\s*$`)

// tierOfComparison decides which contract a compared expression belongs to. `.eventType`
// and `.category` are specific enough on their own; a bare `.type` only counts when the
// receiver is proved to hold frames.

func tierOfComparison(expr string, scopes nameScopes) (string, bool) {
	expr = strings.TrimSpace(expr)
	switch {
	case strings.HasSuffix(expr, ".eventType"):
		return TierDetail, true
	case strings.HasSuffix(expr, ".category"):
		return TierC2, true
	case strings.HasSuffix(expr, ".type"):
		// Only a frame variable: the fact graph also stores a `type` on its nodes, and
		// counting those would put `vulnerability` into the event catalogue.
		if scopes.frames[cleanIdent(strings.TrimSuffix(expr, ".type"))] {
			return TierStream, true
		}
	default:
		// A bare alias: webshell.js compares `_et`, bound from `eventData.type`; the
		// history renderer compares `et`, bound from `detail.eventType`.
		if scopes.frames[expr] {
			return TierStream, true
		}
		if scopes.detail[expr] {
			return TierDetail, true
		}
	}
	return "", false
}

// tierOfField maps a switch operand to the contract tier it carries.
func tierOfField(expr string) (string, bool) {
	switch {
	case strings.HasSuffix(expr, ".type"):
		return TierStream, true
	case strings.HasSuffix(expr, ".eventType"):
		return TierDetail, true
	case strings.HasSuffix(expr, ".category"):
		return TierC2, true
	default:
		return "", false
	}
}

func cleanIdent(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndexAny(expr, "({[."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.TrimSpace(expr)
}

var (
	switchRe      = regexp.MustCompile(`(?s)switch\s*\(\s*([A-Za-z_$][\w.$()]*)\s*\)\s*\{`)
	switchTypeRe  = regexp.MustCompile(`(?s)switch\s*\(\s*([A-Za-z_$][\w.$()]*)\.type\s*\)\s*\{`)
	caseRe        = regexp.MustCompile(`(?m)^\s*case\s*("([a-z][a-z0-9_]*)"|'([a-z][a-z0-9_]*)')\s*:`)
	compareRe     = regexp.MustCompile(`([A-Za-z_$][\w.$()]*)\s*(?:===?|!==?)\s*['"]([a-z][a-z0-9_]*)['"]|['"]([a-z][a-z0-9_]*)['"]\s*(?:===?|!==?)\s*([A-Za-z_$][\w.$()]*)`)
	aliasRe       = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*=\s*([^;\n]*)`)
	fieldTypeRe   = regexp.MustCompile(`([A-Za-z_$][\w.$]*)\.type\b`)
	fieldDetailRe = regexp.MustCompile(`[\w.$]*\.eventType\b`)
	jsonParseRe   = regexp.MustCompile(`(?:var|let|const)\s+([A-Za-z_$][\w$]*)\s*=\s*JSON\.parse\(`)
)

func dedupeWebSources(in []WebSource) []WebSource {
	seen := map[WebSource]bool{}
	out := make([]WebSource, 0, len(in))
	for _, s := range in {
		if s.Name == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Tier != out[b].Tier {
			return out[a].Tier < out[b].Tier
		}
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		if out[a].File != out[b].File {
			return out[a].File < out[b].File
		}
		return out[a].Line < out[b].Line
	})
	return out
}

// WebNames returns the distinct names of one tier, sorted.
func WebNames(sources []WebSource, tier string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range sources {
		if s.Tier != tier || seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// lineOf counts newlines before an offset, which is what makes the catalogue point at code.
func lineOf(code string, offset int) int {
	if offset > len(code) {
		offset = len(code)
	}
	return strings.Count(code[:offset], "\n") + 1
}

// blockFromBrace returns the contents of the block whose `{` sits at open. It only runs on
// comment-stripped source, so braces inside comments cannot unbalance it; string bodies
// are preserved because the branch itself is a literal, and a brace inside a literal is
// balanced by the terminator skip in stripComments.
func blockFromBrace(code string, open int) (string, bool) {
	if open < 0 || open >= len(code) || code[open] != '{' {
		return "", false
	}
	depth := 0
	for i := open; i < len(code); i++ {
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return code[open+1 : i], true
			}
		}
	}
	return "", false
}

// stripComments blanks comment text while keeping length and line positions, so offsets
// still map to lines. Only comments are touched: a name mentioned in prose must not read
// as a branch, and string literals are what the matchers look for. Quotes are skipped
// rather than blanked, so a `//` inside a URL or a template cannot eat real code.
func stripComments(src string) string {
	out := []byte(src)
	blank := func(from, to int) {
		for i := from; i < to && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	for i := 0; i < len(out); i++ {
		switch {
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '/':
			j := i
			for j < len(out) && out[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j - 1
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				blank(i, len(out))
				return string(out)
			}
			blank(i, i+2+j+2)
			i = i + 1 + j
		case out[i] == '\'' || out[i] == '"' || out[i] == '`':
			quote := out[i]
			j := i + 1
			for j < len(out) {
				if out[j] == '\\' {
					j += 2
					continue
				}
				if out[j] == quote {
					break
				}
				j++
			}
			i = j
		}
	}
	return string(out)
}
