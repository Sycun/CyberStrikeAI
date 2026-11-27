package multiagent

import (
	"strings"
	"testing"

	"cyberstrike-ai/internal/project"
	"cyberstrike-ai/internal/skillpackage"
)

func TestPiSystemPromptPartsPreserveModelText(t *testing.T) {
	role := "  Stable role instructions.  "
	first := project.AppendSystemPromptBlock(role, project.FrameDerived("project_blackboard", "One"))
	second := project.AppendSystemPromptBlock(role, project.FrameDerived("project_blackboard", "Two"))
	static, tail := piSystemPromptParts(first, role, true, false)
	other, otherTail := piSystemPromptParts(second, role, true, false)
	if static != strings.TrimSpace(role) || static != other || tail == otherTail || static+tail != first || other+otherTail != second {
		t.Fatal("Pi prompt split changed the prompt or leaked run context to the prefix")
	}
	if full, empty := piSystemPromptParts(first, role, false, true); full != first || empty != "" {
		t.Fatal("disabled split changed the Pi request")
	}
	if unexpected, dynamic := piSystemPromptParts(first, first, true, false); unexpected != "" || dynamic != first {
		t.Fatal("framed text entered the static prompt")
	}
}

func TestCompactSkillIndexBoundsAndSortsNamesOnly(t *testing.T) {
	if got := compactSkillIndexBlock(nil); got != "" {
		t.Fatalf("empty skills produced block: %q", got)
	}
	skills := []skillpackage.SkillSummary{
		{ID: "zeta-check", Description: "long description should not be duplicated"},
		{ID: "alpha-auth", Description: "another description"},
	}
	got := compactSkillIndexBlock(skills)
	if !strings.Contains(got, "alpha-auth, zeta-check") || strings.Contains(got, "long description") {
		t.Fatalf("unexpected skill index: %q", got)
	}
}

func TestPiSystemPromptScopePolicyUsesStablePrefix(t *testing.T) {
	const role = "Stable role instructions."
	want := project.AppendSystemPromptBlock(role, project.ScopeAuthorizationPolicy)
	for _, observation := range []string{"Stored note A", "Stored note B", "Stored note C"} {
		t.Run(observation, func(t *testing.T) {
			t.Parallel()
			frame := project.FrameDerived("project_blackboard", observation)
			full := project.AppendSystemPromptBlock(role, frame)
			static, dynamic := piSystemPromptParts(full, role, true, true)
			if static != want || strings.Contains(static, "CSAI_DATA") || dynamic != "\n\n"+frame {
				t.Fatalf("unexpected split: %q, %q", static, dynamic)
			}
		})
	}
}
