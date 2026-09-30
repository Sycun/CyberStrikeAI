package capability

import (
	"fmt"
	"strings"
	"time"
)

// RuntimeGoInternal is a recipe whose command is an internal: handler.
const RuntimeGoInternal Runtime = "go-internal"

// CoarseFallbackPermission is the historical catch-all. It stays valid for
// mutating recipes but may never authorize a destructive one, which is what
// makes the store unable to widen a human's approved blast radius.
const CoarseFallbackPermission = "agent:local-execute"

// RecipeManifest is the neutral view of a recipe's `capability:` block. Keeping
// it here means the capability package does not depend on config.
type RecipeManifest struct {
	ID         string
	Version    string
	Class      string
	Permission string
	Approval   string
	Runtime    string
	Grants     []string
	Evidence   bool
	Timeout    time.Duration
	ToolName   string
	Title      string
	Publisher  string
}

// SpecFromRecipe turns a declared manifest into a registry entry. Anything it
// cannot enforce is a load-time error, not a runtime surprise.
func SpecFromRecipe(m RecipeManifest) (*Spec, error) {
	name := strings.TrimSpace(m.ToolName)
	if name == "" {
		return nil, fmt.Errorf("recipe manifest: tool name is required")
	}
	id := strings.TrimSpace(m.ID)
	if id == "" {
		if strings.TrimSpace(m.Publisher) == "" {
			return nil, fmt.Errorf("recipe %s: capability.id or capability.publisher is required", name)
		}
		id = fmt.Sprintf("%s.%s", strings.ToLower(m.Publisher), name)
	}
	if _, _, err := ParseID(id); err != nil {
		return nil, fmt.Errorf("recipe %s: %w", name, err)
	}

	class := Class(strings.TrimSpace(m.Class))
	if !class.valid() {
		return nil, fmt.Errorf("recipe %s: class %q must be readonly, mutating or destructive", name, m.Class)
	}

	permission := strings.TrimSpace(m.Permission)
	if permission == "" {
		return nil, fmt.Errorf("recipe %s: capability.permission is required", name)
	}
	if class == ClassDestructive && permission == CoarseFallbackPermission {
		return nil, fmt.Errorf("recipe %s: destructive capabilities may not use the %s fallback, declare a dedicated permission", name, CoarseFallbackPermission)
	}

	approval, err := parseApproval(m.Approval, class)
	if err != nil {
		return nil, fmt.Errorf("recipe %s: %w", name, err)
	}

	runtime := Runtime(strings.TrimSpace(m.Runtime))
	if runtime == "" {
		runtime = RuntimeRecipeExec
	}

	grants := make([]CapabilityGrant, 0, len(m.Grants))
	for _, g := range m.Grants {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		parsed, err := ParseGrant(g)
		if err != nil {
			return nil, fmt.Errorf("recipe %s: %w", name, err)
		}
		grants = append(grants, parsed)
	}

	spec := &Spec{
		ID: id, Version: strings.TrimSpace(m.Version), Name: name, Title: strings.TrimSpace(m.Title),
		Class: class, Runtime: runtime, Permission: permission, Approval: approval,
		Grants: grants, Evidence: m.Evidence, Timeout: m.Timeout,
		Source: "recipe", Publisher: strings.TrimSpace(m.Publisher),
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return spec, nil
}

func parseApproval(raw string, class Class) (Approval, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "inherited":
		if class == ClassDestructive {
			return ApprovalAlways, nil
		}
		return ApprovalInherited, nil
	case "always":
		if class == ClassReadonly {
			return ApprovalInherited, fmt.Errorf("readonly capabilities cannot force approval")
		}
		return ApprovalAlways, nil
	case "never":
		if class != ClassReadonly {
			return ApprovalInherited, fmt.Errorf("only readonly capabilities may skip approval")
		}
		return ApprovalNever, nil
	default:
		return ApprovalInherited, fmt.Errorf("unknown approval %q", raw)
	}
}

// RemoteSpec builds the registry entry for a tool exposed by an external MCP
// server. The publisher namespace comes from the server record so two community
// servers cannot claim the same identity.
func RemoteSpec(publisher, toolName, permission string) (*Spec, error) {
	return SpecFromRecipe(RecipeManifest{
		ID:         fmt.Sprintf("%s.%s", strings.ToLower(publisher), toolName),
		Class:      string(ClassMutating),
		Permission: permission,
		Runtime:    string(RuntimeMCPRemote),
		ToolName:   toolName,
		Grants:     []string{"mcp.invoke(remote)"},
	})
}

// Rejection records a recipe that could not become a registry entry. The tool
// stays loadable for display, but the authorizer refuses it.
type Rejection struct {
	ToolName string
	Reason   string
}
