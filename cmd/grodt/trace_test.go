package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestInferenceTraceArtifacts(t *testing.T) {
	store, err := runstate.New(context.Background(), nil, runstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	trace, err := newInferenceTrace(t.TempDir(), store, "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	status := &terminalStatus{writer: io.Discard, artifacts: trace}
	client := observedClient{status: status, metrics: &runMetrics{Requests: 1}}
	client.traceRequest("tool-generation", toolcall.Request{Instructions: "secret-key"})
	client.traceResponse(toolcall.Response{Calls: []toolcall.Call{{ID: "call-1", Name: "quote", Arguments: json.RawMessage(`{}`)}}}, time.Now(), nil)
	status.saveArtifact("final-state.json", store.JSON())
	for _, name := range []string{"000001-request.json", "000001-response.json", "000001-state.json", "final-state.json"} {
		path := filepath.Join(trace.dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(data) || strings.Contains(string(data), "secret-key") {
			t.Fatalf("invalid or unredacted artifact: %s", name)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("artifact permissions: %s", name)
		}
		if name == "000001-response.json" && !strings.Contains(string(data), "call-1") {
			t.Fatal("native tool call missing")
		}
	}
	client.metrics.Requests = 2
	client.traceResponse(toolcall.Response{}, time.Now(), &openai.ProviderRequestError{StatusCode: 400, Response: json.RawMessage(`{"message":"context too long","echo":"secret-key"}`)})
	data, err := os.ReadFile(filepath.Join(trace.dir, "000002-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		ProviderResponse map[string]string `json:"provider_error_response"`
		Feedback         string            `json:"failure_feedback"`
	}
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.ProviderResponse["message"] != "context too long" || artifact.ProviderResponse["echo"] != "[redacted]" || !strings.Contains(artifact.Feedback, "HTTP 400") {
		t.Fatal("provider response or safe feedback missing from trace")
	}
}

type proceduralTraceClient struct{ openai.Client }

func (proceduralTraceClient) PromptWithSpecification(_ context.Context, _ string, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
	return openai.JSONResult{JSON: json.RawMessage(`{"action":"no_progress","updates":[],"current_step":""}`), Usage: &openai.Usage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20}}, nil
}
func TestReconciliationUsesExistingTraceAndAccounting(t *testing.T) {
	ctx := context.Background()
	store, err := runstate.New(ctx, nil, runstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	trace, err := newInferenceTrace(t.TempDir(), store, "")
	if err != nil {
		t.Fatal(err)
	}
	metrics := &runMetrics{}
	client := observedClient{Client: proceduralTraceClient{}, status: &terminalStatus{writer: io.Discard, artifacts: trace}, metrics: metrics}
	result, err := client.PromptWithSpecification(ctx, "Record consequences only.", "What did this establish?", json.RawMessage(`{"actor_intent":"Inspect configuration","accepted_evidence":[]}`), openai.JSONSpecification{Name: "progress_reconciliation", Schema: json.RawMessage(`{"type":"object"}`), Strict: true})
	if err != nil || len(result.JSON) == 0 || metrics.Requests != 1 {
		t.Fatal("reconciliation bypassed observed client", err)
	}
	request, err := os.ReadFile(filepath.Join(trace.dir, "000001-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := os.ReadFile(filepath.Join(trace.dir, "000001-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(request), "progress_reconciliation") || strings.Contains(string(request), "inputSchema") || !strings.Contains(string(response), "no_progress") || !strings.Contains(string(response), "PromptTokens") {
		t.Fatal("reconciliation request, response, or usage missing from trace")
	}
}
