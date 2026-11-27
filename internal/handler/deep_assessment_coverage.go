package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *AgentHandler) DeepAssessmentCoverage(c *gin.Context) {
	store := h.deepAssessments.Store
	id := c.Param("id")
	if _, err := store.Get(id); err != nil {
		deepAssessmentError(c, err)
		return
	}
	ledger, err := store.Coverage(id)
	if err != nil {
		deepAssessmentError(c, err)
		return
	}
	c.JSON(http.StatusOK, ledger)
}
