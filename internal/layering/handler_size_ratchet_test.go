package layering

import (
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The report's P6 line asks for `AgentHandler` to be decomposed, and the numbers it quotes
// (19 files declaring methods on it, 26 SetXxx) were a diagnosis, not a ratchet - and both
// numbers were low. Measured on this tree, after collapsing the three HITL config savers
// into one injection point:
//
//	AgentHandler: 130 methods across 23 files at the start of the decomposition
//	Set* methods in internal/handler: 64 across 21 receiver types,
//	18 of which are the same SetAudit repeated handler by handler
//
// Those became the ceilings. Two cuts have landed since: the HITL interrupt read surface (nine
// methods, hitl_queue.go) and the finalization service (ten methods, runFinalizer in
// finalization_helpers.go), which is why the numbers below are 112/21 rather than 130/23 - and why
// the setter ceiling did not move: neither collaborator declares a Set* method. The finalizer takes
// four things and nothing else (narrowed storage, logger, one cancel call, one message-content
// write), and AgentHandler hands itself in for the single write it needs.
//
// A decomposition is only real if the number goes down and stays
// down, and "stays down" needs a gate - otherwise the next feature that needs a handle on
// something adds one more method to the biggest type because that is the path of least
// resistance, and the doc's diagnosis quietly becomes worse.

const (
	agentHandlerMethodCeiling = 112
	agentHandlerFileCeiling   = 21
	setterCeiling             = 64
)

// receiverMethodCeilings pins every large handler type, so progress on one is not cancelled
// by drift on another. Values are the measured counts on this tree.
var receiverMethodCeilings = map[string]int{
	"AgentHandler":       agentHandlerMethodCeiling,
	"RobotHandler":       68,
	"ConfigHandler":      45,
	"BatchTaskManager":   40,
	"C2Handler":          39,
	"AgentTaskManager":   34,
	"ChatUploadsHandler": 34,
	"WorkflowHandler":    25,
}

func TestHandlerSizesOnlyShrink(t *testing.T) {
	root := moduleRoot(t)
	perType, perFile, err := MethodsByReceiver(root, "internal/handler")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(perType) < 10 {
		t.Fatalf("only %d receiver types with methods found; the scan is broken", len(perType))
	}

	var regressions []string
	for receiver, ceiling := range receiverMethodCeilings {
		got := perType[receiver]
		if got > ceiling {
			regressions = append(regressions, receiver+": "+strconv.Itoa(got)+" methods > ceiling "+strconv.Itoa(ceiling))
		}
		if files := len(perFile[receiver]); receiver == "AgentHandler" && files > agentHandlerFileCeiling {
			regressions = append(regressions, receiver+" declared across "+strconv.Itoa(files)+
				" files > ceiling "+strconv.Itoa(agentHandlerFileCeiling))
		}
		if got < ceiling {
			// Improvement is reported, never punished.
			t.Logf("%s dropped to %d methods (ceiling %d): tighten receiverMethodCeilings", receiver, got, ceiling)
		}
	}
	sort.Strings(regressions)
	if len(regressions) > 0 {
		t.Fatalf("handler types grew past their measured ceilings: %v. New capability belongs in a "+
			"collaborator with its own state, not as one more method on the biggest type.", regressions)
	}
}

func TestHandlerSettersOnlyShrink(t *testing.T) {
	root := moduleRoot(t)
	total, byReceiver, err := CountAllSetters(root, "internal/handler")
	if err != nil {
		t.Fatalf("count setters: %v", err)
	}
	var detail []string
	for receiver, names := range byReceiver {
		detail = append(detail, receiver+"="+strconv.Itoa(len(names))+"["+strings.Join(names, " ")+"]")
	}
	sort.Strings(detail)
	t.Logf("Set* methods across the handler layer: %d over %d receiver types", total, len(byReceiver))
	if total > setterCeiling {
		t.Fatalf("%d Set* injection methods in internal/handler (ceiling %d): %v. "+
			"A setter the wiring must remember is a half-initialized object; pass the dependency in, or group them.",
			total, setterCeiling, detail)
	}
	if total < setterCeiling {
		t.Logf("setters dropped to %d; tighten setterCeiling", total)
	}
}

func TestHandlerLayerScanIsSane(t *testing.T) {
	// The ceilings only mean something if the scan reads the whole package: a one-file
	// reader would report a tiny AgentHandler and pass.
	root := moduleRoot(t)
	perType, perFile, err := MethodsByReceiver(root, "internal/handler")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	files := map[string]bool{}
	for _, byFile := range perFile {
		for name := range byFile {
			files[name] = true
		}
	}
	if len(files) < 50 {
		t.Fatalf("the scan only saw %d handler files; it is reading a subset", len(files))
	}
	if perType["AgentHandler"] < 100 {
		t.Fatalf("the scan counted %d AgentHandler methods; expected at least 100 (measured 130)", perType["AgentHandler"])
	}
	t.Logf("scan covers %d files, %d receiver types", len(files), len(perType))
}
