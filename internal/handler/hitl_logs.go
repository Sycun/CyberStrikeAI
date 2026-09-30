package handler

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"github.com/gin-gonic/gin"
)

func normalizeHitlReviewer(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "audit_agent", "agent", "ai":
		return "audit_agent"
	default:
		return "human"
	}
}

func normalizeHitlDecidedBy(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "audit_agent", "agent", "ai":
		return "audit_agent"
	case "system", "timeout":
		return "system"
	case "manual":
		return "manual"
	default:
		return "human"
	}
}

var errHitlStoreUnavailable = errors.New("hitl store unavailable")

// newHITLStore gives a domain store the same connection the legacy handle wraps,
// so extracted stores and `*database.DB` consumers share one pool.
func newHITLStore(db *database.DB) *store.HITL {
	if db == nil {
		return nil
	}
	return store.NewHITL(db.DB)
}

// hitlStore is where the HTTP layer reaches the interrupt table now: through the
// domain store instead of a raw statement.
func (h *AgentHandler) hitlStoreOrErr() (*store.HITL, error) {
	if h == nil || h.hitlStore == nil {
		return nil, errHitlStoreUnavailable
	}
	return h.hitlStore, nil
}

func (h *AgentHandler) listHitlInterrupts(set store.InterruptSet, f store.InterruptFilter) ([]store.Interrupt, int, error) {
	s, err := h.hitlStoreOrErr()
	if err != nil {
		return nil, 0, err
	}
	return s.List(set, f)
}

// hitlInterruptToMap renders one stored interrupt for the API. decided_at is a
// nullable column and the UI distinguishes "no decision yet" from a decision
// made at the epoch, so it stays null rather than becoming a zero timestamp.
func hitlInterruptToMap(it store.Interrupt) map[string]interface{} {
	auditBackend, auditModel := hitlAuditBackendFromRecord(it.DecidedBy, it.Comment, it.Payload)
	var decidedAt interface{}
	if it.DecidedAt != nil {
		decidedAt = *it.DecidedAt
	}
	return map[string]interface{}{
		"id":             it.ID,
		"conversationId": it.ConversationID,
		"messageId":      it.MessageID,
		"mode":           it.Mode,
		"toolName":       it.ToolName,
		"toolCallId":     it.ToolCallID,
		"payload":        it.Payload,
		"status":         it.Status,
		"reviewer":       it.Reviewer,
		"decision":       it.Decision,
		"comment":        it.Comment,
		"decidedBy":      it.DecidedBy,
		"auditBackend":   auditBackend,
		"auditModel":     auditModel,
		"createdAt":      it.CreatedAt,
		"decidedAt":      decidedAt,
	}
}

func hitlInterruptMaps(items []store.Interrupt) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		out = append(out, hitlInterruptToMap(it))
	}
	return out
}

// hitlListPaging reads the page parameters, clamped to a range the table can serve.
func hitlListPaging(c *gin.Context) (page, pageSize, offset int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ = strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	pageSize = int(math.Max(1, math.Min(float64(pageSize), 200)))
	return page, pageSize, (page - 1) * pageSize
}

// hitlFilterFromRequest turns the sidebar's query string into a store filter. The
// store treats "" and "all" as no filter; decided_by is mapped through the alias
// table first because the UI sends history written under several names.
func hitlFilterFromRequest(c *gin.Context) store.InterruptFilter {
	f := store.InterruptFilter{
		ConversationID: strings.TrimSpace(c.Query("conversationId")),
		ToolName:       strings.TrimSpace(c.Query("toolName")),
		Decision:       strings.TrimSpace(c.Query("decision")),
		Status:         strings.TrimSpace(c.Query("status")),
		Search:         strings.TrimSpace(c.Query("q")),
	}
	if v := strings.TrimSpace(c.Query("decidedBy")); v != "" && v != "all" {
		f.DecidedBy = normalizeHitlDecidedBy(v)
	}
	return f
}

// hitlAccessFromRequest is the caller's read scope, expressed for the store layer.
func hitlAccessFromRequest(c *gin.Context) store.Access {
	session, ok := security.CurrentSession(c)
	if !ok {
		return store.Access{}
	}
	return store.Access{UserID: session.UserID, Scope: session.Scope}
}

func (h *AgentHandler) ListHITLLogs(c *gin.Context) {
	page, pageSize, offset := hitlListPaging(c)
	f := hitlFilterFromRequest(c)
	f.Access = hitlAccessFromRequest(c)
	f.Limit, f.Offset = pageSize, offset

	items, total, err := h.listHitlInterrupts(store.InterruptsLog, f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": hitlInterruptMaps(items), "page": page, "pageSize": pageSize, "total": total, "retentionDays": h.hitlRetentionDays()})
}

func (h *AgentHandler) hitlRetentionDays() int {
	if h.config != nil {
		return h.config.Hitl.RetentionDaysEffective()
	}
	return config.HitlConfig{}.RetentionDaysEffective()
}

// DeleteHITLLogs 批量删除或按筛选清空已决策的人机协同审计日志（不删除 pending）。
func (h *AgentHandler) DeleteHITLLogs(c *gin.Context) {
	var request struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效: " + err.Error()})
		return
	}

	s, err := h.hitlStoreOrErr()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var deleted int64
	if request.All {
		f := hitlFilterFromRequest(c)
		f.Access = hitlAccessFromRequest(c)
		deleted, err = s.DeleteLogs(store.InterruptsLog, f)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if h.audit != nil {
			h.audit.RecordOK(c, "hitl", "logs_clear", "清空人机协同审计日志", "hitl_interrupt", "", map[string]interface{}{
				"deleted": deleted,
			})
		}
	} else {
		if len(request.IDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "审计日志 ID 列表不能为空"})
			return
		}
		ids, filterErr := h.filterAllowedHitlInterruptIDs(c, request.IDs)
		if filterErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": filterErr.Error()})
			return
		}
		deleted, err = s.DeleteLogsByIDs(ids)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if h.audit != nil {
			h.audit.RecordOK(c, "hitl", "logs_delete_batch", "批量删除人机协同审计日志", "hitl_interrupt", "", map[string]interface{}{
				"count":   len(request.IDs),
				"deleted": deleted,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "删除成功", "deleted": deleted})
}

func (h *AgentHandler) GetHITLLog(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}
	s, err := h.hitlStoreOrErr()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	it, found, err := s.Get(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if !h.hitlConversationAllowed(c, it.ConversationID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该资源"})
		return
	}
	c.JSON(http.StatusOK, hitlInterruptToMap(it))
}

// filterAllowedHitlInterruptIDs drops ids the caller may not touch. Requested ids
// that are not in the table at all drop out too - they cannot be deleted, and
// saying so would leak which ids exist.
func (h *AgentHandler) filterAllowedHitlInterruptIDs(c *gin.Context, ids []string) ([]string, error) {
	clean := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
	}
	if len(clean) == 0 {
		return clean, nil
	}
	s, err := h.hitlStoreOrErr()
	if err != nil {
		return nil, err
	}
	owners, err := s.ConversationOwners(clean)
	if err != nil {
		return nil, err
	}
	allowed := make([]string, 0, len(clean))
	for _, id := range clean {
		conversationID, ok := owners[id]
		if !ok {
			continue
		}
		if h.hitlConversationAllowed(c, conversationID) {
			allowed = append(allowed, id)
		}
	}
	return allowed, nil
}

func (h *AgentHandler) hitlInterruptAllowed(c *gin.Context, interruptID string) bool {
	s, err := h.hitlStoreOrErr()
	if err != nil {
		return false
	}
	conversationID, found, err := s.ConversationOwner(interruptID)
	if err != nil || !found {
		return false
	}
	return h.hitlConversationAllowed(c, conversationID)
}

func (h *AgentHandler) hitlConversationAllowed(c *gin.Context, conversationID string) bool {
	session, ok := security.CurrentSession(c)
	if !ok {
		return false
	}
	return h.db.UserCanAccessResource(session.UserID, session.Scope, "conversation", conversationID)
}
