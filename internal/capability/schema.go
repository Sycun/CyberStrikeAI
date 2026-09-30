package capability

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Param is the manifest's view of one argument. It is deliberately not the YAML
// struct: the capability package must stay a leaf so nothing can grow a second
// policy implementation next to the schema.
type Param struct {
	Name        string
	Type        string // string | int | bool | array | number
	Description string
	Required    bool
	Default     any
	Enum        []string
	ItemType    string
}

// JSONSchema renders the parameter list as JSON Schema. The same document drives
// the generated form, server-side argument validation, the LLM tool schema and
// the OpenAPI fragment, which is what replaces today's five hand-kept copies.
func JSONSchema(params []Param) json.RawMessage {
	if len(params) == 0 {
		return nil
	}
	properties := map[string]any{}
	required := make([]string, 0, len(params))
	for _, p := range params {
		if p.Name == "" {
			continue
		}
		schema := map[string]any{"type": jsonType(p.Type)}
		if p.Description != "" {
			schema["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			schema["enum"] = p.Enum
		}
		if p.Default != nil {
			schema["default"] = p.Default
		}
		if jsonType(p.Type) == "array" {
			schema["items"] = map[string]any{"type": jsonType(p.ItemType)}
		}
		properties[p.Name] = schema
		if p.Required {
			required = append(required, p.Name)
		}
	}
	out := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": true,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return data
}

func jsonType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "int", "integer", "number", "float", "double":
		return "number"
	case "bool", "boolean":
		return "boolean"
	case "array", "list":
		return "array"
	case "":
		return "string"
	default:
		return "string"
	}
}

// ValidateArgs enforces the required/enum half of a schema before execution.
// Returning an error here means the call is malformed, not unauthorized, so the
// model gets an actionable message instead of a permission surprise.
func ValidateArgs(schema json.RawMessage, args map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	var doc struct {
		Required []string `json:"required"`
		Props    map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		return fmt.Errorf("capability argument schema is unreadable: %w", err)
	}
	for _, name := range doc.Required {
		value, ok := args[name]
		if !ok || value == nil {
			return fmt.Errorf("missing required argument %s", name)
		}
		if s, isString := value.(string); isString && strings.TrimSpace(s) == "" {
			return fmt.Errorf("missing required argument %s", name)
		}
		if list, isList := value.([]any); isList && len(list) == 0 {
			return fmt.Errorf("argument %s must not be empty", name)
		}
	}
	for name, prop := range doc.Props {
		value, ok := args[name]
		if !ok || value == nil || len(prop.Enum) == 0 {
			continue
		}
		text, isString := value.(string)
		if !isString {
			continue
		}
		allowed := false
		for _, candidate := range prop.Enum {
			if candidate == text {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("argument %s=%q is not one of %s", name, text, strings.Join(prop.Enum, ", "))
		}
	}
	return nil
}
