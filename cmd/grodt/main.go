package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"

	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/runtime"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/tools"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

func run(ctx context.Context) error {
	store := state.NewMemoryStore()
	if err := store.Save(ctx, &state.AgentState{Goal: "Inspect a fake account"}); err != nil {
		return err
	}
	provider := tools.NewFakeProvider([]tools.ToolDefinition{{
		Name: "get_account", Description: "Read a simulated account",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["status"],"properties":{"status":{"type":"string"}}}`),
	}}, map[string]tools.ToolResult{"get_account": {Data: json.RawMessage(`{"status":"active"}`)}})
	registry := tools.NewRegistry()
	if err := registry.Register(ctx, provider); err != nil {
		return err
	}
	decider := agent.NewFakeAgent([]agent.Decision{
		{Intent: "Inspect account", ToolCall: &tools.ToolCall{Name: "fake.get_account", Arguments: json.RawMessage(`{}`)}},
		{Summary: "Fake account inspected", Done: true},
	})
	runner := runtime.NewRuntime(store, decider, registry, validation.NewValidators("fake.get_account"))
	if err := runner.Run(ctx); err != nil {
		return err
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Fake vertical slice completed: %d decisions, %d tool call, state version %d\n", len(decider.Inputs), len(provider.Calls), snapshot.Version)
	fmt.Printf("Second decision received observation: %s\n", decider.Inputs[1].Observation.Data)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
