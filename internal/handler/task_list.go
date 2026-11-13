package handler

import (
	"fmt"
	"strconv"
	"strings"

	"cyberstrike-ai/internal/security"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ListManagedTasks is the single task-management listing for autonomous work
// and supplied checklists. Existing execution APIs remain compatible.
func (h *AgentHandler) ListManagedTasks(c *gin.Context) {
	session, ok := security.CurrentSession(c)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	mode := c.Query("mode")
	if mode != "" && mode != "goal" && mode != "list" {
		longTaskError(c, fmt.Errorf("无效的执行方式"))
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	if limit < 1 || limit > 100 {
		limit = 30
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	if offset > 100000 {
		offset = 100000
	}
	rows, err := h.db.ListManagedTasks(session.UserID, session.ScopeFor("tasks:read"), mode, c.Query("status"), strings.TrimSpace(c.Query("q")), limit+1, offset)
	if err != nil {
		h.logger.Error("查询任务列表失败", zap.Error(err))
		c.JSON(500, gin.H{"error": "查询任务列表失败"})
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	c.JSON(200, gin.H{"tasks": rows, "has_more": more})
}
