package artifact

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Gate 1 of the review pipeline: static scan of the submitted text, plus the
// content rules the report requires for community knowledge, prompts and roles.
//
// These are code-level controls, not prompt-level requests. Community text ends up
// inside the context of an agent that can drive a real C2 and webshell, and the
// published evidence is that a handful of poisoned documents is enough to steer it.

// Severity ranks a finding.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityBlock Severity = "block"
)

// Finding is one static-scan result.
type Finding struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	Line     int      `json:"line"`
	Snippet  string   `json:"snippet"`
	Message  string   `json:"message"`
}

var (
	reInjectionPhrases = regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override)\s+(all\s+)?(previous|prior|above|earlier)\s+(instructions?|rules?|context)\b`)
	reYouMust          = regexp.MustCompile(`(?i)\byou\s+(must|shall|have to|are required to)\s+`)
	reImperativeTool   = regexp.MustCompile(`(?i)\b(run|execute|invoke|launch|fire)\s+(this|the)\s+(command|tool|payload|script)\b`)
	reExfilRequest     = regexp.MustCompile(`(?i)(send|upload|post|exfiltrate)\s+(it|the\s+(file|output|result|credentials?|key))\s+to\s+https?://`)
	// Dynamic context that executes before the model sees anything - the mechanism
	// behind the recorded malicious-skill incident, and the reason rendering must
	// never run a command.
	reShellInterpolation = regexp.MustCompile("![`$][a-zA-Z0-9_./ -]{2,}")
	rePipeToShell        = regexp.MustCompile(`(?i)(curl|wget)\s+[^|]*\|\s*(sh|bash|zsh|python)\b`)
	reSecretMarkers      = regexp.MustCompile(`(?i)(BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY|AKIA[0-9A-Z]{16}|ghp_[A-Za0-9]{36}|xox[baprs]-[0-9A-Za-z-]{10,})`)
	reMarkdownLinkURL    = regexp.MustCompile(`(?i)\bhttps?://[^\s)"']+`)
	reControlChars       = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
	reHiddenAngleTags    = regexp.MustCompile(`(?i)<(scaffold|system|assistant|user|instructions?|thinking)\s*>`)
)

// Lint scans community-authored text of a given kind. "knowledge" is treated most
// strictly: it is the artifact class that reaches a retrieval-augmented agent.
func Lint(kind, text string) []Finding {
	var findings []Finding
	lines := strings.Split(text, "\n")

	for index, line := range lines {
		lineNumber := index + 1
		add := func(rule string, severity Severity, message string) {
			findings = append(findings, Finding{
				Rule: rule, Severity: severity, Line: lineNumber,
				Snippet: clip(line, 120), Message: message,
			})
		}

		if reInjectionPhrases.MatchString(line) {
			add("instruction_override", SeverityBlock, "text tries to override prior instructions")
		}
		if reExfilRequest.MatchString(line) {
			add("exfiltration_request", SeverityBlock, "text asks for data to be sent to a URL")
		}
		if reSecretMarkers.MatchString(line) {
			add("embedded_secret", SeverityBlock, "text contains what looks like a private key or API token")
		}
		if rePipeToShell.MatchString(line) {
			add("pipe_to_interpreter", SeverityBlock, "text downloads code and pipes it into an interpreter")
		}
		if reShellInterpolation.MatchString(line) {
			add("dynamic_context_shell", SeverityBlock,
				"text embeds shell interpolation; rendering must never execute artifact content")
		}
		if reHiddenAngleTags.MatchString(line) {
			add("role_tag_smuggling", SeverityBlock, "text embeds role tags that impersonate the conversation structure")
		}
		if reControlChars.MatchString(line) {
			add("control_characters", SeverityWarn, "text contains non-printable control characters")
		}
		if kind == "knowledge" || kind == "prompt" || kind == "role" {
			if reYouMust.MatchString(line) {
				add("imperative_to_agent", SeverityWarn,
					"community text addresses the agent as an instruction rather than as reference material")
			}
			if reImperativeTool.MatchString(line) {
				add("imperative_tool_call", SeverityWarn,
					"community text asks the agent to run a command or tool")
			}
			if reMarkdownLinkURL.MatchString(line) {
				add("embedded_url", SeverityInfo,
					"URLs in community knowledge are stripped before embedding; see SanitizeForIndex")
			}
		}
	}
	return findings
}

// Blocks reports whether any finding prevents publication.
func Blocks(findings []Finding) bool {
	for _, finding := range findings {
		if finding.Severity == SeverityBlock {
			return true
		}
	}
	return false
}

// SanitizeForIndex removes network indicators from community text before it is
// embedded. Retrieval should be able to say "this host exposes SMB" without
// conditioning an executing agent on a specific address the submitter chose.
func SanitizeForIndex(text string) (string, []string) {
	removed := []string{}

	replaced := reMarkdownLinkURL.ReplaceAllStringFunc(text, func(match string) string {
		removed = append(removed, match)
		return "[redacted-url]"
	})

	cidr := regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?:/\d{1,2})?\b`)
	replaced = cidr.ReplaceAllStringFunc(replaced, func(match string) string {
		removed = append(removed, match)
		if strings.Contains(match, "/") {
			return "[redacted-cidr]"
		}
		return "[redacted-ip]"
	})

	urlless := replaced
	if host := net.ParseIP(strings.TrimSpace(urlless)); host != nil {
		removed = append(removed, urlless)
		urlless = ""
	}
	return urlless, removed
}

func clip(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}

// Review is the recorded state of one submission moving through the four gates.
type Review struct {
	ArtifactID     string    `json:"artifactId"`
	Digest         string    `json:"digest"`
	Version        string    `json:"version"`
	SubmittedBy    string    `json:"submittedBy"`
	ReviewedBy     []string  `json:"reviewedBy,omitempty"`
	StaticFindings []Finding `json:"staticFindings,omitempty"`
	SandboxRan     bool      `json:"sandboxRan"`
	SandboxLog     []string  `json:"sandboxLog,omitempty"`
	Delta          Delta     `json:"delta"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Verdict is the pipeline's decision on one submission.
type Verdict struct {
	Allowed          bool     `json:"allowed"`
	Reasons          []string `json:"reasons,omitempty"`
	NeedsSecondHuman bool     `json:"needsSecondHuman"`
	Delta            Delta    `json:"delta,omitempty"`
}

// GateOrder is the fixed review sequence: static scan, then sandbox detonation,
// then the human checklist, then publication. Submissions arrive as pull requests;
// there is no self-service upload button.
var GateOrder = []string{"static", "sandbox", "human", "publish"}

// Decide runs the deterministic part of the pipeline for one bundle. It assumes the
// caller already verified the signature against the trust store, since an untrusted
// artifact never reaches review.
func Decide(bundle Bundle, baseline *Manifest, review Review) Verdict {
	verdict := Verdict{Allowed: true}

	if bundle.Signature == nil {
		return Verdict{Allowed: false, Reasons: []string{ErrUnsigned.Error()}}
	}

	manifest := bundle.Manifest
	if blocked := Blocks(Lint(kindFromRuntime(manifest.Runtime), joinPayloadText(manifest))); blocked {
		verdict.Allowed = false
		verdict.Reasons = append(verdict.Reasons, "static scan found a blocking rule")
	}

	delta := Diff(&manifest, baseline)
	verdict.Delta = delta
	if delta.RequiresHumanReReview() {
		verdict.NeedsSecondHuman = true
		verdict.Reasons = append(verdict.Reasons, delta.Reasons()...)
	}
	if verdict.NeedsSecondHuman && len(review.ReviewedBy) < 2 {
		verdict.Allowed = false
		verdict.Reasons = append(verdict.Reasons, "capability delta requires an independent second reviewer before publish")
	}
	if strings.TrimSpace(review.SubmittedBy) != "" && containsFold(review.ReviewedBy, review.SubmittedBy) {
		verdict.Allowed = false
		verdict.Reasons = append(verdict.Reasons, "author cannot be the reviewer")
	}
	if !review.SandboxRan {
		verdict.Allowed = false
		verdict.Reasons = append(verdict.Reasons, "sandbox detonation (no-network first boot, then labelled egress) has not been recorded")
	}
	return verdict
}

func kindFromRuntime(runtime string) string {
	switch {
	case strings.Contains(runtime, "python"), strings.Contains(runtime, "plugin"):
		return "plugin"
	case strings.Contains(runtime, "mcp"):
		return "prompt"
	default:
		return "knowledge"
	}
}

func joinPayloadText(manifest Manifest) string {
	parts := make([]string, 0, len(manifest.Payloads))
	for _, file := range manifest.Payloads {
		parts = append(parts, file.Text)
	}
	return strings.Join(parts, "\n")
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

// FormatReasons renders a verdict for the review UI or PR comment.
func (v Verdict) FormatReasons() string {
	if v.Allowed && len(v.Reasons) == 0 {
		return "ready to publish"
	}
	sort.Strings(v.Reasons)
	out := make([]string, 0, len(v.Reasons))
	for index, reason := range v.Reasons {
		out = append(out, fmt.Sprintf("%d. %s", index+1, reason))
	}
	return strings.Join(out, "\n")
}
