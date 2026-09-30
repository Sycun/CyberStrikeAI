package handler

import (
	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/config"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// HitlPolicy is the approval-*configuration* surface: the endpoints an operator drives from
// the sidebar to say how approvals should behave - per-conversation config, the global
// no-approval tool whitelist, the default mode/reviewer/timeout, the audit-agent prompts.
//
// It is deliberately split from the other two halves of HITL, because they change at
// completely different moments:
//
//   - HITLQueue - what is pending right now, and the audit log of past decisions;
//   - the agent run path - waiting for a decision inside a tool call, which shares task,
//     session and SSE state with the loop and must not be moved out of it;
//   - this type - what a decision rule *is*, which is edited while nothing is running.
//
// Before this split those eleven endpoints were methods on AgentHandler and read whatever
// they liked out of its forty fields. Now the state they need from the running agent is
// named (hitlPolicyState), so adding an endpoint that quietly reaches into the loop's state
// stops being something nobody notices.
type HitlPolicy struct {
	state   hitlPolicyState
	manager *HITLManager
	queue   *HITLQueue
	savers  hitlConfigSavers
	logger  *zap.Logger
	audit   *audit.Service
}

// hitlPolicyState is the part of the live configuration the policy reads but does not own:
// the effective defaults are a merge of config.yaml and the published runtime snapshot, and
// the run loop consumes the same merge, so it stays on the agent that owns both.
type hitlPolicyState interface {
	hitlEffectiveDefaultMode() string
	hitlEffectiveDefaultReviewer() string
	hitlEffectiveDefaultTimeoutSeconds() int
	hitlConfigGlobalToolWhitelist() []string
	hitlAuditEngineInfo() (backend, model string)
	hitlRequestWithMergedConfigWhitelist(req *HITLRequest) *HITLRequest
	loadHITLConversationConfig(conversationID string) (*HITLRequest, error)
	publishHitl(mutate func(hitl *config.HitlConfig))
	hitlSnapshot() config.HitlConfig
	settingsConfigured() bool
}

// NewHitlPolicy takes the state, the manager and the queue: everything the approval
// configuration endpoints act on. The audit service and the persistence channels are
// injected afterwards by the assembly, and AgentHandler forwards both here rather than
// keeping a second copy - a stale copy is how an endpoint starts writing an audit record
// that no longer exists.
func NewHitlPolicy(state hitlPolicyState, manager *HITLManager, queue *HITLQueue, logger *zap.Logger) *HitlPolicy {
	return &HitlPolicy{state: state, manager: manager, queue: queue, logger: logger}
}

func (p *HitlPolicy) setAudit(svc *audit.Service) {
	if p != nil {
		p.audit = svc
	}
}

func (p *HitlPolicy) setSavers(savers hitlConfigSavers) {
	if p != nil {
		p.savers = savers
	}
}

func (p *HitlPolicy) logWarn(message string, err error) {
	if p != nil && p.logger != nil {
		p.logger.Warn(message, zap.Error(err))
	}
}

func (p *HitlPolicy) record(c *gin.Context, action, message, resourceID string) {
	if p == nil || p.audit == nil {
		return
	}
	p.audit.RecordOK(c, "hitl", action, message, "hitl_config", resourceID, nil)
}

// defaultConfigResponse is the body every default-config endpoint answers with, so the
// sidebar cannot be told one default while another endpoint reports something else.
func (p *HitlPolicy) defaultConfigResponse() gin.H {
	backend, model := p.state.hitlAuditEngineInfo()
	return gin.H{
		"defaultMode":             p.state.hitlEffectiveDefaultMode(),
		"defaultReviewer":         p.state.hitlEffectiveDefaultReviewer(),
		"defaultTimeoutSeconds":   p.state.hitlEffectiveDefaultTimeoutSeconds(),
		"hitlGlobalToolWhitelist": p.state.hitlConfigGlobalToolWhitelist(),
		"auditBackend":            backend,
		"auditModel":              model,
	}
}

// GetConversationConfig answers GET /hitl/config/:conversationId.
func (p *HitlPolicy) GetConversationConfig(c *gin.Context) {
	conversationID := strings.TrimSpace(c.Param("conversationId"))
	if conversationID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversationId is required"})
		return
	}
	if p.queue == nil || !p.queue.hitlConversationAllowed(c, conversationID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该资源"})
		return
	}
	cfg, err := p.state.loadHITLConversationConfig(conversationID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !hitlStoredConfigEffective(cfg) {
		if pendMode, ok := p.manager.PendingHITLInterruptMode(conversationID); ok {
			cfg2 := *cfg
			cfg2.Enabled = true
			cfg2.Mode = normalizeHitlMode(pendMode)
			if cfg2.TimeoutSeconds < 0 {
				cfg2.TimeoutSeconds = 0
			}
			cfg = &cfg2
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"conversationId":          conversationID,
		"hitl":                    cfg,
		"defaultMode":             p.state.hitlEffectiveDefaultMode(),
		"defaultReviewer":         p.state.hitlEffectiveDefaultReviewer(),
		"defaultTimeoutSeconds":   p.state.hitlEffectiveDefaultTimeoutSeconds(),
		"hitlGlobalToolWhitelist": p.state.hitlConfigGlobalToolWhitelist(),
	})
}

// UpsertConversationConfig answers PUT /hitl/config, and activates the conversation in the
// same call: a saved-but-not-activated config is the defect where the sidebar shows the new
// rule while the running conversation still applies the old one.
func (p *HitlPolicy) UpsertConversationConfig(c *gin.Context) {
	var req hitlConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if p.queue == nil || !p.queue.hitlConversationAllowed(c, req.ConversationID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该资源"})
		return
	}
	req.Mode = normalizeHitlMode(req.Mode)
	req.Reviewer = normalizeHitlReviewer(req.Reviewer)
	if strings.TrimSpace(req.Reviewer) == "" {
		req.Reviewer = p.state.hitlEffectiveDefaultReviewer()
	}
	if err := p.manager.SaveConversationConfig(req.ConversationID, &req.HITLRequest); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if p.savers.whitelist != nil && len(req.SensitiveTools) > 0 {
		if err := p.savers.whitelist.MergeHitlToolWhitelistIntoConfig(req.SensitiveTools); err != nil {
			p.logWarn("HITL 会话配置已保存，但合并工具白名单到 config.yaml 失败", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "会话配置已保存，但写入 config.yaml 失败: " + err.Error(),
			})
			return
		}
	}
	p.manager.ActivateConversation(req.ConversationID, p.state.hitlRequestWithMergedConfigWhitelist(&req.HITLRequest))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GetGlobalToolWhitelist answers GET /hitl/tool-whitelist.
func (p *HitlPolicy) GetGlobalToolWhitelist(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"toolWhitelist":   p.state.hitlConfigGlobalToolWhitelist(),
		"defaultReviewer": p.state.hitlEffectiveDefaultReviewer(),
	})
}

// SetGlobalToolWhitelist answers PUT /hitl/tool-whitelist: whole-table replacement.
func (p *HitlPolicy) SetGlobalToolWhitelist(c *gin.Context) {
	if p.savers.whitelist == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "HITL 配置持久化不可用"})
		return
	}
	var req setHitlGlobalWhitelistReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := p.savers.whitelist.SetHitlToolWhitelist(req.ToolWhitelist); err != nil {
		p.logWarn("写入 HITL 工具白名单到 config.yaml 失败", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	p.record(c, "tool_whitelist_update", "HITL 全局白名单更新", "tool_whitelist")
	c.JSON(http.StatusOK, gin.H{
		"ok":                        true,
		"toolWhitelist":             p.state.hitlConfigGlobalToolWhitelist(),
		"hitlGlobalToolWhitelist":   p.state.hitlConfigGlobalToolWhitelist(),
		"hitlGlobalWhitelistMerged": false,
	})
}

// MergeGlobalToolWhitelist answers POST /hitl/tool-whitelist: the sidebar sends the tools it
// just exempted without a conversation selected, so they merge into config.yaml instead of
// replacing it - the same rule PUT /hitl/config applies to a conversation's whitelist.
func (p *HitlPolicy) MergeGlobalToolWhitelist(c *gin.Context) {
	if p.savers.whitelist == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "HITL 配置持久化不可用"})
		return
	}
	var req mergeHitlGlobalWhitelistReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.SensitiveTools) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"ok":                      true,
			"hitlGlobalToolWhitelist": p.state.hitlConfigGlobalToolWhitelist(),
		})
		return
	}
	if err := p.savers.whitelist.MergeHitlToolWhitelistIntoConfig(req.SensitiveTools); err != nil {
		p.logWarn("合并 HITL 工具白名单到 config.yaml 失败", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":                        true,
		"hitlGlobalToolWhitelist":   p.state.hitlConfigGlobalToolWhitelist(),
		"hitlGlobalWhitelistMerged": true,
	})
}

// GetDefaultConfig answers GET /hitl/default-config.
func (p *HitlPolicy) GetDefaultConfig(c *gin.Context) {
	c.JSON(http.StatusOK, p.defaultConfigResponse())
}

// UpdateDefaultConfig answers PUT /hitl/default-config: write config.yaml first, then publish
// the snapshot, so a crash between the two leaves the file authoritative rather than the
// memory.
func (p *HitlPolicy) UpdateDefaultConfig(c *gin.Context) {
	if p.savers.defaultReviewer == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "HITL 配置持久化不可用"})
		return
	}
	var req setHitlDefaultConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	mode := normalizeHitlDefaultMode(req.Mode)
	reviewer := normalizeHitlReviewer(req.Reviewer)
	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds < 0 {
		timeoutSeconds = 0
	}
	if err := p.savers.defaultReviewer.UpdateHitlDefaultConfig(mode, reviewer, timeoutSeconds); err != nil {
		p.logWarn("写入 HITL 默认配置到 config.yaml 失败", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	publishedTimeout := timeoutSeconds
	p.state.publishHitl(func(hitl *config.HitlConfig) {
		hitl.DefaultMode = mode
		hitl.DefaultReviewer = reviewer
		hitl.DefaultTimeoutSeconds = &publishedTimeout
	})
	p.record(c, "default_config_update", "HITL 全局默认配置更新", "default")
	out := p.defaultConfigResponse()
	out["ok"] = true
	c.JSON(http.StatusOK, out)
}

// GetDefaultReviewer answers GET /hitl/default-reviewer.
func (p *HitlPolicy) GetDefaultReviewer(c *gin.Context) {
	c.JSON(http.StatusOK, p.defaultConfigResponse())
}

// UpdateDefaultReviewer answers PUT /hitl/default-reviewer: switching who approves while no
// conversation is selected.
func (p *HitlPolicy) UpdateDefaultReviewer(c *gin.Context) {
	if p.savers.defaultReviewer == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "HITL 配置持久化不可用"})
		return
	}
	var req setHitlDefaultReviewerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	reviewer := normalizeHitlReviewer(req.Reviewer)
	if err := p.savers.defaultReviewer.UpdateHitlDefaultReviewer(reviewer); err != nil {
		p.logWarn("写入 HITL 默认审批方到 config.yaml 失败", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	p.state.publishHitl(func(hitl *config.HitlConfig) {
		hitl.DefaultReviewer = reviewer
	})
	p.record(c, "default_reviewer_update", "HITL 全局默认审批方更新", "default_reviewer")
	out := p.defaultConfigResponse()
	out["ok"] = true
	c.JSON(http.StatusOK, out)
}

// GetAuditStrategy answers GET /hitl/audit-strategy: the audit agent's prompts, with the
// shipped default alongside so the sidebar can show what an edit replaced.
func (p *HitlPolicy) GetAuditStrategy(c *gin.Context) {
	approvalPrompt := config.DefaultHitlAuditAgentPrompt()
	reviewEditPrompt := config.DefaultHitlAuditAgentPromptReviewEdit()
	approvalCustom := false
	reviewEditCustom := false
	if p.state.settingsConfigured() {
		snapshot := p.state.hitlSnapshot()
		approvalPrompt = snapshot.EffectiveAuditAgentPromptForMode("approval")
		reviewEditPrompt = snapshot.EffectiveAuditAgentPromptForMode("review_edit")
		approvalCustom = strings.TrimSpace(snapshot.AuditAgentPrompt) != ""
		reviewEditCustom = strings.TrimSpace(snapshot.AuditAgentPromptReviewEdit) != ""
	}
	c.JSON(http.StatusOK, gin.H{
		"auditAgentPrompt":                  approvalPrompt,
		"auditAgentPromptCustom":            approvalCustom,
		"auditAgentPromptReviewEdit":        reviewEditPrompt,
		"auditAgentPromptReviewEditCustom":  reviewEditCustom,
		"defaultAuditAgentPrompt":           config.DefaultHitlAuditAgentPrompt(),
		"defaultAuditAgentPromptReviewEdit": config.DefaultHitlAuditAgentPromptReviewEdit(),
	})
}

// UpdateAuditStrategy answers PUT /hitl/audit-strategy.
func (p *HitlPolicy) UpdateAuditStrategy(c *gin.Context) {
	if p.savers.strategy == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "HITL 策略持久化不可用"})
		return
	}
	var req hitlAuditStrategyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	approvalPrompt := strings.TrimSpace(req.AuditAgentPrompt)
	reviewEditPrompt := strings.TrimSpace(req.AuditAgentPromptReviewEdit)
	if err := p.savers.strategy.UpdateHitlAuditAgentStrategy(approvalPrompt, reviewEditPrompt); err != nil {
		p.logWarn("保存审计 Agent 提示词失败", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	p.record(c, "audit_strategy_update", "HITL 审计策略更新", "audit_agent_prompt")
	p.state.publishHitl(func(hitl *config.HitlConfig) {
		hitl.AuditAgentPrompt = approvalPrompt
		hitl.AuditAgentPromptReviewEdit = reviewEditPrompt
	})
	c.JSON(http.StatusOK, gin.H{
		"ok":                               true,
		"auditAgentPrompt":                 config.HitlConfig{AuditAgentPrompt: approvalPrompt}.EffectiveAuditAgentPromptForMode("approval"),
		"auditAgentPromptCustom":           approvalPrompt != "",
		"auditAgentPromptReviewEdit":       config.HitlConfig{AuditAgentPromptReviewEdit: reviewEditPrompt}.EffectiveAuditAgentPromptForMode("review_edit"),
		"auditAgentPromptReviewEditCustom": reviewEditPrompt != "",
	})
}
