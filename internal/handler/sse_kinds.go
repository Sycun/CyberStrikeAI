package handler

import (
	"errors"

	"cyberstrike-ai/internal/sse"
)

// agentSSE is the declared set of frames the conversational stream can carry.
//
// It is the gate the refactor plan asked for: before this, four closures built frames
// by hand and nothing anywhere recorded which event names exist, so a rename on one
// side of the wire was only discoverable by a user noticing a dead panel. The names
// here are checked against the ones the code actually emits
// (`TestAgentSSEKindsCoverEveryEmittedEvent`, whose truth source is an AST scan of the
// emitters) and against the names the page handles, so both directions of drift fail
// loudly.
var agentSSE = func() *sse.Registry {
	registry := sse.NewRegistry()
	for _, kind := range agentSSEKinds() {
		registry.MustRegister(kind)
	}
	return registry
}()

// agentSSEKinds describes every agent-stream event. `Payload` names the data shape so
// the catalog says something useful about a frame rather than only its name.
func agentSSEKinds() []sse.Kind {
	return []sse.Kind{
		// Transport and run lifecycle.
		{Name: "heartbeat", Description: "keeps proxies from closing an idle stream", Payload: "none"},
		{Name: "conversation", Description: "conversation identity for the run that just started", Payload: "{conversationId}"},
		{Name: "progress", Description: "a step of the run that has no more specific event", Payload: "{iteration, ...detail}"},
		{Name: "message_saved", Description: "the user or assistant message is persisted", Payload: "{messageId, conversationId}"},
		{Name: "done", Description: "the run is over; the stream ends after this frame", Payload: "{conversationId}"},
		{Name: "error", Description: "the run failed", Payload: "{errorType, ...}"},
		{Name: "cancelled", Description: "the user stopped the run", Payload: "{conversationId}"},

		// Assistant output, streamed as it is produced.
		{Name: "response", Description: "the assistant's final answer", Payload: "{content, conversationId, messageId}"},
		{Name: "response_start", Description: "the main answer began streaming", Payload: "{messageId}"},
		{Name: "response_delta", Description: "a chunk of the answer being streamed", Payload: "{delta}"},
		{Name: "model_output_rejected", Description: "the model's output was refused and is being retried", Payload: "{reason}"},
		{Name: "thinking", Description: "a reasoning summary of the current step", Payload: "{content}"},
		{Name: "thinking_stream_start", Description: "reasoning output began streaming", Payload: "{messageId}"},
		{Name: "thinking_stream_delta", Description: "a chunk of streaming reasoning", Payload: "{delta}"},
		{Name: "thinking_stream_end", Description: "reasoning output finished streaming", Payload: "{messageId}"},
		{Name: "reasoning_chain", Description: "the reasoning chain for the current step", Payload: "{steps}"},
		{Name: "reasoning_chain_stream_start", Description: "reasoning chain output began streaming", Payload: "{messageId}"},
		{Name: "reasoning_chain_stream_delta", Description: "a chunk of streaming reasoning chain", Payload: "{delta}"},
		{Name: "reasoning_chain_stream_end", Description: "reasoning chain output finished streaming", Payload: "{messageId}"},

		// Tool execution.
		{Name: "tool_calls_detected", Description: "the model asked for tools; the batch is known", Payload: "{count}"},
		{Name: "tool_call", Description: "a tool call started", Payload: "{toolName, toolCallId, arguments}"},
		{Name: "tool_result", Description: "a tool call finished", Payload: "{toolName, toolCallId, result}"},
		{Name: "tool_result_delta", Description: "partial tool output; not shown live in the detail pane", Payload: "{toolCallId, delta}"},

		// Iteration control.
		{Name: "iteration", Description: "a main-loop iteration began", Payload: "{iteration}"},
		{Name: "iteration_limit_reached", Description: "the iteration budget ran out before a final answer", Payload: "{limit}"},

		// HITL approval.
		{Name: "hitl_interrupt", Description: "a tool call is waiting for approval", Payload: "{interruptId, toolName, payload}"},
		{Name: "hitl_resumed", Description: "an approval was answered; the tool call continues", Payload: "{interruptId, decision}"},
		{Name: "hitl_rejected", Description: "an approval was refused, so the tool call did not run", Payload: "{interruptId}"},
		{Name: "hitl_audit_agent_started", Description: "the audit agent began reviewing this request", Payload: "{interruptId, toolName}"},
		{Name: "hitl_audit_agent", Description: "the audit agent returned its verdict", Payload: "{interruptId, decision, comment}"},

		// Finalization of the run.
		{Name: "finalization_check", Description: "the answer is being judged for completeness before it is stored", Payload: "{status, reason}"},
		{Name: "finalization_auto_continue", Description: "finalization continued the run automatically", Payload: "{reason}"},
		{Name: "finalization_pending_tools_cancelled", Description: "tools still running were cancelled at the end of the loop", Payload: "{count}"},
		{Name: "user_interrupt_continue", Description: "a user interrupt is folded into the continuing run", Payload: "{summary}"},

		// Eino runtime diagnostics and sub-agent output.
		{Name: "eino_empty_response_continue", Description: "the model returned nothing usable; context is reloaded to retry", Payload: "{attempt}"},
		{Name: "eino_model_retry", Description: "a model call is being retried", Payload: "{attempt, model}"},
		{Name: "eino_model_failover", Description: "a model call failed over to another model", Payload: "{from, to}"},
		{Name: "eino_run_retry", Description: "the whole run is being retried after a transient failure", Payload: "{attempt}"},
		{Name: "eino_context_overflow_retry", Description: "the prompt overflowed the context window and is compacted", Payload: "{tokens, limit}"},
		{Name: "eino_pending_orphaned", Description: "tool calls were left pending when the run ended", Payload: "{count}"},
		{Name: "eino_stream_error", Description: "the model stream broke mid-flight", Payload: "{message}"},
		{Name: "eino_usage_summary", Description: "token and cost usage for the finished run", Payload: "{promptTokens, completionTokens}"},
		{Name: "eino_agent_reply", Description: "a sub-agent's reply", Payload: "{agent, content}"},
		{Name: "eino_agent_reply_stream_start", Description: "a sub-agent began replying", Payload: "{agent}"},
		{Name: "eino_agent_reply_stream_delta", Description: "a chunk of a sub-agent's reply", Payload: "{delta}"},
		{Name: "eino_agent_reply_stream_end", Description: "a sub-agent finished replying", Payload: "{agent}"},
		{Name: "eino_trace_start", Description: "an Eino callback trace opened", Payload: "{runId}"},
		{Name: "eino_trace_run", Description: "progress inside an Eino callback trace", Payload: "{node}"},
		{Name: "eino_trace_end", Description: "an Eino callback trace closed", Payload: "{runId}"},
		{Name: "eino_trace_error", Description: "an Eino callback trace failed", Payload: "{message}"},

		// Workflow runs reported through the same stream.
		{Name: "workflow_branch_taken", Description: "a conditional node took this branch", Payload: "{workflowRunId, nodeId, branchLabel, targetId, matched}"},
		{Name: "workflow_branch_skipped", Description: "a conditional node skipped this branch", Payload: "{workflowRunId, nodeId, branchLabel, targetId, matched}"},
		{Name: "workflow_start", Description: "a workflow run began", Payload: "{workflowId, name}"},
		{Name: "workflow_node_start", Description: "a workflow node began", Payload: "{nodeId, name}"},
		{Name: "workflow_node_result", Description: "a workflow node produced its result", Payload: "{nodeId, output}"},
		{Name: "workflow_tool_start", Description: "a workflow node invoked a tool", Payload: "{toolName}"},
		{Name: "workflow_agent_output", Description: "a workflow agent node produced output", Payload: "{content}"},
		{Name: "workflow_hitl_waiting", Description: "a workflow node is waiting for human confirmation", Payload: "{label, arguments}"},
		{Name: "workflow_hitl_checkpoint", Description: "a workflow approval checkpoint was recorded", Payload: "{nodeId}"},
		{Name: "workflow_hitl_resumed", Description: "a workflow approval was answered and the run continued", Payload: "{nodeId, decision}"},
		{Name: "workflow_hitl_rejected", Description: "a workflow approval was refused", Payload: "{nodeId}"},
		{Name: "workflow_paused", Description: "a workflow run paused", Payload: "{nodeId}"},
		{Name: "workflow_done", Description: "a workflow run finished", Payload: "{workflowId, status}"},
	}
}

// AgentSSEKinds exposes the declared set for the drift gate.
func AgentSSEKinds() []sse.Kind { return agentSSE.Kinds() }

// sseBusRecorder encodes the frames mirrored onto the task event bus, so a client
// that reconnects to a run in progress replays the same bytes. It validates against
// the same registry as the live stream: a bus must never carry a frame the stream
// itself would have refused.
var sseBusRecorder = sse.NewRecorder(agentSSE)

// mirrorLine encodes a frame for the replay bus. An unregistered name is a
// programming error and is dropped; an unencodable payload substitutes the error
// frame the handlers used to write by hand, so a bad payload never becomes a
// half-written line on the bus.
func mirrorLine(name, message string, data any) ([]byte, bool) {
	line, err := sseBusRecorder.Line(name, message, data)
	if err == nil {
		return line, true
	}
	if errors.Is(err, sse.ErrUnregisteredKind) {
		return nil, false
	}
	fallback, ferr := sseBusRecorder.Line("error", "marshal failed", nil)
	if ferr != nil {
		return nil, false
	}
	return fallback, true
}
