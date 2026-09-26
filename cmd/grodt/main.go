package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/llm"
	"github.com/vectorphresh/0001-grodt/internal/runtime"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/tools"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

func fakeAccount(ctx context.Context) (*state.MemoryStore, *tools.FakeProvider, *tools.ProviderRegistry, error) {
	store := state.NewMemoryStore()
	if err := store.Save(ctx, &state.AgentState{Goal: "Inspect the fake account by calling fake.get_account exactly once with empty arguments. Once its observation confirms the account status, return done:true. Do not change state or call any additional tools."}); err != nil {
		return nil, nil, nil, err
	}
	provider := tools.NewFakeProvider([]tools.ToolDefinition{{
		Name: "get_account", Description: "Read a simulated account",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["status"],"properties":{"status":{"type":"string"}}}`),
	}}, map[string]tools.ToolResult{"get_account": {Data: json.RawMessage(`{"status":"active"}`)}})
	registry := tools.NewRegistry()
	if err := registry.Register(ctx, provider); err != nil {
		return nil, nil, nil, err
	}
	return store, provider, registry, nil
}

func runFake(ctx context.Context, output io.Writer) error {
	store, provider, registry, err := fakeAccount(ctx)
	if err != nil {
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
	fmt.Fprintf(output, "Fake vertical slice completed: %d decisions, %d tool call, state version %d\n", len(decider.Inputs), len(provider.Calls), snapshot.Version)
	fmt.Fprintf(output, "Second decision received observation: %s\n", decider.Inputs[1].Observation.Data)
	return nil
}

// acceptanceClient records only the model-neutral request data, never HTTP auth.
// Its two-request budget is specific to this acceptance scenario, not runtime policy.
type acceptanceClient struct {
	client llm.Client
	inputs []agent.StepInput
}

func (c *acceptanceClient) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if len(c.inputs) == 2 {
		return llm.CompletionResponse{}, fmt.Errorf("acceptance failed: model did not finish in two requests")
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		return llm.CompletionResponse{}, fmt.Errorf("acceptance failed: expected system and data messages")
	}
	var input agent.StepInput
	if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
		return llm.CompletionResponse{}, fmt.Errorf("acceptance failed: invalid execution context")
	}
	c.inputs = append(c.inputs, input)
	return c.client.Complete(ctx, req)
}

// runLLM proves the seven live acceptance criteria using the actual HTTP client.
// Tests use a local HTTP server; a live run uses the configured model endpoint.
func runLLM(ctx context.Context, config llm.OpenAIConfig, output io.Writer) error {
	client, err := llm.NewOpenAIClient(config)
	if err != nil {
		return err
	}
	store, provider, registry, err := fakeAccount(ctx)
	if err != nil {
		return err
	}
	recorded := &acceptanceClient{client: client}
	runner := runtime.NewRuntime(store, agent.NewLLMAgent(recorded), registry, validation.NewValidators("fake.get_account"))
	if err := runner.Run(ctx); err != nil {
		return err
	}
	if len(recorded.inputs) != 2 || len(provider.Calls) != 1 || provider.Calls[0].Name != "get_account" {
		return fmt.Errorf("acceptance failed: expected two LLM requests and one fake account call")
	}
	first, second := recorded.inputs[0], recorded.inputs[1]
	if len(first.Tools) != 1 || first.Tools[0].Name != "fake.get_account" || first.Observation != nil {
		return fmt.Errorf("acceptance failed: first request did not contain the expected tool context")
	}
	obs := second.Observation
	if obs == nil || obs.Error != nil || obs.Source != "fake" || obs.Tool != "fake.get_account" || string(obs.Data) != `{"status":"active"}` {
		return fmt.Errorf("acceptance failed: second request did not contain the successful account observation")
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		return err
	}
	if snapshot.Version != 2 {
		return fmt.Errorf("acceptance failed: expected two persisted steps")
	}
	for _, criterion := range []string{
		"LLM received fake.get_account",
		"LLM requested fake.get_account",
		"Fake provider executed exactly once",
		"Tool result became a successful Observation",
		"Second LLM request contained that Observation",
		"LLM returned Done",
		"Runtime exited successfully; state version 2",
	} {
		fmt.Fprintln(output, "PASS:", criterion)
	}
	return nil
}

func run(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("grodt", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	mode := flags.String("mode", "fake", "fake demo or llm acceptance run")
	baseURL := flags.String("base-url", "", "OpenAI-compatible API root (overrides environment)")
	model := flags.String("model", "", "model name (overrides environment)")
	timeout := flags.Duration("request-timeout", 0, "per-request timeout, e.g. 2m (overrides environment)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	switch *mode {
	case "fake":
		return runFake(ctx, output)
	case "llm":
		config, err := llm.OpenAIConfigFromEnv(llm.DefaultOpenAIConfig())
		if err != nil {
			return err
		}
		flags.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "base-url":
				config.BaseURL = *baseURL
			case "model":
				config.Model = *model
			case "request-timeout":
				config.RequestTimeout = *timeout
			}
		})
		return runLLM(ctx, config, output)
	default:
		return fmt.Errorf("mode must be fake or llm")
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if err == flag.ErrHelp {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
