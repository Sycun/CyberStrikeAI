package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/agentfinalizer"
	"cyberstrike-ai/internal/multiagent"

	"go.uber.org/zap"
)

func (h *AgentHandler) finalizeAgentRunForDelivery(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	result *multiagent.RunResult,
	mcpExecutionIDs []string,
	reasoningContent string,
) agentfinalizer.Decision {
	return h.finalizeAgentRunForDeliveryWithPolicy(conversationID, assistantMessageID, agentMode, result, mcpExecutionIDs, reasoningContent, false)
}

func (h *AgentHandler) finalizeAgentRunForDeliveryWithPolicy(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	result *multiagent.RunResult,
	mcpExecutionIDs []string,
	reasoningContent string,
	requireExecutionEvidence bool,
) agentfinalizer.Decision {
	decision := agentfinalizer.FromRunResult(h.db, result, agentfinalizer.Input{
		ConversationID:           conversationID,
		AssistantMessageID:       assistantMessageID,
		AgentMode:                agentMode,
		MCPExecutionIDs:          mcpExecutionIDs,
		RequireExecutionEvidence: requireExecutionEvidence,
	})
	h.persistFinalizationDecision(conversationID, assistantMessageID, agentMode, mcpExecutionIDs, reasoningContent, decision)
	return decision
}

func (h *AgentHandler) decideAgentRunForDeliveryAndCleanup(
	ctx context.Context,
	conversationID string,
	assistantMessageID string,
	agentMode string,
	result *multiagent.RunResult,
	mcpExecutionIDs []string,
	requireExecutionEvidence bool,
	progress func(string, string, interface{}),
) (agentfinalizer.Decision, []string) {
	input := agentfinalizer.Input{
		ConversationID:           conversationID,
		AssistantMessageID:       assistantMessageID,
		AgentMode:                agentMode,
		MCPExecutionIDs:          mcpExecutionIDs,
		RequireExecutionEvidence: requireExecutionEvidence,
	}
	decision := agentfinalizer.FromRunResult(h.db, result, input)
	cancelled := h.cleanupPendingToolExecutionsAfterIteration(ctx, conversationID, decision, progress)
	if len(cancelled) == 0 {
		return decision, nil
	}
	input.Status = agentfinalizer.StatusBlocked
	input.CompletionReason = agentfinalizer.ReasonPendingToolsCancelled
	return agentfinalizer.FromRunResult(h.db, result, input), cancelled
}

func (h *AgentHandler) decideAgentRunForDelivery(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	result *multiagent.RunResult,
	mcpExecutionIDs []string,
) agentfinalizer.Decision {
	return agentfinalizer.FromRunResult(h.db, result, agentfinalizer.Input{
		ConversationID:           conversationID,
		AssistantMessageID:       assistantMessageID,
		AgentMode:                agentMode,
		MCPExecutionIDs:          mcpExecutionIDs,
		RequireExecutionEvidence: false,
	})
}

func (h *AgentHandler) persistFinalizationDecision(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	mcpExecutionIDs []string,
	reasoningContent string,
	decision agentfinalizer.Decision,
) {
	if assistantMessageID == "" || h.db == nil {
		return
	}
	_ = h.db.AddProcessDetail(assistantMessageID, conversationID, "finalization_check", finalizationCheckMessage(decision), decision)
	if decision.Finalizable {
		if err := h.db.UpdateAssistantMessageFinalize(assistantMessageID, decision.FinalText, mcpExecutionIDs, reasoningContent); err != nil && h.logger != nil {
			h.logger.Warn("更新最终助手消息失败", zap.Error(err), zap.String("conversationId", conversationID), zap.String("agentMode", agentMode))
		}
		return
	}
	_, _ = h.db.Exec("UPDATE messages SET content = ?, updated_at = ? WHERE id = ?", finalizationBlockedMessage(decision), time.Now(), assistantMessageID)
}

func (h *AgentHandler) finalizeCandidateForDelivery(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	response string,
	mcpExecutionIDs []string,
	awaitingHITL bool,
	reasoningContent string,
) agentfinalizer.Decision {
	return h.finalizeCandidateForDeliveryWithPolicy(conversationID, assistantMessageID, agentMode, response, mcpExecutionIDs, awaitingHITL, reasoningContent, false)
}

func (h *AgentHandler) finalizeCandidateForDeliveryWithPolicy(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	response string,
	mcpExecutionIDs []string,
	awaitingHITL bool,
	reasoningContent string,
	requireExecutionEvidence bool,
) agentfinalizer.Decision {
	decision := agentfinalizer.Decide(h.db, agentfinalizer.Input{
		Response:                 response,
		ConversationID:           conversationID,
		AssistantMessageID:       assistantMessageID,
		AgentMode:                agentMode,
		MCPExecutionIDs:          mcpExecutionIDs,
		AwaitingHITL:             awaitingHITL,
		RequireExecutionEvidence: requireExecutionEvidence,
	})
	if assistantMessageID == "" || h.db == nil {
		return decision
	}
	_ = h.db.AddProcessDetail(assistantMessageID, conversationID, "finalization_check", finalizationCheckMessage(decision), decision)
	if decision.Finalizable {
		if err := h.db.UpdateAssistantMessageFinalize(assistantMessageID, decision.FinalText, mcpExecutionIDs, reasoningContent); err != nil && h.logger != nil {
			h.logger.Warn("更新最终助手消息失败", zap.Error(err), zap.String("conversationId", conversationID), zap.String("agentMode", agentMode))
		}
		return decision
	}
	_, _ = h.db.Exec("UPDATE messages SET content = ?, updated_at = ? WHERE id = ?", finalizationBlockedMessage(decision), time.Now(), assistantMessageID)
	return decision
}

func finalizationCheckMessage(d agentfinalizer.Decision) string {
	if d.Finalizable {
		return "已交付本轮回复。"
	}
	if d.CompletionReason == multiagent.ReasonUnfinishedPlan {
		return unfinishedPlanNotice(d.UnfinishedPlanSteps)
	}
	return finalizationBlockedMessage(d)
}

func unfinishedPlanNotice(steps []multiagent.PlanStep) string {
	if len(steps) == 0 {
		return "本轮任务未完成，计划中仍有待执行或进行中的步骤。"
	}
	var notice strings.Builder
	fmt.Fprintf(&notice, "本轮任务未完成，还有 %d 个计划步骤未结束。\n", len(steps))
	escapeTitle := strings.NewReplacer(
		"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "~", "\\~",
		"[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>", "&", "\\&",
	)
	for _, step := range steps {
		status := "待执行"
		if step.Status == "in_progress" {
			status = "进行中"
		}
		title := strings.Join(strings.Fields(step.Subject), " ")
		if title == "" {
			title = "步骤 #" + step.ID
		}
		fmt.Fprintf(&notice, "\n- %s · %s", status, escapeTitle.Replace(title))
	}
	return notice.String()
}

func finalizationBlockedMessage(d agentfinalizer.Decision) string {
	if d.CompletionReason == "workflow_rejected" && strings.TrimSpace(d.FinalText) != "" {
		return d.FinalText
	}
	if d.CompletionReason == multiagent.ReasonUnfinishedPlan {
		notice := unfinishedPlanNotice(d.UnfinishedPlanSteps)
		if text := strings.TrimSpace(d.FinalText); text != "" {
			return notice + "\n\n**本轮已生成的内容（阶段性结果）**\n\n" + text
		}
		return notice
	}
	notice, keepText := finalizationBlockNotice(d)
	parts := make([]string, 0, 4)
	if keepText {
		parts = append(parts, strings.TrimSpace(d.FinalText), "---")
	}
	parts = append(parts, notice)
	if d.CompletionReason != "" {
		parts = append(parts, "原因: "+d.CompletionReason)
	}
	if len(d.PendingExecutionIDs) > 0 {
		parts = append(parts, fmt.Sprintf("仍有 %d 个工具执行未结束: %s", len(d.PendingExecutionIDs), strings.Join(d.PendingExecutionIDs, ", ")))
	}
	if len(d.MissingChecks) > 0 {
		parts = append(parts, "缺失检查: "+strings.Join(d.MissingChecks, "; "))
	}
	return strings.Join(parts, "\n")
}

func finalizationBlockNotice(d agentfinalizer.Decision) (string, bool) {
	switch d.CompletionReason {
	case agentfinalizer.ReasonPendingToolsCancelled:
		return "收尾时取消了仍在运行的工具，任务未完成。", false
	case multiagent.ReasonModelOutputTruncated:
		if strings.TrimSpace(d.FinalText) != "" {
			return "模型输出达到长度上限，已保留以上已生成内容。本轮回复仍未完成。可发送「继续」接着生成。", true
		}
		return "模型输出达到长度上限，本轮回复未完成。", false
	default:
		return "任务尚未达到最终回复条件，暂不生成成功结论。", false
	}
}

func finalizationResponsePayload(d agentfinalizer.Decision, extra map[string]interface{}) map[string]interface{} {
	return agentfinalizer.ResponsePayload(d, extra)
}

func requestRequiresExecutionEvidence(req *ChatRequest) bool {
	return req != nil && req.Finalization.RequireExecutionEvidence != nil && *req.Finalization.RequireExecutionEvidence
}
