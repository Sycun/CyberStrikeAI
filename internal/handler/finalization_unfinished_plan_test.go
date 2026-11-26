package handler

import (
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/agentfinalizer"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/multiagent"

	"go.uber.org/zap"
)

func TestUnfinishedPlanPreservesDraftAndStepDetails(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "chat.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conversation, err := db.CreateConversation("Draft notes", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.AddMessage(conversation.ID, "assistant", "处理中...", nil)
	if err != nil {
		t.Fatal(err)
	}
	const draft = "这是已整理的 **笔记初稿**。"
	result := &multiagent.RunResult{
		Response:                draft,
		RuntimeStatus:           agentfinalizer.StatusBlocked,
		RuntimeCompletionReason: multiagent.ReasonUnfinishedPlan,
		UnfinishedPlanSteps: []multiagent.PlanStep{
			{ID: "1", Subject: "复核数字", Status: "in_progress"},
			{ID: "2", Subject: "校对[标题]\n和摘要", Status: "pending"},
		},
	}
	h := &AgentHandler{db: db}
	decision := h.finalizeAgentRunForDelivery(conversation.ID, message.ID, "single", result, nil, "")
	if decision.Status != agentfinalizer.StatusBlocked || decision.Finalized || decision.Finalizable || decision.EvidenceVerified {
		t.Fatalf("a draft must not be promoted to a completed result: %+v", decision)
	}
	reply := finalizationBlockedMessage(decision)
	for _, want := range []string{"本轮任务未完成", "进行中 · 复核数字", "待执行 · 校对\\[标题\\] 和摘要", "阶段性结果", draft} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply is missing %q: %q", want, reply)
		}
	}
	if strings.Contains(reply, "agent run status is blocked") {
		t.Fatalf("reply contains a redundant internal status: %q", reply)
	}
	payload := finalizationResponsePayload(decision, nil)
	steps, ok := payload["unfinishedPlanSteps"].([]multiagent.PlanStep)
	if !ok || len(steps) != 2 || steps[1] != result.UnfinishedPlanSteps[1] {
		t.Fatalf("response metadata lost unfinished steps: %+v", payload)
	}
	var saved string
	if err := db.QueryRow("SELECT content FROM messages WHERE id = ?", message.ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved != reply {
		t.Fatalf("reloading chat should show the same reply, got %q", saved)
	}
	details, err := db.GetProcessDetails(message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 || details[0].EventType != "finalization_check" ||
		!strings.Contains(details[0].Message, "复核数字") || strings.Contains(details[0].Message, draft) {
		t.Fatalf("expected a concise finalization check, got %+v", details)
	}
}

func TestUnfinishedPlanWithoutDraftShowsOnlyNotice(t *testing.T) {
	for _, response := range []string{"", "（Pi 会话已完成，但未捕获到助手文本输出。）"} {
		result := &multiagent.RunResult{
			Response:                response,
			RuntimeStatus:           agentfinalizer.StatusBlocked,
			RuntimeCompletionReason: multiagent.ReasonUnfinishedPlan,
		}
		reply := finalizationBlockedMessage(agentfinalizer.FromRunResult(nil, result, agentfinalizer.Input{}))
		if reply != "本轮任务未完成，计划中仍有待执行或进行中的步骤。" {
			t.Fatalf("expected only the notice, got %q", reply)
		}
	}
}
