package capability

import (
	"sync"
	"time"
)

// ApprovalLedger is the durable record of human decisions that release a
// capability requiring per-call authorization. A grant is single-use and short
// lived: approving one destructive call must not authorize the next one.
//
// Grants only ever *narrow* what an operator allowed. Nothing in the store,
// a role, a skill, or an HTTP request body can add an exemption here; the only
// writer is the HITL layer after a human has decided.
type ApprovalLedger struct {
	mu       sync.Mutex
	grants   map[string][]*approvalGrant
	timeout  time.Duration
	now      func() time.Time
	registry *Registry
}

type approvalGrant struct {
	capabilityID string
	expiresAt    time.Time
	uses         int
}

// NewApprovalLedger builds a ledger whose grants expire after ttl. A ttl of zero
// falls back to 60 seconds so a stale approval can never sit and authorize work.
func NewApprovalLedger(ttl time.Duration) *ApprovalLedger {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &ApprovalLedger{
		grants:   map[string][]*approvalGrant{},
		timeout:  ttl,
		now:      time.Now,
		registry: Global(),
	}
}

// UseRegistry points a ledger at a specific registry. Production uses the process
// registry; tests need to exercise approval semantics without polluting it.
func (l *ApprovalLedger) UseRegistry(registry *Registry) *ApprovalLedger {
	l.registry = registry
	return l
}

// GlobalApprovals is the process-wide ledger. Both the HITL layer (writer) and
// the authorizer (reader) consult it, so no second approval path can appear.
var globalApprovals = NewApprovalLedger(0)

// GlobalApprovals returns the process ledger.
func GlobalApprovals() *ApprovalLedger { return globalApprovals }

// Grant records that a human approved one invocation of toolName inside
// conversationID. uses caps how many calls the decision releases.
func (l *ApprovalLedger) Grant(conversationID, toolName string, uses int) {
	if conversationID == "" || toolName == "" {
		return
	}
	if uses <= 0 {
		uses = 1
	}
	spec, err := l.registry.Lookup(toolName)
	if err != nil {
		return
	}
	if !spec.RequiresHumanDecision() {
		// Only the approval floor can be granted; permissions are not.
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.grants[conversationID] = append(l.grants[conversationID], &approvalGrant{
		capabilityID: spec.ID,
		expiresAt:    l.now().Add(l.timeout),
		uses:         uses,
	})
}

// Consume spends one approval for the capability behind toolName.
func (l *ApprovalLedger) Consume(conversationID, toolName string) bool {
	if l == nil || conversationID == "" || toolName == "" {
		return false
	}
	spec, err := l.registry.Lookup(toolName)
	if err != nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	list := l.grants[conversationID]
	now := l.now()
	for i, g := range list {
		if g.uses <= 0 || g.expiresAt.Before(now) || g.capabilityID != spec.ID {
			continue
		}
		g.uses--
		if g.uses == 0 {
			l.grants[conversationID] = append(list[:i], list[i+1:]...)
			if len(l.grants[conversationID]) == 0 {
				delete(l.grants, conversationID)
			}
		}
		return true
	}
	return false
}

// RevokeConversation drops every pending approval for a conversation, used when
// a session ends or an operator revokes trust.
func (l *ApprovalLedger) RevokeConversation(conversationID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.grants, conversationID)
}

// Pending reports how many unspent grants a conversation holds (audit surface).
func (l *ApprovalLedger) Pending(conversationID string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	count := 0
	for _, g := range l.grants[conversationID] {
		if g.uses > 0 && !g.expiresAt.Before(now) {
			count += g.uses
		}
	}
	return count
}
