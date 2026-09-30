package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Role is the dialect-neutral conversation role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is one function call the model asked for.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Message is one turn. Text and ToolCalls are both allowed on an assistant turn, which
// is what the two dialect families represent differently and what a handoff must not lose.
type Message struct {
	Role       Role
	Text       string
	ToolCalls  []ToolCall
	ToolCallID string
	// Name is only meaningful for tool results in the chat dialect; the messages
	// dialect folds the identity into ToolCallID.
	Name string
}

// Conversation is an ordered transcript.
type Conversation []Message

// Validate applies the dialect's structural requirements to a transcript before it is
// sent. A dangling tool call is the classic failure: one vendor rejects the request,
// another silently answers something unrelated, and the run looks healthy either way.
func (d Dialect) Validate(conv Conversation) error {
	open := map[string]bool{}
	for index, message := range conv {
		switch message.Role {
		case RoleAssistant:
			for _, call := range message.ToolCalls {
				if strings.TrimSpace(call.ID) == "" {
					return fmt.Errorf("provider %s: assistant turn %d has a tool call without an id", d.Vendor, index)
				}
				open[call.ID] = true
			}
		case RoleTool:
			if strings.TrimSpace(message.ToolCallID) == "" {
				if !d.Capabilities.RejectsDanglingToolCalls {
					continue
				}
				return fmt.Errorf("provider %s: tool result at turn %d has no tool_call_id", d.Vendor, index)
			}
			delete(open, message.ToolCallID)
		}
	}
	if d.Capabilities.RejectsDanglingToolCalls && len(open) > 0 {
		ids := make([]string, 0, len(open))
		for id := range open {
			ids = append(ids, id)
		}
		return fmt.Errorf("provider %s: %d tool call(s) have no result (%s)", d.Vendor, len(ids), strings.Join(ids, ","))
	}
	return nil
}

// RenderRequest builds the vendor payload. Keeping this here means the capability
// matrix and the body shape can never disagree, which is how a reasoning flag ends up
// sent to a vendor that ignores it.
func (d Dialect) RenderRequest(model string, conv Conversation, maxTokens int, options map[string]any) (map[string]any, error) {
	if err := d.Validate(conv); err != nil {
		return nil, err
	}
	if maxTokens <= 0 {
		maxTokens = d.MaxTokensDefault
	}
	payload := map[string]any{"model": model, "stream": boolValue(options, "stream")}

	switch d.API {
	case APIAnthropicMessages:
		system := ""
		var messages []map[string]any
		for _, message := range conv {
			switch message.Role {
			case RoleSystem:
				system = strings.TrimSpace(system + "\n" + message.Text)
			case RoleAssistant:
				blocks := []map[string]any{}
				if message.Text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": message.Text})
				}
				for _, call := range message.ToolCalls {
					blocks = append(blocks, map[string]any{
						"type": "tool_use", "id": call.ID, "name": call.Name, "input": decodeArguments(call.Arguments),
					})
				}
				messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
			case RoleTool:
				messages = append(messages, map[string]any{
					"role": "user",
					"content": []map[string]any{{
						"type": "tool_result", "tool_use_id": message.ToolCallID, "content": message.Text,
					}},
				})
			default:
				messages = append(messages, map[string]any{"role": "user", "content": message.Text})
			}
		}
		payload["messages"] = messages
		payload["max_tokens"] = maxTokens
		if system != "" {
			payload["system"] = strings.TrimSpace(system)
		}
		if d.Capabilities.ThinkingBlocks {
			if budget := intValue(options, "thinking_budget_tokens"); budget > 0 {
				payload["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
			}
		}
		return payload, nil

	default:
		var messages []map[string]any
		for _, message := range conv {
			switch message.Role {
			case RoleSystem:
				messages = append(messages, map[string]any{"role": "system", "content": message.Text})
			case RoleAssistant:
				entry := map[string]any{"role": "assistant", "content": nullableText(message.Text)}
				if len(message.ToolCalls) > 0 {
					calls := make([]map[string]any, 0, len(message.ToolCalls))
					for _, call := range message.ToolCalls {
						calls = append(calls, map[string]any{
							"id": call.ID, "type": "function",
							"function": map[string]any{"name": call.Name, "arguments": call.Arguments},
						})
					}
					entry["tool_calls"] = calls
				}
				messages = append(messages, entry)
			case RoleTool:
				messages = append(messages, map[string]any{
					"role": "tool", "tool_call_id": message.ToolCallID, "content": message.Text, "name": message.Name,
				})
			default:
				messages = append(messages, map[string]any{"role": "user", "content": message.Text})
			}
		}
		payload["messages"] = messages
		payload["max_tokens"] = maxTokens
		if d.Capabilities.ReasoningEffort {
			if effort := stringValue(options, "reasoning_effort"); effort != "" {
				payload["reasoning_effort"] = effort
			}
		}
		if choice := stringValue(options, "tool_choice"); choice != "" {
			// Gateways that reject a forced choice while reasoning is on degrade to auto
			// here instead of each caller re-discovering the same 400.
			if d.Capabilities.ForcedToolChoiceUnsafe && choice != "auto" &&
				stringValue(options, "reasoning_effort") != "" {
				choice = "auto"
			}
			payload["tool_choice"] = choice
		}
		return payload, nil
	}
}

// ParseRequest reads a vendor payload back into the neutral transcript. It exists so
// cross-provider handoff can be tested for real: a request rendered for A must survive
// reinterpretation and re-rendering for B without losing tool identity or ordering.
func (d Dialect) ParseRequest(payload map[string]any) (Conversation, string, error) {
	conv := Conversation{}
	switch d.API {
	case APIAnthropicMessages:
		if system, ok := payload["system"].(string); ok && strings.TrimSpace(system) != "" {
			conv = append(conv, Message{Role: RoleSystem, Text: system})
		}
		raw := asMapSlice(payload["messages"])
		for _, message := range raw {
			role, _ := message["role"].(string)
			blocks := asMapSlice(message["content"])
			if len(blocks) == 0 {
				text, _ := message["content"].(string)
				conv = append(conv, Message{Role: Role(role), Text: text})
				continue
			}
			var text strings.Builder
			var calls []ToolCall
			var results []Message
			for _, block := range blocks {
				switch block["type"] {
				case "text":
					text.WriteString(stringOf(block["text"]))
				case "tool_use":
					encoded, _ := json.Marshal(block["input"])
					calls = append(calls, ToolCall{ID: stringOf(block["id"]), Name: stringOf(block["name"]), Arguments: string(encoded)})
				case "tool_result":
					results = append(results, Message{Role: RoleTool, ToolCallID: stringOf(block["tool_use_id"]), Text: stringOf(block["content"])})
				}
			}
			if len(results) > 0 {
				conv = append(conv, results...)
				continue
			}
			if len(calls) > 0 {
				conv = append(conv, Message{Role: RoleAssistant, Text: text.String(), ToolCalls: calls})
				continue
			}
			conv = append(conv, Message{Role: Role(role), Text: text.String()})
		}
		return conv, stringOf(payload["model"]), nil

	default:
		raw := asMapSlice(payload["messages"])
		for _, message := range raw {
			role, _ := message["role"].(string)
			switch role {
			case "tool":
				conv = append(conv, Message{Role: RoleTool, ToolCallID: stringOf(message["tool_call_id"]), Text: stringOf(message["content"]), Name: stringOf(message["name"])})
			case "assistant":
				text := assistantText(message["content"])
				var calls []ToolCall
				for _, call := range asMapSlice(message["tool_calls"]) {
					fn := firstMap(call["function"])
					calls = append(calls, ToolCall{ID: stringOf(call["id"]), Name: stringOf(fn["name"]), Arguments: stringOf(fn["arguments"])})
				}
				conv = append(conv, Message{Role: RoleAssistant, Text: text, ToolCalls: calls})
			default:
				conv = append(conv, Message{Role: Role(role), Text: stringOf(message["content"])})
			}
		}
		return conv, stringOf(payload["model"]), nil
	}
}

func assistantText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func decodeArguments(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return map[string]any{"_raw": trimmed}
	}
	return decoded
}

func stringOf(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func boolValue(options map[string]any, key string) bool {
	value, _ := options[key].(bool)
	return value
}

func intValue(options map[string]any, key string) int {
	switch typed := options[key].(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	}
	return 0
}

func stringValue(options map[string]any, key string) string {
	value, _ := options[key].(string)
	return strings.TrimSpace(value)
}

// NormalizeAssistantDelta merges one streamed chunk into an accumulated reply. The two
// dialects split arguments across deltas differently, so this is where "same behaviour"
// is actually defined and tested.
func NormalizeAssistantDelta(text *strings.Builder, calls map[string]*ToolCall, chunk Message) {
	if chunk.Text != "" {
		text.WriteString(chunk.Text)
	}
	for _, call := range chunk.ToolCalls {
		existing, ok := calls[call.ID]
		if !ok {
			copied := call
			calls[call.ID] = &copied
			continue
		}
		if call.Name != "" && call.Name != existing.Name {
			existing.Name = call.Name
		}
		existing.Arguments += call.Arguments
	}
}

// MarshalRoundTrip is the surrogate-safety primitive: a model reply containing
// astral-plane characters must survive JSON encoding unchanged.
func MarshalRoundTrip(text string) (string, error) {
	encoded, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", err
	}
	var decoded map[string]string
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return "", err
	}
	return decoded["text"], nil
}

// asMapSlice reads a JSON array of objects. It has to tolerate []any because a
// payload that has actually been through encoding/json decodes to that type, and a
// reader that only accepts []map[string]any silently returns nothing on real input.
func asMapSlice(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if entry, ok := item.(map[string]any); ok {
				out = append(out, entry)
			}
		}
		return out
	}
	return nil
}

func firstMap(value any) map[string]any {
	if entry, ok := value.(map[string]any); ok {
		return entry
	}
	return map[string]any{}
}
