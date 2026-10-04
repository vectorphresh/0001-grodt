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
}
