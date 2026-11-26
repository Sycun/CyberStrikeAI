package agentfinalizer

import (
	"testing"

	"cyberstrike-ai/internal/multiagent"
)

func TestRuntimeIterationLimitCannotBecomeCompleted(t *testing.T) {
	result := &multiagent.RunResult{Response: "Partial report", RuntimeStatus: "blocked", RuntimeCompletionReason: "iteration_limit"}
	for i := 0; i < 2; i++ {
		decision := FromRunResult(nil, result, Input{})
		if decision.Finalizable || decision.Status != "blocked" || decision.CompletionReason != "iteration_limit" {
			t.Fatalf("unexpected delivery: %+v", decision)
		}
	}
}

func TestTruncatedOutputRetainsReasonWithoutFinalText(t *testing.T) {
	result := &multiagent.RunResult{RuntimeStatus: StatusBlocked, RuntimeCompletionReason: multiagent.ReasonModelOutputTruncated}
	decision := FromRunResult(nil, result, Input{})
	if decision.Finalized || decision.Finalizable || decision.Status != StatusBlocked || decision.CompletionReason != multiagent.ReasonModelOutputTruncated {
		t.Fatalf("unexpected delivery: %+v", decision)
	}
}

func TestTruncatedOutputKeepsCandidateText(t *testing.T) {
	result := &multiagent.RunResult{Response: "已生成的前半段", RuntimeStatus: StatusBlocked, RuntimeCompletionReason: multiagent.ReasonModelOutputTruncated}
	decision := FromRunResult(nil, result, Input{})
	if decision.Finalizable || decision.Finalized || decision.FinalText != "已生成的前半段" || decision.CompletionReason != multiagent.ReasonModelOutputTruncated {
		t.Fatalf("unexpected delivery: %+v", decision)
	}
}
