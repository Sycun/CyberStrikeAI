package handler

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"github.com/gin-gonic/gin"
)

// The HITL interrupt read surface - listing, rendering, permission checks, the log endpoints -
// lives on HITLQueue (hitl_queue.go). What is left here are the shared request/response
// translations plus the store handle the run loop still needs.

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
