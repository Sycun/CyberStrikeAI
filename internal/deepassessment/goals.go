package deepassessment

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"cyberstrike-ai/internal/project"
	"cyberstrike-ai/internal/promptguard"
)

const (
	maxAssessmentGoals       = 32
	maxAssessmentConstraints = 32
)

var deicticTargetRe = regexp.MustCompile(`(?i)(当前目标|本目标|该目标|当前站点|本站点|本站|该站点|当前域名|当前主机|current\s+target|this\s+target|this\s+site|this\s+host)`)

var proceduralGoalForbiddenPrefix = []string{
	"收集信息", "信息收集", "端口扫描", "目录扫描", "子域名枚举", "指纹识别",
	"run nmap", "port scan", "reconnaissance", "enumerate endpoints",
}

// ResolveDeicticTargets replaces deictic target references ("当前目标", "本站", etc.)
// with concrete declared target hosts so extracted constraints stay self-contained.
func ResolveDeicticTargets(text string, targets []string) string {
	text = strings.Join(strings.Fields(promptguard.Neutralize(text)), " ")
	if text == "" || len(targets) == 0 {
		return text
	}
	clean := make([]string, 0, len(targets))
	seen := map[string]bool{}
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return text
	}
	replacement := strings.Join(clean, ", ")
	return deicticTargetRe.ReplaceAllString(text, replacement)
}

func isProceduralNonGoal(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range proceduralGoalForbiddenPrefix {
		if prefix[0] >= 0x80 {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
			continue
		}
		if lower == prefix || strings.HasPrefix(lower, prefix+" ") || strings.HasPrefix(lower, prefix+":") {
			return true
		}
	}
	return false
}

// SaveGoals stores decomposed verifiable goals for a task and bumps its change version.
// It never alters Task.Criteria.
func (s *Store) SaveGoals(taskID string, goals []AssessmentGoal) error {
	if len(goals) == 0 || len(goals) > maxAssessmentGoals {
		return fmt.Errorf("goals must contain 1 to %d items", maxAssessmentGoals)
	}
	normalized := make([]AssessmentGoal, 0, len(goals))
	seen := map[string]bool{}
	for i, g := range goals {
		id := strings.TrimSpace(g.ID)
		if id == "" {
			id = fmt.Sprintf("g%d", i+1)
		}
		if len(id) > 64 || strings.ContainsAny(id, " \t\r\n/\\") || seen[id] {
			return fmt.Errorf("invalid or duplicate goal_id: %s", id)
		}
		seen[id] = true
		text := strings.Join(strings.Fields(promptguard.Neutralize(g.Text)), " ")
		if text == "" || utf8.RuneCountInString(text) > 2000 {
			return fmt.Errorf("goal %s text is required (max 2000 characters)", id)
		}
		if isProceduralNonGoal(text) {
			return fmt.Errorf("goal %s describes a reconnaissance step rather than a verifiable deliverable", id)
		}
		risk := strings.Join(strings.Fields(promptguard.Neutralize(g.RiskClass)), " ")
		if utf8.RuneCountInString(risk) > 128 {
			return fmt.Errorf("goal %s risk_class exceeds 128 characters", id)
		}
		normalized = append(normalized, AssessmentGoal{ID: id, Text: text, RiskClass: risk})
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM deep_assessment_goals WHERE task_id=?`, taskID); err != nil {
		return err
	}
	for _, g := range normalized {
		if _, err := tx.Exec(`INSERT INTO deep_assessment_goals(task_id,goal_id,text,risk_class) VALUES(?,?,?,?)`,
			taskID, g.ID, g.Text, g.RiskClass); err != nil {
			return err
		}
	}
	if err := Signal(tx, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveConstraints replaces extracted ("goals") constraints while preserving
// operator-supplied ("human"/"user") constraints.
func (s *Store) SaveConstraints(taskID, origin string, constraints []AssessmentConstraint, targets []string) error {
	if origin == "" {
		origin = "goals"
	}
	if origin != "goals" && origin != "human" && origin != "user" {
		return fmt.Errorf("invalid constraint origin")
	}
	if len(constraints) > maxAssessmentConstraints {
		return fmt.Errorf("at most %d constraints allowed", maxAssessmentConstraints)
	}
	normalized := make([]AssessmentConstraint, 0, len(constraints))
	seen := map[string]bool{}
	for i, c := range constraints {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			id = fmt.Sprintf("%s-%d", origin, i+1)
		}
		if len(id) > 64 || strings.ContainsAny(id, " \t\r\n/\\") || seen[id] {
			return fmt.Errorf("invalid or duplicate constraint_id: %s", id)
		}
		seen[id] = true
		kind := strings.ToLower(strings.TrimSpace(c.Kind))
		if kind != "allow" && kind != "deny" {
			return fmt.Errorf("constraint %s kind must be allow or deny", id)
		}
		text := ResolveDeicticTargets(c.Text, targets)
		if text == "" || utf8.RuneCountInString(text) > 1000 {
			return fmt.Errorf("constraint %s text is required (max 1000 characters)", id)
		}
		normalized = append(normalized, AssessmentConstraint{ID: id, Kind: kind, Text: text, Origin: origin})
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM deep_assessment_constraints WHERE task_id=? AND origin=?`, taskID, origin); err != nil {
		return err
	}
	for _, c := range normalized {
		if _, err := tx.Exec(`INSERT INTO deep_assessment_constraints(task_id,constraint_id,kind,text,origin) VALUES(?,?,?,?,?)
			ON CONFLICT(task_id,constraint_id) DO UPDATE SET kind=excluded.kind,text=excluded.text,origin=excluded.origin`,
			taskID, c.ID, c.Kind, c.Text, c.Origin); err != nil {
			return err
		}
	}
	if err := Signal(tx, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

// EffectiveNoiseProfile returns the task's override when set, otherwise the
// linked project's noise_profile from scope_json.
func EffectiveNoiseProfile(taskNoise, projectScopeJSON string) string {
	if n := strings.TrimSpace(taskNoise); n != "" {
		if n == "off" {
			return ""
		}
		return n
	}
	var payload struct {
		NoiseProfile string `json:"noise_profile"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(projectScopeJSON)), &payload) == nil {
		if n := strings.TrimSpace(payload.NoiseProfile); n != "" && n != "off" {
			return n
		}
	}
	return ""
}

// RenderTaskExecutionPolicy builds the code-owned task constraints and noise
// profile block without mutating project scope_json. Task constraints override
// same-text project constraints.
func RenderTaskExecutionPolicy(t *Task, goals []AssessmentGoal, constraints []AssessmentConstraint, projectScopeJSON string) string {
	var b strings.Builder
	if len(goals) > 0 {
		b.WriteString("\n[测评可验证子目标（不替代原始验收条件 Criteria）]\n")
		for _, g := range goals {
			if g.RiskClass != "" {
				fmt.Fprintf(&b, "- [%s | risk=%s] %s\n", g.ID, g.RiskClass, g.Text)
			} else {
				fmt.Fprintf(&b, "- [%s] %s\n", g.ID, g.Text)
			}
		}
	}
	if len(constraints) > 0 {
		b.WriteString("\n[测评任务级操作约束（优先级高于同文项目默认约束；deny 优先）]\n")
		for _, c := range constraints {
			fmt.Fprintf(&b, "- [%s/%s] %q\n", c.Kind, c.Origin, c.Text)
		}
	}
	if noise := EffectiveNoiseProfile(t.NoiseProfile, projectScopeJSON); noise != "" {
		b.WriteString(project.BuildNoiseProfileBlock(noise))
	}
	return b.String()
}
