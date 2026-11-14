package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/deepassessment"
	"cyberstrike-ai/internal/security"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func assessmentFindingRouter(h *AgentHandler, user string, permissions map[string]bool) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(security.ContextSessionKey, security.Session{UserID: user, Scope: database.RBACScopeOwn, Permissions: permissions})
	})
	r.Use(security.RBACMiddleware(h.db))
	r.GET("/api/deep-assessments/:id/vulnerabilities", h.DeepAssessmentFindings)
	r.GET("/api/deep-assessments/:id/vulnerabilities/:vulnerabilityId", h.DeepAssessmentFinding)
	r.GET("/api/deep-assessments/:id/steps/:stepId", h.DeepAssessmentStep)
	r.GET("/api/deep-assessments/:id/receipts", h.DeepAssessmentReceipts)
	vh := NewVulnerabilityHandler(h.db, zap.NewNop())
	r.GET("/api/vulnerabilities/:id/context", vh.VulnerabilityContext)
	return r
}
func TestDeepAssessmentFindingLinksRespectBothResourceScopes(t *testing.T) {
	h, task := deepAssessmentFixture(t)
	conv, _ := h.db.CreateConversation("step", database.ConversationCreateMeta{})
	h.db.SetResourceOwner("conversation", conv.ID, task.OwnerID)
	if err := h.deepAssessments.Store.Change(task.ID, "", "checkpoint", "source", func(v *deepassessment.Task) error {
		v.Steps = []deepassessment.Step{{ID: "one", Title: "Origin step", Status: "completed", ConversationID: conv.ID, Summary: "proof"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v, err := h.db.CreateVulnerability(&database.Vulnerability{ID: "finding", ConversationID: conv.ID, Title: "Finding", Severity: "high", ReproSteps: "original steps", Evidence: "original proof"})
	if err != nil {
		t.Fatal(err)
	}
	if v.AssessmentID != task.ID {
		t.Fatal(v)
	}
	owner := assessmentFindingRouter(h, task.OwnerID, map[string]bool{"assessments:read": true, "vulnerability:read": true})
	for _, path := range []string{"/api/deep-assessments/task/vulnerabilities", "/api/deep-assessments/task/vulnerabilities/finding", "/api/deep-assessments/task/steps/one", "/api/deep-assessments/task/receipts?step_id=one", "/api/vulnerabilities/finding/context"} {
		w := httptest.NewRecorder()
		owner.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
		if path == "/api/vulnerabilities/finding/context" {
			var data struct {
				Vulnerability database.Vulnerability `json:"vulnerability"`
			}
			json.Unmarshal(w.Body.Bytes(), &data)
			if !data.Vulnerability.AssessmentCanOpen || data.Vulnerability.AssessmentStepTitle != "Origin step" {
				t.Fatal(w.Body.String())
			}
		}
	}
	other, _ := h.db.CreateRBACUser("source-reader", "Source reader", "hash", true, nil)
	h.db.AssignResourceToUser(other.ID, "deep_assessment", task.ID)
	reader := assessmentFindingRouter(h, other.ID, map[string]bool{"assessments:read": true, "vulnerability:read": true})
	w := httptest.NewRecorder()
	reader.ServeHTTP(w, httptest.NewRequest("GET", "/api/deep-assessments/task/vulnerabilities", nil))
	var list struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || list.Total != 0 {
		t.Fatal("task read leaked findings", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	reader.ServeHTTP(w, httptest.NewRequest("GET", "/api/deep-assessments/task/vulnerabilities/finding", nil))
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Sharing just the finding does not grant access to its assessment.
	findingReader, _ := h.db.CreateRBACUser("finding-reader", "Finding reader", "hash", true, nil)
	h.db.AssignResourceToUser(findingReader.ID, "vulnerability", v.ID)
	r := assessmentFindingRouter(h, findingReader.ID, map[string]bool{"assessments:read": true, "vulnerability:read": true})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/vulnerabilities/finding/context", nil))
	var ctx struct {
		Vulnerability database.Vulnerability `json:"vulnerability"`
	}
	json.Unmarshal(w.Body.Bytes(), &ctx)
	if w.Code != 200 || ctx.Vulnerability.AssessmentCanOpen {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/deep-assessments/task/steps/one", nil))
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}
