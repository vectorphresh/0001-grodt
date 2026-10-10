package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vectorphresh/0001-grodt/internal/config"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
	"github.com/vectorphresh/0001-grodt/internal/validation"
	"golang.org/x/net/http/httpguts"
)

type Tool struct {
	Server       string          `json:"server"`
	Name         string          `json:"name"`
	Alias        string          `json:"alias"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}
type session struct {
	client *sdk.ClientSession
	wire   *wireTransport
}

// Runtime is sequential and run-scoped. Callers must not invoke it concurrently.
type Runtime struct {
	sessions    map[string]session
	catalog     []Tool
	byAlias     map[string]Tool
	sensitive   []string
	invocations int
	closed      bool
}
type Outcome struct {
	Server            string          `json:"server"`
	Tool              string          `json:"tool"`
	Content           json.RawMessage `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
}

var identity = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func New(ctx context.Context, cfg config.MCPConfig) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("mcp: context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := &Runtime{sessions: map[string]session{}, byAlias: map[string]Tool{}}
	// Resolve and check the entire configuration before making any connection.
	headers := make([]http.Header, len(cfg.Servers))
	seen := map[string]bool{}
	if len(cfg.Servers) > MaxServers {
		return nil, errors.New("mcp: server limit exceeded")
	}
	for i, s := range cfg.Servers {
		if !identity.MatchString(s.Name) || seen[s.Name] {
			return nil, errors.New("mcp: invalid or duplicate server name")
		}
		seen[s.Name] = true
		h, err := connection(s)
		if err != nil {
			return nil, fmt.Errorf("mcp: server %s configuration invalid", s.Name)
		}
		headers[i] = h
		for _, v := range s.Environment {
			if v != "" {
				r.sensitive = append(r.sensitive, v)
			}
		}
		for _, vs := range h {
			r.sensitive = append(r.sensitive, vs...)
		}
		u, _ := url.Parse(s.URL)
		if u.RawQuery != "" {
			r.sensitive = append(r.sensitive, u.RawQuery)
			for _, vs := range u.Query() {
				r.sensitive = append(r.sensitive, vs...)
			}
		}
	}
	for i, s := range cfg.Servers {
		err := r.connect(ctx, s, headers[i])
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("mcp: server %s initialization failed: %w", s.Name, safeError(ctx, "connection or catalog invalid"))
		}
	}
	return r, nil
}
func connection(s config.MCPServer) (http.Header, error) {
	u, e := url.Parse(s.URL)
	if e != nil || u == nil || u.Hostname() == "" || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || s.Transport != "http" {
		return nil, errors.New("invalid connection")
	}
	h := http.Header{}
	for key, b := range s.HTTP.Headers {
		k := http.CanonicalHeaderKey(key)
		lower := strings.ToLower(k)
		if !httpguts.ValidHeaderFieldName(k) || h[k] != nil || strings.HasPrefix(lower, "mcp-") || strings.HasPrefix(lower, "proxy-") {
			return nil, errors.New("invalid header binding")
		}
		switch lower {
		case "host", "content-type", "content-length", "accept", "connection", "transfer-encoding", "te", "trailer", "upgrade", "last-event-id", "accept-encoding", "idempotency-key", "x-idempotency-key":
			return nil, errors.New("transport-managed header")
		}
		value, ok := s.Environment[b.FromEnvironment]
		if !ok || b.FromEnvironment == "" || !httpguts.ValidHeaderFieldValue(b.Prefix+value) {
			return nil, errors.New("invalid header value")
		}
		h.Set(k, b.Prefix+value)
	}
	return h, nil
}
func (r *Runtime) connect(parent context.Context, s config.MCPServer, h http.Header) error {
	ctx, cancel := context.WithTimeout(parent, InitializationTimeout)
	defer cancel()
	base := &http.Transport{MaxResponseHeaderBytes: 64 << 10}
	wire := &wireTransport{base: base, headers: h}
	client := sdk.NewClient(&sdk.Implementation{Name: "grodt", Version: "1"}, &sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true}})
	// Detect SDK filtering before ListTools publishes its possibly shortened list.
	prefilterCount := 0
	client.AddSendingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			res, err := next(ctx, method, req)
			if err == nil && method == "tools/list" {
				if list, ok := res.(*sdk.ListToolsResult); ok {
					prefilterCount = len(list.Tools)
				}
			}
			return res, err
		}
	})
	httpClient := &http.Client{Transport: wire, Timeout: InvocationTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("MCP redirects disabled") }}
	cs, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: s.URL, HTTPClient: httpClient, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: MaxWireBytes}, nil)
	if err != nil {
		base.CloseIdleConnections()
		return err
	}
	r.sessions[s.Name] = session{cs, wire}
	cursor := ""
	seen := map[string]bool{}
	names := map[string]bool{}
	for page := 0; page < MaxDiscoveryPages; page++ {
		list, err := cs.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return err
		}
		if len(list.Tools) != prefilterCount {
			return errors.New("invalid advertised tool")
		}
		raw, err := wire.result()
		if err != nil {
			return err
		}
		var original struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor string            `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &original) != nil || original.Tools == nil || len(original.Tools) != len(list.Tools) {
			return errors.New("invalid tool list")
		}
		for _, rawTool := range original.Tools {
			var t Tool
			if json.Unmarshal(rawTool, &t) != nil || t.Name == "" || names[t.Name] || len(t.InputSchema) == 0 {
				return errors.New("invalid tool definition")
			}
			names[t.Name] = true
			t.Server = s.Name
			t.Alias = fmt.Sprintf("grodt_tool_%03d", len(r.catalog)+1)
			for _, schema := range []json.RawMessage{t.InputSchema, t.OutputSchema} {
				if len(schema) > 0 {
					if _, err := validation.New().Validate(ctx, schema, json.RawMessage(`null`)); err != nil {
						return errors.New("invalid tool schema")
					}
				}
			}
			// MCP inputs must be an object schema, including any additional constraints.
			var shape struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(t.InputSchema, &shape) != nil || shape.Type != "object" {
				return errors.New("invalid MCP input schema")
			}
			r.catalog = append(r.catalog, t)
			r.byAlias[t.Alias] = t
			b, _ := json.Marshal(r.catalog)
			if len(r.catalog) > MaxTools || len(b) > MaxCatalogBytes || r.containsSensitive(b) {
				return errors.New("unusable catalog")
			}
		}
		cursor = original.NextCursor
		if cursor == "" {
			return nil
		}
		if seen[cursor] {
			return errors.New("discovery cursor loop")
		}
		seen[cursor] = true
	}
	return errors.New("discovery page limit")
}
func (r *Runtime) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	var failed bool
	for _, s := range r.sessions {
		if s.client.Close() != nil {
			failed = true
		}
		s.wire.base.CloseIdleConnections()
	}
	if failed {
		return errors.New("mcp: session close failed")
	}
	return nil
}
func (r *Runtime) Tools() []toolcall.Definition {
	out := make([]toolcall.Definition, 0, len(r.catalog))
	for _, t := range r.catalog {
		out = append(out, toolcall.Definition{Name: t.Alias, Description: t.Server + "." + t.Name + ": " + t.Description, InputSchema: append(json.RawMessage(nil), t.InputSchema...)})
	}
	return out
}
func (r *Runtime) Lookup(alias string) (Tool, bool) {
	t, ok := r.byAlias[alias]
	t.InputSchema = append(json.RawMessage(nil), t.InputSchema...)
	t.OutputSchema = append(json.RawMessage(nil), t.OutputSchema...)
	return t, ok
}
func (r *Runtime) Invocations() int { return r.invocations }

// Preflight validates the entire batch before tasks or any external effects.
func (r *Runtime) Preflight(ctx context.Context, calls []toolcall.Call) error {
	if ctx == nil {
		return errors.New("mcp: context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.closed {
		return errors.New("mcp: runtime closed")
	}
	if len(calls) == 0 || len(calls) > MaxBatchCalls || len(calls) > MaxInvocations-r.invocations {
		return errors.New("mcp: invocation budget or batch limit exceeded")
	}
	seen := map[string]bool{}
	for _, c := range calls {
		t, ok := r.byAlias[c.Name]
		if !ok || strings.TrimSpace(c.ID) == "" || seen[c.ID] || len(c.ID) > 256 || len(c.Arguments) > MaxArgumentBytes || !json.Valid(c.Arguments) {
			return errors.New("mcp: invalid tool call batch")
		}
		seen[c.ID] = true
		encoded, _ := json.Marshal(c)
		if r.containsSensitive(encoded) {
			return errors.New("mcp: sensitive connection value in tool call")
		}
		result, err := validation.New().Validate(ctx, t.InputSchema, c.Arguments)
		if err != nil || !result.Valid {
			return invocationFailure(ctx, "tool_arguments_schema_violation", "not_dispatched", nil, result.Details)
		}
	}
	return nil
}
func (r *Runtime) Invoke(ctx context.Context, c toolcall.Call) (Outcome, error) {
	if err := r.Preflight(ctx, []toolcall.Call{c}); err != nil {
		return Outcome{}, err
	}
	t := r.byAlias[c.Name]
	s := r.sessions[t.Server]
	callCtx, cancel := context.WithTimeout(ctx, InvocationTimeout)
	defer cancel()
	if err := callCtx.Err(); err != nil {
		return Outcome{}, err
	}
	r.invocations++
	s.wire.reset()
	result, err := s.client.CallTool(callCtx, &sdk.CallToolParams{Name: t.Name, Arguments: c.Arguments})
	if observed, overflow := s.wire.overflow(); overflow {
		return Outcome{}, invocationFailure(ctx, "wire_response_too_large", "result_rejected", map[string]int{"observed_bytes_at_least": observed, "limit_bytes": MaxWireBytes}, nil)
	}
	raw, wireErr := s.wire.result()
	if len(raw) > 0 && r.containsSensitive(raw) {
		return Outcome{}, errors.New("mcp: sensitive connection value in tool result")
	}
	if err != nil {
		if len(raw) > 0 {
			validationResult, _ := validation.New().Validate(callCtx, resultSchema, raw)
			return Outcome{}, invocationFailure(ctx, "malformed_tool_result", "result_rejected", nil, validationResult.Details)
		}
		category := "tool_protocol_or_transport_failure"
		if callCtx.Err() != nil {
			category = "invocation_timeout"
		}
		return Outcome{}, invocationFailure(ctx, category, "outcome_unknown", nil, nil)
	}
	if wireErr != nil {
		return Outcome{}, invocationFailure(ctx, "missing_tool_result", "outcome_unknown", nil, nil)
	}
	if len(raw) > MaxResultBytes {
		return Outcome{}, invocationFailure(ctx, "result_too_large", "result_rejected", map[string]int{"observed_bytes": len(raw), "limit_bytes": MaxResultBytes}, nil)
	}
	if result.NeedsInput() {
		return Outcome{}, invocationFailure(ctx, "unsupported_input_request", "result_rejected", nil, nil)
	}
	checked, checkErr := validation.New().Validate(callCtx, resultSchema, raw)
	if checkErr != nil || !checked.Valid {
		return Outcome{}, invocationFailure(ctx, "malformed_tool_result", "result_rejected", nil, checked.Details)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return Outcome{}, invocationFailure(ctx, "invalid_result_object", "result_rejected", nil, nil)
	}
	var content []json.RawMessage
	if json.Unmarshal(fields["content"], &content) != nil || content == nil {
		return Outcome{}, invocationFailure(ctx, "invalid_result_content", "result_rejected", nil, nil)
	}
	// SDK validates content union kinds; retain every original block unchanged.
	if len(content) != len(result.Content) {
		return Outcome{}, invocationFailure(ctx, "invalid_content_blocks", "result_rejected", map[string]int{"wire_blocks": len(content), "decoded_blocks": len(result.Content)}, nil)
	}
	out := Outcome{Server: t.Server, Tool: t.Name, Content: fields["content"], StructuredContent: fields["structuredContent"], IsError: result.IsError}
	if !out.IsError && len(t.OutputSchema) > 0 {
		if len(out.StructuredContent) == 0 {
			return Outcome{}, invocationFailure(ctx, "missing_structured_output", "result_rejected", nil, nil)
		}
		v, e := validation.New().Validate(callCtx, t.OutputSchema, out.StructuredContent)
		if e != nil || !v.Valid {
			return Outcome{}, invocationFailure(ctx, "output_schema_violation", "result_rejected", nil, v.Details)
		}
	}
	return out, nil
}
func (r *Runtime) containsSensitive(raw []byte) bool {
	// Check decoded JSON strings as well as bytes so JSON escaping is not a bypass.
	var value any
	_ = json.Unmarshal(raw, &value)
	var visit func(any) bool
	visit = func(v any) bool {
		switch x := v.(type) {
		case string:
			for _, secret := range r.sensitive {
				if secret != "" && strings.Contains(x, secret) {
					return true
				}
			}
		case []any:
			for _, v := range x {
				if visit(v) {
					return true
				}
			}
		case map[string]any:
			for k, v := range x {
				if visit(k) || visit(v) {
					return true
				}
			}
		}
		return false
	}
	if visit(value) {
		return true
	}
	for _, s := range r.sensitive {
		if s != "" && bytes.Contains(raw, []byte(s)) {
			return true
		}
	}
	return false
}
