package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/mcp"
	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func TestInvocationRecoveryNarrowedReadAndPurpose(t *testing.T) {
	module := &eventModule{fn: func(e runstate.Event) json.RawMessage {
		if e.Source.Kind == "mcp" {
			return json.RawMessage(`{"status":"mutation","replace":{"items":[]}}`)
		}
		return json.RawMessage(`{"status":"ignored"}`)
	}}
	p, fixture := toolsHarness(t, []runstate.Definition{{Name: "observations", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{}`), Module: module}}, false)
	fixture.Call = func(_ *http.Request, _ string, args json.RawMessage) (json.RawMessage, error) {
		if strings.Contains(string(args), "narrow") {
			return json.RawMessage(`{"content":[],"structuredContent":{"items":[]},"isError":false}`), nil
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"` + strings.Repeat("x", mcp.MaxResultBytes) + `"}]}`), nil
	}
	focus := "Evaluate available observations"
	if err := p.Store.RevisePlan(context.Background(), runstate.Plan{Description: "Inspect", Status: "active", UnresolvedFocus: focus, Steps: []runstate.Step{{ID: "inspect", Description: "Inspect observations", Status: "active"}}}); err != nil {
		t.Fatal(err)
	}
	actor := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		actor++
		if actor == 2 {
			var feedback struct {
				Status, Category, Execution string
				ResultAvailable             bool `json:"result_available"`
				Details                     map[string]int
			}
			if err := json.Unmarshal([]byte(r.Messages[2].Text), &feedback); err != nil {
				t.Fatal(err)
			}
			if feedback.Status != "failed" || feedback.Category != "result_too_large" || feedback.Execution != "result_rejected" || feedback.ResultAvailable || feedback.Details["limit_bytes"] != mcp.MaxResultBytes {
				t.Fatal("missing quantitative failure feedback")
			}
			if strings.Contains(string(p.Store.ModelJSON()), "result_too_large") {
				t.Fatal("synthetic failure admitted as knowledge")
			}
			c := toolRequest(r, "narrow")
			c.Arguments = json.RawMessage(`{"key":"narrow"}`)
			return toolcall.Response{Calls: []toolcall.Call{c}}, nil
		}
		if actor == 3 {
			return toolcall.Response{Text: "Inspection returned zero matches; evaluation remains unresolved."}, nil
		}
		return toolcall.Response{Text: "Acquire observations to evaluate available observations.", Calls: []toolcall.Call{toolRequest(r, "wide")}}, nil
	})
	reconciliations := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if reconciliations == 0 && (len(input.ActionFailures) != 1 || input.ActionFailures[0].Failure.Category != "result_too_large" || len(input.AcceptedEvidence) == 0) {
			t.Fatal("failed acquisition provenance missing from reconciliation")
		}
		if reconciliations == 1 && len(input.ActionFailures) != 0 {
			t.Fatal("historical failure presented as recent")
		}
		reconciliations++
		return reconciliationResult(t, "no_progress", nil, "", input), nil
	}}
	p.Evaluator = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		if !strings.Contains(string(raw), focus) {
			t.Fatal("failed acquisition erased actor purpose")
		}
		return continuityResult(true, "Acquisition does not resolve the retained evaluation purpose."), nil
	}}
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
		t.Fatal(err)
	}
	if actor != 3 || fixture.Calls.Load() != 2 || reconciliations != 2 {
		t.Fatal("recovery did not return to actor once per boundary")
	}
	active, _ := p.Store.Active()
	if active.Plan.UnresolvedFocus != focus {
		t.Fatal("purpose lost")
	}
}

func TestInvocationRecoveryPartialBatchAndUnknownOutcome(t *testing.T) {
	module := &eventModule{fn: func(e runstate.Event) json.RawMessage {
		if e.Source.Kind == "mcp" {
			return json.RawMessage(`{"status":"mutation","replace":{"accepted_first_observation":true}}`)
		}
		return json.RawMessage(`{"status":"ignored"}`)
	}}
	p, fixture := toolsExecutionHarness(t, []runstate.Definition{{Name: "observations", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{}`), Module: module}}, false)
	fixture.Call = func(_ *http.Request, _ string, args json.RawMessage) (json.RawMessage, error) {
		if strings.Contains(string(args), "fail") {
			return nil, errors.New("private-remote-message")
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"accepted first observation"}]}`), nil
	}
	actor := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		actor++
		if actor == 2 {
			if len(r.Messages) != 5 || !strings.Contains(r.Messages[2].Text, "accepted first observation") || !strings.Contains(r.Messages[3].Text, `"execution":"outcome_unknown"`) || !strings.Contains(r.Messages[4].Text, `"execution":"not_dispatched"`) {
				t.Fatal("partial batch feedback incomplete")
			}
			if strings.Contains(fmt.Sprint(r.Messages), "private-remote-message") || !strings.Contains(r.Instructions, "verify authoritative state") {
				t.Fatal("unsafe retry guidance")
			}
			return toolcall.Response{Text: "No further action; uncertain outcome needs verification."}, nil
		}
		var calls []toolcall.Call
		for _, key := range []string{"ok", "fail", "skipped"} {
			c := toolRequest(r, key)
			c.Arguments = mustEncode(map[string]string{"key": key})
			calls = append(calls, c)
		}
		return toolcall.Response{Calls: calls}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
		t.Fatal(err)
	}
	if actor != 2 || fixture.Calls.Load() != 2 {
		t.Fatal("unknown operation replayed or skipped sibling dispatched")
	}
	if !strings.Contains(string(p.Store.Snapshot().Knowledge["observations"].Value), "accepted_first_observation") {
		t.Fatal("partial-batch accepted evidence lost")
	}
	admitted := 0
	for _, event := range module.events {
		if event.Source.Kind == "mcp" {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatal("failure envelopes admitted as domain observations")
	}
	statuses := map[string]string{}
	for _, task := range p.Store.Snapshot().Tasks.Records {
		if task.AgentWork != nil {
			statuses[task.AgentWork.CallID] = task.Status
			if task.Result == "" {
				t.Fatal("invocation provenance missing")
			}
		}
	}
	if statuses["ok"] != "completed" || statuses["fail"] != "failed" || statuses["skipped"] != "failed" {
		t.Fatal("partial batch provenance incorrect")
	}
}

func TestInvocationRecoveryCountsActorOpportunitiesAndResets(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprint(reset), func(t *testing.T) {
			p, fixture := toolsExecutionHarness(t, nil, false)
			fixture.Call = func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
				if reset && fixture.Calls.Load() == 2 {
					return json.RawMessage(`{"content":[],"isError":true}`), nil
				}
				return nil, errors.New("failure")
			}
			actor := 0
			p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
				actor++
				if reset && actor == 5 {
					return toolcall.Response{Text: "Incomplete."}, nil
				}
				// Eight batch members produce only one failure-feedback opportunity.
				var calls []toolcall.Call
				batchSize := 8
				if reset && actor == 2 {
					batchSize = 1
				}
				for i := 0; i < batchSize; i++ {
					calls = append(calls, toolRequest(r, fmt.Sprintf("%d-%d", actor, i)))
				}
				return toolcall.Response{Calls: calls}, nil
			})
			_, err := p.Handle(context.Background(), &State{Objective: "Inspect"})
			if reset {
				if err != nil || actor != 5 {
					t.Fatalf("valid response did not reset recovery: %v actor=%d", err, actor)
				}
			} else if err == nil || actor != 3 || fixture.Calls.Load() != 3 {
				t.Fatalf("not two actual actor opportunities: %v actor=%d calls=%d", err, actor, fixture.Calls.Load())
			}
		})
	}
}

type recoveryGenerationFailure struct{}

func (recoveryGenerationFailure) Error() string           { return "temporary generation failure" }
func (recoveryGenerationFailure) FailureFeedback() string { return "Correct the model request." }

func TestRecoveryOpportunitiesAcrossGenerationFailureAndPlanning(t *testing.T) {
	for _, planning := range []bool{false, true} {
		t.Run(fmt.Sprint(planning), func(t *testing.T) {
			p, fixture := toolsExecutionHarness(t, nil, false)
			fixture.Call = func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
				return nil, errors.New("private-server-error")
			}
			generations := 0
			p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
				generations++
				if generations == 1 {
					return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "first")}}, nil
				}
				if planning {
					if generations == 2 {
						active, _ := p.Store.Active()
						return planCall(t, "plan", planCommand{Action: "revise", Plan: active.Plan}), nil
					}
					if generations != 3 || !strings.Contains(r.Instructions, "Latest invocation failure (host data):") || !strings.Contains(r.Instructions, "tool_protocol_or_transport_failure") {
						t.Fatal("failure detail lost after planning response")
					}
				} else {
					if generations == 2 || generations == 3 {
						return toolcall.Response{}, recoveryGenerationFailure{}
					}
					if generations == 4 {
						return toolcall.Response{Calls: []toolcall.Call{toolRequest(r, "second")}}, nil
					}
					if generations != 5 {
						t.Fatal("generation errors consumed actor recovery opportunities")
					}
				}
				return toolcall.Response{Text: "Incomplete; outcome remains unknown."}, nil
			})
			if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
				t.Fatal(err)
			}
			expected := int64(2)
			if planning {
				expected = 1
			}
			if fixture.Calls.Load() != expected {
				t.Fatal("automatic external replay")
			}
		})
	}
}
