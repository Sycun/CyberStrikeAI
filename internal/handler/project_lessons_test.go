package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestProjectLessonRoutesAndShadowPrompt(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "lessons.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := db.CreateProject(&database.Project{Name: "Fixture", LessonsMode: database.ProjectLessonsShadow})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateProject(&database.Project{Name: "Other", LessonsMode: database.ProjectLessonsShadow})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetResourceOwner("project", p.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	conv, err := db.CreateConversation("Review", database.ConversationCreateMeta{ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateVulnerability(&database.Vulnerability{
		ConversationID: conv.ID, Title: "Private record", Status: "fixed", Severity: "low", Type: "访问控制",
	}); err != nil {
		t.Fatal(err)
	}

	session := security.Session{UserID: "owner", Scope: database.RBACScopeOwn, Permissions: map[string]bool{"project:read": true}}
	router := gin.New()
	protected := router.Group("/api", func(c *gin.Context) { c.Set(security.ContextSessionKey, session) }, security.RBACMiddleware(db))
	h := NewProjectHandler(db, zap.NewNop())
	protected.GET("/projects/:id/lessons", h.ListLessons)
	protected.POST("/projects/:id/lessons/extract", h.ExtractLessons)
	request := func(method, id, suffix string, status int) string {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, "/api/projects/"+id+"/lessons"+suffix, nil))
		if response.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, suffix, response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	body := request(http.MethodGet, p.ID, "", http.StatusOK)
	if !strings.Contains(body, `"cue":"fixed"`) || strings.Contains(body, "Private record") || strings.Contains(body, `"summary"`) {
		t.Fatalf("lesson response: %s", body)
	}
	session.Permissions["vulnerability:read"] = true
	body = request(http.MethodGet, p.ID, "", http.StatusOK)
	if !strings.Contains(body, `"summary":"Fixed · Access control · low severity"`) || strings.Contains(body, "Private record") {
		t.Fatalf("classified review response: %s", body)
	}
	request(http.MethodGet, other.ID, "", http.StatusForbidden)
	request(http.MethodPost, p.ID, "/extract", http.StatusForbidden)
	session.Permissions["project:write"] = true
	request(http.MethodPost, p.ID, "/extract", http.StatusNoContent)
	request(http.MethodPost, other.ID, "/extract", http.StatusForbidden)

	agent := &AgentHandler{db: db, config: &config.Config{Project: config.ProjectConfig{Enabled: true}}, logger: zap.NewNop()}
	block := agent.projectBlackboardBlock(conv.ID)
	if block == "" || strings.Contains(block, "归档修复结论") || strings.Contains(block, "field=project_lessons") {
		t.Fatalf("shadow wrote to the prompt: %s", block)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM project_lesson_usage WHERE project_id=? AND conversation_id=?`, p.ID, conv.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("shadow usage: %d, %v", count, err)
	}

	injectMode := database.ProjectLessonsInject
	if _, err := db.UpdateProject(p.ID, database.ProjectUpdate{LessonsMode: &injectMode}); err != nil {
		t.Fatal(err)
	}
	injected := agent.projectBlackboardBlock(conv.ID)
	_, lessonsPart, hasLessonsFrame := strings.Cut(injected, "field=project_lessons]]")
	if !hasLessonsFrame || !strings.Contains(lessonsPart, "Fixed · Access control · low severity") || strings.Contains(lessonsPart, "Private record") {
		t.Fatalf("inject prompt missing framed lessons or leaked private title: %s", injected)
	}
}
