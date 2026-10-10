package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/testmcp"
)

func TestInvocationFailureSafeDiagnostics(t *testing.T) {
	for _, mode := range []string{"result_size", "wire_size", "protocol", "output_schema", "sensitive"} {
		t.Run(mode, func(t *testing.T) {
			f := &testmcp.Server{Tools: json.RawMessage(testmcp.Tools), Call: func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
				switch mode {
				case "protocol":
					return nil, errors.New("server-secret")
				case "output_schema":
					return json.RawMessage(`{"content":[],"structuredContent":{"count":"server-secret"}}`), nil
				case "sensitive":
					return json.RawMessage(`{"content":[{"type":"text","text":"server-secret"}]}`), nil
				default:
					size := MaxResultBytes
					if mode == "wire_size" {
						size = MaxWireBytes + 128
					}
					return json.RawMessage(`{"content":[{"type":"text","text":"` + strings.Repeat("x", size) + `"}]}`), nil
				}
			}}
			r := runtimeFor(t, f)
			if mode == "output_schema" {
				alias := r.Tools()[0].Name
				tool := r.byAlias[alias]
				tool.OutputSchema = json.RawMessage(`{"type":"object","properties":{"count":{"type":"number"}}}`)
				r.byAlias[alias] = tool
			}
			if mode == "sensitive" {
				r.sensitive = append(r.sensitive, "server-secret")
			}
			_, err := r.Invoke(context.Background(), callFor(r))
			var failure *InvocationFailure
			if mode == "sensitive" {
				if err == nil || errors.As(err, &failure) {
					t.Fatal("sensitive result became recoverable")
				}
				return
			}
			if !errors.As(err, &failure) {
				t.Fatalf("missing typed feedback: %v", err)
			}
			raw, _ := json.Marshal(failure)
			if strings.Contains(string(raw), "server-secret") || len(raw) > 1024 {
				t.Fatal("unsafe or unbounded feedback")
			}
			if mode == "protocol" {
				if failure.Execution != "outcome_unknown" || f.Calls.Load() != 1 {
					t.Fatal("unknown execution was replayed or misrepresented")
				}
			} else if failure.Execution != "result_rejected" {
				t.Fatal("received result not distinguished from transport uncertainty")
			}
			if mode == "result_size" && (failure.Details["observed_bytes"] <= MaxResultBytes || failure.Details["limit_bytes"] != MaxResultBytes) {
				t.Fatal("exact size diagnostics missing")
			}
			if mode == "wire_size" && (failure.Details["observed_bytes_at_least"] <= MaxWireBytes || failure.Details["limit_bytes"] != MaxWireBytes) {
				t.Fatal("wire lower-bound diagnostics missing")
			}
			if mode == "output_schema" && (len(failure.ValidationKeywords) != 1 || failure.ValidationKeywords[0] != "type") {
				t.Fatalf("safe validation diagnostics missing: %+v", failure)
			}
		})
	}
}

func TestInvocationPreflightDiagnosticsAndTerminalConditions(t *testing.T) {
	r := runtimeFor(t, &testmcp.Server{Tools: json.RawMessage(testmcp.Tools)})
	call := callFor(r)
	call.Arguments = json.RawMessage(`{"key":123}`)
	_, err := r.Invoke(context.Background(), call)
	var f *InvocationFailure
	if !errors.As(err, &f) || f.Execution != "not_dispatched" || len(f.ValidationKeywords) != 1 || f.ValidationKeywords[0] != "type" {
		t.Fatalf("missing safe preflight detail: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = r.Invoke(ctx, callFor(r)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	r.invocations = MaxInvocations
	if _, err = r.Invoke(context.Background(), callFor(r)); err == nil || errors.As(err, &f) {
		t.Fatal("budget exhaustion became recoverable")
	}
}
