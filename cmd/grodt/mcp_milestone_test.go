package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/config"
	"github.com/vectorphresh/0001-grodt/internal/loop"
	"github.com/vectorphresh/0001-grodt/internal/mcp"
	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/state/wasm"
	"github.com/vectorphresh/0001-grodt/internal/stateflow"
	"github.com/vectorphresh/0001-grodt/internal/testmcp"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

type mcpMilestone struct {
	store       *runstate.Store
	runtime     *mcp.Runtime
	fixture     *testmcp.Server
	marker      string
	diagnostics bytes.Buffer
	output      bytes.Buffer
	seenCommit  bool
}

func newMCPMilestone(t *testing.T) *mcpMilestone {
	t.Helper()
	h := &mcpMilestone{marker: rand.Text()}
	h.fixture = &testmcp.Server{Tools: json.RawMessage(testmcp.Tools), Call: func(_ *http.Request, name string, _ json.RawMessage) (json.RawMessage, error) {
		if name != "lookup" {
			t.Error("wrong MCP tool name")
		}
		return json.RawMessage(fmt.Sprintf(`{"content":[{"type":"text","text":"Marker retrieved."}],"structuredContent":{"marker":%q}}`, h.marker)), nil
	}}
	server := httptest.NewServer(h.fixture)
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := fmt.Sprintf("mcp:\n  servers:\n    - name: unfamiliar\n      transport: http\n      url: %q\n      environment:\n        LOCAL_OPTION: local-opaque-option\n", server.URL)
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	resolver, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h.runtime, err = mcp.New(context.Background(), resolver.MCP())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.runtime.Close() })
	h.store, err = wasm.Load(context.Background(), "../../modules/mcp-tool-state/state.json", runstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.store.Close(context.Background()) })
	return h
}

type milestoneNative struct {
	toolcall.Client
	h        *mcpMilestone
	status   *terminalStatus
	requests int
}

func (c *milestoneNative) GenerateWithTools(ctx context.Context, request toolcall.Request) (toolcall.Response, error) {
	c.requests++
	raw, _ := json.Marshal(request)
	if c.requests == 1 && bytes.Contains(raw, []byte(c.h.marker)) {
		return toolcall.Response{}, fmt.Errorf("initial context leaked undiscovered marker")
	}
	for _, m := range request.Messages {
		if m.Role == "tool" && strings.Contains(m.Text, c.h.marker) {
			if c.h.storedMarker() == c.h.marker && strings.Contains(request.Instructions, c.h.marker) {
				c.h.seenCommit = true
			}
		}
	}
	result, err := c.Client.GenerateWithTools(ctx, request)
	if err == nil {
		encoded, _ := json.Marshal(struct {
			Text  string          `json:"text"`
			Calls []toolcall.Call `json:"calls,omitempty"`
		}{result.Text, result.Calls})
		if e := c.status.log("MCP milestone LLM response %d: %s", c.requests, encoded); e != nil {
			return toolcall.Response{}, e
		}
	}
	return result, err
}
func (h *mcpMilestone) run(ctx context.Context, client openai.Client, key string) error {
	status := &terminalStatus{writer: &h.diagnostics, key: key}
	metrics := &runMetrics{}
	observed := observedClient{Client: client, status: status, metrics: metrics}
	withState := &stateflow.Client{Client: observed, Store: h.store}
	native := &milestoneNative{Client: observed, h: h, status: status}
	provider := &loop.ToolProvider{Client: native, Observer: withState, Runtime: h.runtime, Store: h.store}
	evaluate := func(_ context.Context, _ openai.Client, _, _ string, s loop.State) (GoalEvaluation, error) {
		partition := h.store.Snapshot().Knowledge["mcp_tools"]
		achieved := h.seenCommit && !partition.Metadata.Stale && partition.Metadata.Version == uint64(h.fixture.Calls.Load()) && h.fixture.Calls.Load() > 0 && h.storedMarker() == h.marker && strings.Contains(s.Response, h.marker)
		rationale := "The retrieved marker has not been verified in the response and committed state."
		if achieved {
			rationale = "The retrieved marker was reported after an admitted MCP observation and validated WASM commit."
		}
		return GoalEvaluation{Achieved: achieved, Rationale: rationale}, nil
	}
	return runObjectiveWithEvaluator(ctx, "Retrieve and report the fixture marker.", "Use the available lookup tool to retrieve the fixture marker using key sample, then report the marker exactly.", []loop.Provider{provider}, withState, &h.output, status, metrics, h.store, evaluate)
}

type milestoneFake struct{ openai.Client }

func (milestoneFake) GenerateWithTools(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
	for _, m := range r.Messages {
		if m.Role == "tool" {
			var out struct {
				Structured struct {
					Marker string `json:"marker"`
				} `json:"structuredContent"`
			}
			if json.Unmarshal([]byte(m.Text), &out) == nil && out.Structured.Marker != "" {
				return toolcall.Response{Text: "Retrieved marker: " + out.Structured.Marker}, nil
			}
		}
	}
	return toolcall.Response{Calls: []toolcall.Call{{ID: "native-lookup", Name: r.Tools[0].Name, Arguments: json.RawMessage(`{"key":"sample"}`)}}}, nil
}
func TestMCPMilestoneThroughYAMLAndWASM(t *testing.T) {
	h := newMCPMilestone(t)
	if err := h.run(context.Background(), milestoneFake{}, ""); err != nil {
		t.Fatal(err)
	}
	if !h.seenCommit || !strings.Contains(h.output.String(), h.marker) || h.store.Snapshot().Intrinsic.GlobalCycle != 1 {
		t.Fatal("MCP milestone not achieved inside one outer cycle")
	}
}

func (h *mcpMilestone) storedMarker() string {
	var value struct {
		Sources map[string]map[string]struct {
			Result struct {
				Structured struct {
					Marker string `json:"marker"`
				} `json:"structuredContent"`
			} `json:"result"`
		} `json:"sources"`
	}
	if json.Unmarshal(h.store.Snapshot().Knowledge["mcp_tools"].Value, &value) != nil {
		return ""
	}
	return value.Sources["unfamiliar"]["lookup"].Result.Structured.Marker
}
