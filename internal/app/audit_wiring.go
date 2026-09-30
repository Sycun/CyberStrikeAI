package app

import (
	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/handler"
)

// bindAudit hands the platform audit service to one handler.
//
// The audit service is built after the handlers that need it, so eighteen of them expose a
// SetAudit injection and the wiring has to remember to call each one. A handler that misses
// its injection does not fail: it serves privileged endpoints that write no audit records at
// all. Routing every injection through this one function makes that a property of the source
// (internal/app/audit_wiring_test.go compares these calls against the SetAudit declarations
// in the handler package) instead of a property of whoever last edited New().
//
// No nil guard: a nil handler here is a programming error, and panicking at startup is the
// correct outcome - silently skipping the injection is precisely the defect being prevented.
func bindAudit(h handler.Auditable, svc *audit.Service) {
	h.SetAudit(svc)
}
