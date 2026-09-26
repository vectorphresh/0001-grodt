package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/agent"
	"github.com/vectorphresh/0001-grodt/internal/llm"
	"github.com/vectorphresh/0001-grodt/internal/observation"
	"github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/tools"
)

func TestFullContextIsRebuiltWithoutConversationHistory(t *testing.T) {
	client := llm.NewFakeClient([]llm.CompletionResponse{{Content: `{"done":true}`}, {Content: `{"done":true}`}})
	decider := agent.NewLLMAgent(client)
	input := agent.StepInput{
		Spec: agent.RunSpec{Goal: "inspect account", Instructions: "Read only"},
		State: state.AgentState{
			World:          state.WorldState{Balances: []state.Balance{{Asset: "USD", Amount: "100"}}},
			WorkingMemory:  []state.MemoryEntry{{Key: "strategy", Value: "wait for confirmation"}},
			PendingIntents: []state.Intent{{ID: "read", Action: "inspect account"}},
		},
		Observation: &observation.Observation{ID: "obs_1", Source: "validator", Tool: "fake.read", Error: &observation.ErrorInfo{Type: "invalid_tool_arguments", Message: "quantity required"}},
		Tools:       []tools.ToolDefinition{{Name: "fake.read", Description: "read account", InputSchema: json.RawMessage(`{"type":"object","required":["quantity"]}`)}},
	}
	for range 2 {
		if _, err := decider.Decide(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	for _, req := range client.Requests {
		if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
			t.Fatalf("messages: %+v", req.Messages)
		}
		var decoded agent.StepInput
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, input) {
			t.Fatalf("context not preserved: %+v", decoded)
		}
	}
}

func TestDecisionDecoding(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		valid         bool
	}{
		{"done", `{"done":true}`, true},
		{"intent with call", `{"intent":"inspect","tool_call":{"name":"fake.read","arguments":{}}}`, true},
		{"upsert only", `{"state_patch":{"upsert_memory":[{"key":"k","value":"v"}]}}`, true},
		{"remove intent only", `{"state_patch":{"remove_intent":["i"]}}`, true},
		{"patch goal", `{"state_patch":{"goal":"new goal"},"done":true}`, false},
		{"patch spec", `{"state_patch":{"spec":{"goal":"new goal"}},"done":true}`, false},
		{"patch instructions", `{"state_patch":{"instructions":"new procedure"},"done":true}`, false},
		{"decision spec", `{"spec":{"goal":"new goal"},"done":true}`, false},
		{"patch only", `{"state_patch":{"add_memory":[{"key":"k","value":"v"}]}}`, true},
		{"malformed", `{"done":`, false},
		{"markdown", "```json\n{}\n```", false},
		{"trailing JSON", `{"done":true}{"done":true}`, false},
		{"unknown field", `{"done":true,"extra":1}`, false},
		{"patch world", `{"done":true,"state_patch":{"world":{"balances":[]}}}`, false},
		{"patch version", `{"done":true,"state_patch":{"version":99}}`, false},
		{"patch updated at", `{"done":true,"state_patch":{"updated_at":"2026-09-25T00:00:00Z"}}`, false},
		{"empty", `{}`, false},
		{"null", `null`, false},
		{"intent only", `{"intent":"inspect"}`, false},
		{"empty patch", `{"state_patch":{}}`, false},
		{"done and call", `{"done":true,"tool_call":{"name":"fake.read","arguments":{}}}`, false},
		{"empty tool", `{"tool_call":{"name":"","arguments":{}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := llm.NewFakeClient([]llm.CompletionResponse{{Content: tc.content}})
			_, err := agent.NewLLMAgent(client).Decide(context.Background(), agent.StepInput{})
			if (err == nil) != tc.valid {
				t.Fatalf("decode: %v", err)
			}
			if !tc.valid {
				var invalid *agent.InvalidDecisionError
				if !errors.As(err, &invalid) {
					t.Fatalf("not recoverable: %v", err)
				}
			}
		})
	}
}

func TestClientFailuresAreFatal(t *testing.T) {
	_, err := agent.NewLLMAgent(llm.NewFakeClient(nil)).Decide(context.Background(), agent.StepInput{})
	var invalid *agent.InvalidDecisionError
	if err == nil || errors.As(err, &invalid) {
		t.Fatalf("transport failure: %v", err)
	}
}
