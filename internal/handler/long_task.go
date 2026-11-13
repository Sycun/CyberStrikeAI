package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/taskflow"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *AgentHandler) InitLongTasks(snapshot func() (*config.Config, error)) error {
	s := &taskflow.Store{DB: h.db}
	if err := s.Recover(); err != nil {
		return err
	}
	r := &longTaskRuntime{h: h, snapshot: snapshot}
	h.longTaskRuntime = r
	h.longTasks = taskflow.NewEngine(s, r)
	return nil
}

func longTaskError(c *gin.Context, err error) {
	code := 400
	if errors.Is(err, sql.ErrNoRows) {
		code = 404
	}
	if errors.Is(err, taskflow.ErrConflict) {
		code = 409
	}
	c.JSON(code, gin.H{"error": err.Error()})
}

func longTaskBody(c *gin.Context, out any) bool {
	if c.GetHeader("Authorization") == "" {
		if _, err := c.Cookie("auth_token"); err == nil {
			u, err := url.Parse(c.GetHeader("Origin"))
			if err != nil || u.Host != c.Request.Host || (u.Scheme != "http" && u.Scheme != "https") {
				c.JSON(403, gin.H{"error": "请求来源不匹配"})
				return false
			}
		}
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	if out != nil {
		if err := c.ShouldBindJSON(out); err != nil {
			longTaskError(c, err)
			return false
		}
	}
	return true
}

func (h *AgentHandler) LongTaskOptions(c *gin.Context) {
	cfg, err := h.longTaskRuntime.snapshot()
	if err != nil {
		longTaskError(c, err)
		return
	}
	models := []gin.H{}
	for id, ch := range cfg.AI.Channels {
		models = append(models, gin.H{"id": id, "name": ch.Name, "model": ch.Model})
	}
	sort.Slice(models, func(i, j int) bool { return models[i]["id"].(string) < models[j]["id"].(string) })
	roles := []string{}
	for name, role := range cfg.Roles {
		if role.Enabled {
			roles = append(roles, name)
		}
	}
	sort.Strings(roles)
	projects := []gin.H{}
	session, _ := security.CurrentSession(c)
	if security.SessionHasPermission(c, "project:read") {
		list, err := h.db.ListProjectsForAccess("active", "", 500, 0, session.UserID, session.ScopeFor("project:read"))
		if err != nil {
			longTaskError(c, err)
			return
		}
		for _, p := range list {
			projects = append(projects, gin.H{"id": p.ID, "name": p.Name})
		}
	}
	c.JSON(200, gin.H{"models": models, "roles": roles, "projects": projects})
}

func (h *AgentHandler) CreateLongTask(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		Objective   string `json:"objective"`
		Scope       string `json:"scope"`
		Criteria    string `json:"criteria"`
		ProjectID   string `json:"project_id"`
		Role        string `json:"role"`
		Model       string `json:"model"`
		HITLPolicy  string `json:"hitl_policy"`
		Concurrency int    `json:"concurrency"`
		MaxRounds   int    `json:"max_rounds"`
		Seconds     int64  `json:"remaining_seconds"`
	}
	if !longTaskBody(c, &req) {
		return
	}
	session, ok := security.CurrentSession(c)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	if req.Name == "" {
		req.Name = taskflow.Clip(req.Objective, 80)
	}
	if req.Concurrency == 0 {
		req.Concurrency = 2
	}
	if req.MaxRounds == 0 {
		req.MaxRounds = 40
	}
	if req.Seconds == 0 {
		req.Seconds = 6 * 3600
	}
	if err := validateBatchHITLPolicy(req.HITLPolicy); err != nil {
		longTaskError(c, err)
		return
	}
	if req.ProjectID != "" && (!security.SessionHasPermission(c, "project:read") || !h.db.UserCanAccessResource(session.UserID, session.ScopeFor("project:read"), "project", req.ProjectID)) {
		c.JSON(403, gin.H{"error": "无权访问项目"})
		return
	}
	now := time.Now().UTC()
	t := &taskflow.Task{ID: uuid.NewString(), OwnerID: session.UserID, ExecutorID: session.UserID, Name: strings.TrimSpace(req.Name), Objective: strings.TrimSpace(req.Objective), Scope: strings.TrimSpace(req.Scope), Criteria: strings.TrimSpace(req.Criteria), ProjectID: req.ProjectID, Role: req.Role, Model: req.Model, HITLPolicy: req.HITLPolicy, Concurrency: req.Concurrency, MaxRounds: req.MaxRounds, RemainingSeconds: req.Seconds, Status: "draft", Steps: []taskflow.Step{}, Guidance: []string{}, CreatedAt: now, UpdatedAt: now}
	if _, err := h.longTaskRuntime.configuration(t); err != nil {
		longTaskError(c, err)
		return
	}
	if err := h.longTasks.Store.Create(t); err != nil {
		longTaskError(c, err)
		return
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "task", "create_long_task", "创建目标任务", "long_task", t.ID, nil)
	}
	c.JSON(201, gin.H{"task": t})
}

func (h *AgentHandler) ListLongTasks(c *gin.Context) {
	session, _ := security.CurrentSession(c)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit < 1 || limit > 100 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	list, err := h.longTasks.Store.List(session.UserID, session.ScopeFor("tasks:read"), c.Query("status"), c.Query("q"), limit+1, offset)
	if err != nil {
		longTaskError(c, err)
		return
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	c.JSON(200, gin.H{"tasks": list, "has_more": more})
}
func (h *AgentHandler) GetLongTask(c *gin.Context) {
	t, err := h.longTasks.Store.Get(c.Param("id"))
	if err != nil {
		longTaskError(c, err)
		return
	}
	c.JSON(200, gin.H{"task": t})
}
func (h *AgentHandler) LongTaskEvents(c *gin.Context) {
	after, _ := strconv.ParseInt(c.Query("after"), 10, 64)
	list, err := h.longTasks.Store.Events(c.Param("id"), after)
	if err != nil {
		longTaskError(c, err)
		return
	}
	c.JSON(200, gin.H{"events": list})
}
func (h *AgentHandler) DeleteLongTask(c *gin.Context) {
	if !longTaskBody(c, nil) {
		return
	}
	if err := h.longTasks.Delete(c.Param("id")); err != nil {
		longTaskError(c, err)
		return
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "task", "delete_long_task", "删除目标任务", "long_task", c.Param("id"), nil)
	}
	c.JSON(200, gin.H{"deleted": true})
}

func (h *AgentHandler) ControlLongTask(c *gin.Context) {
	var req struct {
		Revision         *int64 `json:"revision"`
		CleanupConfirmed bool   `json:"cleanup_confirmed"`
		Message          string `json:"message"`
		StepID           string `json:"step_id"`
		MaxRounds        int    `json:"max_rounds"`
		Seconds          int64  `json:"remaining_seconds"`
	}
	if !longTaskBody(c, &req) {
		return
	}
	if req.Revision == nil {
		longTaskError(c, fmt.Errorf("revision is required"))
		return
	}
	id, action := c.Param("id"), c.Param("action")
	session, _ := security.CurrentSession(c)
	t, err := h.longTasks.Store.Get(id)
	if err != nil {
		longTaskError(c, err)
		return
	}
	if action == "start" || action == "answer" {
		t.ExecutorID = session.UserID
		if _, err = h.longTaskRuntime.principal(t); err != nil {
			c.JSON(403, gin.H{"error": err.Error()})
			return
		}
	}
	if action == "retry" {
		if step := t.Step(req.StepID); step != nil && step.Status == "cleanup_unconfirmed" {
			if !req.CleanupConfirmed {
				longTaskError(c, fmt.Errorf("请先确认上次执行资源已停止"))
				return
			}
			for _, running := range h.tasks.GetActiveTasks() {
				if running.ConversationID == step.ConversationID {
					longTaskError(c, fmt.Errorf("原执行仍在清理，请先在工具监控中确认停止"))
					return
				}
			}
		}
	}
	switch action {
	case "start":
		err = h.longTasks.Start(id, session.UserID, *req.Revision)
	case "pause", "cancel":
		err = h.longTasks.Stop(id, *req.Revision, action == "cancel")
	case "guidance", "answer", "retry", "budget":
		err = h.longTasks.Store.Change(id, req.StepID, action, req.Message, func(t *taskflow.Task) error {
			if t.Revision != *req.Revision || t.Status == "completed" || t.Status == "cancelled" {
				return taskflow.ErrConflict
			}
			if action != "guidance" && taskflow.Active(t.Status) {
				return fmt.Errorf("请先暂停并等待执行收尾")
			}
			switch action {
			case "guidance", "answer":
				if strings.TrimSpace(req.Message) == "" || len(req.Message) > 8000 || len(t.Guidance) >= 32 {
					return fmt.Errorf("补充内容为空、过长或已达到条数上限")
				}
				if action == "answer" && t.Question == "" {
					return taskflow.ErrConflict
				}
				t.Guidance = append(t.Guidance, strings.TrimSpace(req.Message))
				t.Summary = "收到补充方向，将在下一轮规划中使用。"
				if action == "answer" {
					t.Question = ""
					t.Status = "paused"
				}
			case "retry":
				s := t.Step(req.StepID)
				if s == nil || (s.Status != "failed" && s.Status != "blocked" && s.Status != "interrupted" && s.Status != "cleanup_unconfirmed") {
					return fmt.Errorf("该步骤不可重试")
				}
				s.Status = "pending"
				t.Status = "paused"
			case "budget":
				if req.MaxRounds < t.Rounds+1 || req.MaxRounds > 200 || req.Seconds < 1 || req.Seconds > 7*86400 {
					return fmt.Errorf("预算超出范围")
				}
				t.MaxRounds = req.MaxRounds
				t.RemainingSeconds = req.Seconds
				t.Status = "paused"
			}
			return nil
		})
	default:
		err = fmt.Errorf("未知操作")
	}
	if err != nil {
		longTaskError(c, err)
		return
	}
	t, err = h.longTasks.Store.Get(id)
	if err != nil {
		longTaskError(c, err)
		return
	}
	response := gin.H{"task": t}
	if action == "answer" {
		if err = h.longTasks.Start(id, session.UserID, t.Revision); err != nil {
			response["start_error"] = err.Error()
		} else {
			t, _ = h.longTasks.Store.Get(id)
			response["task"] = t
		}
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "task", "long_task_"+action, "更新目标任务", "long_task", id, nil)
	}
	c.JSON(200, response)
}

func (h *AgentHandler) LongTaskReport(c *gin.Context) {
	t, err := h.longTasks.Store.Get(c.Param("id"))
	if err != nil {
		longTaskError(c, err)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n状态：%s\n\n## 目标\n%s\n\n## 范围\n%s\n\n## 验收条件\n%s\n\n## 当前结论\n%s\n\n## 路线\n%s\n", t.Name, t.Status, t.Objective, t.Scope, t.Criteria, t.Summary, t.Strategy)
	for _, s := range t.Steps {
		fmt.Fprintf(&b, "\n## %s · %s [%s]\n\n%s\n\n前置：%s\n\n%s\n\n执行会话：%s\n", s.ID, s.Title, s.Status, s.Instruction, strings.Join(s.DependsOn, ", "), s.Summary, s.ConversationID)
	}
	c.Header("Content-Disposition", `attachment; filename="task-`+t.ID+`.md"`)
	c.Data(200, "text/markdown; charset=utf-8", []byte(b.String()))
}
