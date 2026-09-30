package capability_test

import (
	"testing"

	"cyberstrike-ai/internal/capability"
)

func subsetSpec(id, name string) *capability.Spec {
	return &capability.Spec{
		ID:         id,
		Name:       name,
		Class:      capability.ClassMutating,
		Runtime:    capability.RuntimeMCPRemote,
		Permission: "mcp:external:execute",
	}
}

func TestRegisterSubsetReplacesOnlyItsOwner(t *testing.T) {
	r := capability.NewRegistry()

	if err := r.RegisterSubset(capability.LayerRemote, "github", []*capability.Spec{
		subsetSpec("remote.github.search", "github::search"),
		subsetSpec("remote.github.issues", "github::issues"),
	}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.RegisterSubset(capability.LayerRemote, "jira", []*capability.Spec{
		subsetSpec("remote.jira.create", "jira::create"),
	}); err != nil {
		t.Fatalf("second register: %v", err)
	}

	// A reconnect that lost one tool: the removed identity must leave, the rest stay, and the
	// other server must be untouched - which is what a whole-layer RegisterAll would break.
	if err := r.RegisterSubset(capability.LayerRemote, "github", []*capability.Spec{
		subsetSpec("remote.github.search", "github::search"),
	}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := r.Lookup("github::issues"); err == nil {
		t.Errorf("github::issues is still resolvable after its owner dropped it")
	}
	if _, err := r.Lookup("github::search"); err != nil {
		t.Errorf("github::search disappeared: %v", err)
	}
	if _, err := r.Lookup("jira::create"); err != nil {
		t.Errorf("refreshing github dropped jira: %v", err)
	}
	if _, ok := r.LookupByID("remote.github.issues"); ok {
		t.Errorf("the stale identity is still addressable by ID")
	}

	if removed := r.UnregisterSubset(capability.LayerRemote, "jira"); removed != 1 {
		t.Fatalf("UnregisterSubset removed %d entries, want 1", removed)
	}
	if _, err := r.Lookup("jira::create"); err == nil {
		t.Errorf("jira::create survived an explicit unregister")
	}
	if _, err := r.Lookup("github::search"); err != nil {
		t.Errorf("unregistering jira took github with it: %v", err)
	}
}

func TestRegisterSubsetIsAllOrNothingPerOwner(t *testing.T) {
	r := capability.NewRegistry()
	if err := r.RegisterSubset(capability.LayerRemote, "github", []*capability.Spec{
		subsetSpec("remote.github.a", "github::a"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The second spec is invalid (unknown class), so nothing from this call may go live and the
	// previous state must survive intact.
	bad := subsetSpec("remote.github.b", "github::b")
	bad.Class = capability.Class("bogus-class")
	err := r.RegisterSubset(capability.LayerRemote, "github", []*capability.Spec{
		subsetSpec("remote.github.kept", "github::kept"),
		bad,
	})
	if err == nil {
		t.Fatalf("RegisterSubset accepted an invalid spec")
	}
	if _, e := r.Lookup("github::a"); e != nil {
		t.Errorf("the rolled-back owner lost its previous tool: %v", e)
	}
	if _, e := r.Lookup("github::kept"); e == nil {
		t.Errorf("a half-applied refresh left github::kept registered")
	}
	if _, e := r.Lookup("github::b"); e == nil {
		t.Errorf("the invalid spec went live anyway")
	}
}

func TestSubsetEntriesStaySeparateFromTheBuiltinLayer(t *testing.T) {
	r := capability.NewRegistry()
	if err := r.Register(capability.LayerBuiltin, subsetSpec("core.echo", "echo")); err != nil {
		t.Fatalf("builtin register: %v", err)
	}
	if err := r.RegisterSubset(capability.LayerRemote, "github", []*capability.Spec{
		subsetSpec("remote.github.echo", "github::echo"),
	}); err != nil {
		t.Fatalf("subset register: %v", err)
	}
	// Dropping the remote owner must leave the built-in policy reachable: the two layers claim
	// different identities, and a server removal is not a policy reset.
	r.UnregisterSubset(capability.LayerRemote, "github")
	if _, err := r.Lookup("echo"); err != nil {
		t.Fatalf("built-in policy vanished with the remote subset: %v", err)
	}
	if spec, err := r.Lookup("github::echo"); err == nil {
		t.Fatalf("remote entry survived its owner's removal: %+v", spec.ID)
	}
}

func TestRegisterSubsetRejectsAnUnclaimableSpec(t *testing.T) {
	r := capability.NewRegistry()
	err := r.RegisterSubset(capability.LayerRemote, "github", []*capability.Spec{{
		ID: "remote.github.x", Name: "", Class: capability.ClassMutating,
	}})
	if err == nil {
		t.Fatalf("RegisterSubset accepted a spec with no wire name")
	}
	if _, e := r.Lookup("github::x"); e == nil {
		t.Fatalf("a rejected spec is still resolvable")
	}
}
