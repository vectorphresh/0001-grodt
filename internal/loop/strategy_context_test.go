package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

// strategyModule mirrors the gr33d market_state reducer contract for llm-kind
// events: accepted payloads with a strategy_context string bounded in UTF-8
// bytes atomically replace the stored context while market_state is preserved.
type strategyModule struct{}

const strategyModuleLimit = 8192

func (strategyModule) Process(_ context.Context, current json.RawMessage, e runstate.Event) (json.RawMessage, error) {
	var state struct {
		MarketState     json.RawMessage `json:"market_state"`
		StrategyContext string          `json:"strategy_context"`
	}
	if err := json.Unmarshal(current, &state); err != nil {
		return json.RawMessage(`{"status":"error"}`), nil
	}
	if e.Source.Kind != "llm" {
		return json.RawMessage(`{"status":"ignored"}`), nil
	}
	var payload struct {
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return json.RawMessage(`{"status":"ignored"}`), nil
	}
	if payload.Status != "accepted" {
		return json.RawMessage(`{"status":"ignored"}`), nil
	}
	var result struct {
		StrategyContext *string `json:"strategy_context"`
	}
	if err := json.Unmarshal(payload.Result, &result); err != nil {
		return json.RawMessage(`{"status":"ignored"}`), nil
	}
	if result.StrategyContext == nil {
		return json.RawMessage(`{"status":"ignored"}`), nil
	}
	if len([]byte(*result.StrategyContext)) > strategyModuleLimit {
		return json.RawMessage(`{"status":"error"}`), nil
	}
	out, err := json.Marshal(map[string]any{"status": "mutation", "replace": map[string]any{
		"market_state":     state.MarketState,
		"strategy_context": *result.StrategyContext,
	}})
	if err != nil {
		return json.RawMessage(`{"status":"error"}`), nil
	}
	return out, nil
}
func (strategyModule) Close(context.Context) error { return nil }

func strategyDefinitions() []runstate.Definition {
	return []runstate.Definition{{
		Name:    "gr33d_market_state",
		Schema:  json.RawMessage(`{"type":"object","required":["market_state","strategy_context"],"properties":{"market_state":{"type":"object"},"strategy_context":{"type":"string"}},"additionalProperties":false}`),
		Initial: json.RawMessage(`{"market_state":{},"strategy_context":""}`),
		Module:  strategyModule{},
	}}
}

func strategyCall(id string, args string) toolcall.Call {
	return toolcall.Call{ID: id, Name: strategyContextTool, Arguments: json.RawMessage(args)}
}

func TestStrategyContextSetPersistsAcrossTurns(t *testing.T) {
	p, _ := toolsExecutionHarness(t, strategyDefinitions(), false)
	requests := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		requests++
		switch requests {
		case 1:
			return toolcall.Response{Calls: []toolcall.Call{strategyCall("ctx1", `{"operation":"set","strategy_context":"paper trading momentum test; watching MA crossover"}`)}}, nil
		case 2:
			if !strings.Contains(r.Instructions, "paper trading momentum test") {
				t.Fatal("stored strategy context not projected to actor")
			}
			return toolcall.Response{Calls: []toolcall.Call{strategyCall("ctx2", `{"operation":"set","strategy_context":"revised: reduce size on drawdown"}`)}}, nil
		case 3:
			if !strings.Contains(r.Instructions, "revised: reduce size") || strings.Contains(r.Instructions, "watching MA crossover") {
				t.Fatal("second set did not atomically replace context")
			}
			return toolcall.Response{Text: "done"}, nil
		}
		return toolcall.Response{Text: "unexpected request"}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "objective", Prompt: "trade"}); err != nil {
		t.Fatal(err)
	}
	value := p.Store.Snapshot().Knowledge["gr33d_market_state"].Value
	var state struct {
		MarketState     map[string]any `json:"market_state"`
		StrategyContext string         `json:"strategy_context"`
	}
	if err := json.Unmarshal(value, &state); err != nil {
		t.Fatal(err)
	}
	if state.StrategyContext != "revised: reduce size on drawdown" {
		t.Fatalf("final context %q", state.StrategyContext)
	}
	if state.MarketState == nil {
		t.Fatal("market_state lost during strategy update")
	}
}

func TestStrategyContextOversizedRejectedWithoutStaling(t *testing.T) {
	p, _ := toolsExecutionHarness(t, strategyDefinitions(), false)
	oversized := strings.Repeat("x", DefaultStrategyContextBytes+1)
	requests := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		requests++
		switch requests {
		case 1:
			return toolcall.Response{Calls: []toolcall.Call{strategyCall("ctx1", fmt.Sprintf(`{"operation":"set","strategy_context":%q}`, oversized))}}, nil
		case 2:
			if !strings.Contains(r.Messages[2].Text, "limit is 8192") {
				t.Fatalf("deterministic limit error missing: %q", r.Messages[2].Text)
			}
			return toolcall.Response{Text: "done"}, nil
		}
		return toolcall.Response{Text: "unexpected request"}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "objective", Prompt: "trade"}); err != nil {
		t.Fatal(err)
	}
	partition := p.Store.Snapshot().Knowledge["gr33d_market_state"]
	if string(partition.Value) != `{"market_state":{},"strategy_context":""}` {
		t.Fatalf("oversized set changed value: %s", partition.Value)
	}
	if partition.Metadata.Stale {
		t.Fatal("host-side rejection must not stale the partition")
	}
	if partition.Metadata.Version != 0 {
		t.Fatal("rejected set must not bump version")
	}
}

func TestStrategyContextClear(t *testing.T) {
	p, _ := toolsExecutionHarness(t, strategyDefinitions(), false)
	requests := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		requests++
		switch requests {
		case 1:
			return toolcall.Response{Calls: []toolcall.Call{strategyCall("ctx1", `{"operation":"set","strategy_context":"active strategy"}`)}}, nil
		case 2:
			return toolcall.Response{Calls: []toolcall.Call{strategyCall("ctx2", `{"operation":"clear"}`)}}, nil
		case 3:
			return toolcall.Response{Text: "done"}, nil
		}
		return toolcall.Response{Text: "unexpected request"}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "objective", Prompt: "trade"}); err != nil {
		t.Fatal(err)
	}
	var state struct {
		StrategyContext string `json:"strategy_context"`
	}
	if err := json.Unmarshal(p.Store.Snapshot().Knowledge["gr33d_market_state"].Value, &state); err != nil {
		t.Fatal(err)
	}
	if state.StrategyContext != "" {
		t.Fatalf("clear left %q", state.StrategyContext)
	}
}

func TestStrategyContextAdmittedAsLLMSource(t *testing.T) {
	p, _ := toolsExecutionHarness(t, strategyDefinitions(), false)
	requests := 0
	p.Client = nativeClient(func(_ context.Context, _ toolcall.Request) (toolcall.Response, error) {
		requests++
		if requests == 1 {
			return toolcall.Response{Calls: []toolcall.Call{strategyCall("ctx1", `{"operation":"set","strategy_context":"provenance check"}`)}}, nil
		}
		return toolcall.Response{Text: "done"}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "objective", Prompt: "trade"}); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, entry := range p.Store.Journal() {
		if entry.Kind != "mutation" || entry.Partition != "gr33d_market_state" {
			continue
		}
		found = true
	}
	if !found {
		t.Fatal("no mutation recorded for strategy context set")
	}
	for _, entry := range p.Store.Journal() {
		if entry.Kind == "event" && strings.Contains(string(entry.Data), "provenance check") {
			var event runstate.Event
			if err := json.Unmarshal(entry.Data, &event); err != nil {
				t.Fatal(err)
			}
			if event.Source.Kind != "llm" {
				t.Fatalf("source kind %q, want llm", event.Source.Kind)
			}
			if !strings.HasPrefix(event.Source.ID, "tools/strategy/") {
				t.Fatalf("source id %q", event.Source.ID)
			}
			return
		}
	}
	t.Fatal("strategy event missing from journal")
}

func TestStrategyContextMustBeAlone(t *testing.T) {
	p, _ := toolsExecutionHarness(t, strategyDefinitions(), false)
	requests := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		requests++
		switch requests {
		case 1:
			return toolcall.Response{Calls: []toolcall.Call{
				strategyCall("ctx1", `{"operation":"set","strategy_context":"batched"}`),
				toolRequest(r, "other"),
			}}, nil
		case 2:
			if !strings.Contains(r.Messages[2].Text, "alone") {
				t.Fatalf("alone requirement missing: %q", r.Messages[2].Text)
			}
			return toolcall.Response{Text: "done"}, nil
		}
		return toolcall.Response{Text: "unexpected request"}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "objective", Prompt: "trade"}); err != nil {
		t.Fatal(err)
	}
	var state struct {
		StrategyContext string `json:"strategy_context"`
	}
	if err := json.Unmarshal(p.Store.Snapshot().Knowledge["gr33d_market_state"].Value, &state); err != nil {
		t.Fatal(err)
	}
	if state.StrategyContext != "" {
		t.Fatal("batched strategy call must not execute")
	}
}

func TestStrategyContextInvalidArguments(t *testing.T) {
	for _, args := range []string{
		`{"operation":"set"}`,
		`{"operation":"set","strategy_context":""}`,
		`{"operation":"delete","strategy_context":"x"}`,
		`{"operation":"set","strategy_context":"x","extra":1}`,
	} {
		p, _ := toolsExecutionHarness(t, strategyDefinitions(), false)
		requests := 0
		p.Client = nativeClient(func(_ context.Context, _ toolcall.Request) (toolcall.Response, error) {
			requests++
			if requests == 1 {
				return toolcall.Response{Calls: []toolcall.Call{strategyCall("ctx1", args)}}, nil
			}
			return toolcall.Response{Text: "done"}, nil
		})
		if _, err := p.Handle(context.Background(), &State{Objective: "objective", Prompt: "trade"}); err != nil {
			t.Fatal(err)
		}
		partition := p.Store.Snapshot().Knowledge["gr33d_market_state"]
		if string(partition.Value) != `{"market_state":{},"strategy_context":""}` {
			t.Fatalf("args %s changed value: %s", args, partition.Value)
		}
		if partition.Metadata.Stale {
			t.Fatalf("args %s staled the partition", args)
		}
	}
}
