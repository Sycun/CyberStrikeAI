package multiagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/agentruntime"
	"cyberstrike-ai/internal/agents"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/project"
	"cyberstrike-ai/internal/reasoning"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type piRun struct {
	cfg                                               *config.Config
	ag                                                *agent.Agent
	conversationID, agentID, systemPrompt, sessionKey string
	history                                           []agent.ChatMessage
	roleTools                                         []string
	catalogAllow                                      map[string]bool
	progress                                          func(string, string, interface{})
	reasoning                                         *reasoning.ClientIntent
	child                                             bool
	maxTurns                                          int
	extraTools                                        []agentruntime.Tool
	extraExecute                                      agentruntime.ToolExecutor
}

func runPiSingle(ctx context.Context, cfg *config.Config, ag *agent.Agent, db *database.DB, logger *zap.Logger,
	conversationID, projectID, message string, history []agent.ChatMessage, roleTools []string,
	progress func(string, string, interface{}), intent *reasoning.ClientIntent, extra string) (*RunResult, error) {
	if ag == nil {
		return nil, fmt.Errorf("Pi agent is nil")
	}
	prompt := project.AppendSystemPromptBlock(ag.EinoSingleAgentSystemInstruction(), extra)
	prompt = project.AppendVisionImageAnalysisIfReady(prompt, cfg.Vision.Ready())
	return (&piRun{cfg: cfg, ag: ag, conversationID: conversationID, agentID: "main", systemPrompt: prompt,
		history: history, roleTools: roleTools, progress: progress, reasoning: intent}).run(ctx, message)
}

func piToolSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required}
}

func runPiMulti(ctx context.Context, cfg *config.Config, ma *config.MultiAgentConfig, ag *agent.Agent, db *database.DB, logger *zap.Logger,
	conversationID, projectID, message string, history []agent.ChatMessage, roleTools []string,
	progress func(string, string, interface{}), markdownDir, mode string, intent *reasoning.ClientIntent, extra string) (*RunResult, error) {
	if ag == nil || ma == nil {
		return nil, fmt.Errorf("Pi agent/config is nil")
	}
	if mode == "" {
		mode = ma.Orchestration
	}
	mode = config.NormalizeMultiAgentOrchestration(mode)
	subs := ma.SubAgents
	var loaded *agents.MarkdownDirLoad
	if markdownDir != "" {
		var err error
		loaded, err = agents.LoadMarkdownAgentsDir(markdownDir)
		if err != nil {
			return nil, fmt.Errorf("load Pi agent roles: %w", err)
		}
		subs = agents.MergeYAMLAndMarkdown(subs, loaded.SubAgents)
	}
	if mode == "plan_execute" {
		subs = []config.MultiAgentSubConfig{{ID: "executor", Description: "Execute one concrete plan step and return observed evidence", Instruction: ag.EinoSingleAgentSystemInstruction(), RoleTools: roleTools}}
	}
	if len(subs) == 0 && !ma.WithoutGeneralSubAgent && mode != "supervisor" {
		subs = []config.MultiAgentSubConfig{{ID: "general", Description: "General task specialist", Instruction: ag.EinoSingleAgentSystemInstruction(), RoleTools: roleTools}}
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("Pi %s requires at least one configured sub-agent", mode)
	}
	registry := map[string]config.MultiAgentSubConfig{}
	var catalog []string
	for _, sub := range subs {
		id := strings.TrimSpace(sub.ID)
		if id == "" {
			id = sub.Name
		}
		if id == "" {
			return nil, fmt.Errorf("Pi sub-agent has no ID")
		}
		if _, exists := registry[id]; exists {
			return nil, fmt.Errorf("duplicate Pi sub-agent: %s", id)
		}
		registry[id] = sub
		catalog = append(catalog, id+": "+sub.Description)
	}
	prompt, _ := resolveMainOrchestratorInstruction(mode, ma, loaded)
	prompt = project.AppendSystemPromptBlock(prompt, extra)
	prompt += "\n\nAvailable specialists:\n" + strings.Join(catalog, "\n") + "\nUse task to delegate a concrete objective. Each child receives only the tools permitted for it and this conversation. Verify evidence before reporting completion."
	if mode == "plan_execute" {
		prompt += "\nFirst create a concrete plan with write_todos. Delegate one step at a time to executor using task, examine the result, update the plan, and replan when evidence requires it. Finish with the verified deliverable."
	}
	main := &piRun{cfg: cfg, ag: ag, conversationID: conversationID, agentID: "main", systemPrompt: prompt, history: history, roleTools: roleTools, progress: progress, reasoning: intent, sessionKey: mode}
	str := map[string]any{"type": "string"}
	main.extraTools = []agentruntime.Tool{
		{Name: "task", Description: "Delegate a concrete task to a named specialist and return its evidence-backed result", Parameters: piToolSchema(map[string]any{"subagent_type": str, "description": str}, "subagent_type", "description")},
		{Name: "write_todos", Description: "Record/update the execution plan", Parameters: piToolSchema(map[string]any{"todos": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"content": str, "status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}}}, "required": []string{"content", "status"}}}}, "todos")},
	}
	var idsMu sync.Mutex
	var childIDs []string
	var deliverable string
	main.extraTools = append(main.extraTools, agentruntime.Tool{Name: "exit", Description: "Finish with the verified final deliverable", Parameters: piToolSchema(map[string]any{"final_result": str}, "final_result")})
	if mode == "supervisor" {
		main.extraTools = append(main.extraTools, agentruntime.Tool{Name: "transfer_to_agent", Description: "Delegate to a named specialist", Parameters: piToolSchema(map[string]any{"agent_name": str, "description": str}, "agent_name", "description")})
	}
	main.extraExecute = func(callCtx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
		if call.Name == "exit" {
			text, _ := call.Arguments["final_result"].(string)
			if strings.TrimSpace(text) == "" {
				return agentruntime.ToolResult{}, fmt.Errorf("final_result is required")
			}
			idsMu.Lock()
			deliverable = text
			idsMu.Unlock()
			return agentruntime.ToolResult{Content: text, Terminate: true}, nil
		}
		if call.Name == "write_todos" {
			b, _ := json.Marshal(call.Arguments)
			if progress != nil {
				progress("plan_update", string(b), call.Arguments)
			}
			return agentruntime.ToolResult{Content: string(b)}, nil
		}
		id, _ := call.Arguments["subagent_type"].(string)
		if call.Name == "transfer_to_agent" {
			id, _ = call.Arguments["agent_name"].(string)
		}
		task, _ := call.Arguments["description"].(string)
		sub, ok := registry[id]
		if !ok || strings.TrimSpace(task) == "" {
			return agentruntime.ToolResult{}, fmt.Errorf("unknown specialist or empty task")
		}
		tools := sub.RoleTools
		if sub.BindRole != "" {
			role, ok := cfg.Roles[sub.BindRole]
			if !ok || !role.Enabled {
				return agentruntime.ToolResult{}, fmt.Errorf("sub-agent role unavailable: %s", sub.BindRole)
			}
			if len(tools) == 0 {
				tools = role.Tools
			}
		}
		// A child may narrow its parent's catalog, never expand it.
		parentNames := map[string]bool{}
		for _, t := range ag.ToolsForRole(roleTools) {
			parentNames[t.Function.Name] = true
		}
		childPrompt := project.AppendSystemPromptBlock(sub.Instruction, extra) + "\n\nOriginal user task:\n" + message
		child := &piRun{cfg: cfg, ag: ag, conversationID: conversationID, agentID: id, systemPrompt: childPrompt, roleTools: tools, catalogAllow: parentNames, progress: progress, reasoning: intent, child: true, sessionKey: call.ID, maxTurns: sub.MaxIterations}
		res, err := child.run(callCtx, task)
		if res == nil {
			return agentruntime.ToolResult{}, err
		}
		idsMu.Lock()
		childIDs = append(childIDs, res.MCPExecutionIDs...)
		idsMu.Unlock()
		return agentruntime.ToolResult{Content: res.Response, IsError: err != nil}, err
	}
	res, err := main.run(ctx, message)
	if res != nil {
		idsMu.Lock()
		if deliverable != "" {
			res.Response = deliverable
			res.LastAgentTraceOutput = deliverable
		}
		res.MCPExecutionIDs = append(res.MCPExecutionIDs, childIDs...)
		idsMu.Unlock()
	}
	return res, err
}

func (r *piRun) run(ctx context.Context, message string) (*RunResult, error) {
	defs := r.ag.ToolsForRole(r.roleTools)
	localDefs, localNames, skillPrompt, err := r.piLocalCatalog(ctx)
	if err != nil {
		return nil, err
	}
	r.systemPrompt += skillPrompt
	allowed := map[string]bool{}
	tools := make([]agentruntime.Tool, 0, len(defs)+len(r.extraTools))
	for _, d := range defs {
		if r.catalogAllow != nil && !r.catalogAllow[d.Function.Name] {
			continue
		}
		allowed[d.Function.Name] = true
		tools = append(tools, agentruntime.Tool{Name: d.Function.Name, Description: d.Function.Description, Parameters: d.Function.Parameters})
	}
	for _, t := range localDefs {
		if !allowed[t.Name] {
			tools = append(tools, t)
			allowed[t.Name] = true
		} else {
			delete(localNames, t.Name)
		}
	}
	extraNames := map[string]bool{}
	for _, t := range r.extraTools {
		if allowed[t.Name] {
			return nil, fmt.Errorf("Pi tool name collision: %s", t.Name)
		}
		extraNames[t.Name] = true
		tools = append(tools, t)
	}
	oa := r.cfg.OpenAI
	headers := make(map[string]string, len(oa.Headers))
	for name, value := range oa.Headers {
		if strings.Contains(value, "%session_id%") && r.conversationID == "" {
			continue
		}
		headers[name] = strings.ReplaceAll(value, "%session_id%", r.conversationID)
	}
	mode, effort := oa.Reasoning.ModeEffective(), oa.Reasoning.Effort
	if r.reasoning != nil && oa.Reasoning.AllowClientReasoningEffective() {
		if r.reasoning.Mode != "" {
			mode = r.reasoning.Mode
		}
		if r.reasoning.Effort != "" {
			effort = r.reasoning.Effort
		}
	}
	thinking := "off"
	if mode == "on" {
		thinking = effort
		if thinking == "" {
			thinking = "medium"
		}
	}
	api := "openai-completions"
	if oa.Provider == "claude" || oa.Provider == "anthropic" {
		api = "anthropic-messages"
	}
	base := oa.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
		if api == "anthropic-messages" {
			base = "https://api.anthropic.com"
		}
	}
	root := r.cfg.AgentRuntime.SessionDir
	if root == "" {
		root = filepath.Join(filepath.Dir(r.cfg.Database.Path), "pi-sessions")
		if r.cfg.Database.Path == "" {
			root = "data/pi-sessions"
		}
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(r.conversationID+"\x00"+r.agentID+"\x00"+r.sessionKey)))
	history, err := json.Marshal(r.history)
	if err != nil {
		return nil, err
	}
	max := r.maxTurns
	if max <= 0 {
		max = agentMaxIterations(r.cfg)
	}
	runID := uuid.NewString()
	req := agentruntime.Request{RunID: runID, ConversationID: r.conversationID, SystemPrompt: r.systemPrompt, Message: message, History: history,
		Model: agentruntime.Model{Provider: oa.Provider, API: api, ID: oa.Model, BaseURL: base, APIKey: oa.APIKey, Headers: headers, ContextWindow: oa.MaxTotalTokens, MaxTokens: oa.MaxCompletionTokensEffective(), Thinking: thinking, ExtraFields: reasoning.AgenticOpenAIExtraFields(&oa, r.reasoning)},
		Tools: tools, SessionDir: filepath.Join(root, key), MaxTurns: max, Profile: agentruntime.PromptProfile{Enabled: r.cfg.AgentRuntime.Purge.EnabledEffective(), Instruction: r.cfg.AgentRuntime.Purge.InstructionFor(oa.Provider, oa.Model), Child: r.child}}
	if strings.TrimSpace(r.conversationID) == "" {
		req.SessionDir = ""
	}
	req.RetryMax = r.cfg.MultiAgent.EinoMiddleware.ModelRetryMaxRetries
	for _, id := range r.cfg.MultiAgent.EinoMiddleware.ModelFailoverChannels {
		ch, _, ok := r.cfg.ResolveAIChannel(id)
		if !ok {
			return nil, fmt.Errorf("unknown fallback AI channel: %s", id)
		}
		if ch.Model == oa.Model && ch.BaseURL == oa.BaseURL && ch.Provider == oa.Provider {
			continue
		}
		fallback := req.Model
		fallback.Provider = ch.Provider
		fallback.ID = ch.Model
		fallback.APIKey = ch.APIKey
		fallback.BaseURL = ch.BaseURL
		fallback.Headers = map[string]string{}
		for key, value := range ch.Headers {
			fallback.Headers[key] = strings.ReplaceAll(value, "%session_id%", r.conversationID)
		}
		fallback.API = "openai-completions"
		if ch.Provider == "claude" || ch.Provider == "anthropic" {
			fallback.API = "anthropic-messages"
		}
		if fallback.BaseURL == "" {
			fallback.BaseURL = "https://api.openai.com/v1"
			if fallback.API == "anthropic-messages" {
				fallback.BaseURL = "https://api.anthropic.com"
			}
		}
		fallback.MaxTokens = ch.MaxCompletionTokensEffective()
		fallback.ContextWindow = ch.MaxTotalTokens
		fallback.ExtraFields = reasoning.AgenticOpenAIExtraFields(&ch, r.reasoning)
		req.FallbackModels = append(req.FallbackModels, fallback)
		if cap := r.cfg.MultiAgent.EinoMiddleware.ModelFailoverMaxRetries; cap > 0 && len(req.FallbackModels) >= cap {
			break
		}
	}
	var idsMu sync.Mutex
	var ids []string
	var progressMu sync.Mutex
	var iteration atomic.Int32
	progress := func(kind, text string, data map[string]any) {
		if r.progress == nil {
			return
		}
		progressMu.Lock()
		defer progressMu.Unlock()
		data["source"] = "pi"
		if strings.HasPrefix(kind, "response_") {
			data["streamId"] = fmt.Sprintf("%s-%d", runID, iteration.Load())
			data["iteration"] = iteration.Load()
			data["messageGeneratedBy"] = "pi:" + r.agentID
			idsMu.Lock()
			data["mcpExecutionIds"] = append([]string(nil), ids...)
			idsMu.Unlock()
		}
		data["runId"] = runID
		data["conversationId"] = r.conversationID
		data["einoAgent"] = r.agentID
		data["einoRole"] = "orchestrator"
		if r.child {
			data["einoRole"] = "subagent"
		}
		r.progress(kind, text, data)
	}
	invoke := func(callCtx context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
		if !allowed[call.Name] && !extraNames[call.Name] {
			return agentruntime.ToolResult{}, fmt.Errorf("tool not allowed: %s", call.Name)
		}
		if _, marked := call.Arguments[modelOutputRecoveryKey]; marked {
			return agentruntime.ToolResult{}, fmt.Errorf("legacy model-output recovery markers cannot be executed")
		}
		if localToolPermissionDenied(callCtx, call.Name) {
			return agentruntime.ToolResult{}, fmt.Errorf("agent:local-execute permission required")
		}
		body, _ := json.Marshal(call.Arguments)
		if fn, _ := callCtx.Value(hitlInterceptorKey{}).(HITLToolInterceptor); fn != nil {
			edited, err := fn(callCtx, call.Name, string(body))
			if err != nil {
				return agentruntime.ToolResult{}, err
			}
			if edited != "" {
				if err = json.Unmarshal([]byte(edited), &call.Arguments); err != nil {
					return agentruntime.ToolResult{}, fmt.Errorf("invalid approved tool arguments: %w", err)
				}
			}
		}
		progress("tool_call", "", map[string]any{"toolName": call.Name, "toolCallId": call.ID, "arguments": call.Arguments})
		var result agentruntime.ToolResult
		var err error
		if localNames[call.Name] {
			result, err = r.executePiLocal(callCtx, call)
		} else if extraNames[call.Name] {
			result, err = r.extraExecute(callCtx, call)
		} else {
			res, invokeErr := r.ag.ExecuteMCPToolForConversation(callCtx, r.conversationID, call.Name, call.Arguments)
			err = invokeErr
			if res != nil {
				result = agentruntime.ToolResult{Content: res.Result, IsError: res.IsError || res.Blocked, ExecutionID: res.ExecutionID}
				if res.ExecutionID != "" {
					idsMu.Lock()
					ids = append(ids, res.ExecutionID)
					idsMu.Unlock()
				}
			}
		}
		if err != nil {
			result.Content = err.Error()
			result.IsError = true
		}
		progress("tool_result", result.Content, map[string]any{"toolName": call.Name, "toolCallId": call.ID, "executionId": result.ExecutionID, "result": result.Content, "success": !result.IsError, "isError": result.IsError, "agentFacing": true})
		return result, err
	}
	var text, thought string
	var modelCalls, promptTokens, completionTokens, cacheRead, cacheWrite int
	reasonID := func() string { return fmt.Sprintf("%s-reasoning-%d", runID, iteration.Load()) }
	emit := func(e agentruntime.Event) {
		if e.Type == "turn_start" {
			if thought != "" {
				progress("reasoning_chain_stream_end", thought, map[string]any{"streamId": reasonID()})
			}
			text = ""
			thought = ""
			iteration.Add(1)
		}
		if e.Type == "turn_end" {
			var d struct {
				Message struct {
					Usage struct {
						Input      int `json:"input"`
						Output     int `json:"output"`
						CacheRead  int `json:"cacheRead"`
						CacheWrite int `json:"cacheWrite"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(e.Data, &d) == nil {
				modelCalls++
				promptTokens += d.Message.Usage.Input + d.Message.Usage.CacheRead + d.Message.Usage.CacheWrite
				completionTokens += d.Message.Usage.Output
				cacheRead += d.Message.Usage.CacheRead
				cacheWrite += d.Message.Usage.CacheWrite
			}
		}

		if e.Type == "message_update" {
			var data struct {
				Delta struct {
					Type  string `json:"type"`
					Delta string `json:"delta"`
				} `json:"assistantMessageEvent"`
			}
			if json.Unmarshal(e.Data, &data) != nil {
				return
			}
			switch data.Delta.Type {
			case "text_delta":
				if r.child {
					return
				}
				if text == "" {
					progress("response_start", "", map[string]any{})
				}
				text += data.Delta.Delta
				progress("response_delta", data.Delta.Delta, map[string]any{"accumulated": text})
			case "thinking_delta":
				if thought == "" {
					progress("reasoning_chain_stream_start", " ", map[string]any{"streamId": reasonID()})
				}
				thought += data.Delta.Delta
				progress("reasoning_chain_stream_delta", data.Delta.Delta, map[string]any{"streamId": reasonID(), "accumulated": thought})
			}
		} else if e.Type == "auto_compaction_start" || e.Type == "auto_retry_start" || e.Type == "model_failover" {
			progress("progress", e.Type, map[string]any{})
		}
	}
	bind := func(control agentruntime.Control) func() {
		var undo []func()
		if !r.child {
			if registrar := agentRuntimeCancelRegistrarFromContext(ctx); registrar != nil {
				undo = append(undo, registrar(func(error) bool { return control.Cancel() == nil }))
			}
			if registrar := agentTurnLoopInterruptRegistrarFromContext(ctx); registrar != nil {
				undo = append(undo, registrar(func(note string) bool { return control.Steer(note) == nil }))
			}
		}
		return func() {
			for _, f := range undo {
				if f != nil {
					f()
				}
			}
		}
	}
	res, runErr := (agentruntime.Pi{NodePath: r.cfg.AgentRuntime.NodePath, WorkerPath: r.cfg.AgentRuntime.WorkerPath}).Run(ctx, req, invoke, emit, bind)
	if thought != "" {
		progress("reasoning_chain_stream_end", thought, map[string]any{"streamId": reasonID()})
	}
	if modelCalls > 0 {
		progress("eino_usage_summary", "Pi token usage summary", map[string]any{"model": oa.Model, "modelCalls": modelCalls, "promptTokens": promptTokens, "completionTokens": completionTokens, "totalTokens": promptTokens + completionTokens, "cachedTokens": cacheRead, "cacheWriteTokens": cacheWrite})
	}
	if res == nil {
		return nil, runErr
	}
	idsMu.Lock()
	out := &RunResult{Response: res.Response, LastAgentTraceInput: string(res.History), LastAgentTraceOutput: res.Response, MCPExecutionIDs: append([]string(nil), ids...), Status: res.Status}
	switch res.Status {
	case "iteration_limit":
		out.RuntimeStatus = "blocked"
		out.RuntimeCompletionReason = "iteration_limit"
	case "canceled":
		out.RuntimeStatus = "cancelled"
	case "failed":
		out.RuntimeStatus = "failed"
	}
	idsMu.Unlock()
	if r.child {
		progress("sub_agent_result", res.Response, map[string]any{"agentName": r.agentID})
	}
	return out, runErr
}
