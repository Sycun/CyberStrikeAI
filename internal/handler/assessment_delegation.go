package handler

import (
	"cyberstrike-ai/internal/assessment"
	"cyberstrike-ai/internal/security"
	"github.com/gin-gonic/gin"
)

// Answer records clarification independently of HITL and resumes under the
// answering operator's current execution permissions.
func (h *AssessmentHandler) Answer(c *gin.Context) {
	t, ok := h.task(c)
	if !ok {
		return
	}
	if !security.SessionHasPermission(c, "agent:execute") {
		c.JSON(403, gin.H{"error": "agent:execute is required to answer and resume"})
		return
	}
	var req assessment.OwnerAnswer
	if err := h.bind(c, &req); err != nil {
		h.fail(c, err)
		return
	}
	session, _ := security.CurrentSession(c)
	result, err := h.Engine.AnswerAs(t.ID, c.Param("questionId"), session.UserID, req)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.Audit.RecordOK(c, "assessment", "owner_answer", "Answered task question", "assessment", t.ID, map[string]interface{}{"question_id": c.Param("questionId"), "revision": req.Revision})
	c.JSON(200, result)
}
