//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/llm"
	"github.com/vectorphresh/0001-grodt/internal/mcpclient"
)

// A local read-only protocol peer, distinct from the real acceptance server.
func TestAcceptanceMCPHelper(t *testing.T) {
	if os.Getenv("GRODT_ACCEPTANCE_MCP_CHILD") != "1" {
		return
	}
	_ = os.WriteFile(os.Getenv("GRODT_ACCEPTANCE_MCP_PID"), []byte(strconv.Itoa(os.Getpid())), 0600)
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(2)
		}
		if len(req.ID) == 0 {
			continue
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			reply["result"] = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "read-only-acceptance-test", "version": "1"}}
		case "tools/list":
			reply["result"] = map[string]any{"tools": []any{map[string]any{"name": "inspect", "description": "A dynamically discovered read-only tool", "inputSchema": map[string]any{"type": "object", "additionalProperties": false}}}}
		case "tools/call":
			var params struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &params)
			if params.Name != "inspect" {
				os.Exit(4)
			}
			f, _ := os.OpenFile(os.Getenv("GRODT_ACCEPTANCE_MCP_CALLS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if f != nil {
				_, _ = f.WriteString("called\n")
				_ = f.Close()
			}
			reply["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "{\n  \"status\": \"private-result\"\n}"}}}
		}
		if encoder.Encode(reply) != nil {
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func TestMCPAcceptanceThroughRealTransports(t *testing.T) {
	for _, scenario := range []string{"success", "early-done", "wrong-tool", "repeat-call", "transport-error"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				number := requests.Add(1)
				if scenario == "transport-error" {
					w.WriteHeader(500)
					return
				}
				var req llm.CompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				var input agent.StepInput
				if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
					t.Error(err)
					return
				}
				if len(input.Tools) != 1 || input.Tools[0].Name != "local.inspect" || input.Tools[0].Description != "A dynamically discovered read-only tool" {
					t.Error("discovered tool metadata missing")
				}
				if scenario == "early-done" {
					completion(w, `{"done":true}`)
					return
				}
				if scenario == "wrong-tool" {
					completion(w, `{"tool_call":{"name":"local.mutate","arguments":{}}}`)
					return
				}
				if number == 1 || scenario == "repeat-call" {
					completion(w, `{"tool_call":{"name":"local.inspect","arguments":{}}}`)
					return
				}
				if input.Observation == nil || input.Observation.Tool != "local.inspect" || input.Observation.Error != nil || !equalJSON(input.Observation.Data, []byte(`{"status":"private-result"}`)) {
					t.Error("MCP result not delivered")
				}
				completion(w, `{"done":true}`)
			}))
			defer server.Close()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pid")
			callsFile := filepath.Join(dir, "calls")
			config := mcpclient.Config{Name: "local", Command: binary, Args: []string{"-test.run=^TestAcceptanceMCPHelper$"}, Env: map[string]string{"GRODT_ACCEPTANCE_MCP_CHILD": "1", "GRODT_ACCEPTANCE_MCP_PID": pidFile, "GRODT_ACCEPTANCE_MCP_CALLS": callsFile}}
			var output bytes.Buffer
			err = runMCP(context.Background(), llm.OpenAIConfig{BaseURL: server.URL, Model: "local-test-model", RequestTimeout: time.Second}, config, "inspect", "Inspect the status once", &output)
			if scenario == "success" {
				if err != nil || requests.Load() != 2 || bytes.Count(output.Bytes(), []byte("PASS ")) != 10 {
					t.Fatalf("acceptance err=%v requests=%d output=%s", err, requests.Load(), output.String())
				}
			} else if err == nil || output.Len() != 0 {
				t.Fatalf("invalid acceptance succeeded: %v", err)
			}
			if scenario == "repeat-call" && !strings.Contains(err.Error(), "latest feedback: successful_observation") {
				t.Fatalf("missing safe feedback category: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-result") {
				t.Fatal("sensitive observation exposed in diagnostics")
			}
			if bytes.Contains(output.Bytes(), []byte("private-result")) {
				t.Fatal("sensitive tool result logged")
			}
			calls, _ := os.ReadFile(callsFile)
			expected := 0
			if scenario == "success" || scenario == "repeat-call" {
				expected = 1
			}
			if bytes.Count(calls, []byte("called")) != expected {
				t.Fatal("wrong remote execution count")
			}
			pidData, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, _ := strconv.Atoi(string(pidData))
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("server not reaped on %s: %v (pid %d)", scenario, err, pid)
			}
		})
	}
}

func TestJSONObservationComparison(t *testing.T) {
	if !equalJSON([]byte("{\n \"value\": \"<data>\"}"), []byte(`{"value":"\u003cdata\u003e"}`)) {
		t.Fatal("equivalent JSON observation rejected")
	}
	if equalJSON([]byte(`{"n":9007199254740992}`), []byte(`{"n":9007199254740993}`)) {
		t.Fatal("large numbers compared with precision loss")
	}
}
