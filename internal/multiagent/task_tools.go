package multiagent

import (
	"context"
	"cyberstrike-ai/internal/agentruntime"
)

type taskToolsKey struct{}
type taskTools struct {
	definitions []agentruntime.Tool
	execute     agentruntime.ToolExecutor
}

// WithTaskTools adds host-owned, task-scoped memory/result tools to a single
// Agent run. Tools still pass through the standard authorization/HITL hook.
// They are not part of the global catalog and cannot leak into another run.
func WithTaskTools(ctx context.Context, definitions []agentruntime.Tool, execute agentruntime.ToolExecutor) context.Context {
	return context.WithValue(ctx, taskToolsKey{}, taskTools{append([]agentruntime.Tool(nil), definitions...), execute})
}
