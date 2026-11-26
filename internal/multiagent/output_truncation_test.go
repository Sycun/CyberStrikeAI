package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/config"
)

func TestOutputTruncationContinueMaxAttemptsFromConfig(t *testing.T) {
	if got := OutputTruncationContinueMaxAttemptsFromConfig(nil); got != defaultOutputTruncationContinueMaxAttempts {
		t.Fatalf("default: got %d", got)
	}
	if got := OutputTruncationContinueMaxAttemptsFromConfig(&config.AgentTuningConfig{}); got != 2 {
		t.Fatalf("zero: got %d", got)
	}
	if got := OutputTruncationContinueMaxAttemptsFromConfig(&config.AgentTuningConfig{OutputTruncationContinueMaxAttempts: -1}); got != 0 {
		t.Fatalf("disabled: got %d", got)
	}
	if got := OutputTruncationContinueMaxAttemptsFromConfig(&config.AgentTuningConfig{OutputTruncationContinueMaxAttempts: 3}); got != 3 {
		t.Fatalf("custom: got %d", got)
	}
	if got := OutputTruncationContinueMaxAttemptsFromConfig(&config.AgentTuningConfig{OutputTruncationContinueMaxAttempts: 99}); got != outputTruncationContinueAttemptCap {
		t.Fatalf("cap: got %d", got)
	}
}

func TestStitchOutputSegmentsJoinsOverlapAndWhitespace(t *testing.T) {
	if got := stitchOutputSegments("Alpha-", "Beta."); got != "Alpha-Beta." {
		t.Fatalf("direct join: %q", got)
	}
	if got := stitchOutputSegments("Hello wor", "world"); got != "Hello world" {
		t.Fatalf("overlap: %q", got)
	}
	if got := stitchOutputSegments("First,", "First,"); got != "First," {
		t.Fatalf("duplicate: %q", got)
	}
	if got := stitchOutputSegments("段落结束\n\n", "\n\n下一段"); got != "段落结束\n\n下一段" {
		t.Fatalf("blank lines: %q", got)
	}
	if got := stitchOutputSegments("这里是已经写完的前半段内容", "前半段内容以及后续"); got != "这里是已经写完的前半段内容以及后续" {
		t.Fatalf("rune overlap: %q", got)
	}
	if got := stitchOutputSegments("Hello ", " world"); got != "Hello world" {
		t.Fatalf("space: %q", got)
	}
}

func TestContinueAfterOutputTruncationStitchesTextAndExecutionIDs(t *testing.T) {
	initial := &RunResult{
		RuntimeStatus:           "blocked",
		RuntimeCompletionReason: ReasonModelOutputTruncated,
		Status:                  "output_truncated",
		Response:                "Alpha-",
		LastAgentTraceOutput:    "Alpha-",
		LastAgentTraceInput:     `[{"role":"user","content":"write"}]`,
		MCPExecutionIDs:         []string{"exec-1"},
	}
	calls := 0
	var sawFragment bool
	var events []int
	out, err := continueAfterOutputTruncation(context.Background(), 2, initial, func(_ context.Context, attempt int, history, instruction string) (*RunResult, error) {
		calls++
		if attempt != 1 {
			t.Fatalf("attempt = %d", attempt)
		}
		if !strings.Contains(history, "Alpha-") {
			t.Fatalf("history lost the truncated assistant: %s", history)
		}
		sawFragment = true
		if !strings.Contains(instruction, outputTruncationContinueMarker) {
			t.Fatalf("instruction = %q", instruction)
		}
		historyBody, err := json.Marshal([]map[string]string{
			{"role": "user", "content": "write"},
			{"role": "assistant", "content": "Alpha-", "reasoning_content": "think-1"},
			{"role": "user", "content": instruction},
			{"role": "assistant", "content": "Beta.", "reasoning_content": "think-2"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return &RunResult{
			RuntimeStatus:        "completed",
			Status:               "completed",
			Response:             "Beta.",
			LastAgentTraceOutput: "Beta.",
			LastAgentTraceInput:  string(historyBody),
			MCPExecutionIDs:      []string{"exec-2"},
		}, nil
	}, func(attempt, maxAttempts int) {
		events = append(events, attempt)
		if maxAttempts != 2 {
			t.Fatalf("max = %d", maxAttempts)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawFragment || calls != 1 || len(events) != 1 {
		t.Fatalf("calls=%d events=%v saw=%v", calls, events, sawFragment)
	}
	if out.RuntimeStatus != "completed" || out.Response != "Alpha-Beta." {
		t.Fatalf("stitched result: %+v", out)
	}
	if strings.Join(out.MCPExecutionIDs, ",") != "exec-1,exec-2" {
		t.Fatalf("ids: %#v", out.MCPExecutionIDs)
	}
	if out.LastAgentTraceOutput != "Beta." {
		t.Fatalf("trace output should stay the last segment: %q", out.LastAgentTraceOutput)
	}
	if !strings.Contains(out.LastAgentTraceInput, "think-1") || !strings.Contains(out.LastAgentTraceInput, "think-2") {
		t.Fatalf("reasoning was dropped: %s", out.LastAgentTraceInput)
	}
	parsed, err := agent.ParseTraceMessages(out.LastAgentTraceInput)
	if err != nil {
		t.Fatal(err)
	}
	var prompt agent.ChatMessage
	found := false
	for _, msg := range parsed {
		if strings.Contains(msg.Content, outputTruncationContinueMarker) {
			prompt = msg
			found = true
		}
	}
	if !found || prompt.GeneratedContextKind != agent.OutputTruncationContinueContext {
		t.Fatalf("continuation prompt was not marked generated: %+v", parsed)
	}
	provider, err := piHistoryJSON(parsed)
	if err != nil {
		t.Fatal(err)
	}
	var replayed []agent.ChatMessage
	if err := jsonUnmarshalHistory(provider, &replayed); err != nil {
		t.Fatal(err)
	}
	for _, msg := range replayed {
		if msg.Role == "user" && strings.Contains(msg.Content, outputTruncationContinueMarker) {
			t.Fatalf("continuation prompt replayed as a user turn: %+v", replayed)
		}
	}
}

func TestContinueAfterOutputTruncationExhaustedKeepsPartial(t *testing.T) {
	initial := &RunResult{
		RuntimeStatus:           "blocked",
		RuntimeCompletionReason: ReasonModelOutputTruncated,
		Response:                "这里是已经写完的前半段内容",
		MCPExecutionIDs:         []string{"exec-1"},
		LastAgentTraceInput:     `[{"role":"user","content":"write"}]`,
	}
	calls := 0
	out, err := continueAfterOutputTruncation(context.Background(), 1, initial, func(context.Context, int, string, string) (*RunResult, error) {
		calls++
		return &RunResult{
			RuntimeStatus:           "blocked",
			RuntimeCompletionReason: ReasonModelOutputTruncated,
			Status:                  "output_truncated",
			Response:                "前半段内容以及后续",
			MCPExecutionIDs:         []string{"exec-2"},
			LastAgentTraceInput:     `[{"role":"assistant","content":"前半段内容以及后续"}]`,
		}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if out.RuntimeStatus != "blocked" || out.RuntimeCompletionReason != ReasonModelOutputTruncated {
		t.Fatalf("status: %+v", out)
	}
	if out.Response != "这里是已经写完的前半段内容以及后续" {
		t.Fatalf("response = %q", out.Response)
	}
	if strings.Join(out.MCPExecutionIDs, ",") != "exec-1,exec-2" {
		t.Fatalf("ids: %#v", out.MCPExecutionIDs)
	}
	if !strings.Contains(out.LastAgentTraceInput, "这里是已经写完的前半段内容") {
		t.Fatalf("degraded trace lost the first fragment: %s", out.LastAgentTraceInput)
	}
}

func TestContinueAfterOutputTruncationStopsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	initial := &RunResult{
		RuntimeStatus:           "blocked",
		RuntimeCompletionReason: ReasonModelOutputTruncated,
		Response:                "部分内容",
		LastAgentTraceInput:     `[{"role":"assistant","content":"部分内容"}]`,
	}
	calls := 0
	out, err := continueAfterOutputTruncation(ctx, 2, initial, func(context.Context, int, string, string) (*RunResult, error) {
		calls++
		return &RunResult{Response: "不应出现"}, nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if calls != 0 {
		t.Fatalf("continuation started after cancel: %d", calls)
	}
	if out.Response != "部分内容" {
		t.Fatalf("partial text lost: %q", out.Response)
	}
}

func TestContinueAfterOutputTruncationAbortsInFlightSegment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		<-started
		cancel()
	}()
	initial := &RunResult{
		RuntimeStatus:           "blocked",
		RuntimeCompletionReason: ReasonModelOutputTruncated,
		Response:                "AAA",
		LastAgentTraceInput:     `[{"role":"assistant","content":"AAA"}]`,
	}
	calls := 0
	out, err := continueAfterOutputTruncation(ctx, 3, initial, func(waitCtx context.Context, _ int, _, _ string) (*RunResult, error) {
		calls++
		close(started)
		<-waitCtx.Done()
		return nil, waitCtx.Err()
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if out.Response != "AAA" {
		t.Fatalf("response = %q", out.Response)
	}
}

func TestShouldContinueTruncatedOutputSkipsUnfinishedPlanAndHITL(t *testing.T) {
	cases := []*RunResult{
		{RuntimeStatus: "blocked", RuntimeCompletionReason: ReasonUnfinishedPlan, Response: "计划还在"},
		{RuntimeStatus: "awaiting_hitl", RuntimeCompletionReason: "awaiting_hitl", Response: "等待审批"},
		{RuntimeStatus: "cancelled", RuntimeCompletionReason: "cancelled", Response: "已取消"},
		{RuntimeStatus: "failed", RuntimeCompletionReason: "failed", Response: "失败"},
		{RuntimeStatus: "completed", RuntimeCompletionReason: "verified", Response: "完成"},
	}
	for _, result := range cases {
		if shouldContinueTruncatedOutput(result, 0, 2) {
			t.Fatalf("continued for %+v", result)
		}
	}
	truncated := &RunResult{RuntimeStatus: "blocked", RuntimeCompletionReason: ReasonModelOutputTruncated, Response: "半段"}
	if !shouldContinueTruncatedOutput(truncated, 0, 2) {
		t.Fatal("truncated output should continue")
	}
	if shouldContinueTruncatedOutput(truncated, 2, 2) {
		t.Fatal("attempt cap ignored")
	}
	if shouldContinueTruncatedOutput(&RunResult{RuntimeStatus: "blocked", RuntimeCompletionReason: ReasonModelOutputTruncated, Response: "  "}, 0, 2) {
		t.Fatal("empty truncation has no context")
	}
}

func jsonUnmarshalHistory(body []byte, dest *[]agent.ChatMessage) error {
	return json.Unmarshal(body, dest)
}
