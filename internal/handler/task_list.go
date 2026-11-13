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
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	if offset > 100000 {
		offset = 100000
	}
	total, err := h.db.CountManagedTasks(session.UserID, session.ScopeFor("tasks:read"), mode, c.Query("status"), strings.TrimSpace(c.Query("q")))
	if err != nil {
		h.logger.Error("统计任务列表失败", zap.Error(err))
		c.JSON(500, gin.H{"error": "查询任务列表失败"})
		return
	}
	if offset >= total {
		if total == 0 {
			offset = 0
		} else {
			offset = ((total - 1) / limit) * limit
		}
	}
	rows, err := h.db.ListManagedTasks(session.UserID, session.ScopeFor("tasks:read"), mode, c.Query("status"), strings.TrimSpace(c.Query("q")), limit, offset)
	if err != nil {
		h.logger.Error("查询任务列表失败", zap.Error(err))
		c.JSON(500, gin.H{"error": "查询任务列表失败"})
		return
	}
	pages := (total + limit - 1) / limit
	if pages < 1 {
		pages = 1
	}
	c.JSON(200, gin.H{"tasks": rows, "has_more": offset+len(rows) < total, "total": total, "page": offset/limit + 1, "page_size": limit, "total_pages": pages, "offset": offset})
}
