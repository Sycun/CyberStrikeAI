package capability

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Outcome is the terminal state of one policy evaluation.
type Outcome string

const (
	OutcomeAllow  Outcome = "allow"
	OutcomeDeny   Outcome = "deny"
	OutcomeAsk    Outcome = "ask"
	OutcomeNotice Outcome = "error"
)

// Decision is the non-repudiable record of one evaluation. Every Decide call
// produces exactly one, including denials and internal errors.
type Decision struct {
	CapabilityID string
	ToolName     string
	PrincipalID  string
	Outcome      Outcome
	Reason       string
	Permission   string
	Class        Class
	Stage        string
	Duration     time.Duration
	Timestamp    time.Time
}

// Denied reports a refusal.
func (d Decision) Denied() bool { return d.Outcome == OutcomeDeny || d.Outcome == OutcomeNotice }

// Source is where a denial or grant came from, so the audit trail can name the stage.
type StageEvaluator struct {
	Name string
	Eval func(ctx context.Context, s *Spec, p Principal, args map[string]any, d Deps) (reason string, block bool)
}

// Evaluator is the single policy entry point. Order is fixed and non-negotiable:
// registration → deny → scoped check → approval floor → allow. A stage that
// cannot complete returns a denial, never a pass-through.
type Evaluator struct {
	registry   *Registry
	deps       Deps
	postDeny   []func(Decision)
	postAllow  []func(Decision)
	approvals  *ApprovalLedger
	evaluators []StageEvaluator
}

// Option configures an Evaluator.
type Option func(*Evaluator)

// WithDeps injects the side effects a check may consult.
func WithDeps(d Deps) Option {
	return func(e *Evaluator) { e.deps = d }
}

// WithStage appends a custom evaluation stage (used by the tool-guard and
// revocation layers). Stages run after registration and before the allow.
func WithStage(name string, fn func(context.Context, *Spec, Principal, map[string]any, Deps) (string, bool)) Option {
	return func(e *Evaluator) { e.evaluators = append(e.evaluators, StageEvaluator{Name: name, Eval: fn}) }
}

// WithAudit records every decision. Denials are recorded too; a governance
// product cannot keep only the successful path.
func WithAudit(onDeny, onAllow func(Decision)) Option {
	return func(e *Evaluator) {
		e.postDeny = append(e.postDeny, onDeny)
		e.postAllow = append(e.postAllow, onAllow)
	}
}

func NewEvaluator(r *Registry, opts ...Option) *Evaluator {
	e := &Evaluator{registry: r, approvals: GlobalApprovals()}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Registry exposes the underlying registry for wiring and code generation.
func (e *Evaluator) Registry() *Registry { return e.registry }

// ErrNoPrincipal is returned when a call reaches the evaluator unauthenticated.
var ErrNoPrincipal = errors.New("missing authenticated principal")

// Decide evaluates one call. It never panics on a nil principal or a missing
// spec, and it never returns OutcomeAllow unless every stage passed.
func (e *Evaluator) Decide(ctx context.Context, toolName string, args map[string]any) Decision {
	started := time.Now()
	decision := Decision{ToolName: toolName, Timestamp: started, Stage: "register"}

	if args == nil {
		args = map[string]any{}
	}

	spec, err := e.registry.Lookup(toolName)
	if err != nil {
		decision.Outcome = OutcomeDeny
		decision.Reason = err.Error()
		e.record(decision)
		return decision
	}
	decision.CapabilityID = spec.ID
	decision.Class = spec.Class
	decision.Permission = spec.Permission
	decision.Stage = "principal"

	principal, ok := principalFromContext(ctx)
	if !ok || principal == nil {
		decision.Outcome = OutcomeDeny
		decision.Reason = ErrNoPrincipal.Error()
		e.record(decision)
		return decision
	}
	decision.PrincipalID = principal.UserID()

	deps := e.deps
	if injected, ok := depsFromContext(ctx); ok {
		deps = injected
	}

	// Deny on scope before any per-tool logic runs.
	if spec.Permission != "" && !principal.HasPermission(spec.Permission) {
		decision.Outcome = OutcomeDeny
		decision.Reason = fmt.Sprintf("missing permission %s", spec.Permission)
		e.record(decision)
		return decision
	}

	// Custom stages (tool guard, revocation, project boundary).
	decision.Stage = "stage"
	for _, st := range e.evaluators {
		if reason, block := st.Eval(ctx, spec, principal, args, deps); block {
			decision.Outcome = OutcomeDeny
			decision.Reason = reason
			decision.Stage = st.Name
			e.record(decision)
			return decision
		}
	}

	// Per-tool check. A panic or a nil Deps is a denial, not a pass.
	if spec.Check != nil {
		decision.Stage = "check"
		if deps == nil {
			decision.Outcome = OutcomeDeny
			decision.Reason = "policy dependencies are not configured"
			e.record(decision)
			return decision
		}
		if err := func() (err error) {
			defer func() {
				if rec := recover(); rec != nil {
					err = fmt.Errorf("policy check panicked: %v", rec)
				}
			}()
			return spec.Check(ctx, principal, args, deps)
		}(); err != nil {
			decision.Outcome = OutcomeDeny
			decision.Reason = err.Error()
			e.record(decision)
			return decision
		}
	}

	// Approval floor: destructive capabilities require a human even when the
	// session never opted into HITL. A spent ledger grant is the only way past
	// it, which keeps "approve once" scoped to one invocation.
	decision.Stage = "approval"
	if spec.RequiresHumanDecision() && !e.approvalSatisfied(ctx, spec, deps, toolName) {
		decision.Outcome = OutcomeAsk
		decision.Reason = fmt.Sprintf("capability %s is %s and requires per-call authorization", spec.ID, spec.Class)
		e.recordAsk(decision)
		return decision
	}

	decision.Outcome = OutcomeAllow
	decision.Duration = time.Since(started)
	e.record(decision)
	return decision
}

// approvalSatisfied accepts a context-scoped decision or spends a single-use
// grant from the ledger for the conversation executing the call.
func (e *Evaluator) approvalSatisfied(ctx context.Context, spec *Spec, deps Deps, toolName string) bool {
	if approvalGranted(ctx) {
		return true
	}
	ledger := e.approvals
	if ledger == nil || deps == nil {
		return false
	}
	conversationID := deps.ConversationID(ctx)
	if conversationID == "" {
		return false
	}
	return ledger.Consume(conversationID, toolName)
}

// Approvable reports whether a capability demands a human decision, used by the
// HITL layer before it consults any whitelist.
func (e *Evaluator) Approvable(toolName string) (bool, Class, error) {
	spec, err := e.registry.Lookup(toolName)
	if err != nil {
		return false, "", err
	}
	return spec.RequiresHumanDecision(), spec.Class, nil
}

// Grants returns the mediated-side-effect ceiling for a capability, so the
// plugin host can refuse anything the manifest never declared.
func (e *Evaluator) Grants(toolName string) ([]CapabilityGrant, error) {
	spec, err := e.registry.Lookup(toolName)
	if err != nil {
		return nil, err
	}
	return spec.Grants, nil
}

// GrantAllowed reports whether a requested mediated call falls inside the
// approved set. Unknown capabilities and unknown grants both fail closed.
func (e *Evaluator) GrantAllowed(toolName, requested string) bool {
	spec, err := e.registry.Lookup(toolName)
	if err != nil {
		return false
	}
	g, err := ParseGrant(requested)
	if err != nil {
		return false
	}
	for _, allowed := range spec.Grants {
		if allowed.Name != g.Name {
			continue
		}
		if allowed.Target == "" || allowed.Target == "*" {
			return true
		}
		if allowed.Target == g.Target {
			return true
		}
	}
	return false
}

func (e *Evaluator) record(d Decision) {
	if d.Denied() {
		for _, fn := range e.postDeny {
			fn(d)
		}
		return
	}
	if d.Outcome == OutcomeAllow {
		for _, fn := range e.postAllow {
			fn(d)
		}
	}
}

func (e *Evaluator) recordAsk(d Decision) {
	for _, fn := range e.postDeny {
		fn(d)
	}
}
