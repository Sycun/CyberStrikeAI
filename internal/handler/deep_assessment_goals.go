package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/agentruntime"
	"cyberstrike-ai/internal/deepassessment"
	"cyberstrike-ai/internal/multiagent"
	"github.com/google/uuid"
)

func (r *deepAssessmentRuntime) declaredTargetNames(t *deepassessment.Task) []string {
	var out []string
	seen := map[string]bool{}
	for _, hit := range r.h.declaredAssessmentHits(t) {
		name := hit.Domain
		if name == "" {
			name = hit.IP
		}
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func (r *deepAssessmentRuntime) Decompose(ctx context.Context, t *deepassessment.Task) (string, error) {
	if _, err := r.principal(t); err != nil {
		return "", err
	}
	cfg, err := r.configuration(t)
	if err != nil {
		return "", err
	}
	targets := r.declaredTargetNames(t)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		question, called, runErr := r.runDecomposeOnce(ctx, cfg, t, targets)
		if runErr == nil && called {
			return question, nil
		}
		if runErr != nil && errors.Is(runErr, deepassessment.ErrRetryable) {
			return "", runErr
		}
		if runErr != nil {
			lastErr = runErr
		} else {
			lastErr = fmt.Errorf("未调用 set_goals 结构化工具")
		}
	}
	// Graceful fallback: when the user already wrote a concrete objective, keep
	// the raw objective/criteria and proceed to planning without blocking the task.
	if strings.TrimSpace(t.Objective) != "" {
		_ = r.h.deepAssessments.Store.Change(t.ID, "", "goals_fallback",
			"目标拆解未返回结构化工具调用，已保留原始目标直接进入规划（不启用必测风险类自动门禁）："+deepassessment.Clip(lastErr.Error(), 300),
			func(*deepassessment.Task) error { return nil })
		return "", nil
	}
	return "请补充明确的可核验测评目标与范围后再继续。", nil
}

func (r *deepAssessmentRuntime) runDecomposeOnce(ctx context.Context, cfg anyConfig, t *deepassessment.Task, targets []string) (string, bool, error) {
	runCfg, err := r.configuration(t)
	if err != nil {
		return "", false, err
	}
	_ = cfg
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	var goalsSaved bool
	var questionOut string
	defs := []agentruntime.Tool{
		{
			Name:        "set_constraints",
			Description: "提取用户在目标或补充限制中显式声明的 allow/deny 操作边界（可选，无显式边界时不调用；禁止凭空编造）。",
			Exposure:    "direct",
			Parameters: assessmentObject(map[string]any{
				"constraints": map[string]any{
					"type": "array",
					"items": assessmentObject(map[string]any{
						"id":   assessmentString(),
						"kind": map[string]any{"type": "string", "enum": []string{"allow", "deny"}},
						"text": assessmentString(),
					}, "kind", "text"),
				},
			}, "constraints"),
		},
		{
			Name:        "set_goals",
			Description: "提交可独立核验的最终交付子目标（禁止将信息收集、端口扫描、枚举过程写成子目标）。目标完全缺失或严重歧义时填 question。",
			Exposure:    "direct",
			Parameters: assessmentObject(map[string]any{
				"goals": map[string]any{
					"type": "array",
					"items": assessmentObject(map[string]any{
						"id":         assessmentString(),
						"text":       assessmentString(),
						"risk_class": assessmentString(),
					}, "text"),
				},
				"question": assessmentString(),
			}),
		},
	}

	execute := func(_ context.Context, call agentruntime.ToolCall) (agentruntime.ToolResult, error) {
		if _, err := r.principal(t); err != nil {
			return agentruntime.ToolResult{}, err
		}
		raw, err := json.Marshal(call.Arguments)
		if err != nil {
			return agentruntime.ToolResult{}, err
		}
		switch call.Name {
		case "set_constraints":
			var input struct {
				Constraints []struct {
					ID   string `json:"id"`
					Kind string `json:"kind"`
					Text string `json:"text"`
				} `json:"constraints"`
			}
			if err := decodeDeepAssessmentJSON(string(raw), &input); err != nil {
				return agentruntime.ToolResult{}, err
			}
			items := make([]deepassessment.AssessmentConstraint, 0, len(input.Constraints))
			for _, item := range input.Constraints {
				items = append(items, deepassessment.AssessmentConstraint{ID: item.ID, Kind: item.Kind, Text: item.Text, Origin: "goals"})
			}
			if err := r.h.deepAssessments.Store.SaveConstraints(t.ID, "goals", items, targets); err != nil {
				return agentruntime.ToolResult{}, err
			}
			return agentruntime.ToolResult{Content: "Constraints saved.", StructuredContent: call.Arguments}, nil
		case "set_goals":
			var input struct {
				Goals []struct {
					ID        string `json:"id"`
					Text      string `json:"text"`
					RiskClass string `json:"risk_class"`
				} `json:"goals"`
				Question string `json:"question"`
			}
			if err := decodeDeepAssessmentJSON(string(raw), &input); err != nil {
				return agentruntime.ToolResult{}, err
			}
			q := strings.TrimSpace(input.Question)
			if q != "" && len(input.Goals) == 0 {
				questionOut = deepassessment.Clip(q, 4000)
				goalsSaved = true
				return agentruntime.ToolResult{Content: "Clarification requested.", Terminate: true}, nil
			}
			items := make([]deepassessment.AssessmentGoal, 0, len(input.Goals))
			for _, g := range input.Goals {
				items = append(items, deepassessment.AssessmentGoal{ID: g.ID, Text: g.Text, RiskClass: g.RiskClass})
			}
			if err := r.h.deepAssessments.Store.SaveGoals(t.ID, items); err != nil {
				return agentruntime.ToolResult{}, err
			}
			goalsSaved = true
			return agentruntime.ToolResult{Content: "Goals saved.", StructuredContent: call.Arguments, Terminate: true}, nil
		default:
			return agentruntime.ToolResult{}, fmt.Errorf("unknown tool: %s", call.Name)
		}
	}

	instruction := `你是授权安全深度测评的目标与操作边界拆解器。只识别最终要核验的可交付结果与用户显式规则，不规划探测步骤。
1. 若用户在「目标」或「补充限制与排除项」中明确写了允许/禁止操作（如“禁止口令爆破”、“仅测 443 端口”、“排除支付接口”），先调用 set_constraints 登记（kind 为 allow 或 deny）；把“当前目标/本站/该站点”等指代词替换成具体目标主机；绝不编造用户未提及的限制；若无显式操作限制则跳过 set_constraints。
2. 调用 set_goals 提交最终可独立验证的交付子目标：
   - 严禁把“信息收集、资产探测、端口扫描、目录枚举”等中间过程写成子目标；
   - 若对应明确风险类别，在 risk_class 标注标准编号或类别（如 CWE-89、WSTG-ATHZ-02、idor、sqli），综合/业务逻辑目标可留空；
   - 不改写、不扩充用户未声明的目标。`
	contextBlock := fmt.Sprintf("目标:\n%s\n\n补充限制与排除项:\n%s\n\n原始验收条件（保持原样不改写）:\n%s\n\n已识别目标主机: %s",
		t.Objective, t.Scope, t.Criteria, strings.Join(targets, ", "))

	var metricsErr error
	res, err := multiagent.RunNativeControl(callCtx, runCfg, t.ID+"/goals/"+uuid.NewString(),
		instruction+"\n必须通过调用 set_goals 结束，不要直接输出纯文本。",
		agentruntime.NativeWorkflow{Context: contextBlock, CompletionTool: "set_goals"}, defs, execute, func(e agentruntime.Event) {
			if e.Type == "session_metrics" {
				var data map[string]any
				if json.Unmarshal(e.Data, &data) == nil {
					if x := r.h.deepAssessments.Store.RecordMetrics(t.ID, "goals", data); x != nil {
						metricsErr = x
						cancel()
					}
				}
			}
		})
	if metricsErr != nil {
		return "", false, metricsErr
	}
	if err != nil {
		return "", false, assessmentRuntimeError(err)
	}
	if res != nil && res.Error != "" && !goalsSaved {
		return "", false, assessmentRuntimeError(errors.New(res.Error))
	}
	return questionOut, goalsSaved, nil
}

type anyConfig = any
