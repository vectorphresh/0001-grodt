package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/config"
	"github.com/vectorphresh/0001-grodt/internal/testmcp"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func runtimeFor(t *testing.T, f *testmcp.Server) *Runtime {
	t.Helper()
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	r, err := New(context.Background(), config.MCPConfig{Servers: []config.MCPServer{{Name: "unknown", Transport: "http", URL: s.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}
func callFor(r *Runtime) toolcall.Call {
	return toolcall.Call{ID: "call-1", Name: r.Tools()[0].Name, Arguments: json.RawMessage(`{"key":"sample"}`)}
}
func TestDiscoveryBindingsPaginationAndCollision(t *testing.T) {
	cfg := config.MCPConfig{}
	fixtures := []*testmcp.Server{}
	for _, name := range []string{"one", "two"} {
		f := &testmcp.Server{Check: func(r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer fixture-private-token" {
				t.Error("missing binding")
			}
			if r.Header.Get("UNBOUND") != "" {
				t.Error("unbound value transmitted")
			}
		}}
		f.List = func(cursor string) json.RawMessage {
			if cursor == "" {
				return json.RawMessage(`{"tools":[],"nextCursor":"page2"}`)
			}
			return json.RawMessage(`{"tools":` + testmcp.Tools + `}`)
		}
		s := httptest.NewServer(f)
		t.Cleanup(s.Close)
		fixtures = append(fixtures, f)
		cfg.Servers = append(cfg.Servers, config.MCPServer{Name: name, Transport: "http", URL: s.URL, Environment: map[string]string{"TOKEN": "fixture-private-token", "UNBOUND": "local-only-value"}, HTTP: config.MCPHTTP{Headers: map[string]config.HeaderBinding{"Authorization": {FromEnvironment: "TOKEN", Prefix: "Bearer "}}}})
	}
	r, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	defs := r.Tools()
	if len(defs) != 2 || defs[0].Name == defs[1].Name {
		t.Fatal("collision")
	}
	for i, d := range defs {
		c := toolcall.Call{ID: fmt.Sprint(i), Name: d.Name, Arguments: json.RawMessage(`{"key":"sample"}`)}
		if _, err := r.Invoke(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range fixtures {
		if f.Calls.Load() != 1 || f.Lists.Load() != 2 {
			t.Fatal("incorrect route/discovery")
		}
	}
	encoded, _ := json.Marshal(defs)
	if strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), "local-only") {
		t.Fatal("configuration leaked")
	}
}
func TestBatchPreflightAndRunBudget(t *testing.T) {
	f := &testmcp.Server{Tools: json.RawMessage(testmcp.Tools)}
	r := runtimeFor(t, f)
	c := callFor(r)
	bad := c
	bad.ID = "bad"
	bad.Arguments = json.RawMessage(`{"key":9}`)
	for _, batch := range [][]toolcall.Call{{c, bad}, {c, c}, {{ID: "x", Name: "missing", Arguments: json.RawMessage(`{}`)}}, {{ID: "x", Name: c.Name, Arguments: json.RawMessage(`{`)}}, make([]toolcall.Call, MaxBatchCalls+1)} {
		if r.Preflight(context.Background(), batch) == nil {
			t.Fatal("accepted bad batch")
		}
	}
	if f.Calls.Load() != 0 {
		t.Fatal("preflight dispatched")
	}
	for i := 0; i < MaxInvocations-1; i++ {
		if _, err := r.Invoke(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	other := c
	other.ID = "other"
	if r.Preflight(context.Background(), []toolcall.Call{c, other}) == nil || f.Calls.Load() != 31 {
		t.Fatal("partial budget accepted")
	}
	if _, err := r.Invoke(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Invoke(context.Background(), c); err == nil || f.Calls.Load() != 32 {
		t.Fatal("budget exceeded")
	}
}
func TestResultAcceptance(t *testing.T) {
	for _, tt := range []struct {
		name, result, schema string
		fail                 bool
	}{
		{"text", `{"content":[{"type":"text","text":"not JSON"}]}`, "", false},
		{"null", `{"content":[],"structuredContent":null}`, `{"type":"null"}`, false},
		{"false", `{"content":[],"structuredContent":false}`, `{"type":"boolean"}`, false},
		{"number", `{"content":[],"structuredContent":9007199254740993}`, `{"const":9007199254740993}`, false},
		{"tool error", `{"content":[{"type":"text","text":"failure"}],"isError":true}`, `{"type":"object"}`, false},
		{"schema invalid", `{"content":[],"structuredContent":false}`, `{"type":"object"}`, true},
		{"missing output", `{"content":[]}`, `{"type":"object"}`, true},
		{"missing content", `{"structuredContent":{}}`, "", true},
		{"null content", `{"content":null}`, "", true},
		{"invalid block", `{"content":[{"type":"unknown"}]}`, "", true},
		{"needs input", `{"resultType":"input_required","inputRequests":{}}`, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tools := testmcp.Tools
			if tt.schema != "" {
				tools = strings.TrimSuffix(tools, "}]") + `,"outputSchema":` + tt.schema + `}]`
			}
			f := &testmcp.Server{Tools: json.RawMessage(tools), SSE: true, Call: func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(tt.result), nil
			}}
			r := runtimeFor(t, f)
			out, err := r.Invoke(context.Background(), callFor(r))
			if (err != nil) != tt.fail {
				t.Fatalf("outcome=%+v error=%v", out, err)
			}
			if f.Calls.Load() != 1 {
				t.Fatal("invocation retried")
			}
			if tt.name == "number" && string(out.StructuredContent) != "9007199254740993" {
				t.Fatal("precision lost")
			}
		})
	}
}
func TestStartupFailureClosesEarlierSessions(t *testing.T) {
	first := &testmcp.Server{Tools: json.RawMessage(testmcp.Tools)}
	a := httptest.NewServer(first)
	defer a.Close()
	for _, tools := range []string{`[null]`, `[{"name":"bad","inputSchema":{"type":"nonsense"}}]`, `[{"name":"bad","inputSchema":{"type":"object","properties":{"x":{"type":"object","x-mcp-header":"foo"}}}}]`, strings.TrimSuffix(testmcp.Tools, "]") + "," + strings.TrimPrefix(testmcp.Tools, "[")} {
		b := httptest.NewServer(&testmcp.Server{Tools: json.RawMessage(tools)})
		r, err := New(context.Background(), config.MCPConfig{Servers: []config.MCPServer{{Name: "one", Transport: "http", URL: a.URL}, {Name: "two", Transport: "http", URL: b.URL}}})
		b.Close()
		if err == nil {
			r.Close()
			t.Fatal("accepted unusable catalog")
		}
	}
	if first.Closed.Load() != 4 {
		t.Fatalf("sessions not closed: %d", first.Closed.Load())
	}
}
func TestProtocolCancellationAndLimits(t *testing.T) {
	t.Run("protocol", func(t *testing.T) {
		f := &testmcp.Server{Tools: json.RawMessage(testmcp.Tools), Call: func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("failure")
		}}
		r := runtimeFor(t, f)
		if _, err := r.Invoke(context.Background(), callFor(r)); err == nil {
			t.Fatal("protocol failure accepted")
		}
		if f.Calls.Load() != 1 {
			t.Fatal("retried")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		f := &testmcp.Server{Tools: json.RawMessage(testmcp.Tools), Call: func(r *http.Request, _ string, _ json.RawMessage) (json.RawMessage, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}}
		r := runtimeFor(t, f)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := r.Invoke(ctx, callFor(r))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
	t.Run("result size", func(t *testing.T) {
		b, _ := json.Marshal(strings.Repeat("x", MaxResultBytes))
		f := &testmcp.Server{Tools: json.RawMessage(testmcp.Tools), Call: func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"content":[{"type":"text","text":` + string(b) + `}]}`), nil
		}}
		r := runtimeFor(t, f)
		if _, err := r.Invoke(context.Background(), callFor(r)); err == nil {
			t.Fatal("oversized result accepted")
		}
	})
	t.Run("pages", func(t *testing.T) {
		f := &testmcp.Server{List: func(c string) json.RawMessage {
			return json.RawMessage(fmt.Sprintf(`{"tools":[],"nextCursor":%q}`, c+"x"))
		}}
		s := httptest.NewServer(f)
		defer s.Close()
		if _, err := New(context.Background(), config.MCPConfig{Servers: []config.MCPServer{{Name: "pages", Transport: "http", URL: s.URL}}}); err == nil {
			t.Fatal("unbounded discovery")
		}
		if f.Lists.Load() != 32 {
			t.Fatal(f.Lists.Load())
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var destination int
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destination++ }))
		defer target.Close()
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
		defer s.Close()
		if _, err := New(context.Background(), config.MCPConfig{Servers: []config.MCPServer{{Name: "redirect", Transport: "http", URL: s.URL}}}); err == nil {
			t.Fatal("redirect accepted")
		}
		if destination != 0 {
			t.Fatal("redirect followed")
		}
	})
}

func TestConfigurationValidationAndSensitiveValues(t *testing.T) {
	for _, s := range []config.MCPServer{
		{Name: "x", Transport: "stdio", URL: "https://example.invalid"},
		{Name: "x", Transport: "http", URL: "file:///tmp/test"},
		{Name: "x", Transport: "http", URL: "https://user:password@example.invalid"},
		{Name: "x", Transport: "http", URL: "https://example.invalid", HTTP: config.MCPHTTP{Headers: map[string]config.HeaderBinding{"Authorization": {FromEnvironment: "MISSING"}}}},
		{Name: "x", Transport: "http", URL: "https://example.invalid", Environment: map[string]string{"TOKEN": "token"}, HTTP: config.MCPHTTP{Headers: map[string]config.HeaderBinding{"Mcp-Session-Id": {FromEnvironment: "TOKEN"}}}},
	} {
		if _, err := New(context.Background(), config.MCPConfig{Servers: []config.MCPServer{s}}); err == nil {
			t.Fatal("invalid connection accepted")
		}
	}
	f := &testmcp.Server{Tools: json.RawMessage(testmcp.Tools), Call: func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"content":[{"type":"text","text":"echo confidential-value-123"}]}`), nil
	}}
	server := httptest.NewServer(f)
	defer server.Close()
	r, err := New(context.Background(), config.MCPConfig{Servers: []config.MCPServer{{Name: "safe-identity", Transport: "http", URL: server.URL, Environment: map[string]string{"TOKEN": "confidential-value-123"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if out, err := r.Invoke(context.Background(), callFor(r)); err == nil || strings.Contains(err.Error(), "confidential") || len(out.Content) > 0 {
		t.Fatal("sensitive result escaped acceptance boundary")
	}
}
