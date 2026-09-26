package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/llm"
	"github.com/vectorphresh/0001-grodt/internal/observation"
	"github.com/vectorphresh/0001-grodt/internal/runtime"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/tools"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

var ctx = context.Background()
var instant = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

func toolDecision(arguments string) agent.Decision {
	return agent.Decision{Intent: "Inspect the fake account", ToolCall: &tools.ToolCall{Name: "fake.get_account", Arguments: json.RawMessage(arguments)}}
}

func fixture(t *testing.T, decisions ...agent.Decision) (*runtime.Runtime, *agent.FakeAgent, *tools.FakeProvider, *state.MemoryStore, *validation.Validators) {
	t.Helper()
	provider := tools.NewFakeProvider([]tools.ToolDefinition{{
		Name:         "get_account",
		InputSchema:  json.RawMessage(`{"type":"object","required":["quantity"],"properties":{"quantity":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["balance"],"properties":{"balance":{"type":"string"}},"additionalProperties":false}`),
	}}, map[string]tools.ToolResult{"get_account": {Data: json.RawMessage(`{"balance":"100"}`)}})
	registry := tools.NewRegistry()
	if err := registry.Register(ctx, provider); err != nil {
		t.Fatal(err)
	}
	store := state.NewMemoryStore()
	if err := store.Save(ctx, &state.AgentState{
		Goal: "inspect account", PendingIntents: []state.Intent{{ID: "pending", Action: "inspect"}},
	}); err != nil {
		t.Fatal(err)
	}
	decider := agent.NewFakeAgent(decisions)
	validators := validation.NewValidators("fake.get_account")
	runner := runtime.NewRuntime(store, decider, registry, validators, runtime.WithClock(func() time.Time { return instant }))
	return runner, decider, provider, store, validators
}

// The first milestone: tool -> observation -> next decision -> Done -> saved state.
func TestFakeVerticalSlice(t *testing.T) {
	runner, decider, provider, store, _ := fixture(t, toolDecision(`{"quantity":1}`), agent.Decision{Done: true})
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(provider.Calls) != 1 || provider.Calls[0].Name != "get_account" {
		t.Fatalf("remote calls: %+v", provider.Calls)
	}
	if len(decider.Inputs) != 2 || decider.Inputs[0].Observation != nil {
		t.Fatalf("inputs: %+v", decider.Inputs)
	}
	obs := decider.Inputs[1].Observation
	if obs == nil || obs.Tool != "fake.get_account" || obs.Source != "fake" || string(obs.Data) != `{"balance":"100"}` || obs.Error != nil {
		t.Fatalf("feedback: %+v", obs)
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 2 || decider.Inputs[1].State.Version != 1 || !snapshot.UpdatedAt.Equal(instant) {
		t.Fatalf("state: %+v", snapshot)
	}
	if len(snapshot.WorkingMemory) != 0 {
		t.Fatal("raw observation was stored as memory")
	}
}

type actionFunc func(context.Context, state.AgentState, tools.ToolCall) error

func (f actionFunc) Validate(c context.Context, s state.AgentState, call tools.ToolCall) error {
	return f(c, s, call)
}

func TestRejectedActionsNeverReachProvider(t *testing.T) {
	cases := []struct {
		name, kind string
		change     func(*agent.Decision, *validation.Validators)
	}{
		{"unknown tool", "unknown_tool", func(d *agent.Decision, _ *validation.Validators) { d.ToolCall.Name = "fake.missing" }},
		{"missing required argument", "invalid_tool_arguments", func(d *agent.Decision, _ *validation.Validators) { d.ToolCall.Arguments = json.RawMessage(`{}`) }},
		{"wrong argument type", "invalid_tool_arguments", func(d *agent.Decision, _ *validation.Validators) {
			d.ToolCall.Arguments = json.RawMessage(`{"quantity":"1"}`)
		}},
		{"below minimum", "invalid_tool_arguments", func(d *agent.Decision, _ *validation.Validators) {
			d.ToolCall.Arguments = json.RawMessage(`{"quantity":0}`)
		}},
		{"extra argument", "invalid_tool_arguments", func(d *agent.Decision, _ *validation.Validators) {
			d.ToolCall.Arguments = json.RawMessage(`{"quantity":1,"extra":true}`)
		}},
		{"string arguments", "invalid_tool_arguments", func(d *agent.Decision, _ *validation.Validators) {
			d.ToolCall.Arguments = json.RawMessage(`"arguments"`)
		}},
		{"permission denied", "permission_denied", func(_ *agent.Decision, v *validation.Validators) {
			v.Permission = validation.NewValidators().Permission
		}},
		{"risk rejected", "action_rejected", func(_ *agent.Decision, v *validation.Validators) {
			v.Action = actionFunc(func(context.Context, state.AgentState, tools.ToolCall) error {
				return errors.New("allocation exceeded")
			})
		}},
		{"invalid decision", "invalid_decision", func(d *agent.Decision, _ *validation.Validators) { d.Done = true }},
		{"invalid patch", "invalid_state_patch", func(d *agent.Decision, _ *validation.Validators) {
			d.StatePatch.AddMemory = []state.MemoryEntry{{Key: ""}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := toolDecision(`{"quantity":1}`)
			// Even a valid intent update must roll back when the action is rejected.
			d.StatePatch = &state.StatePatch{UpdateIntent: []state.IntentUpdate{{ID: "pending", Executed: true, ExecutedAt: instant}}}
			runner, decider, provider, store, validators := fixture(t)
			tc.change(&d, validators)
			decider.Decisions = []agent.Decision{d, {Done: true}}
			first, err := runner.Step(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if first.Done || first.Observation == nil || first.Observation.Error == nil || first.Observation.Error.Type != tc.kind {
				t.Fatalf("result: %+v", first)
			}
			saved, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if saved.PendingIntents[0].Executed {
				t.Fatal("rejected patch leaked into storage")
			}
			if _, err := runner.Step(ctx); err != nil {
				t.Fatal(err)
			}
			if decider.Inputs[1].Observation.Error.Type != tc.kind {
				t.Fatal("error not delivered to next step")
			}
			if len(provider.Calls) != 0 {
				t.Fatal("rejected call reached provider")
			}
		})
	}
}

func TestPatchOnlyAndDonePersist(t *testing.T) {
	goal := "new goal"
	runner, decider, provider, store, _ := fixture(t,
		agent.Decision{StatePatch: &state.StatePatch{Goal: &goal, AddMemory: []state.MemoryEntry{{Key: "useful", Value: "remember this"}}}},
		agent.Decision{Done: true, StatePatch: &state.StatePatch{UpdateIntent: []state.IntentUpdate{{ID: "pending", Executed: true, ExecutedAt: instant}}}},
	)
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Load(ctx)
	if saved.Goal != goal || len(saved.WorkingMemory) != 1 || !saved.PendingIntents[0].Executed || saved.Version != 2 {
		t.Fatalf("state: %+v", saved)
	}
	if decider.Inputs[1].State.Goal != goal || len(provider.Calls) != 0 {
		t.Fatal("patch-only behavior incorrect")
	}
}

func TestProviderErrorBecomesObservation(t *testing.T) {
	runner, decider, provider, _, _ := fixture(t, toolDecision(`{"quantity":1}`), agent.Decision{Done: true})
	provider.ExecuteError = errors.New("temporary provider failure")
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	obs := decider.Inputs[1].Observation
	if obs.Error.Type != "tool_execution_error" || obs.Source != "fake" || obs.Tool != "fake.get_account" {
		t.Fatalf("observation: %+v", obs)
	}
	if len(provider.Calls) != 1 {
		t.Fatal("unexpected automatic retry")
	}
}

type balanceReducer struct{ calls int }

func (*balanceReducer) Supports(obs observation.Observation) bool {
	return obs.Tool == "fake.get_account"
}
func (r *balanceReducer) Reduce(s state.AgentState, obs observation.Observation) (state.AgentState, error) {
	r.calls++
	var account struct {
		Balance string `json:"balance"`
	}
	if err := json.Unmarshal(obs.Data, &account); err != nil {
		return s, err
	}
	s.World.Balances = []state.Balance{{Asset: "USD", Amount: account.Balance}}
	s.World.ObservedAt = obs.Timestamp
	return s, nil
}

func TestOnlyValidatedOutputReachesReducers(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "invalid"}[valid], func(t *testing.T) {
			_, decider, provider, store, validators := fixture(t, toolDecision(`{"quantity":1}`), agent.Decision{Done: true})
			if !valid {
				provider.Results["get_account"] = tools.ToolResult{Data: json.RawMessage(`{"balance":100}`)}
			}
			registry := tools.NewRegistry()
			if err := registry.Register(ctx, provider); err != nil {
				t.Fatal(err)
			}
			reducer := &balanceReducer{}
			runner := runtime.NewRuntime(store, decider, registry, validators, runtime.WithReducers(reducer), runtime.WithClock(func() time.Time { return instant }))
			if err := runner.Run(ctx); err != nil {
				t.Fatal(err)
			}
			saved, _ := store.Load(ctx)
			if valid {
				if reducer.calls != 1 || saved.World.Balances[0].Amount != "100" || !saved.World.ObservedAt.Equal(instant) || len(decider.Inputs[1].State.World.Balances) != 1 {
					t.Fatalf("state: %+v", saved)
				}
			} else {
				if reducer.calls != 0 || len(saved.World.Balances) != 0 || decider.Inputs[1].Observation.Error.Type != "invalid_tool_output" {
					t.Fatal("invalid output was trusted")
				}
			}
		})
	}
}

func TestReturnedObservationCannotMutateFeedback(t *testing.T) {
	runner, decider, _, _, _ := fixture(t, toolDecision(`{"quantity":1}`), agent.Decision{Done: true})
	result, err := runner.Step(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result.Observation.Data[0] = '!'
	result.Observation.Source = "tampered"
	if _, err := runner.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if decider.Inputs[1].Observation.Source != "fake" || string(decider.Inputs[1].Observation.Data) != `{"balance":"100"}` {
		t.Fatal("feedback alias")
	}
}

type failingStore struct {
	state.Store
	loadErr, saveErr error
}

func (s failingStore) Load(c context.Context) (*state.AgentState, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.Store.Load(c)
}
func (s failingStore) Save(c context.Context, value *state.AgentState) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.Store.Save(c, value)
}

func TestFatalFailuresAndCancellation(t *testing.T) {
	sentinel := errors.New("fatal failure")
	for _, operation := range []string{"load", "save", "provider", "cancellation"} {
		t.Run(operation, func(t *testing.T) {
			_, decider, provider, store, validators := fixture(t, toolDecision(`{"quantity":1}`), agent.Decision{Done: true})
			registry := tools.NewRegistry()
			if err := registry.Register(ctx, provider); err != nil {
				t.Fatal(err)
			}
			storage := failingStore{Store: store}
			runCtx := ctx
			expected := sentinel
			switch operation {
			case "load":
				storage.loadErr = sentinel
			case "save":
				storage.saveErr = sentinel
			case "provider":
				provider.ExecuteError = &tools.FatalError{Err: sentinel}
			case "cancellation":
				var cancel context.CancelFunc
				runCtx, cancel = context.WithCancel(ctx)
				cancel()
				expected = context.Canceled
			}
			runner := runtime.NewRuntime(storage, decider, registry, validators)
			if err := runner.Run(runCtx); !errors.Is(err, expected) {
				t.Fatalf("got %v, want %v", err, expected)
			}
			if len(decider.Inputs) > 1 {
				t.Fatal("continued after fatal failure")
			}
			if (operation == "load" || operation == "cancellation") && len(provider.Calls) != 0 {
				t.Fatal("executed after early failure")
			}
		})
	}
}

func TestMalformedLLMDecisionCanBeCorrected(t *testing.T) {
	client := llm.NewFakeClient([]llm.CompletionResponse{{Content: `{"state_patch":{"world":{}},"done":true}`}, {Content: `{"done":true}`}})
	store := state.NewMemoryStore()
	runner := runtime.NewRuntime(store, agent.NewLLMAgent(client), tools.NewRegistry(), validation.NewValidators())
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var input agent.StepInput
	if err := json.Unmarshal([]byte(client.Requests[1].Messages[1].Content), &input); err != nil {
		t.Fatal(err)
	}
	if input.Observation.Error.Type != "invalid_decision" {
		t.Fatalf("observation: %+v", input.Observation)
	}
}

type agentFunc func(context.Context, agent.StepInput) (agent.Decision, error)

func (f agentFunc) Decide(c context.Context, input agent.StepInput) (agent.Decision, error) {
	return f(c, input)
}

func TestAgentCannotMutateRuntimeState(t *testing.T) {
	store := state.NewMemoryStore()
	initial := state.AgentState{World: state.WorldState{Balances: []state.Balance{{Asset: "USD", Amount: "100"}}}}
	if err := store.Save(ctx, &initial); err != nil {
		t.Fatal(err)
	}
	decider := agentFunc(func(_ context.Context, input agent.StepInput) (agent.Decision, error) {
		input.State.World.Balances[0].Amount = "999"
		return agent.Decision{Done: true}, nil
	})
	runner := runtime.NewRuntime(store, decider, tools.NewRegistry(), validation.NewValidators())
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Load(ctx)
	if !reflect.DeepEqual(saved.World, initial.World) {
		t.Fatal("agent mutated world")
	}
}

func TestObservationIDsDistinctWithFixedClock(t *testing.T) {
	runner, _, _, _, _ := fixture(t, toolDecision(`{"quantity":1}`), toolDecision(`{"quantity":1}`))
	a, err := runner.Step(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := runner.Step(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.Observation.ID == b.Observation.ID {
		t.Fatal("duplicate observation IDs")
	}
}

func TestCancellationAfterDecisionPreventsExecution(t *testing.T) {
	_, _, provider, store, validators := fixture(t)
	registry := tools.NewRegistry()
	if err := registry.Register(ctx, provider); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	decider := agentFunc(func(context.Context, agent.StepInput) (agent.Decision, error) {
		cancel()
		return toolDecision(`{"quantity":1}`), nil
	})
	runner := runtime.NewRuntime(store, decider, registry, validators)
	if err := runner.Run(runCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error: %v", err)
	}
	if len(provider.Calls) != 0 {
		t.Fatal("executed after cancellation")
	}
}

type failingReducer struct{ err error }

func (failingReducer) Supports(observation.Observation) bool { return true }
func (r failingReducer) Reduce(s state.AgentState, _ observation.Observation) (state.AgentState, error) {
	s.PendingIntents[0].Executed = true
	return s, r.err
}

func TestReducerFailureDoesNotPersistPartialState(t *testing.T) {
	_, decider, provider, store, validators := fixture(t, toolDecision(`{"quantity":1}`), agent.Decision{Done: true})
	registry := tools.NewRegistry()
	if err := registry.Register(ctx, provider); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("reduction failed")
	runner := runtime.NewRuntime(store, decider, registry, validators, runtime.WithReducers(failingReducer{sentinel}))
	if err := runner.Run(ctx); !errors.Is(err, sentinel) {
		t.Fatalf("error: %v", err)
	}
	saved, _ := store.Load(ctx)
	if saved.PendingIntents[0].Executed || saved.Version != 0 {
		t.Fatal("failed reduction partially persisted")
	}
	if len(provider.Calls) != 1 || len(decider.Inputs) != 1 {
		t.Fatal("failure did not stop after the executed call")
	}
}

func TestNilReducerFailsBeforeSideEffects(t *testing.T) {
	_, decider, provider, store, validators := fixture(t, toolDecision(`{"quantity":1}`))
	registry := tools.NewRegistry()
	if err := registry.Register(ctx, provider); err != nil {
		t.Fatal(err)
	}
	runner := runtime.NewRuntime(store, decider, registry, validators, runtime.WithReducers(nil))
	if err := runner.Run(ctx); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	if len(provider.Calls) != 0 {
		t.Fatal("executed with invalid configuration")
	}
}
