package app

import (
	"fmt"
	"strings"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/mcp"

	"go.uber.org/zap"
)

// Remote MCP tools used to have no identity: every tool of every configured server was admitted
// by one namespace-level policy (`mcp:external:execute`), so "this particular remote tool" was not
// something a rule could name, an approver could see, or a revocation could target.
//
// The manager now reports each server's actual tool inventory, and this file turns it into
// capability specs in capability.LayerRemote - one subset per server, so reconnecting or removing
// a server replaces exactly its own entries.

// remoteIDPrefix marks a remote tool identity. core.* is reserved for the shipped binary and
// publisher.* for community recipes; remote.* is the third owner: an external server at runtime.
const remoteIDPrefix = "remote."

// remoteToolName is the wire form the executor authorizes: "<server>::<tool>".
func remoteToolName(serverName, toolName string) string {
	return serverName + "::" + toolName
}

// remoteToolID is the canonical identity for one remote tool.
func remoteToolID(serverName, toolName string) string {
	return remoteIDPrefix + serverName + "." + toolName
}

// remoteToolInventoryChanged is the ExternalMCPManager observer wiring. Errors are logged rather
// than propagated: the inventory event arrives from a background refresh, and a rejected spec
// must not look like a failed tool call.
func remoteToolInventoryChanged(logger *zap.Logger) func(string, []mcp.Tool) {
	return func(serverName string, tools []mcp.Tool) {
		if err := applyRemoteToolInventory(serverName, tools); err != nil {
			if logger != nil {
				logger.Error("登记外部 MCP 工具能力失败", zap.String("server", serverName), zap.Error(err))
			}
			return
		}
		if logger != nil {
			logger.Info("外部 MCP 工具能力已登记",
				zap.String("server", serverName), zap.Int("tools", len(tools)))
		}
	}
}

// applyRemoteToolInventory replaces one server's remote capability entries with its current tool
// list. An empty list (server stopped, removed, or cache invalidated) drops them.
func applyRemoteToolInventory(serverName string, tools []mcp.Tool) error {
	name := strings.TrimSpace(serverName)
	if name == "" {
		return fmt.Errorf("remote capability: empty server name")
	}
	registry := capability.Global()
	if err := capability.GlobalErr(); err != nil {
		return fmt.Errorf("remote capability: builtin policy install failed: %w", err)
	}
	if len(tools) == 0 {
		registry.UnregisterSubset(capability.LayerRemote, name)
		return nil
	}

	specs := make([]*capability.Spec, 0, len(tools))
	seen := map[string]bool{}
	for _, tool := range tools {
		toolName := strings.TrimSpace(tool.Name)
		if toolName == "" {
			continue
		}
		if seen[toolName] {
			// A server that lists the same tool twice would otherwise register the same
			// identity twice and the second would silently win.
			continue
		}
		seen[toolName] = true
		specs = append(specs, &capability.Spec{
			ID:          remoteToolID(name, toolName),
			Name:        remoteToolName(name, toolName),
			Title:       remoteToolTitle(name, toolName, tool),
			Description: strings.TrimSpace(tool.Description),
			// Mutating, not readonly: nothing here can tell whether a remote tool writes. The
			// approval policy is inherited rather than pinned, so this step does not change
			// what is allowed today - it makes each tool name addressable by a rule.
			Class:      capability.ClassMutating,
			Runtime:    capability.RuntimeMCPRemote,
			Permission: externalMCPNamespacePermission,
			Approval:   capability.ApprovalInherited,
			// Same scope floor as the namespace policy. Dropping it here would quietly
			// loosen what a per-server-scoped principal may call.
			Check:  capability.GlobalScopeRequired(externalMCPNamespacePermission),
			Source: "external-mcp",
		})
	}
	if len(specs) == 0 {
		registry.UnregisterSubset(capability.LayerRemote, name)
		return nil
	}
	return registry.RegisterSubset(capability.LayerRemote, name, specs)
}

// remoteToolTitle prefers the short description the server supplied, because that is what an
// approver or an audit row should show.
func remoteToolTitle(serverName, toolName string, tool mcp.Tool) string {
	if short := strings.TrimSpace(tool.ShortDescription); short != "" {
		return short
	}
	return serverName + " / " + toolName
}

// lookupRemoteToolSpec resolves a remote tool by its wire name, reporting whether it has an
// identity of its own.
func lookupRemoteToolSpec(toolName string) (*capability.Spec, bool) {
	spec, err := capability.Global().Lookup(strings.TrimSpace(toolName))
	if err != nil || spec == nil {
		return nil, false
	}
	if spec.Runtime != capability.RuntimeMCPRemote || !strings.HasPrefix(spec.ID, remoteIDPrefix) {
		return nil, false
	}
	return spec, true
}
