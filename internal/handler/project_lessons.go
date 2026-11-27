package handler

import (
	"errors"
	"net/http"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *ProjectHandler) ListLessons(c *gin.Context) {
	projectID := c.Param("id")
	proj, err := h.db.GetProject(projectID)
	if errors.Is(err, database.ErrProjectNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return
	}
	if err != nil {
		h.logger.Error("读取项目失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取项目失败"})
		return
	}
	var access database.RBACListAccess
	if session, ok := security.CurrentSession(c); ok && security.SessionHasPermission(c, "vulnerability:read") {
		access = database.RBACListAccess{UserID: session.UserID, Scope: session.ScopeFor("vulnerability:read")}
	}
	items, err := h.db.ListProjectLessonsForReview(projectID, access)
	if err != nil {
		h.logger.Error("读取项目经验失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取项目经验失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"lessons_mode": proj.LessonsMode, "items": items})
}

func (h *ProjectHandler) ExtractLessons(c *gin.Context) {
	err := h.db.ExtractProjectLessons(c.Param("id"))
	switch {
	case errors.Is(err, database.ErrProjectNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, database.ErrProjectLessonsOff):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case err != nil:
		h.logger.Error("提炼项目经验失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "提炼项目经验失败"})
	default:
		c.Status(http.StatusNoContent)
	}
}
