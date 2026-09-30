// Package provider separates API dialects from vendor names.
//
// The failure mode this removes: behaviour decided by comparing provider strings at
// a dozen call sites, so adding a vendor means editing code, and two vendors that
// speak the same wire protocol silently diverge because each branch grew on its own.
// A Dialect is data: endpoint shape, auth shape, capability matrix, retry policy and
// error markers. Adding a vendor is a catalog row plus, at most, one dialect variant.
package provider

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// API is the wire dialect.
type API string

const (
	// APIOpenAIChat is POST {base}/chat/completions with Bearer auth.
	APIOpenAIChat API = "openai-chat"
	// APIOpenAIResponses is POST {base}/responses.
	APIOpenAIResponses API = "openai-responses"
	// APIAnthropicMessages is POST {base}/v1/messages with x-api-key + anthropic-version.
	APIAnthropicMessages API = "anthropic-messages"
)

// ErrorClass is the normalized outcome of a failed model call. Callers branch on this,
// never on a vendor name or a substring of the vendor's error text.
type ErrorClass string

const (
	ClassOK              ErrorClass = ""
	ClassContextOverflow ErrorClass = "context_overflow"
	ClassRateLimited     ErrorClass = "rate_limited"
	ClassTransient       ErrorClass = "transient"
	ClassAborted         ErrorClass = "aborted"
	ClassAuth            ErrorClass = "auth"
	ClassUnsupported     ErrorClass = "unsupported"
	ClassUnknown         ErrorClass = "unknown"
)

// Capabilities is what a dialect can be asked to do. Anything not here must not be
// probed with string comparisons elsewhere.
type Capabilities struct {
	// ThinkingBlocks means the vendor takes an explicit thinking budget in the request body.
	ThinkingBlocks bool
	// ReasoningEffort means the vendor takes reasoning_effort=low|medium|high.
	ReasoningEffort bool
	// ForcedToolChoice is risky on some gateways: they reject tool_choice when
	// reasoning is on, so callers must degrade to auto.
	ForcedToolChoiceUnsafe bool
	// VisionInput accepts image parts in message content.
	VisionInput bool
	// StrictJSONSchema supports structured output with a strict flag.
	StrictJSONSchema bool
	// RejectsDanglingToolCalls refuses a history where a tool call has no result.
	RejectsDanglingToolCalls bool
	// AgenticBackend means the runtime can drive this dialect through the shared
	// agentic chat-model path. It mirrors the rule the runtime used before this layer
	// existed, so migrating does not silently widen or narrow what is supported.
	AgenticBackend bool
	// StreamCarriesErrorsInBody means a 200 response can contain an error event,
	// so status codes alone are not enough to detect failure.
	StreamCarriesErrorsInBody bool
}

// Retry is the dialect's retry posture. The status set mirrors what the runtime
// already treats as transient so behaviour does not shift when sites migrate here.
type Retry struct {
	RetryableStatus    []int
	RetryableOn5xx     bool
	MaxAttempts        int
	BackoffStartMillis int
}

// Cost is in USD per million tokens; it is data so budget reporting does not need a
// vendor switch either.
type Cost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Dialect is one row of the catalog.
type Dialect struct {
	Vendor           string
	API              API
	BaseURL          string
	ContextWindow    int
	MaxTokensDefault int
	Cost             Cost
	Capabilities     Capabilities
	Retry            Retry
	// OverflowMarkers are the vendor phrases that mean "input too long". Matching is
	// lower-cased substring, identical to what the budget middleware does today.
	OverflowMarkers []string
	// DefaultBaseURL is used only when the deployment leaves base_url empty. Vendors
	// without an entry fall back to the chat default, matching what the settings
	// handler did before this table existed.
	DefaultBaseURL string
	// AliasesTo names the vendor whose behaviour this row inherits for channel
	// normalisation purposes (an "openai_compatible" channel behaves as "openai").
	AliasesTo string
}

// Default is the retry posture shared by every dialect until a vendor proves it
// differs: the set the runtime already treats as transient.
func defaultRetry() Retry {
	return Retry{
		RetryableStatus:    []int{http.StatusRequestTimeout, http.StatusConflict, 425, http.StatusTooManyRequests},
		RetryableOn5xx:     true,
		MaxAttempts:        3,
		BackoffStartMillis: 500,
	}
}

// Catalog is the vendor table. Keys are the values users put in openai.provider.
type Catalog struct {
	dialects map[string]Dialect
}

// NewCatalog builds the shipped catalog. A deployment can add a vendor without
// touching any of the call sites that ask these questions.
func NewCatalog() *Catalog {
	c := &Catalog{dialects: map[string]Dialect{}}
	for _, d := range []Dialect{
		{
			Vendor: "openai", API: APIOpenAIChat, ContextWindow: 400_000, MaxTokensDefault: 16_384,
			Cost:            Cost{Input: 2.50, Output: 10.00},
			DefaultBaseURL:  "https://api.openai.com/v1",
			Capabilities:    Capabilities{ReasoningEffort: true, VisionInput: true, StrictJSONSchema: true, RejectsDanglingToolCalls: true, AgenticBackend: true},
			OverflowMarkers: []string{"context_length", "input is too long", "maximum context length"},
		},
		{
			// The catch-all for gateways that speak the chat protocol. Defaults must stay
			// permissive: a self-hosted gateway is not going to match any vendor's quirks.
			Vendor: "openai_compatible", API: APIOpenAIChat, ContextWindow: 128_000, MaxTokensDefault: 8_192,
			DefaultBaseURL:  "https://api.openai.com/v1",
			Capabilities:    Capabilities{VisionInput: true, ForcedToolChoiceUnsafe: true, RejectsDanglingToolCalls: true, AgenticBackend: true},
			AliasesTo:       "openai",
			OverflowMarkers: []string{"context_length", "input is too long", "prompt is too long", "too many tokens"},
		},
		{
			Vendor: "claude", API: APIAnthropicMessages, ContextWindow: 200_000, MaxTokensDefault: 8_192,
			Cost:            Cost{Input: 3.00, Output: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
			Capabilities:    Capabilities{ThinkingBlocks: true, VisionInput: true, RejectsDanglingToolCalls: true, StreamCarriesErrorsInBody: true, AgenticBackend: true},
			DefaultBaseURL:  "https://api.anthropic.com",
			OverflowMarkers: []string{"prompt is too long", "input is too long", "context_length"},
			Retry:           defaultRetry(),
		},
		{
			Vendor: "anthropic", API: APIAnthropicMessages, ContextWindow: 200_000, MaxTokensDefault: 8_192,
			Cost:            Cost{Input: 3.00, Output: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
			Capabilities:    Capabilities{ThinkingBlocks: true, VisionInput: true, RejectsDanglingToolCalls: true, StreamCarriesErrorsInBody: true, AgenticBackend: true},
			DefaultBaseURL:  "https://api.anthropic.com",
			OverflowMarkers: []string{"prompt is too long", "input is too long", "context_length"},
		},
		{
			Vendor: "deepseek", API: APIOpenAIChat, ContextWindow: 128_000, MaxTokensDefault: 8_192,
			Cost:            Cost{Input: 0.14, Output: 0.28},
			Capabilities:    Capabilities{ReasoningEffort: true, RejectsDanglingToolCalls: true},
			OverflowMarkers: []string{"context_length", "input is too long"},
		},
		{
			// DashScope speaks OpenAI-shaped chat but its Anthropic-compatible surface needs
			// the messages endpoint; the multimodal embedding quirk stays a dialect flag.
			Vendor: "dashscope", API: APIOpenAIChat, ContextWindow: 128_000, MaxTokensDefault: 8_192,
			Capabilities:    Capabilities{VisionInput: true, RejectsDanglingToolCalls: true},
			OverflowMarkers: []string{"context_length", "range of input length", "input is too long"},
		},
	} {
		if d.Retry.RetryableStatus == nil {
			d.Retry = defaultRetry()
		}
		c.dialects[d.Vendor] = d
	}
	return c
}

// Register adds or replaces a vendor row; used by tests and by deployments that need a
// gateway whose quirks are not in the shipped table.
func (c *Catalog) Register(d Dialect) error {
	if strings.TrimSpace(d.Vendor) == "" {
		return fmt.Errorf("provider: dialect vendor is required")
	}
	switch d.API {
	case APIOpenAIChat, APIOpenAIResponses, APIAnthropicMessages:
	default:
		return fmt.Errorf("provider: dialect %s has unknown API %q", d.Vendor, d.API)
	}
	if d.Retry.RetryableStatus == nil {
		d.Retry = defaultRetry()
	}
	c.dialects[strings.ToLower(strings.TrimSpace(d.Vendor))] = d
	return nil
}

// Lookup returns a registered dialect.
func (c *Catalog) Lookup(vendor string) (Dialect, bool) {
	d, ok := c.dialects[strings.ToLower(strings.TrimSpace(vendor))]
	return d, ok
}

// Resolve maps a configured provider plus base URL to a dialect. An unknown vendor
// name is not an error: it degrades to the chat dialect, because that is what every
// gateway in the wild speaks, and the alternative is refusing to start.
func (c *Catalog) Resolve(vendor, baseURL string) Dialect {
	key := strings.ToLower(strings.TrimSpace(vendor))
	if key == "" {
		key = "openai_compatible"
	}
	d, ok := c.Lookup(key)
	if !ok {
		d = c.dialects["openai_compatible"]
		d.Vendor = key
	}
	if given := strings.TrimSpace(baseURL); given != "" {
		d.BaseURL = strings.TrimRight(given, "/")
		return d
	}
	// An empty base URL still has to produce a usable dialect. Falling back to the
	// row's own default is what the settings handler used to do before this table
	// existed; leaving BaseURL empty instead would only fail later, at request time,
	// with an error that names no vendor.
	if d.DefaultBaseURL != "" {
		d.BaseURL = d.DefaultBaseURL
	} else {
		d.BaseURL = ChatDefaultBaseURL
	}
	return d
}

// Vendors lists the catalog keys, sorted, for the settings UI and the docs generator.
func (c *Catalog) Vendors() []string {
	out := make([]string, 0, len(c.dialects))
	for name := range c.dialects {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// EndpointURL builds the request URL for one call. This is the only place the path
// shape is written down.
func (d Dialect) EndpointURL() (string, error) {
	if strings.TrimSpace(d.BaseURL) == "" {
		return "", fmt.Errorf("provider: dialect %s has no base URL configured", d.Vendor)
	}
	// Trimmed here as well as in Resolve: a Dialect can be built directly, and a
	// doubled slash in the path is a 404 nobody explains.
	base := strings.TrimRight(d.BaseURL, "/")
	d.BaseURL = base
	switch d.API {
	case APIOpenAIChat:
		return base + "/chat/completions", nil
	case APIOpenAIResponses:
		return base + "/responses", nil
	case APIAnthropicMessages:
		if strings.HasSuffix(base, "/v1") {
			return base + "/messages", nil
		}
		return base + "/v1/messages", nil
	}
	return "", fmt.Errorf("provider: dialect %s has unknown API %q", d.Vendor, d.API)
}

// ApplyAuth writes the dialect's authentication headers. Splitting this out is what
// stops callers from guessing whether a vendor uses Bearer or x-api-key.
func (d Dialect) ApplyAuth(header http.Header, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	switch d.API {
	case APIAnthropicMessages:
		header.Set("x-api-key", apiKey)
		header.Set("anthropic-version", "2023-06-01")
		header.Set("content-type", "application/json")
	default:
		header.Set("Authorization", "Bearer "+apiKey)
		header.Set("content-type", "application/json")
	}
}

// ClassifyError normalizes an HTTP status plus response body into one of the shared
// classes. Order matters: an overflow reported as a 400 must not be retried just
// because 400s are otherwise stable.
func (d Dialect) ClassifyError(status int, body string) ErrorClass {
	lowered := strings.ToLower(body)
	for _, marker := range d.OverflowMarkers {
		if marker != "" && strings.Contains(lowered, strings.ToLower(marker)) {
			return ClassContextOverflow
		}
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ClassAuth
	case status == http.StatusTooManyRequests:
		return ClassRateLimited
	case status == 499 || strings.Contains(lowered, "\"error\": \"aborted\"") ||
		strings.Contains(lowered, "context canceled"):
		return ClassAborted
	case status == http.StatusNotImplemented || status == http.StatusBadRequest &&
		(strings.Contains(lowered, "not supported") || strings.Contains(lowered, "unsupported")):
		return ClassUnsupported
	case d.IsRetryableStatus(status):
		return ClassTransient
	default:
		return ClassUnknown
	}
}

// IsRetryableStatus reports whether a status is worth another attempt.
func (d Dialect) IsRetryableStatus(status int) bool {
	for _, candidate := range d.Retry.RetryableStatus {
		if candidate == status {
			return true
		}
	}
	return d.Retry.RetryableOn5xx && status >= 500 && status <= 599
}

// IsAnthropicMessages lets the remaining transport code ask the question it used to
// answer by comparing provider names, without encoding the vendor list again.
func (d Dialect) IsAnthropicMessages() bool { return d.API == APIAnthropicMessages }

// DefaultCatalog is the process-wide table. Install replaces it, which is how a
// deployment registers a gateway with unusual quirks.
var defaultCatalog = NewCatalog()

// Default returns the active catalog.
func Default() *Catalog { return defaultCatalog }

// Install replaces the active catalog.
func Install(c *Catalog) {
	if c != nil {
		defaultCatalog = c
	}
}

// AgenticBackendSupported reports whether a configured provider name may be driven by
// the agentic chat-model path. The rule is deliberately identical to the pre-dialect
// string comparison: an unknown or empty name is only accepted when empty, because
// that is what deployments without an explicit provider field mean today.
func AgenticBackendSupported(vendor string) bool {
	key := strings.ToLower(strings.TrimSpace(vendor))
	if key == "" {
		return true
	}
	dialect, ok := Default().Lookup(key)
	if !ok {
		return false
	}
	return dialect.Capabilities.AgenticBackend
}

// EffectiveProviderName normalises a channel's provider name for model construction.
func EffectiveProviderName(vendor string) string {
	key := strings.ToLower(strings.TrimSpace(vendor))
	if key == "" {
		return "openai"
	}
	if dialect, ok := Default().Lookup(key); ok {
		if dialect.AliasesTo != "" {
			return dialect.AliasesTo
		}
		return dialect.Vendor
	}
	return key
}

// IsAnthropicMessagesVendor is the one question several call sites asked by comparing
// two literal strings; they now ask the catalog instead.
func IsAnthropicMessagesVendor(vendor string) bool {
	dialect, ok := Default().Lookup(vendor)
	return ok && dialect.API == APIAnthropicMessages
}

// ChatDefaultBaseURL is the fallback for any vendor without its own entry, which is
// what the settings handler used for every non-anthropic provider historically.
const ChatDefaultBaseURL = "https://api.openai.com/v1"

// DefaultBaseURLFor resolves the base URL to use when a deployment left it empty.
func DefaultBaseURLFor(vendor string) string {
	if dialect, ok := Default().Lookup(vendor); ok && dialect.DefaultBaseURL != "" {
		return dialect.DefaultBaseURL
	}
	return ChatDefaultBaseURL
}

// ListsModels reports whether model discovery is worth attempting for a vendor.
// It is a property of the wire family, not a per-vendor switch: the chat protocol has
// a listing endpoint reachable with the same credentials, the messages protocol does
// not. Encoding it as a boolean flag was a trap - a new catalog row that forgot the
// field silently took listing away from a vendor that had it.
func ListsModels(vendor string) bool {
	return !IsAnthropicMessagesVendor(vendor)
}
