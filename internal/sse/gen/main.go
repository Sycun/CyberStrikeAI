//go:generate go run . -root ../../..

// Command gen derives the SSE event catalogue from the code that emits it, so the
// contract between the stream and the page is produced rather than maintained.
//
// Run from the repository root: go run ./internal/sse/gen -root .
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cyberstrike-ai/internal/sse"
)

// The traversal domain of the inventory is declared in internal/sse/contract.go, next to
// the extractor and the floors, because the registry gate scans the same tree: two copies
// of a domain drift, and a gate reading a narrower tree than this one would certify a
// contract the catalogue never claimed.

const (
	goldenPath = "internal/sse/testdata/sse-kinds.golden.json"
	jsPath     = "web/static/js/generated/sse-events.js"
	docsPath   = "docs/zh-CN/sse-event-catalog.md"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, "sse gen:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	// One domain, one rule: the generator and the registry gate both call
	// sse.BuildInventory, so the catalogue the page is validated against cannot be
	// derived from a wider tree than the contract tests certify.
	merged, err := sse.BuildInventory(root)
	if err != nil {
		return err
	}
	names := merged.Names()
	// The floors live beside the extractor so the generator and the registry gate fail
	// on the same measured count. An extractor that silently stopped following names
	// would otherwise produce a short catalogue that every downstream gate then passes.
	minTotal, minAgent, wantTerminal := sse.InventoryFloors()
	if len(names) < minTotal || len(merged.NamesIn(sse.StreamAgent)) < minAgent || len(merged.NamesIn(sse.StreamTerminal)) != wantTerminal {
		return fmt.Errorf("inventory found %d names (%d agent, %d terminal); expected at least %d with %d terminal - the extractor is broken",
			len(names), len(merged.NamesIn(sse.StreamAgent)), len(merged.NamesIn(sse.StreamTerminal)), minTotal, wantTerminal)
	}

	// The page side of the same contract, scanned out of the shipped scripts. Recording it
	// beside the server's list is what makes a one-sided rename visible in the catalogue
	// instead of only in a test failure.
	web, err := sse.ScanWeb(root)
	if err != nil {
		return err
	}
	// The persisted tier: `process_details.event_type` rows the timeline is rebuilt from after
	// a refresh. Its sink is the store rather than the writer, so it is inventoried separately
	// - and a name that only ever reaches a row would otherwise look unconsumed forever.
	persisted, err := sse.ScanPersistedDetails(filepath.Join(root, "internal"), filepath.Join(root, "cmd"))
	if err != nil {
		return err
	}
	detailNames := persisted.NamesIn(sse.StreamDetail)
	if len(detailNames) < 8 {
		return fmt.Errorf("persisted inventory found %d event types; expected at least 8 - the detail sink table is stale", len(detailNames))
	}
	doc := inventoryDocument{
		Agent:      merged.NamesIn(sse.StreamAgent),
		Terminal:   merged.NamesIn(sse.StreamTerminal),
		Sources:    merged.Sources,
		Consumed:   sse.WebNames(web, sse.TierStream),
		Details:    sse.WebNames(web, sse.TierDetail),
		Categories: sse.WebNames(web, sse.TierC2),
		Persisted:  detailNames,
		PersistSrc: persisted.Sources,
	}
	golden, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := write(root, goldenPath, append(golden, '\n')); err != nil {
		return err
	}
	if err := write(root, jsPath, renderJS(doc)); err != nil {
		return err
	}
	if err := write(root, docsPath, renderMarkdown(merged, web, doc)); err != nil {
		return err
	}
	fmt.Printf("sse inventory: %d event names (%d agent, %d terminal)\n",
		len(names), len(merged.NamesIn(sse.StreamAgent)), len(merged.NamesIn(sse.StreamTerminal)))
	return nil
}

// inventoryDocument is the committed contract: what the server can emit, and what the
// page branches on. Both halves are derived, so neither side of a rename can quietly
// outlive the other.
type inventoryDocument struct {
	Agent    []string         `json:"agent"`
	Terminal []string         `json:"terminal"`
	Sources  []sse.KindSource `json:"sources"`
	// Consumed is the stream tier: names read off a parsed frame. Details is the
	// persisted process-detail tier, which the timeline is rebuilt from after a refresh;
	// it is recorded but not compared against the stream, because its producer is the
	// store, not the writer. Categories is the C2 pane's switch on event.category.
	Consumed   []string `json:"consumed"`
	Details    []string `json:"detailConsumed"`
	Categories []string `json:"c2Consumed"`
	// Persisted is the event_type universe of process_details rows; PersistSrc keeps the
	// place that proved each one.
	Persisted  []string         `json:"persisted"`
	PersistSrc []sse.KindSource `json:"persistedSources"`
}

func renderJS(doc inventoryDocument) []byte {
	var b strings.Builder
	b.WriteString("// Generated by `make generate` (internal/sse/gen). Do not edit.\n")
	b.WriteString("//\n")
	b.WriteString("// Every event name the server can put on an SSE stream. The page reads this\n")
	b.WriteString("// instead of keeping its own list, so a rename on one side shows up on the\n")
	b.WriteString("// other instead of silently dropping a frame.\n")
	b.WriteString("window.CSAI = window.CSAI || {};\n")
	b.WriteString("window.CSAI.sseEvents = {\n")
	b.WriteString("  agent: [\n")
	for _, name := range doc.Agent {
		fmt.Fprintf(&b, "    %q,\n", name)
	}
	b.WriteString("  ],\n")
	b.WriteString("  terminal: [\n")
	for _, name := range doc.Terminal {
		fmt.Fprintf(&b, "    %q,\n", name)
	}
	b.WriteString("  ],\n")
	b.WriteString("};\n")
	b.WriteString("window.CSAI.isSSEEvent = function (name, stream) {\n")
	b.WriteString("  var list = window.CSAI.sseEvents[stream || 'agent'] || [];\n")
	b.WriteString("  return list.indexOf(name) !== -1;\n};\n")
	return []byte(b.String())
}

func renderMarkdown(inv sse.Inventory, web []sse.WebSource, doc inventoryDocument) []byte {
	names := inv.Names()
	byName := map[string][]sse.KindSource{}
	for _, s := range inv.Sources {
		byName[s.Name] = append(byName[s.Name], s)
	}
	byWeb := map[string][]sse.WebSource{}
	for _, s := range web {
		byWeb[s.Name+"|"+s.Tier] = append(byWeb[s.Name+"|"+s.Tier], s)
	}
	var b strings.Builder
	b.WriteString("# SSE 事件目录（生成物）\n\n")
	b.WriteString("由 `make generate` 提取：服务端一侧扫 `internal/handler`（帧在这里拼装），\n")
	b.WriteString("再并上 `internal` 全树的进度回调调用点（事件名多半拼在发射包之外）；\n")
	b.WriteString("前端一侧扫 `web/static/js`（排除 `generated/`）。\n")
	b.WriteString("注册表在 `internal/handler/sse_kinds.go`，写入器在 `internal/sse`：\n")
	b.WriteString("未在此目录登记的事件名无法写到线上。\n\n")
	b.WriteString("两条流各自一套契约：`agent` 是 `{type,message,data}`，`terminal` 是短键 `{t,d,c}`。\n\n")
	b.WriteString("前端消费列由同一份生成器扫 `web/static/js` 得到：`switch (event.type)` 的 case、" +
		"以及对帧变量（含 `_et` 这类别名）的比较。历史时间线读的是持久化 `eventType`，" +
		"那是另一份契约，见文末。\n\n")
	b.WriteString("| 事件名 | 流 | 发射点 | 前端消费点 |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, name := range names {
		sources := byName[name]
		locations := make([]string, 0, len(sources))
		for _, s := range sources {
			locations = append(locations, fmt.Sprintf("`%s:%d`(%s)", s.File, s.Line, s.Mode))
		}
		stream := sources[0].Stream
		consumed := "—（页面未分支）"
		if sites := byWeb[name+"|"+sse.TierStream]; len(sites) > 0 {
			parts := make([]string, 0, len(sites))
			for _, s := range sites {
				parts = append(parts, fmt.Sprintf("`%s:%d`", s.File, s.Line))
			}
			consumed = strings.Join(parts, ", ")
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", name, stream, strings.Join(locations, ", "), consumed)
	}

	b.WriteString("\n## 双向差集\n\n")
	fmt.Fprintf(&b, "- 服务端可发、页面不分支：**%d** 个：%s\n", len(onlyIn(doc.Agent, doc.Consumed)), joinNames(onlyIn(doc.Agent, doc.Consumed)))
	fmt.Fprintf(&b, "- 页面分支、服务端从不发：**%d** 个：%s\n", len(onlyIn(doc.Consumed, doc.Agent)), joinNames(onlyIn(doc.Consumed, doc.Agent)))
	b.WriteString("\n这两个数字都是 ratchet，只许降。第一个方向意味着帧到了客户端被丢弃；" +
		"第二个方向是页面上的死分支——写入器会拒绝未登记的名字，所以那一支永远走不到。\n")

	b.WriteString("\n## 持久化契约（`process_details.eventType`）\n\n")
	fmt.Fprintf(&b, "页面重建历史时间线时读的是库里的行，共 **%d** 个名字：%s。\n", len(doc.Details), joinNames(doc.Details))
	b.WriteString("它的生产者不是写入器而是存储层（`AddProcessDetail` 第 3 参、`InterruptedUpdate.EventType`），\n")
	b.WriteString("因此单列一份服务端真相源并与它做双向比对，见下一节。\n\n")
	b.WriteString("## C2 事件流\n\n")
	fmt.Fprintf(&b, "页面按 `event.category` 分支的名字：%s。\n", joinNames(doc.Categories))

	b.WriteString("\n## 持久化事件类型：服务端可写入的行 vs 页面重建历史时分支的名字\n\n")
	b.WriteString("服务端一侧由 `AddProcessDetail`/`AddProcessDetailWithID` 的第 3 参与 `InterruptedUpdate.EventType` 证明。\n")
	b.WriteString("已知边界：经**进度回调变量**传入的持久化不在这个精确集合里——值图按声明过的函数名记参数位，\n")
	b.WriteString("而回调是变量。所以第二个方向可能少报，第一个方向用「持久化 ∪ 流式」做上界只会多报、不会误判。\n\n")
	fmt.Fprintf(&b, "- 服务端可持久化：**%d** 个：%s\n", len(doc.Persisted), joinNames(doc.Persisted))
	unrendered := onlyIn(doc.Persisted, doc.Details)
	fmt.Fprintf(&b, "- 可持久化而页面历史不分支：**%d** 个：%s\n", len(unrendered), joinNames(unrendered))
	producible := append(append([]string{}, doc.Persisted...), doc.Agent...)
	dead := onlyIn(doc.Details, producible)
	fmt.Fprintf(&b, "- 页面历史分支、两个生产者都给不出：**%d** 个：%s\n", len(dead), joinNames(dead))
	b.WriteString("\n两个数字都由 `internal/handler/detail_contract_test.go` 钉成只许降的 ratchet。\n")
	return []byte(b.String())
}

func write(root, rel string, data []byte) error {
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return nil
}

// onlyIn lists the names of `in` that are absent from `against`.
func onlyIn(list, against []string) []string {
	have := map[string]bool{}
	for _, name := range against {
		have[name] = true
	}
	out := []string{}
	for _, name := range list {
		if !have[name] {
			out = append(out, "`"+name+"`")
		}
	}
	return out
}

func joinNames(names []string) string {
	if len(names) == 0 {
		return "无"
	}
	return strings.Join(names, ", ")
}
