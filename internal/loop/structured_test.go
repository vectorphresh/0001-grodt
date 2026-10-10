package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/stateflow"
)

type structuredClient struct {
	openai.Client
	capture func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error)
	call    func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error)
}

func (c structuredClient) PromptWithSpecification(ctx context.Context, i, p string, input json.RawMessage, s openai.JSONSpecification) (openai.JSONResult, error) {
	if s.Name == "actor_plan_capture" {
		if c.capture != nil {
			return c.capture(ctx, i, p, input, s)
		}
		// Legacy fixtures model no new actor-declared plan at acquisition boundaries.
		return openai.JSONResult{JSON: json.RawMessage(`{"capture_required":false,"rationale":"No additional declared plan in this fixture."}`)}, nil
	}
	return c.call(ctx, i, p, input, s)
}
func TestStructuredProviderAdmissionBoundary(t *testing.T) {
	for _, scenario := range []string{"corrected", "malformed", "exhausted", "transport"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := runstate.New(context.Background(), nil, runstate.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close(context.Background())
			calls := 0
			schema := json.RawMessage(`{"type":"object","properties":{"action":{"const":"explore"}},"required":["action"],"additionalProperties":false}`)
			raw := structuredClient{call: func(ctx context.Context, i, p string, input json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
				calls++
				if p != "prompt" || !strings.Contains(string(input), `"objective":"goal"`) || !strings.Contains(string(input), `"prior context"`) || !strings.Contains(i, "Current actionable GRODT state") {
					t.Fatal("missing original inputs/state")
				}
				if calls > 1 && !strings.Contains(i, "Structural validation feedback") {
					t.Fatal("missing correction feedback")
				}
				switch scenario {
				case "transport":
					return openai.JSONResult{}, errors.New("failure")
				case "malformed":
					return openai.JSONResult{JSON: json.RawMessage(`{`)}, nil
				case "corrected":
					if calls == 2 {
						return openai.JSONResult{JSON: json.RawMessage(`{"action":"explore"}`)}, nil
					}
				}
				return openai.JSONResult{JSON: json.RawMessage(`{"invalid":"rejected candidate"}`)}, nil
			}}
			client := &stateflow.Client{Client: raw, Store: store}
			provider := NewStructuredProvider(client, "instructions", openai.JSONSpecification{Name: "action", Schema: schema, Strict: true})
			// Construction must own a copy of the contract.
			schema[0] = '!'
			state := &State{Objective: "goal", Prompt: "prompt", Context: []string{"prior context"}}
			handled, err := provider.Handle(context.Background(), state)
			wantCalls := 1
			if scenario == "corrected" {
				wantCalls = 2
			}
			if scenario == "exhausted" {
				wantCalls = 5
			}
			if calls != wantCalls || handled != (scenario == "corrected") || (err == nil) != (scenario == "corrected") {
				t.Fatalf("calls=%d handled=%v err=%v", calls, handled, err)
			}
			events := []runstate.Event{}
			for _, entry := range store.Journal() {
				if entry.Kind == "event" {
					var e runstate.Event
					json.Unmarshal(entry.Data, &e)
					events = append(events, e)
				}
			}
			if len(events) != 1 || strings.Contains(string(events[0].Payload), "rejected candidate") {
				t.Fatal("intermediate candidate admitted")
			}
			if scenario == "corrected" {
				if state.Response != `{"action":"explore"}` || !strings.Contains(string(events[0].Payload), `"status":"accepted"`) {
					t.Fatal("missing accepted action")
				}
			} else if state.Response != "" || !strings.Contains(string(events[0].Payload), `"status":"error"`) {
				t.Fatal("terminal failure admitted a response")
			}
		})
	}
}

func TestStructuredProviderCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	raw := structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		cancel()
		return openai.JSONResult{JSON: json.RawMessage(`{}`)}, nil
	}}
	p := NewStructuredProvider(raw, "instructions", openai.JSONSpecification{Schema: json.RawMessage(`true`)})
	s := &State{Prompt: "prompt"}
	if ok, err := p.Handle(ctx, s); ok || !errors.Is(err, context.Canceled) || s.Response != "" {
		t.Fatal("cancelled action published")
	}
}
