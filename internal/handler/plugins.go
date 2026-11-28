package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub"
	"cyberstrike-ai/internal/pluginhub/llmgateway"
	"cyberstrike-ai/internal/pluginhub/manifest"
	"cyberstrike-ai/internal/pluginhub/source"
	"cyberstrike-ai/internal/pluginhub/webproxy"
	"cyberstrike-ai/internal/security"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const pluginInspectionTTL = 20 * time.Minute

type pluginInspector interface {
	Inspect(context.Context, string, string) (*source.Inspection, error)
}

type PluginHandler struct {
	db        *database.DB
	audit     *audit.Service
	inspector pluginInspector
	hub       *pluginhub.Manager
}

func NewPluginHandler(db *database.DB, auditSvc *audit.Service, hub *pluginhub.Manager) *PluginHandler {
	return &PluginHandler{db: db, audit: auditSvc, inspector: hub.Inspector(), hub: hub}
}

func (h *PluginHandler) Register(group *gin.RouterGroup) {
	group = group.Group("/plugins", func(c *gin.Context) {
		scheme, token, _ := strings.Cut(c.GetHeader("Authorization"), " ")
		if !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "plugin_bearer_required"})
			return
		}
		c.Next()
	})
	group.GET("/catalog", h.listCatalog)
	group.POST("/catalog", h.saveCatalog)
	group.DELETE("/catalog/:id", h.deleteCatalog)
	group.POST("/catalog/restore", h.restoreCatalog)
	group.GET("/schemas/:name", h.schema)
	group.GET("/llm/channels", h.listLLMChannels)
	group.GET("/installs", h.listInstalls)
	group.GET("/installs/:id", h.getInstall)
	group.POST("/imports/inspect", h.inspect)
	group.POST("/install-plans", h.createPlan)
	group.GET("/install-plans/:id", h.getPlan)
	group.GET("/runtime/probes", h.probes)
	group.POST("/installs", h.install)
	group.POST("/installs/:id/operations", h.operation)
	group.GET("/operations/:id", h.getOperation)
	group.GET("/installs/:id/logs", h.logs)
	group.POST("/installs/:id/embed-sessions", h.embed)
	group.PATCH("/installs/:id/llm", h.patchLLM)
}

func (h *PluginHandler) embed(c *gin.Context) {
	session, ok := security.CurrentSession(c)
	if !ok {
		pluginError(c, http.StatusUnauthorized, "plugin_session_required")
		return
	}
	entry, err := h.hub.IssueEmbed(c.Request, c.Param("id"), session.Token, session.ExpiresAt)
	switch {
	case errors.Is(err, webproxy.ErrForbidden):
		pluginError(c, http.StatusForbidden, "plugin_embed_origin_or_permission_rejected")
	case err != nil:
		pluginError(c, http.StatusServiceUnavailable, "plugin_web_unavailable")
	default:
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusCreated, entry)
	}
}

func (h *PluginHandler) patchLLM(c *gin.Context) {
	var patch llmgateway.Patch
	if !bindPluginRequest(c, &patch) {
		return
	}
	bindings, restart, err := h.hub.PatchLLM(c.Request.Context(), c.Param("id"), patch)
	switch {
	case errors.Is(err, llmgateway.ErrBinding):
		pluginError(c, http.StatusUnprocessableEntity, "plugin_llm_binding_invalid")
	case errors.Is(err, llmgateway.ErrUnavailable):
		pluginError(c, http.StatusServiceUnavailable, "plugin_llm_gateway_unavailable")
	case err != nil:
		pluginRecordError(c, err)
	default:
		if h.audit != nil {
			h.audit.RecordOK(c, "plugins", "model_binding", "Plugin model binding updated", "plugin_install", c.Param("id"),
				map[string]interface{}{"restart_required": restart})
		}
		c.JSON(http.StatusOK, gin.H{"bindings": bindings, "restart_required": restart})
	}
}

func (h *PluginHandler) listCatalog(c *gin.Context) {
	items, hiddenBuiltins, err := h.hub.ListCatalog(c.Request.Context())
	if err != nil {
		pluginError(c, http.StatusInternalServerError, "plugin_storage_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "hidden_builtins": hiddenBuiltins})
}

func (h *PluginHandler) saveCatalog(c *gin.Context) {
	var input pluginhub.CatalogInput
	if !bindPluginRequest(c, &input) {
		return
	}
	entry, err := h.hub.SaveCatalogEntry(c.Request.Context(), input)
	if err != nil {
		pluginLifecycleError(c, err)
		return
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "plugins", "catalog_save", "Plugin catalog entry saved", "plugin_catalog", entry.ID,
			map[string]interface{}{"repository": entry.Repository, "availability": entry.Availability})
	}
	c.JSON(http.StatusCreated, gin.H{"item": entry})
}

func (h *PluginHandler) deleteCatalog(c *gin.Context) {
	id := c.Param("id")
	if err := h.hub.DeleteCatalogEntry(c.Request.Context(), id); err != nil {
		pluginLifecycleError(c, err)
		return
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "plugins", "catalog_delete", "Plugin catalog entry removed", "plugin_catalog", id, nil)
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func (h *PluginHandler) restoreCatalog(c *gin.Context) {
	restored, err := h.hub.RestoreBuiltinCatalog(c.Request.Context())
	if err != nil {
		pluginError(c, http.StatusInternalServerError, "plugin_storage_error")
		return
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "plugins", "catalog_restore", "Built-in plugin catalog entries restored", "plugin_catalog", "builtin",
			map[string]interface{}{"restored": restored})
	}
	c.JSON(http.StatusOK, gin.H{"restored": restored})
}

func (h *PluginHandler) schema(c *gin.Context) {
	raw, err := manifest.Schema(c.Param("name"))
	if err != nil {
		pluginError(c, http.StatusNotFound, "plugin_schema_not_found")
		return
	}
	c.Data(http.StatusOK, "application/schema+json", raw)
}

func (h *PluginHandler) listInstalls(c *gin.Context) {
	session, _ := security.CurrentSession(c)
	items, err := h.db.ListPluginInstalls(c.Request.Context(), session.UserID, session.ScopeFor("plugins:read"))
	if err != nil {
		pluginError(c, http.StatusInternalServerError, "plugin_storage_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *PluginHandler) listLLMChannels(c *gin.Context) {
	defaultChannel, channels := h.hub.AvailableLLMChannels()
	c.JSON(http.StatusOK, gin.H{"default_channel": defaultChannel, "channels": channels})
}

func (h *PluginHandler) getInstall(c *gin.Context) {
	item, err := h.hub.InstallDetail(c.Request.Context(), c.Param("id"))
	if err != nil {
		pluginRecordError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *PluginHandler) inspect(c *gin.Context) {
	var request struct {
		Repository string `json:"repository"`
		Revision   string `json:"revision"`
	}
	if !bindPluginRequest(c, &request) {
		return
	}
	result, err := h.inspector.Inspect(c.Request.Context(), request.Repository, request.Revision)
	if err != nil {
		var status int
		var code string
		switch {
		case errors.Is(err, source.ErrInvalidSource), errors.Is(err, source.ErrInvalidRevision):
			status, code = http.StatusUnprocessableEntity, "plugin_invalid_source"
		case errors.Is(err, source.ErrAmbiguousRef):
			status, code = http.StatusConflict, "plugin_ambiguous_git_ref"
		case errors.Is(err, source.ErrSourceNotFound):
			status, code = http.StatusNotFound, "plugin_source_not_found"
		case errors.Is(err, source.ErrManifestInvalid):
			status, code = http.StatusUnprocessableEntity, "plugin_invalid_manifest"
		case errors.Is(err, source.ErrRateLimited):
			status, code = http.StatusTooManyRequests, "plugin_github_rate_limited"
		default:
			status, code = http.StatusBadGateway, "plugin_source_unavailable"
		}
		if h.audit != nil {
			h.audit.RecordFail(c, "plugins", "inspect", "Plugin import inspection failed", map[string]interface{}{"code": code})
		}
		pluginError(c, status, code)
		return
	}
	session, _ := security.CurrentSession(c)
	now := time.Now().UTC()
	expires := now.Add(pluginInspectionTTL)
	id := uuid.NewString()
	payload, err := json.Marshal(result)
	if err != nil {
		pluginError(c, http.StatusInternalServerError, "plugin_inspection_error")
		return
	}
	if err := h.db.SavePluginInspection(c.Request.Context(), id, session.UserID, string(payload), now, expires); err != nil {
		pluginError(c, http.StatusInternalServerError, "plugin_storage_error")
		return
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "plugins", "inspect", "Plugin import inspected", "plugin_inspection", id,
			map[string]interface{}{"repository": result.Repository, "commit": result.Commit})
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "expires_at": expires, "inspection": result})
}

func (h *PluginHandler) createPlan(c *gin.Context) {
	var request struct {
		InspectionID string          `json:"inspection_id"`
		CatalogID    string          `json:"catalog_id"`
		InstallID    string          `json:"install_id"`
		Manifest     json.RawMessage `json:"manifest"`
		Driver       string          `json:"driver"`
		Isolation    string          `json:"isolation"`
	}
	if !bindPluginRequest(c, &request) {
		return
	}
	session, _ := security.CurrentSession(c)
	result, err := h.hub.CreatePlan(c.Request.Context(), pluginhub.PlanRequest{
		Actor: session.UserID, InspectionID: request.InspectionID, CatalogID: request.CatalogID,
		InstallID: request.InstallID, Manifest: request.Manifest, Driver: request.Driver, Isolation: request.Isolation,
	})
	if err != nil {
		pluginLifecycleError(c, err)
		return
	}
	record := result.Record
	if h.audit != nil {
		h.audit.RecordOK(c, "plugins", "plan", "Plugin install plan prepared for review", "plugin_plan", record.ID,
			map[string]interface{}{"plan_digest": record.PlanDigest})
	}
	response := pluginPlanResponse(record)
	response["artifact_digest"], response["host_approved"] = result.ArtifactDigest, result.HostApproved
	response["approval_policy"] = h.hub.ApprovalPolicy()
	response["can_confirm"] = h.hub.CanConfirm(record.ManifestDigest, result.ArtifactDigest)
	c.JSON(http.StatusCreated, response)
}

func (h *PluginHandler) getPlan(c *gin.Context) {
	session, _ := security.CurrentSession(c)
	record, err := h.db.GetPluginPlan(c.Request.Context(), c.Param("id"), session.UserID, time.Now())
	if err != nil {
		pluginRecordError(c, err)
		return
	}
	response := pluginPlanResponse(record)
	response["approval_policy"] = h.hub.ApprovalPolicy()
	if plan, err := manifest.DecodeInstallPlan([]byte(record.PlanJSON)); err == nil {
		artifact := pluginhub.ArtifactDigest(plan.Artifact)
		response["artifact_digest"] = artifact
		response["can_confirm"] = record.ApprovedAt == nil && h.hub.CanConfirm(record.ManifestDigest, artifact)
		if record.ApprovedAt != nil {
			response["approved_at"] = record.ApprovedAt
		}
	}
	c.JSON(http.StatusOK, response)
}

func pluginPlanResponse(record database.PluginPlanRecord) gin.H {
	return gin.H{"plan": json.RawMessage(record.PlanJSON), "plan_digest": record.PlanDigest,
		"manifest": json.RawMessage(record.ManifestJSON), "policy": json.RawMessage(record.PolicyJSON)}
}

func (h *PluginHandler) probes(c *gin.Context) {
	isolation := c.DefaultQuery("isolation", "standard")
	if isolation != "standard" && isolation != "strict" {
		pluginError(c, http.StatusUnprocessableEntity, "plugin_invalid_isolation")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	c.JSON(http.StatusOK, gin.H{"execution_enabled": h.hub.Ready(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"drivers": h.hub.Probes(ctx, isolation)})
}

type pluginOperationRequest struct {
	Kind        string `json:"kind"`
	PlanID      string `json:"plan_id"`
	PlanDigest  string `json:"plan_digest"`
	Key         string `json:"idempotency_key"`
	Purge       bool   `json:"purge_data"`
	AutoBindLLM *bool  `json:"auto_bind_llm,omitempty"`
}

func (h *PluginHandler) install(c *gin.Context) {
	var request pluginOperationRequest
	if !bindPluginRequest(c, &request) {
		return
	}
	if request.Kind != "" && request.Kind != "install" {
		pluginError(c, http.StatusUnprocessableEntity, "plugin_invalid_operation")
		return
	}
	request.Kind = "install"
	h.queue(c, request)
}

func (h *PluginHandler) operation(c *gin.Context) {
	var request pluginOperationRequest
	if !bindPluginRequest(c, &request) {
		return
	}
	if request.Kind == "install" {
		pluginError(c, http.StatusUnprocessableEntity, "plugin_invalid_operation")
		return
	}
	h.queue(c, request)
}

func (h *PluginHandler) queue(c *gin.Context, request pluginOperationRequest) {
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		if request.Key != "" && request.Key != key {
			pluginError(c, http.StatusUnprocessableEntity, "plugin_invalid_idempotency_key")
			return
		}
		request.Key = key
	}
	session, _ := security.CurrentSession(c)
	op, err := h.hub.Queue(c.Request.Context(), pluginhub.OperationRequest{
		Actor: session.UserID, Kind: request.Kind, InstallID: c.Param("id"), PlanID: request.PlanID,
		PlanDigest: request.PlanDigest, Key: request.Key, Purge: request.Purge, AutoBindLLM: request.AutoBindLLM,
	})
	if err != nil {
		pluginLifecycleError(c, err)
		return
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "plugins", "operation", "Plugin operation accepted", "plugin_operation", op.ID,
			map[string]interface{}{"kind": op.Kind, "install_id": op.InstallID, "plan_digest": op.PlanDigest})
	}
	c.Header("Location", "/api/plugins/operations/"+op.ID)
	c.JSON(http.StatusAccepted, op)
}

func (h *PluginHandler) getOperation(c *gin.Context) {
	item, err := h.db.GetPluginOperation(c.Request.Context(), c.Param("id"))
	if err != nil {
		pluginRecordError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *PluginHandler) logs(c *gin.Context) {
	id := c.Param("id")
	if _, err := h.db.RuntimePluginInstall(c.Request.Context(), id); err != nil {
		pluginRecordError(c, err)
		return
	}
	cursor := c.DefaultQuery("after", "0")
	if value := c.GetHeader("Last-Event-ID"); value != "" && !c.Request.URL.Query().Has("after") {
		cursor = value
	}
	after, cursorErr := strconv.ParseInt(cursor, 10, 64)
	limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if cursorErr != nil || after < 0 || limitErr != nil || limit < 1 || limit > 200 {
		pluginError(c, http.StatusUnprocessableEntity, "plugin_invalid_log_cursor")
		return
	}
	if strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		h.streamLogs(c, id, after, limit)
		return
	}
	entries, err := h.db.ReadPluginLogs(c.Request.Context(), id, after, limit)
	if err != nil {
		pluginRecordError(c, err)
		return
	}
	if len(entries) != 0 {
		after = entries[len(entries)-1].ID
	}
	c.JSON(http.StatusOK, gin.H{"items": entries, "next_cursor": after})
}

func (h *PluginHandler) streamLogs(c *gin.Context, id string, after int64, limit int) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	_, _ = io.WriteString(c.Writer, ": connected\n\n")
	c.Writer.Flush()
	poll := time.NewTicker(400 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		entries, err := h.db.ReadPluginLogs(ctx, id, after, limit)
		if err != nil {
			return
		}
		for _, entry := range entries {
			raw, _ := json.Marshal(entry)
			if _, err := fmt.Fprintf(c.Writer, "id: %d\nevent: log\ndata: %s\n\n", entry.ID, raw); err != nil {
				return
			}
			after = entry.ID
		}
		if len(entries) != 0 {
			c.Writer.Flush()
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := io.WriteString(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func pluginLifecycleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, pluginhub.ErrInvalidRequest):
		pluginError(c, http.StatusUnprocessableEntity, "plugin_invalid_request")
	case errors.Is(err, pluginhub.ErrManifestRequired), errors.Is(err, pluginhub.ErrCatalogManifest):
		pluginError(c, http.StatusUnprocessableEntity, "plugin_manifest_required")
	case errors.Is(err, pluginhub.ErrInvalidManifest):
		pluginError(c, http.StatusUnprocessableEntity, "plugin_invalid_manifest")
	case errors.Is(err, pluginhub.ErrSourceMismatch):
		pluginError(c, http.StatusConflict, "plugin_manifest_source_mismatch")
	case errors.Is(err, pluginhub.ErrUnapprovedArtifact):
		pluginError(c, http.StatusForbidden, "plugin_artifact_not_approved")
	case errors.Is(err, database.ErrPluginOperationConflict):
		pluginError(c, http.StatusConflict, "plugin_operation_conflict")
	case errors.Is(err, pluginhub.ErrStalePlan), errors.Is(err, database.ErrPluginPlanUnavailable):
		pluginError(c, http.StatusConflict, "plugin_plan_stale")
	case errors.Is(err, pluginhub.ErrRuntimeUnavailable):
		pluginError(c, http.StatusServiceUnavailable, "plugin_runtime_unavailable")
	case errors.Is(err, source.ErrArtifactUnavailable):
		pluginError(c, http.StatusUnprocessableEntity, "plugin_artifact_unavailable")
	case errors.Is(err, sql.ErrNoRows):
		pluginError(c, http.StatusNotFound, "plugin_record_not_found")
	default:
		pluginError(c, http.StatusBadGateway, "plugin_operation_unavailable")
	}
}

func bindPluginRequest(c *gin.Context, value any) bool {
	reader := http.MaxBytesReader(c.Writer, c.Request.Body, manifest.MaxDocumentBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		pluginError(c, http.StatusBadRequest, "plugin_invalid_request")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		pluginError(c, http.StatusBadRequest, "plugin_invalid_request")
		return false
	}
	return true
}

func pluginRecordError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		pluginError(c, http.StatusNotFound, "plugin_record_not_found")
		return
	}
	pluginError(c, http.StatusInternalServerError, "plugin_storage_error")
}

func pluginRuntimeUnavailable(c *gin.Context) {
	pluginError(c, http.StatusNotImplemented, "plugin_runtime_unavailable")
}

func pluginError(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": code})
}
