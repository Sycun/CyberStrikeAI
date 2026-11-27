package multiagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/agentruntime"
	"cyberstrike-ai/internal/agents"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp/builtin"
	"cyberstrike-ai/internal/project"
	"cyberstrike-ai/internal/promptguard"
	"cyberstrike-ai/internal/reasoning"
	"cyberstrike-ai/internal/tooloutput"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type piRun struct {
	cfg                                               *config.Config
	ag                                                *agent.Agent
	db                                                *database.DB
	projectID                                         string
	conversationID, agentID, systemPrompt, sessionKey string
	systemPromptPrefix                                string
	history                                           []agent.ChatMessage
	roleTools                                         []string
	catalogAllow                                      map[string]bool
	availableCatalog                                  map[string]bool
	progress                                          func(string, string, interface{})
	reasoning                                         *reasoning.ClientIntent
	child                                             bool
	maxTurns                                          int
	extraTools                                        []agentruntime.Tool
	extraExecute                                      agentruntime.ToolExecutor
}

type chatImagesKey struct{}

func WithChatImages(ctx context.Context, images []agentruntime.Image) context.Context {
	if len(images) == 0 {
		return ctx
	}
	return context.WithValue(ctx, chatImagesKey{}, images)
}

func runPiSingle(ctx context.Context, cfg *config.Config, ag *agent.Agent, db *database.DB, logger *zap.Logger,
	conversationID, projectID, message string, history []agent.ChatMessage, roleTools []string,
	progress func(string, string, interface{}), intent *reasoning.ClientIntent, extra string) (*RunResult, error) {
	if ag == nil {
		return nil, fmt.Errorf("Pi agent is nil")
	}
	instruction := ag.SystemInstruction()
	prompt := project.AppendSystemPromptBlock(instruction, extra)
	prompt = project.AppendVisionImageAnalysisIfReady(prompt, cfg.Vision.Ready())
	return (&piRun{cfg: cfg, ag: ag, db: db, projectID: projectID, conversationID: conversationID, agentID: "main", systemPrompt: prompt,
		systemPromptPrefix: instruction, history: history, roleTools: roleTools, progress: progress, reasoning: intent}).run(ctx, message)
}

func piToolSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	// A nil slice would encode as null, which is not a valid required list.
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
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
		subs = []config.MultiAgentSubConfig{{ID: "executor", Description: "Execute one concrete plan step and return observed evidence", Instruction: ag.SystemInstruction(), RoleTools: roleTools}}
	}
	if len(subs) == 0 && !ma.WithoutGeneralSubAgent && mode != "supervisor" {
		subs = []config.MultiAgentSubConfig{{ID: "general", Description: "General task specialist", Instruction: ag.SystemInstruction(), RoleTools: roleTools}}
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
	instruction, _ := resolveMainOrchestratorInstruction(mode, ma, loaded)
	prompt := project.AppendSystemPromptBlock(instruction, extra)
	prompt += "\n\nAvailable specialists:\n" + strings.Join(catalog, "\n") + "\nUse task to delegate a concrete objective. Each child receives only the tools permitted for it and this conversation. Verify evidence before reporting completion."
	if mode == "plan_execute" {
		prompt += "\nCreate a concrete plan using the available task-board tools or a concise plan in text. Delegate one step at a time to executor using task, examine the result, update the plan, and replan when evidence requires it. Finish with the verified deliverable."
	}
	main := &piRun{cfg: cfg, ag: ag, db: db, projectID: projectID, conversationID: conversationID, agentID: "main", systemPrompt: prompt, systemPromptPrefix: instruction, history: history, roleTools: roleTools, progress: progress, reasoning: intent, sessionKey: mode}
	str := map[string]any{"type": "string"}
	main.extraTools = []agentruntime.Tool{
		{Name: "task", Description: "Delegate a concrete task to a named specialist and return its evidence-backed result", Parameters: piToolSchema(map[string]any{"subagent_type": str, "description": str}, "subagent_type", "description")},
		{Name: "write_todos", Description: "Record/update the execution plan (replaces the whole list). Items left pending or in_progress when you finish keep the run from completing; mark each item completed once done and drop items you abandon.", Parameters: piToolSchema(map[string]any{"todos": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"content": str, "status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}}}, "required": []string{"content", "status"}}}}, "todos")},
	}
	var idsMu sync.Mutex
	var childIDs []string
	var deliverable string
	if ma.WithoutWriteTodos {
		main.extraTools = main.extraTools[:1]
	}
	planLimitReached := false
	planCalls := 0
	planLimit := ma.PlanExecuteLoopMaxIterations
	if planLimit <= 0 {
		planLimit = 10
	}
	if mode == "plan_execute" {
		main.maxTurns = planLimit*3 + 3
		if cap := agentMaxIterations(cfg); cap < main.maxTurns {
			main.maxTurns = cap
		}
	}
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
			if cfg.MultiAgent.Runtime.PlantaskEnable && cfg.SkillsDir != "" {
				return main.executePiPlan(callCtx, call)
			}
			b, _ := json.Marshal(call.Arguments)
			if progress != nil {
				progress("plan_update", string(b), call.Arguments)
			}
			return agentruntime.ToolResult{Content: string(b)}, nil
		}
		id, _ := call.Arguments["subagent_type"].(string)
		if mode == "plan_execute" {
			idsMu.Lock()
			if planCalls >= planLimit {
				planLimitReached = true
				idsMu.Unlock()
				return agentruntime.ToolResult{Content: "Planning step budget reached. Existing results are preserved; further execution needs a new budget.", Terminate: true}, nil
			}
			planCalls++
			idsMu.Unlock()
		}
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
		parentNames := main.availableCatalog
		childPrompt := project.AppendSystemPromptBlock(sub.Instruction, extra) + "\n\nOriginal user task:\n" + message
		child := &piRun{cfg: cfg, ag: ag, db: db, projectID: projectID, conversationID: conversationID, agentID: id, systemPrompt: childPrompt, systemPromptPrefix: sub.Instruction, roleTools: tools, catalogAllow: parentNames, progress: progress, reasoning: intent, child: true, sessionKey: call.ID, maxTurns: sub.MaxIterations}
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
		if planLimitReached {
			res.RuntimeStatus = "blocked"
			res.RuntimeCompletionReason = "planning_iteration_limit"
		}
		if deliverable != "" {
			res.Response = deliverable
			res.LastAgentTraceOutput = deliverable
		}
		res.MCPExecutionIDs = append(res.MCPExecutionIDs, childIDs...)
		idsMu.Unlock()
	}
	return res, err
}

func piSystemPromptParts(full, instruction string, split, projectScope bool) (string, string) {
	if !split {
		return full, ""
	}
	prefix := strings.TrimSpace(instruction)
	tail, ok := strings.CutPrefix(full, prefix)
	if !ok || strings.Contains(prefix, "CSAI_DATA") {
		prefix, tail = "", full
	}
	if projectScope {
		if prefix == "" && tail != "" {
			tail = "\n\n" + tail
		}
		prefix = project.AppendSystemPromptBlock(prefix, project.ScopeAuthorizationPolicy)
	}
	return prefix, tail
}

func (r *piRun) run(ctx context.Context, message string) (*RunResult, error) {
	if extra, ok := ctx.Value(taskToolsKey{}).(taskTools); ok && !r.child {
		r.extraTools = append(r.extraTools, extra.definitions...)
		r.extraExecute = extra.execute
	}
	options, _ := ctx.Value(piOptionsKey{}).(PiRunOptions)
	defs := r.ag.ToolsForRole(r.roleTools)
	// MCP catalogs are backed by maps. Keep the tool prefix and the first N
	// always-visible tools stable across runs with the same catalog.
	slices.SortFunc(defs, func(a, b agent.Tool) int {
		return strings.Compare(a.Function.Name, b.Function.Name)
	})
	localDefs, localNames, skillPrompt, err := r.piLocalCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if skillPrompt != "" {
		if idx := strings.Index(r.systemPrompt, "[Data envelope (code-owned)]:"); idx > 0 {
			r.systemPrompt = strings.TrimRight(r.systemPrompt[:idx], "\n") + skillPrompt + "\n\n" + r.systemPrompt[idx:]
		} else {
			r.systemPrompt += skillPrompt
		}
		if r.systemPromptPrefix != "" && !strings.Contains(r.systemPromptPrefix, "CSAI_DATA") {
			r.systemPromptPrefix = strings.TrimSpace(r.systemPromptPrefix) + skillPrompt
		}
	}
	allowed := map[string]bool{}
	tools := make([]agentruntime.Tool, 0, len(defs)+len(r.extraTools))
	for _, d := range defs {
		if r.catalogAllow != nil && !r.catalogAllow[d.Function.Name] {
			continue
		}
		allowed[d.Function.Name] = true
		tools = append(tools, agentruntime.Tool{Name: d.Function.Name, Description: d.Function.Description, Parameters: d.Function.Parameters, OutputSchema: d.Function.OutputSchema, Annotations: d.Function.Annotations})
	}
	for _, t := range localDefs {
		if r.catalogAllow != nil && !r.catalogAllow[t.Name] {
			delete(localNames, t.Name)
			continue
		}
		if !allowed[t.Name] {
			tools = append(tools, t)
			allowed[t.Name] = true
		} else {
			delete(localNames, t.Name)
		}
	}
	extraNames := map[string]bool{}
	r.availableCatalog = allowed
	for _, t := range r.extraTools {
		if allowed[t.Name] {
			return nil, fmt.Errorf("Pi tool name collision: %s", t.Name)
		}
		extraNames[t.Name] = true
		tools = append(tools, t)
	}
	oa := r.cfg.OpenAI
	root := r.cfg.AgentSessionDir()
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(r.agentID+"\x00"+r.sessionKey)))
	if r.db != nil && strings.TrimSpace(r.conversationID) != "" {
		if err := r.db.SetAgentSessionDir(root); err != nil {
			return nil, fmt.Errorf("register Pi session root: %w", err)
		}
		revision, err := r.db.AgentSessionRevision(r.conversationID)
		if err != nil {
			return nil, fmt.Errorf("read Pi session revision: %w", err)
		}
		// A worker that was already in flight at deletion may publish its old file
		// again. Never resume that generation after the database history changes.
		if revision > 0 {
			key = fmt.Sprintf("%s-r%d", key, revision)
		}
	}
	history, err := piHistoryJSON(r.history)
	if err != nil {
		return nil, err
	}
	max := r.maxTurns
	if max <= 0 {
		max = agentMaxIterations(r.cfg)
	}
	runID := uuid.NewString()
	prefix, dynamic := piSystemPromptParts(r.systemPrompt, r.systemPromptPrefix,
		r.cfg.MultiAgent.Runtime.PromptCachePrefixSplit, r.cfg.Project.Enabled && strings.TrimSpace(r.projectID) != "")
	req := agentruntime.Request{RunID: runID, ConversationID: r.conversationID, SystemPrompt: prefix, SystemPromptDynamic: dynamic, Message: message, History: history,
		Model: piModelConfig(oa, r.reasoning, r.conversationID),
		Tools: tools, SessionDir: filepath.Join(root, agentruntime.ConversationStorageKey(r.conversationID), key), MaxTurns: max}
	if !r.child {
		req.Images, _ = ctx.Value(chatImagesKey{}).([]agentruntime.Image)
	}
	if strings.TrimSpace(r.conversationID) == "" {
		req.SessionDir = ""
	}
	req.NativeWorkflow = options.Native
	req.ToolSearch = r.cfg.MultiAgent.Runtime.ToolSearchEnable
	req.ToolSearchMinTools = r.cfg.MultiAgent.Runtime.ToolSearchMinTools
	req.AlwaysVisibleTools = append([]string(nil), r.cfg.MultiAgent.Runtime.ToolSearchAlwaysVisibleTools...)
	if options.Native == nil && r.cfg.MultiAgent.Runtime.ToolSearchIncludeBuiltinsEffective() {
		req.AlwaysVisibleTools = append(req.AlwaysVisibleTools, builtin.GetAllBuiltinTools()...)
	}
	visibleCount := r.cfg.MultiAgent.Runtime.ToolSearchAlwaysVisible
	if visibleCount <= 0 {
		visibleCount = 12
	}
	if oa.IsLocalContextProfile() && visibleCount > 6 {
		visibleCount = 6
	}
	for i, t := range tools {
		if i >= visibleCount {
			break
		}
		req.AlwaysVisibleTools = append(req.AlwaysVisibleTools, t.Name)
	}
	req.RetryMax = r.cfg.MultiAgent.Runtime.ModelRetryMaxRetries
	req.RetryMaxDelayMS = r.cfg.MultiAgent.Runtime.ModelRetryMaxBackoffSec * 1000
	for _, id := range r.cfg.MultiAgent.Runtime.ModelFailoverChannels {
		ch, _, ok := r.cfg.ResolveAIChannel(id)
		if !ok {
			return nil, fmt.Errorf("unknown fallback AI channel: %s", id)
		}
		if ch.Model == oa.Model && ch.BaseURL == oa.BaseURL && ch.Provider == oa.Provider {
			continue
		}
		fallback := piModelConfig(ch, r.reasoning, r.conversationID)
		req.FallbackModels = append(req.FallbackModels, fallback)
		if cap := r.cfg.MultiAgent.Runtime.ModelFailoverMaxRetries; cap > 0 && len(req.FallbackModels) >= cap {
			break
		}
	}
	var idsMu sync.Mutex
	var ids []string
	plan := make(piRunPlan)
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
		data["agentName"] = r.agentID
		data["agentRole"] = "orchestrator"
		if r.child {
			data["agentRole"] = "subagent"
		}
		r.progress(kind, text, data)
	}
	invoke := func(callCtx context.Context, call agentruntime.ToolCall) (reply agentruntime.ToolResult, runErr error) {
		if options.AuthorizeContext != nil {
			var err error
			callCtx, err = options.AuthorizeContext(callCtx, call)
			if err != nil {
				return agentruntime.ToolResult{}, err
			}
		}
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
		if options.BeforeTool != nil {
			if err := options.BeforeTool(callCtx, call); err != nil {
				return agentruntime.ToolResult{}, err
			}
		}
		if options.AfterTool != nil {
			defer func() {
				if runErr != nil {
					reply.IsError = true
					reply.Content = runErr.Error()
				}
				if err := options.AfterTool(callCtx, call, reply); err != nil {
					runErr = err
					reply.IsError = true
				}
			}()
		}
		progress("tool_call", "", map[string]any{"toolName": call.Name, "toolCallId": call.ID, "providerToolCallId": call.ProviderID, "arguments": call.Arguments})
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
				result = agentruntime.ToolResult{Content: res.Result, IsError: res.IsError || res.Blocked, ExecutionID: res.ExecutionID, StructuredContent: res.StructuredContent}
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
		if options.Native == nil && !r.child && !result.IsError && (localNames[call.Name] || (extraNames[call.Name] && call.Name == "write_todos")) {
			idsMu.Lock()
			plan.observe(call, result.Content)
			idsMu.Unlock()
		}
		if r.cfg.MultiAgent.Runtime.ReductionEnable {
			limit := r.cfg.MultiAgent.Runtime.ReductionMaxLengthForTruncEffective()
			opts := tooloutput.SpillOpts{RootDir: r.cfg.MultiAgent.Runtime.ReductionRootDir, ConversationID: r.conversationID, ExecutionID: result.ExecutionID}
			if oa.IsEconomyContextProfile() {
				// Automatic previews require an authorized way to read the full evidence.
				if localNames["read_file"] {
					result.Content = tooloutput.ReduceForModel(result.Content, limit, opts)
				}
			} else {
				result.Content = tooloutput.BoundWithSpill(result.Content, limit, opts)
			}
		}
		if r.cfg != nil {
			result.Content = promptguard.FrameToolResult(r.cfg.MultiAgent.Runtime.PromptGuardToolResultsEnabled(), result.Content)
		}
		progress("tool_result", result.Content, map[string]any{"toolName": call.Name, "toolCallId": call.ID, "executionId": result.ExecutionID, "result": result.Content, "success": !result.IsError, "isError": result.IsError, "agentFacing": true})
		return result, err
	}
	var text, thought, streamPrefix string
	var modelCalls, promptTokens, completionTokens, cacheRead, cacheWrite, reasoningTokens int
	reasonID := func() string { return fmt.Sprintf("%s-reasoning-%d", runID, iteration.Load()) }
	emit := func(e agentruntime.Event) {
		if options.Event != nil {
			options.Event(e)
		}
		if e.Type == "session_metrics" && options.Metrics != nil {
			var data map[string]any
			if json.Unmarshal(e.Data, &data) == nil {
				options.Metrics(data)
			}
		}
		if e.Type == "compaction_end" {
			var data struct {
				Result *struct {
					Summary string `json:"summary"`
				} `json:"result"`
				SessionFile string `json:"sessionFile"`
				EntryID     string `json:"sessionEntryId"`
			}
			if json.Unmarshal(e.Data, &data) == nil && data.Result != nil {
				key, err := savePiContextCheckpoint(r.db, r.cfg.Project, r.projectID, r.conversationID, r.agentID, data.Result.Summary, data.SessionFile, data.EntryID)
				if err != nil {
					progress("progress", "保存项目续接记录失败；Pi 会话记录仍保留", map[string]any{"kind": "context_checkpoint_error"})
				} else if key != "" {
					progress("context_checkpoint", "已保存项目续接记录", map[string]any{"factKey": key, "sessionFile": data.SessionFile, "sessionEntryId": data.EntryID})
				}
			}
		}
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
						Reasoning  int `json:"reasoning"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(e.Data, &d) == nil {
				modelCalls++
				promptTokens += d.Message.Usage.Input + d.Message.Usage.CacheRead + d.Message.Usage.CacheWrite
				completionTokens += d.Message.Usage.Output
				cacheRead += d.Message.Usage.CacheRead
				cacheWrite += d.Message.Usage.CacheWrite
				reasoningTokens += d.Message.Usage.Reasoning
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
				if streamPrefix == "" && text == "" {
					progress("response_start", "", map[string]any{})
				}
				text += data.Delta.Delta
				progress("response_delta", data.Delta.Delta, map[string]any{"accumulated": stitchOutputSegments(streamPrefix, text)})
			case "thinking_delta":
				if thought == "" {
					progress("reasoning_chain_stream_start", " ", map[string]any{"streamId": reasonID()})
				}
				thought += data.Delta.Delta
				progress("reasoning_chain_stream_delta", data.Delta.Delta, map[string]any{"streamId": reasonID(), "accumulated": thought})
			}
		} else if e.Type == "auto_retry_start" {
			var retry struct {
				Attempt      int    `json:"attempt"`
				MaxAttempts  int    `json:"maxAttempts"`
				DelayMS      int    `json:"delayMs"`
				ErrorMessage string `json:"errorMessage"`
			}
			if json.Unmarshal(e.Data, &retry) == nil {
				code, detail := piModelTransportFailure(retry.ErrorMessage)
				message := fmt.Sprintf("模型请求将在 %s 后重试（%d/%d）。", time.Duration(retry.DelayMS)*time.Millisecond, retry.Attempt, retry.MaxAttempts)
				if detail != "" {
					message += detail + "。"
				}
				progress("pi_model_retry", message, map[string]any{
					"attempt": retry.Attempt, "maxAttempts": retry.MaxAttempts, "delayMs": retry.DelayMS, "reasonCode": code,
				})
			}
		} else if e.Type == "compaction_start" || e.Type == "model_failover" {
			progress("progress", e.Type, map[string]any{})
		}
	}
	bind := func(control agentruntime.Control) func() {
		var undo []func()
		if options.Bind != nil {
			undo = append(undo, options.Bind(control))
		}
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
	runSegment := func(segmentCtx context.Context, segmentMessage string, segmentHistory json.RawMessage, continuation bool) (*RunResult, error) {
		if err := segmentCtx.Err(); err != nil {
			return nil, err
		}
		req.Message = segmentMessage
		if len(segmentHistory) > 0 {
			req.History = segmentHistory
		}
		if continuation {
			req.Images = nil
		}
		res, runErr := (agentruntime.Pi{NodePath: r.cfg.AgentRuntime.NodePath, WorkerPath: r.cfg.AgentRuntime.WorkerPath}).Run(segmentCtx, req, invoke, emit, bind)
		reasoning := thought
		if thought != "" {
			progress("reasoning_chain_stream_end", thought, map[string]any{"streamId": reasonID()})
			thought = ""
		}
		text = ""
		if res == nil {
			return nil, runErr
		}
		idsMu.Lock()
		out := piResultFromRuntime(res, ids, plan)
		idsMu.Unlock()
		if strings.TrimSpace(out.Response) != "" || strings.TrimSpace(reasoning) != "" {
			out.LastAgentTraceInput = ensureTruncatedAssistantHistory(out.LastAgentTraceInput, out.Response, reasoning)
		}
		out.LastAgentTraceInput = tagOutputContinuationPrompts(out.LastAgentTraceInput)
		return out, runErr
	}
	out, runErr := runSegment(ctx, message, nil, false)
	if out != nil && runErr == nil {
		maxAttempts := OutputTruncationContinueMaxAttemptsFromConfig(&r.cfg.MultiAgent.Runtime)
		confirmed := out.Response
		out, runErr = continueAfterOutputTruncation(ctx, maxAttempts, out, func(segmentCtx context.Context, attempt int, history, instruction string) (*RunResult, error) {
			// 续写段的 delta 只有本段增量；先记下已拼接正文，前端才不会把前半段盖掉。
			streamPrefix = confirmed
			seg, err := runSegment(segmentCtx, instruction, json.RawMessage(history), true)
			if seg != nil {
				confirmed = stitchOutputSegments(confirmed, seg.Response)
			}
			return seg, err
		}, func(attempt, maxAttempts int) {
			progress("agent_output_truncated_continue", fmt.Sprintf("模型输出达到长度上限，正在第 %d/%d 次自动续写…", attempt, maxAttempts), map[string]any{
				"attempt":          attempt,
				"maxAttempts":      maxAttempts,
				"completionReason": ReasonModelOutputTruncated,
				"contextInjection": true,
				"contextPersisted": false,
			})
		})
	}
	if modelCalls > 0 {
		progress("agent_usage_summary", "Pi token usage summary", map[string]any{
			"model": oa.Model, "modelCalls": modelCalls,
			"promptTokens": promptTokens, "completionTokens": completionTokens, "totalTokens": promptTokens + completionTokens,
			"cachedTokens": cacheRead, "cacheWriteTokens": cacheWrite, "reasoningTokens": reasoningTokens,
		})
	}
	if r.child && out != nil {
		progress("agent_reply", out.Response, map[string]any{"agentName": r.agentID})
	}
	return out, runErr
}

func piResultFromRuntime(res *agentruntime.Result, ids []string, plan piRunPlan) *RunResult {
	out := &RunResult{Response: res.Response, LastAgentTraceInput: string(res.History), LastAgentTraceOutput: res.Response, MCPExecutionIDs: append([]string(nil), ids...), Status: res.Status}
	switch res.Status {
	case "completed":
		out.RuntimeStatus = "completed"
		if steps := plan.unfinishedSteps(); len(steps) > 0 {
			out.RuntimeStatus = "blocked"
			out.RuntimeCompletionReason = ReasonUnfinishedPlan
			out.UnfinishedPlanSteps = steps
		}
	case "output_truncated":
		out.RuntimeStatus = "blocked"
		out.RuntimeCompletionReason = ReasonModelOutputTruncated
	case "iteration_limit":
		out.RuntimeStatus = "blocked"
		out.RuntimeCompletionReason = "iteration_limit"
	case "canceled":
		out.RuntimeStatus = "cancelled"
	case "failed":
		out.RuntimeStatus = "failed"
	}
	return out
}
