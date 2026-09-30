package multiagent

import (
	"context"
	"fmt"

	"cyberstrike-ai/internal/security"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/schema"
)

// streamingShell is the ADK's execute-tool adapter over the process rules in
// internal/security. It exists here rather than there so the security package only ever
// speaks its own ShellEvent/ShellSink: the SDK's stream type, its chunk envelope and the
// cancel signal are this file's business.
type streamingShell struct{}

func (streamingShell) ExecuteStreaming(ctx context.Context, input *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	if input == nil || input.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	reader, writer := schema.Pipe[*filesystem.ExecuteResponse](100)
	security.RunShellStreaming(ctx, input.Command, input.RunInBackendGround, streamingShellSink{writer: writer})
	return reader, nil
}

// streamingShellSink forwards security's events onto the SDK writer. Send's bool result is
// the framework's "consumer gone" signal, which the caller uses to stop the process group.
type streamingShellSink struct {
	writer *schema.StreamWriter[*filesystem.ExecuteResponse]
}

func (s streamingShellSink) Send(event security.ShellEvent) bool {
	if event.Err != nil {
		return s.writer.Send(nil, event.Err)
	}
	return s.writer.Send(&filesystem.ExecuteResponse{
		Output:   event.Output,
		ExitCode: event.ExitCode,
	}, nil)
}

func (s streamingShellSink) Close() { s.writer.Close() }
