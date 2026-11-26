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

func TestFinalizationBlockedMessageKeepsTruncatedText(t *testing.T) {
	decision := agentfinalizer.Decision{
		Status:           agentfinalizer.StatusBlocked,
		CompletionReason: multiagent.ReasonModelOutputTruncated,
		FinalText:        "已经写好的前半段。",
		MissingChecks:    []string{"agent run status is blocked"},
	}
	got := finalizationBlockedMessage(decision)
	if !strings.Contains(got, "已经写好的前半段。") || !strings.Contains(got, "\n---\n") || !strings.Contains(got, "可发送「继续」接着生成。") {
		t.Fatalf("truncated text was discarded: %q", got)
	}
	if strings.TrimSpace(got) == "模型输出达到长度上限，本轮回复未完成。\n原因: model_output_truncated\n缺失检查: agent run status is blocked" {
		t.Fatal("blocked notice replaced the generated text")
	}

	empty := finalizationBlockedMessage(agentfinalizer.Decision{
		Status:           agentfinalizer.StatusBlocked,
		CompletionReason: multiagent.ReasonModelOutputTruncated,
		MissingChecks:    []string{"agent run status is blocked"},
	})
	if strings.Contains(empty, "---") || !strings.HasPrefix(empty, "模型输出达到长度上限，本轮回复未完成。") {
		t.Fatalf("empty truncation should stay a notice: %q", empty)
	}

	unfinished := finalizationBlockedMessage(agentfinalizer.Decision{
		Status:           agentfinalizer.StatusBlocked,
		CompletionReason: multiagent.ReasonUnfinishedPlan,
		FinalText:        "计划正文",
	})
	if !strings.Contains(unfinished, "计划正文") || !strings.Contains(unfinished, "未完成") || strings.Contains(unfinished, "长度上限") {
		t.Fatalf("unfinished plan notice changed: %q", unfinished)
	}

	hitl := finalizationBlockedMessage(agentfinalizer.Decision{
		Status:           agentfinalizer.StatusAwaitingHITL,
		CompletionReason: agentfinalizer.ReasonAwaitingHITL,
		FinalText:        "等待审批的正文",
	})
	if strings.Contains(hitl, "等待审批的正文") {
		t.Fatalf("awaiting HITL should keep its status notice: %q", hitl)
	}
}

func TestPersistTruncatedFinalizationKeepsGeneratedText(t *testing.T) {
	logger := zap.NewNop()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "truncation.db"), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conv, err := db.CreateConversation("truncation", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := db.AddMessage(conv.ID, "assistant", "占位", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &AgentHandler{db: db, logger: logger}
	decision := agentfinalizer.Decision{
		Status:           agentfinalizer.StatusBlocked,
		CompletionReason: multiagent.ReasonModelOutputTruncated,
		FinalText:        "已经写好的前半段。",
		MissingChecks:    []string{"agent run status is blocked"},
		Finalizable:      false,
	}
	h.persistFinalizationDecision(conv.ID, msg.ID, "single", nil, "", decision)
	messages, err := db.GetMessages(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	for _, message := range messages {
		if message.ID == msg.ID {
			stored = message.Content
		}
	}
	if !strings.Contains(stored, "已经写好的前半段。") || !strings.Contains(stored, multiagent.ReasonModelOutputTruncated) {
		t.Fatalf("stored content discarded the partial answer: %q", stored)
	}
}
