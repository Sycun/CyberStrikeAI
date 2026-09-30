package handler

import (
	"context"
	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/update"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// UpdateHandler is the "update my own source" surface: what this installation is, what
// its remote has that it does not, and one action that moves the tree and rebuilds the
// binary.
//
// It exists because the alternative was a shell script pointed at one fixed upstream
// repository, which for anyone running a fork meant "downgrade yourself to somebody
// else's code". Here the remote is whatever this directory already tracks, and the person
// at the keyboard never types git or go build.
type UpdateHandler struct {
	root   string // the installation to update: the directory the config file lives in
	logger *zap.Logger
	audit  *audit.Service
	// restart is the operator's own shutdown hook. It is only ever called when the
	// request asked for a restart, because an unsupervised process that exits stays
	// exited - "I restarted you" would be a lie on most deployments.
	restart func()

	mu     sync.Mutex
	jobs   map[string]*updateJob
	order  []string
	active string
}

// updateJob is one running or finished apply. A build takes minutes, so the request
// returns a handle and the page polls it rather than sitting in one HTTP call until the
// compiler finishes.
type updateJob struct {
	ID       string         `json:"id"`
	State    string         `json:"state"` // running | succeeded | failed
	Started  string         `json:"started"`
	Finished string         `json:"finished,omitempty"`
	Steps    []update.Step  `json:"steps"`
	Result   *update.Result `json:"result,omitempty"`
	Failure  *update.Error  `json:"failure,omitempty"`
	Restart  bool           `json:"restartRequested"`
}

// view copies a job for transport, because its fields are written by the job's own
// goroutine: a response must never serialize the live struct. Callers hold h.mu.
func (j *updateJob) view() *updateJob {
	out := *j
	out.Steps = append([]update.Step{}, j.Steps...)
	return &out
}

// NewUpdateHandler takes the audit service as a constructor argument rather than through
// a SetAudit the assembly has to remember, for the same reason PluginHandler does:
// forgetting here is a compile error instead of a privileged endpoint that writes no
// audit records at all.
func NewUpdateHandler(root string, logger *zap.Logger, auditSvc *audit.Service, restart func()) *UpdateHandler {
	return &UpdateHandler{
		root:    root,
		logger:  logger,
		audit:   auditSvc,
		restart: restart,
		jobs:    map[string]*updateJob{},
	}
}

func (h *UpdateHandler) options() update.Options { return update.Options{Root: h.root} }

// GetStatus answers GET /api/system/update: the install tree as it is on disk, without a
// network round trip, so opening the page is never gated on GitHub being reachable.
func (h *UpdateHandler) GetStatus(c *gin.Context) {
	snap, err := update.Status(c.Request.Context(), h.options())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":     snap,
		"job":        h.latest(),
		"canRestart": h.restart != nil,
	})
}

// Check answers POST /api/system/update/check: fetch the tracked branch and report the
// gap. A failed fetch is a 200 with checkError, not a 500 - "offline" and "bad token"
// are answers the operator needs to read, not server faults to debug.
func (h *UpdateHandler) Check(c *gin.Context) {
	snap, err := update.Check(c.Request.Context(), h.options())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.record(c, "update.check", resultOf(snap.CheckError), fmt.Sprintf("检查更新：%s/%s", snap.Remote, snap.Branch), map[string]interface{}{
		"commit": snap.Commit, "remote_commit": snap.RemoteCommit, "behind": snap.Behind, "error": snap.CheckError,
	})
	c.JSON(http.StatusOK, gin.H{"status": snap})
}

// Apply answers POST /api/system/update/apply. It starts one job and returns immediately;
// a second concurrent apply is refused, because two processes moving the same working
// tree and swapping the same binary is how an installation becomes unrecoverable.
func (h *UpdateHandler) Apply(c *gin.Context) {
	var body struct {
		Restart bool `json:"restart"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求体不是合法 JSON"})
			return
		}
	}
	if body.Restart && h.restart == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "本次启动没有提供重启钩子，更新完成后请由外部进程管理器重启服务"})
		return
	}

	job, started := h.startJob(body.Restart)
	if !started {
		c.JSON(http.StatusConflict, gin.H{"error": "已有一次更新在进行中", "job": job})
		return
	}
	h.record(c, "update.apply", "started", "开始一键更新源码并重编译", map[string]interface{}{"job": job.ID, "restart": body.Restart})

	c.JSON(http.StatusAccepted, gin.H{"job_id": job.ID, "state": job.State})
}

// Job answers GET /api/system/update/job: the running job, or the most recent one, with
// every progress line so far.
func (h *UpdateHandler) Job(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"job": h.latest()})
}

// Rollback answers POST /api/system/update/rollback: back to the commit the last update
// came from, with the binary kept before the swap. It refuses when HEAD has moved since
// that update, rather than resetting away work nobody asked it to touch.
func (h *UpdateHandler) Rollback(c *gin.Context) {
	if busy := h.activeJob(); busy != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "更新进行中，不能同时回滚", "job": busy})
		return
	}
	res, err := update.Rollback(c.Request.Context(), h.options())
	if err != nil {
		h.record(c, "update.rollback", "failure", "回滚失败: "+err.Error(), map[string]interface{}{"error": errMessage(err)})
		c.JSON(statusForError(err), errorBody(err))
		return
	}
	h.record(c, "update.rollback", "success", fmt.Sprintf("已回滚到 %s", res.ToCommit), map[string]interface{}{
		"from": res.FromCommit, "to": res.ToCommit,
	})
	c.JSON(http.StatusOK, gin.H{"result": res})
}

func (h *UpdateHandler) startJob(restart bool) (*updateJob, bool) {
	h.mu.Lock()
	if h.active != "" {
		return h.jobs[h.active].view(), false
	}
	job := &updateJob{
		ID:      fmt.Sprintf("upd-%d", time.Now().UnixNano()),
		State:   "running",
		Started: time.Now().Format(time.RFC3339),
		Restart: restart,
	}
	h.jobs[job.ID] = job
	h.order = append(h.order, job.ID)
	for len(h.order) > 20 {
		oldest := h.order[0]
		h.order = h.order[1:]
		if oldest != h.active {
			delete(h.jobs, oldest)
		}
	}
	h.active = job.ID
	snapshot := job.view()
	h.mu.Unlock()

	// The update outlives its HTTP request on purpose: the caller gets a handle, and a
	// browser that closes the tab must not cancel a merge halfway through. Once the
	// working tree has moved there is no going back, so the context is generous with
	// time instead of inheriting the request's deadline.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()

		onStep := func(s update.Step) {
			h.mu.Lock()
			job.Steps = append(job.Steps, s)
			h.mu.Unlock()
			if h.logger != nil {
				h.logger.Info("更新进度", zap.String("phase", s.Phase), zap.String("message", s.Message))
			}
		}
		res, err := update.Apply(ctx, h.options(), onStep)
		finish := "succeeded"
		if err != nil {
			finish = "failed"
		}

		h.mu.Lock()
		job.Result = res
		if ue, ok := asUpdateError(err); ok {
			job.Failure = ue
		} else if err != nil {
			job.Failure = &update.Error{Reason: "error", Message: err.Error()}
		}
		job.State = finish
		job.Finished = time.Now().Format(time.RFC3339)
		if job.Restart && finish == "succeeded" && res != nil && res.BinaryBuilt && h.restart != nil {
			h.active = ""
			h.mu.Unlock()
			// Give the polling page one more chance to read the final state before the
			// process it is watching goes away.
			time.AfterFunc(2*time.Second, h.restart)
			return
		}
		h.active = ""
		h.mu.Unlock()
	}()
	return snapshot, true
}

func (h *UpdateHandler) activeJob() *updateJob {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == "" {
		return nil
	}
	if job := h.jobs[h.active]; job != nil {
		return job.view()
	}
	return nil
}

func (h *UpdateHandler) latest() *updateJob {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.order) == 0 {
		return nil
	}
	if job := h.jobs[h.order[len(h.order)-1]]; job != nil {
		return job.view()
	}
	return nil
}

func (h *UpdateHandler) record(c *gin.Context, action, result, message string, detail map[string]interface{}) {
	if h.audit == nil {
		return
	}
	h.audit.Record(c, audit.Entry{
		Level:        auditLevelFor(result),
		Category:     "update",
		Action:       action,
		Result:       result,
		Message:      message,
		ResourceType: "system",
		ResourceID:   h.root,
		Detail:       detail,
	})
}

func auditLevelFor(result string) string {
	if result == "failure" {
		return "error"
	}
	return "info"
}

func resultOf(checkError string) string {
	if checkError != "" {
		return "failure"
	}
	return "success"
}

func asUpdateError(err error) (*update.Error, bool) {
	if err == nil {
		return nil, false
	}
	if ue, ok := err.(*update.Error); ok {
		return ue, true
	}
	return nil, false
}

func errMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// statusForError keeps a refusal distinguishable from an accident: the precondition
// failures are the ones the page can act on (409/400), while anything else is a genuine
// server-side problem.
func statusForError(err error) int {
	ue, ok := asUpdateError(err)
	if !ok {
		return http.StatusInternalServerError
	}
	switch ue.Reason {
	case "local_source_edits", "diverged", "no_state", "moved_since_update", "no_binary":
		return http.StatusConflict
	case "no_toolchain":
		return http.StatusAccepted
	default:
		return http.StatusBadRequest
	}
}

func errorBody(err error) gin.H {
	body := gin.H{"error": errMessage(err)}
	if ue, ok := asUpdateError(err); ok {
		body["reason"] = ue.Reason
		if len(ue.Items) > 0 {
			body["items"] = ue.Items
		}
	}
	return body
}
