package handler

import "cyberstrike-ai/internal/audit"

// Auditable is implemented by every handler that writes platform audit records.
//
// Eighteen handlers hold an *audit.Service and each declares its own SetAudit, because the
// audit service is built after the handlers that need it. That shape is only safe while
// every one of those setters is actually called; a handler that quietly misses its injection
// serves privileged endpoints that produce no audit trail at all, which is the failure an
// audit system must not have. internal/app binds every handler through one function so that
// completeness is a property of the source, not of the wiring author's memory.
type Auditable interface {
	SetAudit(svc *audit.Service)
}
