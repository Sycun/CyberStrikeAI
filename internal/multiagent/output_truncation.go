package multiagent

import (
	"context"
	"encoding/json"
	"strings"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/config"
)

const (
	defaultOutputTruncationContinueMaxAttempts = 2
	outputTruncationContinueAttemptCap         = 5
	truncationOverlapMinRunes                  = 3
	truncationOverlapMaxRunes                  = 480
	outputTruncationContinueMarker             = "【系统自动续写 / Auto continue】"
)

// OutputTruncationContinueMaxAttemptsFromConfig 是长度截断后的自动续写上限。
// 0 使用默认 2 次；小于 0 关闭；大于 5 按 5，避免无限续写。
func OutputTruncationContinueMaxAttemptsFromConfig(mw *config.AgentTuningConfig) int {
	if mw == nil || mw.OutputTruncationContinueMaxAttempts == 0 {
		return defaultOutputTruncationContinueMaxAttempts
	}
	if mw.OutputTruncationContinueMaxAttempts < 0 {
		return 0
	}
	if mw.OutputTruncationContinueMaxAttempts > outputTruncationContinueAttemptCap {
		return outputTruncationContinueAttemptCap
	}
	return mw.OutputTruncationContinueMaxAttempts
}

func outputTruncationContinueInstruction() string {
	return outputTruncationContinueMarker + "\n上一段助手输出因达到长度上限在中途截断。请从中断处紧接续写，不要重复已写出的正文，不要重新开题，也不要总结前文。若内容已经完整，直接结束。"
}

func isOutputContinuationPrompt(content string) bool {
	return strings.Contains(content, outputTruncationContinueMarker)
}

// shouldContinueTruncatedOutput 只对仍可恢复的长度截断续写。
// unfinished_plan、awaiting_hitl、cancelled、failed 都不走这条路径。
func shouldContinueTruncatedOutput(result *RunResult, attempt, max int) bool {
	if result == nil || max <= 0 || attempt >= max {
		return false
	}
	switch result.RuntimeStatus {
	case "cancelled", "failed", "awaiting_hitl":
		return false
	}
	if result.RuntimeCompletionReason != ReasonModelOutputTruncated {
		return false
	}
	if strings.TrimSpace(result.Response) != "" {
		return true
	}
	return hasUsableTruncationTrace(result.LastAgentTraceInput)
}

func hasUsableTruncationTrace(historyJSON string) bool {
	msgs, ok := parseHistory(historyJSON)
	return ok && len(msgs) > 0
}

// continueAfterOutputTruncation 在首次截断之后续写，并把各段正文、执行 ID 和轨迹合成一个结果。
// next 收到的 history 已补上被截断的助手正文；instruction 只进入下一次模型请求，不写入消息表。
func continueAfterOutputTruncation(
	ctx context.Context,
	maxAttempts int,
	initial *RunResult,
	next func(ctx context.Context, attempt int, history, instruction string) (*RunResult, error),
	notify func(attempt, maxAttempts int),
) (*RunResult, error) {
	if initial == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	merged := cloneRunResult(initial)
	fragment := initial.Response
	merged.LastAgentTraceInput = prepareTruncationHistory(merged.LastAgentTraceInput, fragment, "")
	latest := merged
	instruction := outputTruncationContinueInstruction()
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if !shouldContinueTruncatedOutput(latest, attempt-1, maxAttempts) {
			break
		}
		if err := ctx.Err(); err != nil {
			return merged, err
		}
		if notify != nil {
			notify(attempt, maxAttempts)
		}
		history := prepareTruncationHistory(merged.LastAgentTraceInput, fragment, "")
		seg, segErr := next(ctx, attempt, history, instruction)
		if segErr != nil {
			if seg != nil {
				merged = mergeTruncatedSegment(merged, seg, fragment, "")
			}
			return merged, segErr
		}
		if seg == nil {
			return merged, nil
		}
		previous := merged.Response
		merged = mergeTruncatedSegment(merged, seg, fragment, "")
		fragment = seg.Response
		latest = seg
		if seg.RuntimeCompletionReason == ReasonModelOutputTruncated && merged.Response == previous {
			break
		}
	}
	merged.LastAgentTraceInput = tagOutputContinuationPrompts(merged.LastAgentTraceInput)
	return merged, nil
}

func cloneRunResult(in *RunResult) *RunResult {
	if in == nil {
		return nil
	}
	out := *in
	out.UnfinishedPlanSteps = append([]PlanStep(nil), in.UnfinishedPlanSteps...)
	out.MCPExecutionIDs = append([]string(nil), in.MCPExecutionIDs...)
	out.EvidenceRefs = append([]string(nil), in.EvidenceRefs...)
	out.PendingExecutionIDs = append([]string(nil), in.PendingExecutionIDs...)
	out.MissingChecks = append([]string(nil), in.MissingChecks...)
	return &out
}

func mergeTruncatedSegment(base, next *RunResult, priorFragment, reasoning string) *RunResult {
	if base == nil {
		return cloneRunResult(next)
	}
	if next == nil {
		return cloneRunResult(base)
	}
	out := cloneRunResult(next)
	out.Response = stitchOutputSegments(base.Response, next.Response)
	out.MCPExecutionIDs = mergeExecutionIDs(base.MCPExecutionIDs, next.MCPExecutionIDs)
	out.LastAgentTraceInput = mergeTruncationHistories(base.LastAgentTraceInput, next.LastAgentTraceInput, priorFragment, reasoning)
	if strings.TrimSpace(next.Response) != "" {
		out.LastAgentTraceOutput = next.Response
	} else if strings.TrimSpace(base.LastAgentTraceOutput) != "" {
		out.LastAgentTraceOutput = base.LastAgentTraceOutput
	}
	return out
}

func mergeExecutionIDs(parts ...[]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, part := range parts {
		for _, id := range part {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func stitchOutputSegments(base, next string) string {
	if base == "" {
		return next
	}
	if next == "" {
		return base
	}
	if strings.HasPrefix(next, base) {
		next = next[len(base):]
	} else {
		br := []rune(base)
		nr := []rune(next)
		limit := len(br)
		if len(nr) < limit {
			limit = len(nr)
		}
		if limit > truncationOverlapMaxRunes {
			limit = truncationOverlapMaxRunes
		}
		cut := 0
		for n := limit; n >= truncationOverlapMinRunes; n-- {
			if string(br[len(br)-n:]) == string(nr[:n]) {
				cut = n
				break
			}
		}
		if cut > 0 {
			next = string(nr[cut:])
		}
	}
	if next == "" {
		return base
	}
	return joinOutputBoundary(base, next)
}

func joinOutputBoundary(base, next string) string {
	baseTrimmed := strings.TrimRight(base, " \t")
	nextTrimmed := strings.TrimLeft(next, " \t")
	hadSpace := baseTrimmed != base || nextTrimmed != next
	for strings.HasSuffix(baseTrimmed, "\n") && strings.HasPrefix(nextTrimmed, "\n") {
		nextTrimmed = strings.TrimPrefix(nextTrimmed, "\n")
	}
	if strings.HasSuffix(baseTrimmed, "\n") || strings.HasPrefix(nextTrimmed, "\n") {
		return baseTrimmed + nextTrimmed
	}
	if hadSpace {
		return baseTrimmed + " " + nextTrimmed
	}
	return base + next
}

func prepareTruncationHistory(historyJSON, fragment, reasoning string) string {
	return tagOutputContinuationPrompts(ensureTruncatedAssistantHistory(historyJSON, fragment, reasoning))
}

func ensureTruncatedAssistantHistory(historyJSON, fragment, reasoning string) string {
	if strings.TrimSpace(fragment) == "" && strings.TrimSpace(reasoning) == "" {
		return historyJSON
	}
	msgs, ok := parseHistory(historyJSON)
	if !ok {
		if strings.TrimSpace(fragment) == "" {
			return historyJSON
		}
		msgs = nil
	}
	changed := false
	if idx := lastAssistantIndex(msgs); idx >= 0 && !hasToolCalls(msgs[idx]) {
		content := messageContent(msgs[idx])
		covered, upgrade := fragmentCovered(content, fragment)
		if covered {
			if upgrade && strings.TrimSpace(fragment) != "" {
				msgs[idx]["content"] = fragment
				changed = true
			}
			if fillReasoning(msgs[idx], reasoning) {
				changed = true
			}
			if !changed {
				return historyJSON
			}
			return marshalHistory(msgs)
		}
	}
	for _, msg := range msgs {
		role, _ := msg["role"].(string)
		if !strings.EqualFold(role, "assistant") || hasToolCalls(msg) {
			continue
		}
		content := messageContent(msg)
		if content == fragment || strings.TrimSpace(content) == strings.TrimSpace(fragment) || (fragment != "" && strings.HasSuffix(content, fragment)) {
			if fillReasoning(msg, reasoning) {
				return marshalHistory(msgs)
			}
			return historyJSON
		}
	}
	if strings.TrimSpace(fragment) == "" {
		return historyJSON
	}
	msg := map[string]any{"role": "assistant", "content": fragment}
	if strings.TrimSpace(reasoning) != "" {
		msg["reasoning_content"] = reasoning
	}
	return marshalHistory(append(msgs, msg))
}

func mergeTruncationHistories(prev, next, fragment, reasoning string) string {
	prev = ensureTruncatedAssistantHistory(prev, fragment, reasoning)
	prevMsgs, prevOK := parseHistory(prev)
	nextMsgs, nextOK := parseHistory(next)
	if !nextOK || len(nextMsgs) == 0 {
		return tagOutputContinuationPrompts(prev)
	}
	if !prevOK {
		return tagOutputContinuationPrompts(ensureTruncatedAssistantHistory(next, fragment, reasoning))
	}
	merged := mergeHistoryMessages(prevMsgs, nextMsgs)
	return tagOutputContinuationPrompts(ensureTruncatedAssistantHistory(marshalHistory(merged), fragment, reasoning))
}

func mergeHistoryMessages(prev, next []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(prev)+len(next))
	for _, msg := range prev {
		out = append(out, cloneHistoryMessage(msg))
	}
	for _, msg := range next {
		if i := indexSameMessage(out, msg); i >= 0 {
			mergeMessageFields(out[i], msg)
			continue
		}
		out = append(out, cloneHistoryMessage(msg))
	}
	return out
}

func indexSameMessage(msgs []map[string]any, candidate map[string]any) int {
	for i, msg := range msgs {
		if sameHistoryMessage(msg, candidate) {
			return i
		}
	}
	return -1
}

func sameHistoryMessage(a, b map[string]any) bool {
	roleA, _ := a["role"].(string)
	roleB, _ := b["role"].(string)
	if !strings.EqualFold(strings.TrimSpace(roleA), strings.TrimSpace(roleB)) {
		return false
	}
	if strings.EqualFold(roleA, "tool") {
		idA, _ := a["tool_call_id"].(string)
		idB, _ := b["tool_call_id"].(string)
		return idA != "" && idA == idB
	}
	return messageContent(a) == messageContent(b)
}

func mergeMessageFields(dst, src map[string]any) {
	if strings.TrimSpace(messageString(dst["reasoning_content"])) == "" {
		if rc := strings.TrimSpace(messageString(src["reasoning_content"])); rc != "" {
			dst["reasoning_content"] = rc
		}
	}
	dstContent := messageContent(dst)
	srcContent := messageContent(src)
	if !hasToolCalls(dst) && !hasToolCalls(src) && dstContent != "" && strings.HasPrefix(srcContent, dstContent) && len(srcContent) > len(dstContent) {
		dst["content"] = srcContent
	}
	if dst["extra"] == nil && src["extra"] != nil {
		dst["extra"] = src["extra"]
	}
}

func tagOutputContinuationPrompts(historyJSON string) string {
	msgs, ok := parseHistory(historyJSON)
	if !ok || len(msgs) == 0 {
		return historyJSON
	}
	changed := false
	for _, msg := range msgs {
		role, _ := msg["role"].(string)
		if !strings.EqualFold(role, "user") || !isOutputContinuationPrompt(messageContent(msg)) {
			continue
		}
		extra, _ := msg["extra"].(map[string]any)
		if extra == nil {
			extra = map[string]any{}
		}
		if extra[agent.GeneratedContextTypeKey] == agent.OutputTruncationContinueContext {
			msg["extra"] = extra
			continue
		}
		extra[agent.GeneratedContextTypeKey] = agent.OutputTruncationContinueContext
		msg["extra"] = extra
		changed = true
	}
	if !changed {
		return historyJSON
	}
	return marshalHistory(msgs)
}

func fillReasoning(msg map[string]any, reasoning string) bool {
	if strings.TrimSpace(reasoning) == "" || strings.TrimSpace(messageString(msg["reasoning_content"])) != "" {
		return false
	}
	msg["reasoning_content"] = reasoning
	return true
}

func fragmentCovered(content, fragment string) (covered bool, upgrade bool) {
	if strings.TrimSpace(fragment) == "" {
		return true, false
	}
	if content == fragment || strings.TrimSpace(content) == strings.TrimSpace(fragment) || strings.HasSuffix(content, fragment) {
		return true, false
	}
	if content != "" && strings.HasPrefix(fragment, content) {
		return true, true
	}
	return false, false
}

func lastAssistantIndex(msgs []map[string]any) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		role, _ := msgs[i]["role"].(string)
		if strings.EqualFold(role, "assistant") {
			return i
		}
	}
	return -1
}

func hasToolCalls(msg map[string]any) bool {
	switch calls := msg["tool_calls"].(type) {
	case []any:
		return len(calls) > 0
	case []map[string]any:
		return len(calls) > 0
	default:
		return false
	}
}

func messageContent(msg map[string]any) string {
	return messageString(msg["content"])
}

func messageString(value any) string {
	text, _ := value.(string)
	return text
}

func parseHistory(historyJSON string) ([]map[string]any, bool) {
	historyJSON = strings.TrimSpace(historyJSON)
	if historyJSON == "" || historyJSON == "null" {
		return nil, true
	}
	var msgs []map[string]any
	if err := json.Unmarshal([]byte(historyJSON), &msgs); err != nil {
		return nil, false
	}
	return msgs, true
}

func marshalHistory(msgs []map[string]any) string {
	if msgs == nil {
		msgs = []map[string]any{}
	}
	body, err := json.Marshal(msgs)
	if err != nil {
		return "[]"
	}
	return string(body)
}

func cloneHistoryMessage(msg map[string]any) map[string]any {
	body, err := json.Marshal(msg)
	if err != nil {
		out := make(map[string]any, len(msg))
		for key, value := range msg {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if json.Unmarshal(body, &out) != nil || out == nil {
		return msg
	}
	return out
}
