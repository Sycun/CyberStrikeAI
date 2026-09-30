package provider

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The published form of the catalog. Building it here rather than in the generator is what
// makes the artifacts checkable: a test can compare the committed files against Publish()
// field for field, so changing any value in the table - a context window, a cost, a retry
// posture - fails the check until the artifacts are regenerated. A generator-side struct
// would leave the test hand-picking which fields matter, and it would miss the ones nobody
// thought to list.

// PublishedRow is one vendor: the dialect itself plus the answers derived from it.
type PublishedRow struct {
	Dialect
	Endpoint          string   `json:"endpoint"`
	ProviderName      string   `json:"provider_name"`
	AgenticBackend    bool     `json:"agentic_backend"`
	AnthropicMessages bool     `json:"anthropic_messages"`
	ListsModels       bool     `json:"lists_models"`
	CapabilityFlags   []string `json:"capability_flags"`
}

// Published is the catalog document: the vendor list and one row per vendor.
type Published struct {
	Vendors []string       `json:"vendors"`
	Rows    []PublishedRow `json:"rows"`
}

// Publish renders the installed catalog.
func Publish() Published {
	catalog := Default()
	vendors := catalog.Vendors()
	rows := make([]PublishedRow, 0, len(vendors))
	for _, vendor := range vendors {
		dialect, ok := catalog.Lookup(vendor)
		if !ok {
			// Vendors() and Lookup() read the same map, so this can only mean the
			// catalog was edited concurrently; publishing a partial table would be worse.
			panic("provider: vendor " + vendor + " is listed but not registered")
		}
		// Resolve rather than the raw row: a table entry without a base URL is not a
		// usable dialect, and the document should show what a caller actually gets.
		endpoint, err := catalog.Resolve(vendor, "").EndpointURL()
		if err != nil {
			panic("provider: vendor " + vendor + " has no usable endpoint: " + err.Error())
		}
		rows = append(rows, PublishedRow{
			Dialect:           dialect,
			Endpoint:          endpoint,
			ProviderName:      EffectiveProviderName(vendor),
			AgenticBackend:    AgenticBackendSupported(vendor),
			AnthropicMessages: IsAnthropicMessagesVendor(vendor),
			ListsModels:       ListsModels(vendor),
			CapabilityFlags:   CapabilityFlags(dialect.Capabilities),
		})
	}
	return Published{Vendors: vendors, Rows: rows}
}

// CapabilityFlags names the bits that are true, sorted so a reordering is never read as a
// capability change.
func CapabilityFlags(c Capabilities) []string {
	out := []string{}
	for name, on := range map[string]bool{
		"thinking_blocks":             c.ThinkingBlocks,
		"reasoning_effort":            c.ReasoningEffort,
		"forced_tool_choice_unsafe":   c.ForcedToolChoiceUnsafe,
		"vision_input":                c.VisionInput,
		"strict_json_schema":          c.StrictJSONSchema,
		"rejects_dangling_tool_calls": c.RejectsDanglingToolCalls,
		"agentic_backend":             c.AgenticBackend,
		"stream_errors_in_body":       c.StreamCarriesErrorsInBody,
	} {
		if on {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// PublishJSON is the machine-readable artifact.
func PublishJSON() ([]byte, error) {
	payload, err := json.MarshalIndent(Publish(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

// PublishMarkdown is the human-readable artifact, and it is compared byte for byte: a
// "mentions every vendor" check passed while a renamed row sat unreviewed.
func PublishMarkdown() []byte {
	published := Publish()
	var b strings.Builder
	b.WriteString("# Provider 方言目录（生成物）\n\n")
	b.WriteString("由 `make generate` 从 `internal/provider` 的方言表生成，**不要手改**。\n\n")
	b.WriteString("这张表是「厂商差异」的唯一落点：能力位、重试姿态、溢出标记、鉴权头形态都在这里，\n")
	b.WriteString("调用方只允许问 `internal/provider` 的问题，不许再比较厂商字符串。\n")
	b.WriteString("`api` 决定线格式（`anthropic-messages` 与 `openai-chat` 是两套），`→ alias` 只做渠道归一化。\n\n")
	b.WriteString("| vendor | api | endpoint | 默认 base_url | context | max_tokens | 能力位 | 重试 | 溢出标记 |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range published.Rows {
		alias := ""
		if r.AliasesTo != "" {
			alias = " → " + r.AliasesTo
		}
		retry := fmt.Sprintf("%d 次 / 起 %dms / 5xx %v", r.Retry.MaxAttempts, r.Retry.BackoffStartMillis, r.Retry.RetryableOn5xx)
		fmt.Fprintf(&b, "| `%s`%s | `%s` | `%s` | `%s` | %d | %d | %s | %s | %s |\n",
			r.Vendor, alias, r.API, r.Endpoint, orNone(r.DefaultBaseURL),
			r.ContextWindow, r.MaxTokensDefault,
			orNone(strings.Join(r.CapabilityFlags, ", ")),
			retry,
			orNone(strings.Join(r.OverflowMarkers, ", ")))
	}

	b.WriteString("\n## 派生答案（改动即行为变更）\n\n")
	b.WriteString("| vendor | provider 名（归一化后） | agentic 后端 | anthropic 线格式 | 支持列模型 |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, r := range published.Rows {
		fmt.Fprintf(&b, "| `%s` | `%s` | %v | %v | %v |\n",
			r.Vendor, r.ProviderName, r.AgenticBackend, r.AnthropicMessages, r.ListsModels)
	}

	b.WriteString("\n## 成本（USD / 百万 token）\n\n")
	b.WriteString("| vendor | input | output | cache read | cache write |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, r := range published.Rows {
		fmt.Fprintf(&b, "| `%s` | %.2f | %.2f | %.2f | %.2f |\n",
			r.Vendor, r.Cost.Input, r.Cost.Output, r.Cost.CacheRead, r.Cost.CacheWrite)
	}
	return []byte(b.String())
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}
