package manifest

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

const MaxDocumentBytes = 128 << 10

//go:embed schemas/*.json
var schemas embed.FS

var declarationSchema = sync.OnceValues(func() (*jsonschema.Resolved, error) {
	return resolveSchema("manifest-v1")
})
var installPlanSchema = sync.OnceValues(func() (*jsonschema.Resolved, error) {
	return resolveSchema("install-plan-v1")
})

// Schema returns a copy of an embedded schema without resolving remote references.
func Schema(name string) ([]byte, error) {
	switch name {
	case "manifest-v1", "install-plan-v1":
		return schemas.ReadFile("schemas/" + name + ".json")
	default:
		return nil, fmt.Errorf("unknown schema")
	}
}

func resolveSchema(name string) (*jsonschema.Resolved, error) {
	raw, err := Schema(name)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	return schema.Resolve(nil)
}

func Digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func decode(data []byte, schema func() (*jsonschema.Resolved, error), result any) ([]byte, error) {
	if len(data) > MaxDocumentBytes {
		return nil, fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
	}
	parser := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := parser.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode document: %w", err)
	}
	var trailing yaml.Node
	if err := parser.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("expected a single document")
	}
	if len(document.Content) != 1 {
		return nil, fmt.Errorf("expected a document value")
	}
	nodes := 4096
	value, err := jsonValue(document.Content[0], 0, &nodes)
	if err != nil {
		return nil, err
	}
	resolved, err := schema()
	if err != nil {
		return nil, fmt.Errorf("resolve embedded schema: %w", err)
	}
	if err := resolved.Validate(value); err != nil {
		return nil, fmt.Errorf("schema validation: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(canonical, result); err != nil {
		return nil, err
	}
	return canonical, nil
}

func jsonValue(node *yaml.Node, depth int, remaining *int) (any, error) {
	*remaining -= 1
	if depth > 32 || *remaining < 0 {
		return nil, fmt.Errorf("document nesting or entry limit exceeded")
	}
	if node.Anchor != "" || node.Kind == yaml.AliasNode || node.Style&yaml.TaggedStyle != 0 {
		return nil, fmt.Errorf("aliases, anchors and explicit tags are unsupported at line %d", node.Line)
	}
	switch node.Kind {
	case yaml.MappingNode:
		object := make(map[string]any, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" || key.Style&yaml.TaggedStyle != 0 || key.Anchor != "" || key.Value == "<<" {
				return nil, fmt.Errorf("expected a string mapping key at line %d", key.Line)
			}
			if _, exists := object[key.Value]; exists {
				return nil, fmt.Errorf("duplicate mapping key at line %d", key.Line)
			}
			value, err := jsonValue(node.Content[i+1], depth+1, remaining)
			if err != nil {
				return nil, err
			}
			object[key.Value] = value
		}
		return object, nil
	case yaml.SequenceNode:
		values := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := jsonValue(child, depth+1, remaining)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	case yaml.ScalarNode:
		switch node.ShortTag() {
		case "!!str", "!!bool", "!!int", "!!float", "!!null":
			var value any
			if err := node.Decode(&value); err != nil {
				return nil, err
			}
			return value, nil
		}
	}
	return nil, fmt.Errorf("unsupported value at line %d", node.Line)
}
