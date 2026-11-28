package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestProcessDetailsPageIncludesTerminalToolStatusAcrossPageBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.NewDB(filepath.Join(t.TempDir(), "process-details-page.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conversation, err := db.CreateConversation("page boundary", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	message, err := db.AddMessage(conversation.ID, "assistant", "done", nil)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("call-%d", i)
		if err := db.AddProcessDetail(message.ID, conversation.ID, "tool_call", "call", map[string]interface{}{
			"toolName": "http-framework-test", "toolCallId": id, "index": i, "total": 4,
		}); err != nil {
			t.Fatalf("AddProcessDetail(tool_call): %v", err)
		}
	}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("call-%d", i)
		if err := db.AddProcessDetail(message.ID, conversation.ID, "tool_result", "result", map[string]interface{}{
			"toolName": "http-framework-test", "toolCallId": id, "success": true,
		}); err != nil {
			t.Fatalf("AddProcessDetail(tool_result): %v", err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/messages/"+message.ID+"/process-details?limit=6&offset=0", nil)
	c.Params = gin.Params{{Key: "id", Value: message.ID}}
	NewConversationHandler(db, zap.NewNop()).GetMessageProcessDetails(c)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		HasMore        bool                                   `json:"hasMore"`
		ProcessDetails []map[string]interface{}               `json:"processDetails"`
		ToolExecutions []database.ProcessDetailsToolExecution `json:"toolExecutions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.HasMore || len(response.ProcessDetails) != 6 {
		t.Fatalf("page hasMore=%v details=%d, want true/6", response.HasMore, len(response.ProcessDetails))
	}
	if len(response.ToolExecutions) != 4 {
		t.Fatalf("tool executions = %d, want 4", len(response.ToolExecutions))
	}
	for i, execution := range response.ToolExecutions {
		if execution.Status != "completed" {
			t.Fatalf("execution %d status = %q, want completed", i, execution.Status)
		}
	}
}

func TestProcessDetailsPageUsesPersistedExecutionStatusAfterBackgroundCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.NewDB(filepath.Join(t.TempDir(), "process-details-cancelled.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conversation, err := db.CreateConversation("cancelled background", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	message, err := db.AddMessage(conversation.ID, "assistant", "done", nil)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	execID := "exec-cancelled-after-background"
	if err := db.AddProcessDetail(message.ID, conversation.ID, "tool_call", "call", map[string]interface{}{
		"toolName": "exec", "toolCallId": "call-cancelled", "index": 1, "total": 1,
	}); err != nil {
		t.Fatalf("AddProcessDetail(tool_call): %v", err)
	}
	if err := db.AddProcessDetail(message.ID, conversation.ID, "tool_result", "background", map[string]interface{}{
		"toolName": "exec", "toolCallId": "call-cancelled", "executionId": execID, "status": "background_running", "success": true,
	}); err != nil {
		t.Fatalf("AddProcessDetail(tool_result): %v", err)
	}
	now := time.Now()
	if err := db.SaveToolExecution(&mcp.ToolExecution{
		ID:        execID,
		ToolName:  "exec",
		Status:    mcp.ToolExecutionStatusCancelled,
		StartTime: now,
		EndTime:   &now,
	}); err != nil {
		t.Fatalf("SaveToolExecution: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/messages/"+message.ID+"/process-details?limit=10&offset=0", nil)
	c.Params = gin.Params{{Key: "id", Value: message.ID}}
	NewConversationHandler(db, zap.NewNop()).GetMessageProcessDetails(c)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		ToolExecutions []database.ProcessDetailsToolExecution `json:"toolExecutions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.ToolExecutions) != 1 {
		t.Fatalf("tool executions = %d, want 1", len(response.ToolExecutions))
	}
	if got := response.ToolExecutions[0].Status; got != mcp.ToolExecutionStatusCancelled {
		t.Fatalf("tool execution status = %q, want cancelled", got)
	}
}

func TestProcessDetailsFullBackfillsEmptyToolCallArgumentsFromExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.NewDB(filepath.Join(t.TempDir(), "process-details-args.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conversation, err := db.CreateConversation("empty args", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	message, err := db.AddMessage(conversation.ID, "assistant", "done", nil)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := db.AddProcessDetail(message.ID, conversation.ID, "tool_call", "calling exec", map[string]interface{}{
		"toolName": "exec", "toolCallId": "call-empty", "arguments": "", "argumentsObj": nil,
	}); err != nil {
		t.Fatalf("AddProcessDetail(tool_call): %v", err)
	}
	if err := db.SaveToolExecution(&mcp.ToolExecution{
		ID:             "exec-whoami",
		ToolName:       "exec",
		Arguments:      map[string]interface{}{"command": "whoami"},
		Status:         "completed",
		StartTime:      time.Now(),
		ConversationID: conversation.ID,
	}); err != nil {
		t.Fatalf("SaveToolExecution: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/messages/"+message.ID+"/process-details?full=1", nil)
	c.Params = gin.Params{{Key: "id", Value: message.ID}}
	NewConversationHandler(db, zap.NewNop()).GetMessageProcessDetails(c)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		ProcessDetails []map[string]interface{} `json:"processDetails"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.ProcessDetails) != 1 {
		t.Fatalf("process details = %d, want 1", len(response.ProcessDetails))
	}
	data, ok := response.ProcessDetails[0]["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data = %#v", response.ProcessDetails[0]["data"])
	}
	args, ok := data["argumentsObj"].(map[string]interface{})
	if !ok {
		t.Fatalf("argumentsObj = %#v", data["argumentsObj"])
	}
	if args["command"] != "whoami" {
		t.Fatalf("command = %#v, want whoami", args["command"])
	}
	if data["executionId"] != "exec-whoami" {
		t.Fatalf("executionId = %#v, want exec-whoami", data["executionId"])
	}
}

func TestProcessDetailsPageBackfillsAgentFilesystemArgumentsFromPrefixedExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.NewDB(filepath.Join(t.TempDir(), "process-details-eino-fs-args.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conversation, err := db.CreateConversation("eino fs args", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	message, err := db.AddMessage(conversation.ID, "assistant", "done", nil)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := db.AddProcessDetail(message.ID, conversation.ID, "tool_call", "calling read_file", map[string]interface{}{
		"toolName": "read_file", "toolCallId": "call-read", "arguments": "", "argumentsObj": nil,
	}); err != nil {
		t.Fatalf("AddProcessDetail(tool_call): %v", err)
	}
	if err := db.SaveToolExecution(&mcp.ToolExecution{
		ID:             "exec-read",
		ToolName:       "eino_fs::read_file",
		Arguments:      map[string]interface{}{"file_path": "/tmp/requirements.txt", "limit": float64(2000)},
		Status:         "completed",
		StartTime:      time.Now(),
		ConversationID: conversation.ID,
	}); err != nil {
		t.Fatalf("SaveToolExecution: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/messages/"+message.ID+"/process-details?limit=50&offset=0", nil)
	c.Params = gin.Params{{Key: "id", Value: message.ID}}
	NewConversationHandler(db, zap.NewNop()).GetMessageProcessDetails(c)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		ProcessDetails []map[string]interface{} `json:"processDetails"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.ProcessDetails) != 1 {
		t.Fatalf("process details = %d, want 1", len(response.ProcessDetails))
	}
	data, ok := response.ProcessDetails[0]["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data = %#v", response.ProcessDetails[0]["data"])
	}
	args, ok := data["argumentsObj"].(map[string]interface{})
	if !ok {
		t.Fatalf("argumentsObj = %#v", data["argumentsObj"])
	}
	if args["file_path"] != "/tmp/requirements.txt" {
		t.Fatalf("file_path = %#v, want /tmp/requirements.txt", args["file_path"])
	}
	if data["arguments"] == nil {
		t.Fatalf("arguments should be preserved in summarized page data: %#v", data)
	}
}

func TestGetConversationSummaryDefersLargePayloadsAndSkipsProgress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.NewDB(filepath.Join(t.TempDir(), "conversation-summary.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	conv, err := db.CreateConversation("heavy deepagent session", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	msg, err := db.AddMessage(conv.ID, "assistant", "final report body", nil)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := db.UpdateAssistantMessageFinalize(msg.ID, "final report body", nil, "duplicate reasoning fallback"); err != nil {
		t.Fatalf("UpdateAssistantMessageFinalize: %v", err)
	}

	largeArgs := strings.Repeat("A", 4096)
	largeOutput := strings.Repeat("open 80/tcp http\n", 1024)
	largeThought := strings.Repeat("深入分析目标指纹与端口开放情况。", 120)

	if err := db.AddProcessDetail(msg.ID, conv.ID, "progress", "heartbeat noise", nil); err != nil {
		t.Fatalf("AddProcessDetail(progress): %v", err)
	}
	thoughtID, err := db.AddProcessDetailWithID(msg.ID, conv.ID, "thinking", largeThought, map[string]interface{}{"streamId": "s1"})
	if err != nil {
		t.Fatalf("AddProcessDetailWithID(thinking): %v", err)
	}
	callID, err := db.AddProcessDetailWithID(msg.ID, conv.ID, "tool_call", "calling write_file", map[string]interface{}{
		"toolName":     "write_file",
		"toolCallId":   "call-big",
		"agentName":    "main",
		"argumentsObj": map[string]interface{}{"content": largeArgs},
	})
	if err != nil {
		t.Fatalf("AddProcessDetailWithID(tool_call): %v", err)
	}
	resultID, err := db.AddProcessDetailWithID(msg.ID, conv.ID, "tool_result", largeOutput, map[string]interface{}{
		"toolName":   "write_file",
		"toolCallId": "call-big",
		"agentName":  "main",
		"result":     largeOutput,
		"success":    true,
	})
	if err != nil {
		t.Fatalf("AddProcessDetailWithID(tool_result): %v", err)
	}
	if err := db.AddProcessDetail(msg.ID, conv.ID, "finalization_check", "ok", map[string]interface{}{
		"finalizable": true,
		"finalText":   strings.Repeat("report copy ", 500),
	}); err != nil {
		t.Fatalf("AddProcessDetail(finalization_check): %v", err)
	}

	h := NewConversationHandler(db, zap.NewNop())

	// Summary mode should be tiny and omit large payloads + progress rows + duplicate reasoningContent.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/conversations/"+conv.ID+"?include_process_details=summary", nil)
	c.Params = gin.Params{{Key: "id", Value: conv.ID}}
	h.GetConversation(c)
	if w.Code != 200 {
		t.Fatalf("summary status = %d: %s", w.Code, w.Body.String())
	}
	bodyStr := w.Body.String()
	if strings.Contains(bodyStr, largeArgs) || strings.Contains(bodyStr, largeOutput) || strings.Contains(bodyStr, largeThought) {
		t.Fatalf("summary response leaked deferred payload (len=%d)", len(bodyStr))
	}
	if strings.Contains(bodyStr, "heartbeat noise") {
		t.Fatalf("summary response should skip progress rows")
	}
	if strings.Contains(bodyStr, "report copy") {
		t.Fatalf("summary response should strip finalization_check.finalText")
	}
	if strings.Contains(bodyStr, "duplicate reasoning fallback") {
		t.Fatalf("summary response should omit duplicate reasoningContent when thinking detail exists")
	}

	var convResp struct {
		Messages []struct {
			ProcessDetails []map[string]interface{} `json:"processDetails"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &convResp); err != nil {
		t.Fatalf("decode summary response: %v", err)
	}
	if len(convResp.Messages) != 1 || len(convResp.Messages[0].ProcessDetails) != 4 {
		t.Fatalf("unexpected summary details count: %+v", convResp.Messages)
	}

	// Single-detail fetch still returns the full payloads on demand.
	for _, tc := range []struct {
		id      string
		wantSub string
	}{
		{thoughtID, "深入分析目标指纹与端口开放情况。"},
		{callID, largeArgs[:64]},
		{resultID, "open 80/tcp http"},
	} {
		dw := httptest.NewRecorder()
		dc, _ := gin.CreateTestContext(dw)
		dc.Request = httptest.NewRequest("GET", "/api/process-details/"+tc.id, nil)
		dc.Params = gin.Params{{Key: "id", Value: tc.id}}
		h.GetProcessDetail(dc)
		if dw.Code != 200 {
			t.Fatalf("GetProcessDetail(%s) status=%d: %s", tc.id, dw.Code, dw.Body.String())
		}
		if !strings.Contains(dw.Body.String(), tc.wantSub) {
			t.Fatalf("GetProcessDetail(%s) missing full payload", tc.id)
		}
	}
}
