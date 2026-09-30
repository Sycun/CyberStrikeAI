package store

import (
	"strings"
	"testing"
)

func TestConstrainConversationScopeAllPassesThrough(t *testing.T) {
	query, args := ConstrainConversation("SELECT 1 FROM t WHERE 1=1", []any{"keep"}, "conversation_id", Access{UserID: "u1", Scope: ScopeAll})
	if strings.Contains(query, "EXISTS") || strings.Contains(query, "1=0") {
		t.Fatalf("unrestricted caller got a visibility clause: %s", query)
	}
	if len(args) != 1 || args[0] != "keep" {
		t.Fatalf("args = %v, want untouched", args)
	}
}

// The clause is applied where a session is expected, so an empty user id is a
// missing session rather than a superuser: it must deny, not widen.
func TestConstrainConversationWithoutUserDeniesEverything(t *testing.T) {
	query, args := ConstrainConversation("SELECT 1 FROM t WHERE 1=1", []any{}, "conversation_id", Access{UserID: "   ", Scope: ScopeOwn})
	if !strings.Contains(query, "1=0") {
		t.Fatalf("caller without an id got: %s", query)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want none bound", args)
	}
}

func TestConstrainConversationBindsTheCallerFourWays(t *testing.T) {
	query, args := ConstrainConversation("SELECT 1 FROM t WHERE 1=1", []any{"existing"}, "conversation_id", Access{UserID: "  u1  ", Scope: ScopeAssigned})
	if strings.Contains(query, "1=0") {
		t.Fatalf("a caller with an id should not be denied outright: %s", query)
	}
	for _, want := range []string{"conversations c", "rbac_resource_assignments ra", "JOIN projects p", "resource_type = 'project'"} {
		if !strings.Contains(query, want) {
			t.Errorf("clause is missing the %q path: %s", want, query)
		}
	}
	if len(args) != 5 {
		t.Fatalf("args = %v, want the caller's own args plus the id four times", args)
	}
	if args[0] != "existing" {
		t.Fatalf("caller args must stay in front of the ones the clause adds")
	}
	for _, bound := range args[1:] {
		if bound != "u1" {
			t.Fatalf("clause bound %v, want the trimmed user id four times", bound)
		}
	}
}

// The column is a query-internal name, not caller input, but the clause has to
// apply to whichever one the hosting query carries.
func TestConstrainConversationUsesTheGivenColumn(t *testing.T) {
	query, _ := ConstrainConversation("SELECT 1 FROM t WHERE 1=1", []any{}, "parent_conversation_id", Access{UserID: "u1", Scope: ScopeOwn})
	if strings.Count(query, "parent_conversation_id") != 6 {
		t.Fatalf("column applied %d times, want all six reference sites: %s",
			strings.Count(query, "parent_conversation_id"), query)
	}
	if strings.Contains(query, "conversation_id =") && !strings.Contains(query, "parent_conversation_id =") {
		t.Fatalf("clause still points at the wrong column: %s", query)
	}
}

func TestAccessSeeAllIsCaseSensitive(t *testing.T) {
	if !(Access{Scope: ScopeAll}).SeeAll() {
		t.Fatal("scope all must read as unrestricted")
	}
	if (Access{Scope: " ALL "}).SeeAll() {
		t.Fatal("only the exact scope value lifts visibility, matching the legacy data layer")
	}
}
