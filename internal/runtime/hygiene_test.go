package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/llm"
	"github.com/vectorphresh/0001-grodt/internal/runtime"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/tools"
	"github.com/vectorphresh/0001-grodt/internal/validation"
)

func factCall() *tools.ToolCall {
	return &tools.ToolCall{Name: "fake.read_fact", Arguments: json.RawMessage(`{}`)}
}

func hygieneFixture(t *testing.T, decider agent.Agent) (*runtime.Runtime, *state.MemoryStore, *tools.FakeProvider) {
	t.Helper()
	provider := tools.NewFakeProvider([]tools.ToolDefinition{{Name: "read_fact", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"fact":{"type":"string"}},"required":["fact"]}`)}}, map[string]tools.ToolResult{})
	registry := tools.NewRegistry()
	if err := registry.Register(ctx, provider); err != nil {
		t.Fatal(err)
	}
	store := state.NewMemoryStore()
	spec := agent.RunSpec{Goal: "Maintain current facts", Instructions: "Read observations, correct compact memory, and retire irrelevant intents."}
	runner := runtime.NewRuntime(spec, store, decider, registry, validation.NewValidators("fake.read_fact"), runtime.WithClock(func() time.Time { return instant }))
	return runner, store, provider
}

func TestStaleFactReplacementAndRemovalAcrossSteps(t *testing.T) {
	oldFact, newFact := "NVDA earnings are tomorrow", "NVDA earnings have occurred"
	decider := agent.NewFakeAgent([]agent.Decision{
		{ToolCall: factCall()},
		{StatePatch: &state.StatePatch{AddMemory: []state.MemoryEntry{{Key: "earnings", Value: oldFact}}}, ToolCall: factCall()},
		{StatePatch: &state.StatePatch{UpsertMemory: []state.MemoryEntry{{Key: "earnings", Value: newFact}}}},
		{StatePatch: &state.StatePatch{RemoveMemory: []string{"earnings"}}},
		{Done: true},
	})
	runner, store, provider := hygieneFixture(t, decider)
	for step := 0; step < 5; step++ {
		fact := oldFact
		if step > 0 {
			fact = newFact
		}
		payload, err := json.Marshal(map[string]string{"fact": fact})
		if err != nil {
			t.Fatal(err)
		}
		provider.Results["read_fact"] = tools.ToolResult{Data: payload}
		result, err := runner.Step(ctx)
		if err != nil || (result.Observation != nil && result.Observation.Error != nil) {
			t.Fatalf("step %d: %+v, %v", step, result, err)
		}
		saved, err := store.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch step {
		case 1:
			if len(saved.WorkingMemory) != 1 || saved.WorkingMemory[0].Value != oldFact {
				t.Fatal("initial fact not saved")
			}
		case 2:
			if len(saved.WorkingMemory) != 1 || saved.WorkingMemory[0].Key != "earnings" || saved.WorkingMemory[0].Value != newFact {
				t.Fatal("stale fact not replaced atomically")
			}
		case 3, 4:
			if len(saved.WorkingMemory) != 0 {
				t.Fatal("removed memory remained")
			}
		}
	}
	if !strings.Contains(string(decider.Inputs[1].Observation.Data), oldFact) || !strings.Contains(string(decider.Inputs[2].Observation.Data), newFact) {
		t.Fatal("observations did not establish both facts")
	}
	if decider.Inputs[3].Observation != nil || decider.Inputs[3].State.WorkingMemory[0].Value != newFact || len(decider.Inputs[4].State.WorkingMemory) != 0 {
		t.Fatal("next decisions did not receive maintained state")
	}
	if len(provider.Calls) != 2 {
		t.Fatal("unexpected tool calls")
	}
}

func TestIntentLifecycleAcrossSteps(t *testing.T) {
	decider := agent.NewFakeAgent([]agent.Decision{
		{StatePatch: &state.StatePatch{AddIntent: []state.Intent{{ID: "inspect-account", Action: "Inspect account"}}}},
		{StatePatch: &state.StatePatch{UpdateIntent: []state.IntentUpdate{{ID: "inspect-account", Executed: true, ExecutedAt: instant}}}},
		{StatePatch: &state.StatePatch{RemoveIntent: []string{"inspect-account"}}},
		{Done: true},
	})
	runner, store, _ := hygieneFixture(t, decider)
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	active, completed, retired := decider.Inputs[1].State.PendingIntents, decider.Inputs[2].State.PendingIntents, decider.Inputs[3].State.PendingIntents
	if len(active) != 1 || active[0].Executed || len(completed) != 1 || !completed[0].Executed || !completed[0].ExecutedAt.Equal(instant) || len(retired) != 0 {
		t.Fatal("intent lifecycle not reflected in subsequent inputs")
	}
	saved, err := store.Load(ctx)
	if err != nil || len(saved.PendingIntents) != 0 {
		t.Fatal("retired intent persisted")
	}
}

func TestRunSpecIsOwnedByRuntime(t *testing.T) {
	spec := agent.RunSpec{Goal: "original goal", Instructions: "original procedure"}
	want := spec
	calls := 0
	decider := agentFunc(func(_ context.Context, input agent.StepInput) (agent.Decision, error) {
		calls++
		if input.Spec != want {
			t.Fatalf("spec changed: %+v", input.Spec)
		}
		input.Spec.Goal = "agent changed goal"
		input.Spec.Instructions = "agent changed procedure"
		if calls == 1 {
			return agent.Decision{StatePatch: &state.StatePatch{UpsertMemory: []state.MemoryEntry{{Key: "progress", Value: "started"}}}}, nil
		}
		return agent.Decision{Done: true}, nil
	})
	runner := runtime.NewRuntime(spec, state.NewMemoryStore(), decider, tools.NewRegistry(), validation.NewValidators())
	spec.Goal, spec.Instructions = "caller changed goal", "caller changed procedure"
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("expected two decisions")
	}
}

func TestMemoryLimitFailureIsRecoverableAndAtomic(t *testing.T) {
	bad := toolDecision(`{"quantity":1}`)
	bad.StatePatch = &state.StatePatch{
		UpsertMemory: []state.MemoryEntry{{Key: "too-large", Value: strings.Repeat("x", state.MaxMemoryEntryBytes)}},
		RemoveIntent: []string{"pending"},
	}
	runner, decider, provider, store, _ := fixture(t, bad, agent.Decision{Done: true})
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(provider.Calls) != 0 || decider.Inputs[1].Observation.Error.Type != "invalid_state_patch" {
		t.Fatal("limit failure was not validated before execution")
	}
	saved, err := store.Load(ctx)
	if err != nil || len(saved.WorkingMemory) != 0 || len(saved.PendingIntents) != 1 {
		t.Fatal("invalid patch partially persisted")
	}
}

// This test exercises real prompt construction with a deterministic fake LLM.
// The recording fake is a test oracle; its request history never feeds the model.
func TestLongRunMaintainsBoundedCurrentContext(t *testing.T) {
	const steps = 150
	responses := make([]llm.CompletionResponse, 0, steps+1)
	for i := 0; i < steps; i++ {
		patch := &state.StatePatch{UpsertMemory: []state.MemoryEntry{{Key: "current-fact", Value: fmt.Sprintf("conclusion-%03d", i)}}}
		id := fmt.Sprintf("intent-%03d", i/3)
		switch i % 3 {
		case 0:
			patch.AddIntent = []state.Intent{{ID: id, Action: "Inspect current fact"}}
		case 1:
			patch.UpdateIntent = []state.IntentUpdate{{ID: id, Executed: true, ExecutedAt: instant}}
		case 2:
			patch.RemoveIntent = []string{id}
		}
		decision := agent.Decision{Summary: fmt.Sprintf("discarded-reasoning-%03d", i), StatePatch: patch, ToolCall: factCall()}
		payload, err := json.Marshal(decision)
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, llm.CompletionResponse{Content: string(payload)})
	}
	responses = append(responses, llm.CompletionResponse{Content: `{"done":true}`})
	client := llm.NewFakeClient(responses)
	runner, store, provider := hygieneFixture(t, agent.NewLLMAgent(client))
	var firstSpec agent.RunSpec
	for i := 0; i <= steps; i++ {
		provider.Results["read_fact"] = tools.ToolResult{Data: json.RawMessage(fmt.Sprintf(`{"fact":"raw-observation-%03d"}`, i))}
		result, err := runner.Step(ctx)
		if err != nil || (result.Observation != nil && result.Observation.Error != nil) {
			t.Fatalf("step %d: %+v, %v", i, result, err)
		}
		if result.Done != (i == steps) {
			t.Fatal("unexpected completion")
		}
		request := client.Requests[i]
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
			t.Fatal("conversation accumulated")
		}
		payload := request.Messages[1].Content
		var input agent.StepInput
		if err := json.Unmarshal([]byte(payload), &input); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstSpec = input.Spec
		}
		if input.Spec != firstSpec {
			t.Fatal("run specification drifted")
		}
		if len(input.Tools) != 1 || input.Tools[0].Name != "fake.read_fact" {
			t.Fatal("tool context lost")
		}
		if strings.Contains(payload, "discarded-reasoning-") {
			t.Fatal("previous decisions replayed")
		}
		if i == 0 {
			if input.Observation != nil || len(input.State.WorkingMemory) != 0 {
				t.Fatal("unexpected initial context")
			}
		} else {
			if input.Observation == nil || input.Observation.Error != nil || string(input.Observation.Data) != fmt.Sprintf(`{"fact":"raw-observation-%03d"}`, i-1) || strings.Count(payload, "raw-observation-") != 1 {
				t.Fatal("observations lost or accumulated")
			}
			if len(input.State.WorkingMemory) != 1 || input.State.WorkingMemory[0].Value != fmt.Sprintf("conclusion-%03d", i-1) || strings.Count(payload, "conclusion-") != 1 {
				t.Fatal("stale facts accumulated")
			}
		}
		if i%3 == 0 {
			if len(input.State.PendingIntents) != 0 {
				t.Fatal("removed intent remained model-visible")
			}
		} else {
			if len(input.State.PendingIntents) != 1 || input.State.PendingIntents[0].ID != fmt.Sprintf("intent-%03d", i/3) || input.State.PendingIntents[0].Executed != (i%3 == 2) {
				t.Fatal("incorrect intent lifecycle")
			}
		}
		if err := state.ValidateWorkingMemory(input.State.WorkingMemory); err != nil {
			t.Fatal(err)
		}
		encodedMemory, err := json.Marshal(input.State.WorkingMemory)
		if err != nil {
			t.Fatal(err)
		}
		// Count/entry policy bounds the memory array even if every slot is used.
		if len(encodedMemory) > state.MaxWorkingMemoryEntries*(state.MaxMemoryEntryBytes+1)+2 {
			t.Fatal("working-memory budget exceeded")
		}
		// This fixed workload has one memory, <=1 intent, one observation, and fixed
		// tools/spec. Metadata grows by a few digits; no history term is allowed.
		if len(payload) > state.MaxMemoryEntryBytes+2048 {
			t.Fatalf("context accumulated: %d bytes at step %d", len(payload), i)
		}
		if request.Messages[0].Content != client.Requests[0].Messages[0].Content {
			t.Fatal("system prompt changed")
		}
	}
	saved, err := store.Load(ctx)
	if err != nil || saved.Version != steps+1 || len(saved.WorkingMemory) != 1 || len(saved.PendingIntents) != 0 || len(provider.Calls) != steps {
		t.Fatalf("long run result: %+v, %v", saved, err)
	}
}

type loadedMemoryStore struct {
	state.Store
	snapshot state.AgentState
}

func (s loadedMemoryStore) Load(context.Context) (*state.AgentState, error) {
	copy := s.snapshot.Clone()
	return &copy, nil
}

func TestInvalidLoadedMemoryNeverReachesAgent(t *testing.T) {
	decider := agent.NewFakeAgent([]agent.Decision{{Done: true}})
	invalid := state.AgentState{WorkingMemory: []state.MemoryEntry{{Key: "huge", Value: strings.Repeat("x", state.MaxMemoryEntryBytes)}}}
	store := loadedMemoryStore{Store: state.NewMemoryStore(), snapshot: invalid}
	runner := runtime.NewRuntime(agent.RunSpec{}, store, decider, tools.NewRegistry(), validation.NewValidators())
	if _, err := runner.Step(ctx); err == nil {
		t.Fatal("invalid stored memory reached the model")
	}
	if len(decider.Inputs) != 0 {
		t.Fatal("agent called with invalid memory")
	}
	saved, err := store.Store.Load(ctx)
	if err != nil || !reflect.DeepEqual(*saved, state.AgentState{}) {
		t.Fatal("invalid load changed storage")
	}
}
