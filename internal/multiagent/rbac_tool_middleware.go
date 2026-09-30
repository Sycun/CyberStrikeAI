package multiagent

import (
	"context"
	"strings"

	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/capability"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// localToolPermissionDenied asks the capability registry which permission an
// agent-local tool needs, instead of matching the tool name against a second
// hand-kept list. A tool the registry does not know is left to the execution-path
// policy, which fails closed.
func localToolPermissionDenied(ctx context.Context, name string) bool {
	spec, err := capability.Global().Lookup(strings.ToLower(strings.TrimSpace(name)))
	if err != nil || spec.Source != capability.SourceAgentLocal {
		return false
	}
	principal, ok := authctx.PrincipalFromContext(ctx)
	if !ok {
		return true
	}
	return !principal.HasPermission(spec.Permission)
}

func localToolRBACMiddleware() compose.ToolMiddleware {
	denied := "Permission denied: the capability registry requires agent:local-execute for local filesystem and shell tools."
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				if input != nil && localToolPermissionDenied(ctx, input.Name) {
					return &compose.ToolOutput{Result: denied}, nil
				}
				return next(ctx, input)
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				if input != nil && localToolPermissionDenied(ctx, input.Name) {
					return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{denied})}, nil
				}
				return next(ctx, input)
			}
		},
	}
}
