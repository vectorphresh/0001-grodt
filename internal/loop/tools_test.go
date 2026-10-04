package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/config"
	"github.com/vectorphresh/0001-grodt/internal/mcp"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/state/wasm"
	"github.com/vectorphresh/0001-grodt/internal/stateflow"
	"github.com/vectorphresh/0001-grodt/internal/testmcp"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

type nativeClient func(context.Context, toolcall.Request) (toolcall.Response, error)

func (f nativeClient) GenerateWithTools(c context.Context, r toolcall.Request) (toolcall.Response, error) {
	return f(c, r)
}

type eventModule struct {
	fn     func(runstate.Event) json.RawMessage
	events []runstate.Event
}

func (m *eventModule) Process(_ context.Context, _ json.RawMessage, e runstate.Event) (json.RawMessage, error) {
	m.events = append(m.events, e)
	if m.fn != nil {
		return m.fn(e), nil
	}
	return json.RawMessage(`{"status":"ignored"}`), nil
}
func (*eventModule) Close(context.Context) error { return nil }
func toolsHarness(t *testing.T, definitions []runstate.Definition, allowHTTP bool) (*ToolProvider, *testmcp.Server) {
	t.Helper()
	ctx := context.Background()
	fixture := &testmcp.Server{Tools: json.RawMessage(testmcp.Tools)}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	runtime, err := mcp.New(ctx, config.MCPConfig{Servers: []config.MCPServer{{Name: "unfamiliar", Transport: "http", URL: server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close() })
	store, err := runstate.New(ctx, definitions, runstate.Options{RunID: "tools", AllowHTTP: allowHTTP})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(ctx) })
	if _, err = store.Push(ctx, "objective", "initial"); err != nil {
		t.Fatal(err)
	}
	if err = store.BeginCycle(ctx); err != nil {
		t.Fatal(err)
	}
	return &ToolProvider{Runtime: runtime, Store: store, Observer: &stateflow.Client{Store: store}}, fixture
}
func toolRequest(r toolcall.Request, id string) toolcall.Call {
	return toolcall.Call{ID: id, Name: r.Tools[0].Name, Arguments: json.RawMessage(`{"key":"sample"}`)}
}
func TestToolContinuationIndependentOfStateConsumption(t *testing.T) {
	for _, mode := range []string{"none", "ignored", "processed", "mutation", "error"} {
		t.Run(mode, func(t *testing.T) {
			modules := []*eventModule{}
			defs := []runstate.Definition{}
			if mode != "none" {
				for i := 0; i < 2; i++ {
					m := &eventModule{fn: func(e runstate.Event) json.RawMessage {
						if e.Source.Kind != "mcp" {
							return json.RawMessage(`{"status":"ignored"}`)
						}
						if mode == "mutation" {
							return json.RawMessage(`{"status":"mutation","replace":1}`)
						}
						return json.RawMessage(fmt.Sprintf(`{"status":%q}`, mode))
					}}
					modules = append(modules, m)
					defs = append(defs, runstate.Definition{Name: fmt.Sprintf("part%d", i), Schema: json.RawMessage(`{"type":"integer"}`), Initial: json.RawMessage(`0`), Module: m})
				}
			}
			p, f := toolsHarness(t, defs, false)
			requests := 0
			p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
				requests++
				if requests == 1 {
					return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "first"), toolRequest(r, "second")}}, nil
				}
				if requests == 2 {
					if len(r.Messages) != 4 || r.Messages[2].Role != "tool" || r.Messages[3].CallID != "second" {
						t.Fatal("lost native tool context")
					}
					if !strings.Contains(r.Messages[2].Text, "fixture result") {
						t.Fatal("lost content")
					}
					snap := p.Store.Snapshot()
					if snap.Intrinsic.GlobalCycle != 1 {
						t.Fatal("continuation consumed cycle")
					}
					if mode == "mutation" && string(snap.Knowledge["part0"].Value) != "1" {
						t.Fatal("missing committed state")
					}
					for _, m := range modules {
						if len(m.events) != 2 {
							t.Fatal("recursive or missed module processing")
						}
					}
				}
				return toolcall.Response{Text: "finished"}, nil
			})
			if _, err := p.Handle(context.Background(), &State{Objective: "objective", Prompt: "initial"}); err != nil {
				t.Fatal(err)
			}
			if f.Calls.Load() != 2 {
				t.Fatal("dispatch count")
			}
			snap := p.Store.Snapshot()
			root, _ := p.Store.Active()
			operation := ""
			for _, task := range snap.Tasks.Records {
				if task.AgentWork == nil {
					continue
				}
				if task.ParentID != root.ID || task.Status != "completed" || task.Cycles != 0 {
					t.Fatalf("wrong lineage: %+v", task)
				}
				if operation != "" && operation != task.AgentWork.OperationID {
					t.Fatal("operation changed")
				}
				operation = task.AgentWork.OperationID
			}
			for _, m := range modules {
				for _, e := range m.events {
					if e.Source.Kind == "mcp" && (e.Correlation == nil || e.Correlation.OperationID != operation || e.Correlation.Turn != 1) {
						t.Fatal("missing correlation")
					}
				}
			}
			if mode == "error" && !snap.Knowledge["part0"].Metadata.Stale {
				t.Fatal("failure not isolated")
			}
			// A later outer operation retains earlier native result messages.
			if err := p.Store.BeginCycle(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Handle(context.Background(), &State{Objective: "objective", Prompt: "again"}); err != nil {
				t.Fatal(err)
			}
			if len(p.history) != 7 || p.history[2].Role != "tool" {
				t.Fatal("history not retained across cycles")
			}
		})
	}
}
func TestMCPResultThroughRealWASM(t *testing.T) {
	ctx := context.Background()
	binary, err := os.ReadFile("../state/wasm/testdata/reference.wasm")
	if err != nil {
		t.Fatal(err)
	}
	module, err := wasm.New(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "counter", Schema: json.RawMessage(`{"type":"integer"}`), Initial: json.RawMessage(`0`), Module: module}}, false)
	requests := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		requests++
		if requests == 1 {
			return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "observation")}}, nil
		}
		if string(p.Store.Snapshot().Knowledge["counter"].Value) != "1" || !strings.Contains(r.Instructions, `"value":1`) {
			t.Fatal("WASM commit not in continuation")
		}
		return toolcall.Response{Text: "observed"}, nil
	})
	if _, err := p.Handle(ctx, &State{Objective: "observe"}); err != nil {
		t.Fatal(err)
	}
	if p.Store.Snapshot().Knowledge["counter"].Metadata.Version != 1 {
		t.Fatal("mutation recursively admitted")
	}
}
func TestToolBatchFailsBeforeAnyDispatchOrTask(t *testing.T) {
	p, f := toolsHarness(t, nil, false)
	c := toolcall.Call{ID: "earlier", Name: p.Runtime.Tools()[0].Name, Arguments: json.RawMessage(`{"key":"sample"}`)}
	for i := 0; i < mcp.MaxInvocations-1; i++ {
		if _, err := p.Runtime.Invoke(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "one"), toolRequest(r, "two")}}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "objective"}); err == nil {
		t.Fatal("accepted over-budget batch")
	}
	if f.Calls.Load() != 31 || len(p.Store.Snapshot().Tasks.Records) != 1 {
		t.Fatal("partial batch dispatched or queued")
	}
}
func TestMCPObservationHTTPChainAndTerminalRetention(t *testing.T) {
	for _, exhaust := range []bool{false, true} {
		t.Run(fmt.Sprint(exhaust), func(t *testing.T) {
			ctx := context.Background()
			sequence := 0
			broadcasts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if broadcasts < 2 {
					t.Error("HTTP executed before broadcast finished")
				}
				w.Write([]byte("page"))
			}))
			defer server.Close()
			requester := &eventModule{fn: func(e runstate.Event) json.RawMessage {
				if e.Source.Kind == "mcp" {
					broadcasts++
				}
				if e.Source.Kind != "mcp" && e.Source.Kind != "http" {
					return json.RawMessage(`{"status":"ignored"}`)
				}
				if !exhaust && sequence >= 2 {
					return json.RawMessage(`{"status":"processed"}`)
				}
				sequence++
				return json.RawMessage(fmt.Sprintf(`{"status":"ignored","requests":[{"id":"page%d","kind":"http","payload":{"method":"GET","url":%q}}]}`, sequence, server.URL))
			}}
			watcher := &eventModule{fn: func(e runstate.Event) json.RawMessage {
				if e.Source.Kind == "mcp" {
					broadcasts++
				}
				return json.RawMessage(`{"status":"ignored"}`)
			}}
			defs := []runstate.Definition{{Name: "requester", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`{}`), Module: requester}, {Name: "watcher", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`{}`), Module: watcher}}
			p, f := toolsHarness(t, defs, true)
			requests := 0
			p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
				requests++
				if requests == 1 {
					return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "tool")}}, nil
				}
				return toolcall.Response{Text: "done"}, nil
			})
			_, err := p.Handle(ctx, &State{Objective: "objective"})
			if exhaust {
				if !errors.Is(err, runstate.ErrHostBudget) || requests != 1 {
					t.Fatalf("error=%v requests=%d", err, requests)
				}
			} else if err != nil || requests != 2 {
				t.Fatalf("error=%v requests=%d", err, requests)
			}
			if f.Calls.Load() != 1 || len(p.history) < 3 || !strings.Contains(p.history[2].Text, "fixture result") {
				t.Fatal("MCP result not retained")
			}
			expected := uint64(2)
			if exhaust {
				expected = 32
			}
			if p.Store.Snapshot().Intrinsic.HostExecutions != expected {
				t.Fatal("HTTP budget mixed with MCP")
			}
			for _, task := range p.Store.Snapshot().Tasks.Records {
				if task.AgentWork != nil && task.Result == "" {
					t.Fatal("outcome lost")
				}
			}
		})
	}
}
func TestToolFailureStopsSiblingsAndRejectsInvalidObservation(t *testing.T) {
	p, f := toolsHarness(t, nil, false)
	f.Call = func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("protocol failure")
	}
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "one"), toolRequest(r, "two")}}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "objective"}); err == nil {
		t.Fatal("protocol failure accepted")
	}
	if f.Calls.Load() != 1 {
		t.Fatal("dispatched later sibling")
	}
	for _, entry := range p.Store.Journal() {
		if entry.Kind == "event_admitted" && strings.Contains(string(entry.Data), `"kind":"mcp"`) {
			t.Fatal("invalid domain observation")
		}
	}
}

func TestToolExecutionErrorRemainsAvailableToReasoning(t *testing.T) {
	p, f := toolsHarness(t, nil, false)
	f.Call = func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"content":[{"type":"text","text":"resource unavailable"}],"isError":true}`), nil
	}
	requests := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		requests++
		if requests == 1 {
			return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "one")}}, nil
		}
		if !strings.Contains(r.Messages[2].Text, `"isError":true`) {
			t.Fatal("tool error omitted")
		}
		return toolcall.Response{Text: "Tool reported resource unavailable."}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "objective"}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || f.Calls.Load() != 1 {
		t.Fatal("incorrect continuation")
	}
	for _, task := range p.Store.Snapshot().Tasks.Records {
		if task.AgentWork != nil && task.Status != "failed" {
			t.Fatal("tool failure not recorded")
		}
	}
}
