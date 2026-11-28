package llmgateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub/manifest"

	"github.com/google/uuid"
)

const maxJSON = 24 << 20
const maxUsageTokens int64 = 1000000000

func gatewayError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": code, "message": code}})
}

func models(w http.ResponseWriter, protocol string) {
	w.Header().Set("Content-Type", "application/json")
	data := []map[string]any{{"id": "csai-managed", "object": "model", "owned_by": "csai"}}
	if protocol == manifest.Anthropic {
		data = []map[string]any{{"id": "csai-managed", "type": "model", "display_name": "CSAI managed model", "created_at": "1970-01-01T00:00:00Z"}}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "has_more": false, "first_id": "csai-managed", "last_id": "csai-managed"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (s *Service) forward(ctx context.Context, w http.ResponseWriter, r *http.Request, key database.PluginGatewayKey, c config.OpenAIConfig, channel string) {
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSON))
	if err != nil {
		gatewayError(w, http.StatusRequestEntityTooLarge, "request_limit")
		return
	}
	count := strings.HasSuffix(r.URL.Path, "/count_tokens")
	request, stream, reserve, err := prepareRequest(payload, c, key.Binding.Protocol, count)
	if err != nil {
		gatewayError(w, http.StatusBadRequest, "invalid_model_request")
		return
	}
	endpoint, err := upstreamEndpoint(c.BaseURL, r.URL.Path)
	if err != nil {
		gatewayError(w, http.StatusBadGateway, "invalid_channel_endpoint")
		return
	}
	id := uuid.NewString()
	err = s.db.ReservePluginModelUsage(ctx, id, key, channel, reserve)
	if errors.Is(err, database.ErrPluginModelBudget) {
		gatewayError(w, http.StatusTooManyRequests, "token_budget_exhausted")
		return
	}
	if err != nil {
		gatewayError(w, http.StatusServiceUnavailable, "usage_store_unavailable")
		return
	}
	usage := tokenUsage{}
	status := "canceled"
	defer func() {
		finalCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.db.FinishPluginModelUsage(finalCtx, id, status, usage.Input, usage.Output, status == "ok" && usage.Metered)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(request))
	if err != nil {
		gatewayError(w, http.StatusBadGateway, "invalid_channel_endpoint")
		return
	}
	setUpstreamHeaders(req, c, key.Binding.Protocol, stream, r.Header.Get("anthropic-version"))
	protected := []string{c.APIKey, req.Header.Get("X-API-Key"), strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")}
	resp, err := s.client.Do(req)
	if err != nil {
		gatewayError(w, http.StatusBadGateway, "upstream_unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status = "upstream_error"
		code := resp.StatusCode
		if code < 400 || code > 599 {
			code = http.StatusBadGateway
		}
		gatewayError(w, code, "upstream_error")
		return
	}
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)
		if err := copySSE(w, resp.Body, &usage, protected); err == nil {
			status = "ok"
		}
		return
	}
	response, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil || len(response) > maxJSON || !json.Valid(response) || containsCredential(response, protected) {
		gatewayError(w, http.StatusBadGateway, "invalid_upstream_response")
		return
	}
	usage.observe(response)
	status = "ok"
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(response)
}

func prepareRequest(payload []byte, c config.OpenAIConfig, protocol string, count bool) ([]byte, bool, int64, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return nil, false, 0, fmt.Errorf("invalid JSON object")
	}
	for name := range fields {
		switch strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(name), "_", ""), "-", "") {
		case "baseurl", "apibase", "apikey", "authorization", "headers", "proxy", "proxyurl", "upstreamurl", "endpoint", "provider":
			return nil, false, 0, fmt.Errorf("upstream settings are owned by the bound channel")
		}
	}
	var model string
	if err := json.Unmarshal(fields["model"], &model); err != nil || (model != "csai-managed" && model != c.Model) {
		return nil, false, 0, fmt.Errorf("model not bound")
	}
	if !clientTools(fields, protocol) {
		return nil, false, 0, fmt.Errorf("unsupported managed tool")
	}
	fields["model"], _ = json.Marshal(c.Model)
	stream := false
	if raw := fields["stream"]; raw != nil && json.Unmarshal(raw, &stream) != nil {
		return nil, false, 0, fmt.Errorf("invalid stream")
	}
	maxOutput := int64(4096)
	if c.MaxCompletionTokens > 0 {
		maxOutput = int64(c.MaxCompletionTokens)
	}
	if maxOutput > maxUsageTokens {
		maxOutput = maxUsageTokens
	}
	if !count {
		limit := "max_tokens"
		if protocol == manifest.OpenAI && fields["max_completion_tokens"] != nil {
			limit = "max_completion_tokens"
		}
		if raw := fields[limit]; raw != nil {
			var requested int64
			if json.Unmarshal(raw, &requested) != nil || requested < 1 {
				return nil, false, 0, fmt.Errorf("invalid max tokens")
			}
			if requested < maxOutput {
				maxOutput = requested
			}
		}
		fields[limit], _ = json.Marshal(maxOutput)
		for _, name := range []string{"max_tokens", "max_completion_tokens"} {
			if name != limit {
				delete(fields, name)
			}
		}
	} else {
		maxOutput = 0
	}
	if protocol == manifest.OpenAI && stream {
		options := map[string]json.RawMessage{}
		if raw := fields["stream_options"]; raw != nil && json.Unmarshal(raw, &options) != nil {
			return nil, false, 0, fmt.Errorf("invalid stream options")
		}
		if options == nil {
			options = map[string]json.RawMessage{}
		}
		options["include_usage"] = json.RawMessage("true")
		fields["stream_options"], _ = json.Marshal(options)
	}
	body, err := json.Marshal(fields)
	// Charge a conservative byte-based input estimate and the bounded output
	// before forwarding. Provider-reported usage replaces the estimate.
	return body, stream, int64(len(payload)) + maxOutput, err
}

func clientTools(fields map[string]json.RawMessage, protocol string) bool {
	for _, name := range []string{"mcp_servers", "server_tools", "container", "computer_use", "web_search_options"} {
		if _, present := fields[name]; present {
			return false
		}
	}
	if fields["tools"] == nil {
		return true
	}
	var tools []struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Function    json.RawMessage `json:"function"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	if json.Unmarshal(fields["tools"], &tools) != nil {
		return false
	}
	for _, tool := range tools {
		if protocol == manifest.OpenAI {
			if tool.Type != "function" || len(tool.Function) == 0 {
				return false
			}
		} else if (tool.Type != "" && tool.Type != "custom") || tool.Name == "" || len(tool.InputSchema) == 0 {
			return false
		}
	}
	return true
}

func upstreamEndpoint(base, route string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid configured upstream")
	}
	prefix := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/messages/count_tokens", "/messages"} {
		prefix = strings.TrimSuffix(prefix, suffix)
	}
	if strings.HasSuffix(prefix, "/v1") {
		route = strings.TrimPrefix(route, "/v1")
	}
	u.Path, u.RawPath = prefix+route, ""
	return u.String(), nil
}

func setUpstreamHeaders(req *http.Request, c config.OpenAIConfig, protocol string, stream bool, version string) {
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if protocol == manifest.Anthropic {
		req.Header.Set("x-api-key", c.APIKey)
	} else if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for name, value := range c.Headers {
		switch strings.ToLower(name) {
		case "host", "content-length", "transfer-encoding", "connection", "upgrade", "proxy-authorization", "proxy-connection",
			"forwarded", "cookie", "content-type", "accept":
			continue
		}
		if strings.ContainsAny(name+value, "\r\n\x00") || strings.HasPrefix(strings.ToLower(name), "x-forwarded-") {
			continue
		}
		req.Header.Set(name, value)
	}
	if protocol == manifest.Anthropic {
		if req.Header.Get("anthropic-version") == "" {
			if version != "2023-06-01" {
				version = "2023-06-01"
			}
			req.Header.Set("anthropic-version", version)
		}
	}
}

type tokenUsage struct {
	Input, Output int64
	Metered       bool
}

func (u *tokenUsage) observe(payload []byte) {
	type counts struct {
		Input      *int64 `json:"input_tokens"`
		Output     *int64 `json:"output_tokens"`
		Prompt     *int64 `json:"prompt_tokens"`
		Completion *int64 `json:"completion_tokens"`
	}
	var event struct {
		Input   *int64  `json:"input_tokens"`
		Output  *int64  `json:"output_tokens"`
		Usage   *counts `json:"usage"`
		Message struct {
			Usage *counts `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return
	}
	for _, c := range []*counts{{Input: event.Input, Output: event.Output}, event.Usage, event.Message.Usage} {
		if c == nil {
			continue
		}
		for _, i := range []*int64{c.Input, c.Prompt} {
			if i != nil && *i >= 0 && *i <= maxUsageTokens {
				u.Input = max(u.Input, *i)
				u.Metered = true
			}
		}
		for _, o := range []*int64{c.Output, c.Completion} {
			if o != nil && *o >= 0 && *o <= maxUsageTokens {
				u.Output = max(u.Output, *o)
				u.Metered = true
			}
		}
	}
}

func containsCredential(payload []byte, credentials []string) bool {
	for _, secret := range credentials {
		// Short compatibility placeholders are not realistic secrets and would
		// match ordinary response text.
		if len(secret) >= 8 && bytes.Contains(payload, []byte(secret)) {
			return true
		}
	}
	return false
}

func copySSE(w http.ResponseWriter, stream io.Reader, usage *tokenUsage, protected []string) error {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 32<<10), maxJSON)
	event := make([]byte, 0, 512)
	for scanner.Scan() {
		line := scanner.Bytes()
		if containsCredential(line, protected) {
			return fmt.Errorf("upstream stream contains a protected credential")
		}
		if len(line) == 0 {
			usage.observe(event)
			event = event[:0]
		} else if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			if len(event)+len(data) > maxJSON {
				return fmt.Errorf("SSE event exceeds limit")
			}
			if len(event) > 0 {
				event = append(event, '\n')
			}
			event = append(event, bytes.TrimSpace(data)...)
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return err
		}
		if err := http.NewResponseController(w).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
	}
	usage.observe(event)
	return scanner.Err()
}
