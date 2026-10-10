package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

type purposeEvaluationInput struct {
	Context reconciliationInput `json:"context"`
	Focus   string              `json:"proposed_unresolved_focus"`
	Updates []runstate.Step     `json:"proposed_updates"`
}

func checkPurposeInstructions(t *testing.T, instructions string) {
	t.Helper()
	for _, rule := range []string{"higher-level purpose across the tool boundary", "existing plan or current", "unless accepted evidence supports", "comprehensive procedural coverage"} {
		if !strings.Contains(instructions, rule) {
			t.Fatalf("missing purpose rule: %s", rule)
		}
	}
}

func purposeFixture(t *testing.T, data json.RawMessage) *ToolProvider {
	t.Helper()
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "observations", Schema: json.RawMessage(`{"type":"object"}`), Initial: data, Module: &eventModule{}}}, false)
	return p
}

func TestReconciliationPurposeEstablishmentAndPreservation(t *testing.T) {
	for _, mode := range []string{"missing_no_progress", "erased", "acquisition_only"} {
		t.Run(mode, func(t *testing.T) {
			p := purposeFixture(t, json.RawMessage(`{"quotes":{"A":{"bid":10,"ask":11}},"isError":false}`))
			purpose := "Evaluate available opportunities; assess execution constraints"
			if mode == "missing_no_progress" {
				seedProcedure(t, p, "assess", "Assess execution constraints")
			}
			if mode != "missing_no_progress" {
				plan := runstate.Plan{Description: "Actor intent", Status: "active", UnresolvedFocus: purpose, Steps: []runstate.Step{{ID: "assess", Description: "Assess execution constraints", Status: "active"}}, CurrentStep: "assess"}
				if err := p.Store.RevisePlan(context.Background(), plan); err != nil {
					t.Fatal(err)
				}
			}
			before := p.Store.Snapshot()
			calls, evaluations := 0, 0
			p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				checkPurposeInstructions(t, instructions)
				calls++
				var input reconciliationInput
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				if input.ActorIntent != "Get quotes to evaluate available opportunities and assess execution constraints." {
					t.Fatal("actor-authored purpose lost at boundary")
				}
				if calls == 1 {
					if mode == "missing_no_progress" {
						return reconciliationResult(t, "no_progress", nil, "", input), nil
					}
					focus := ""
					if mode == "acquisition_only" {
						focus = "Obtain quotes"
					}
					return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, ""), &focus), nil
				}
				if !strings.Contains(instructions, "Preserve the stated opportunity evaluation and execution-constraint purpose") {
					t.Fatal("purpose-specific feedback missing")
				}
				return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, "", input), &purpose), nil
			}}
			p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				checkPurposeInstructions(t, instructions)
				evaluations++
				var input purposeEvaluationInput
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				if input.Focus != purpose {
					return continuityResult(false, "Preserve the stated opportunity evaluation and execution-constraint purpose; acquiring quotes did not perform that evaluation."), nil
				}
				return continuityResult(true, "The actor-authored purpose remains unresolved after acquisition."), nil
			}}
			if err := p.reconcile(context.Background(), before, "Get quotes to evaluate available opportunities and assess execution constraints.", nil); err != nil {
				t.Fatal(err)
			}
			active, _ := p.Store.Active()
			if active.Plan == nil || active.Plan.UnresolvedFocus != purpose || calls != 2 || evaluations != 2 {
				t.Fatal("unresolved purpose was not established or preserved")
			}
			if !reflect.DeepEqual(before.Knowledge, p.Store.Snapshot().Knowledge) {
				t.Fatal("focus correction changed evidence")
			}
			if mode == "missing_no_progress" {
				if len(active.Plan.Steps) != 1 || active.Plan.CurrentStep != "assess" {
					t.Fatal("focus establishment generated a workflow")
				}
			} else {
				old := before.Tasks.Records[active.ID].Plan
				expected := *old
				if !reflect.DeepEqual(active.Plan, &expected) {
					t.Fatal("purpose preservation changed actor-authored plan")
				}
			}
		})
	}
}

func TestReconciliationPurposeResolutionRequiresEvidence(t *testing.T) {
	for _, mode := range []string{"acquisition_only", "empty", "failed", "missing", "partial", "resolved"} {
		t.Run(mode, func(t *testing.T) {
			data := `{"quotes":{"A":{"bid":10,"ask":11}},"isError":false}`
			if mode == "empty" {
				data = `{"items":[],"isError":false}`
			}
			if mode == "failed" {
				data = `{"items":[],"isError":true}`
			}
			if mode == "missing" {
				data = `{"isError":false}`
			}
			if mode == "partial" {
				data = `{"items":[],"isError":false,"large":"` + strings.Repeat("x", maxReconciliationInput*2) + `"}`
			}
			if mode == "resolved" {
				data = `{"assessment":{"complete":true,"outcome":"Available opportunities evaluated"},"isError":false}`
			}
			p := purposeFixture(t, json.RawMessage(data))
			purpose := "Evaluate available opportunities"
			if err := p.Store.RevisePlan(context.Background(), runstate.Plan{Description: "Actor intent", Status: "active", UnresolvedFocus: purpose, Steps: []runstate.Step{{ID: "assess", Description: purpose, Status: "pending"}}}); err != nil {
				t.Fatal(err)
			}
			before := p.Store.Snapshot()
			calls := 0
			p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				empty := ""
				return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, ""), &empty), nil
			}}
			p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				checkPurposeInstructions(t, instructions)
				calls++
				var input purposeEvaluationInput
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				if input.Context.Plan == nil || input.Context.Plan.UnresolvedFocus != purpose {
					t.Fatal("previous purpose missing from evaluation")
				}
				if mode == "partial" && (!input.Context.EvidenceTruncated || input.Context.StateCoverage[0].Coverage != "partial") {
					t.Fatal("partial coverage not disclosed")
				}
				// The scripted evaluator's decision must be accompanied by the actual accepted
				// assessment, rather than inferred from successful acquisition or empty results.
				supported := strings.Contains(string(mustEncode(input.Context.AcceptedEvidence)), `"complete":true`)
				if supported != (mode == "resolved") {
					t.Fatal("resolution evidence missing or invented")
				}
				return continuityResult(supported, "Acquisition alone does not establish purpose resolution; accepted completed assessment does."), nil
			}}
			err := p.reconcile(context.Background(), before, "", nil)
			active, _ := p.Store.Active()
			if mode == "resolved" {
				if err != nil || calls != 1 || active.Plan.UnresolvedFocus != "" || active.Plan.Revision != 2 {
					t.Fatalf("supported resolution rejected: %v", err)
				}
			} else if !errors.Is(err, errContinuityRejected) || calls != maxReconciliationAttempts || !reflect.DeepEqual(before.Tasks, p.Store.Snapshot().Tasks) {
				t.Fatalf("unsupported purpose resolution accepted: %v evaluations=%d", err, calls)
			}
		})
	}
}

func TestReconciliationPurposeNotInferredFromBroadObjective(t *testing.T) {
	for _, invent := range []bool{false, true} {
		t.Run(fmt.Sprint(invent), func(t *testing.T) {
			p := purposeFixture(t, json.RawMessage(`{"quotes":{"A":{"bid":10,"ask":11}},"isError":false}`))
			seedProcedure(t, p, "quotes", "Inspect quotes")
			p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				if !invent {
					return reconciliationResult(t, "no_progress", nil, ""), nil
				}
				focus := "Buy instrument A using a momentum strategy"
				return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, ""), &focus), nil
			}}
			evaluations := 0
			p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				checkPurposeInstructions(t, instructions)
				evaluations++
				var input purposeEvaluationInput
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				if input.Context.ActorIntent != "Get quotes." || input.Context.Plan == nil {
					t.Fatal("unexpected higher-level purpose")
				}
				return continuityResult(input.Focus == "", "No explicitly authored higher-level purpose; preserve no focus and reject invented strategy."), nil
			}}
			err := p.reconcile(context.Background(), p.Store.Snapshot(), "Get quotes.", nil)
			if invent {
				if !errors.Is(err, errContinuityRejected) || evaluations != maxReconciliationAttempts {
					t.Fatal("invented purpose was accepted")
				}
			} else if err != nil || evaluations != 1 {
				t.Fatalf("acquisition without stated purpose required invented focus: %v", err)
			}
			if active, _ := p.Store.Active(); active.Plan == nil || active.Plan.UnresolvedFocus != "" {
				t.Fatal("no-purpose acquisition created procedural state")
			}
		})
	}
}

func TestReconciliationPurposeAcrossAccountQuotesOrdersSetupChain(t *testing.T) {
	ctx := context.Background()
	accumulated := map[string]json.RawMessage{}
	module := &eventModule{fn: func(e runstate.Event) json.RawMessage {
		if e.Source.Kind != "mcp" {
			return json.RawMessage(`{"status":"ignored"}`)
		}
		var event struct {
			Result struct {
				StructuredContent struct {
					Kind  string          `json:"kind"`
					Value json.RawMessage `json:"value"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(e.Payload, &event); err != nil {
			t.Fatal(err)
		}
		acquired := event.Result.StructuredContent
		accumulated[acquired.Kind] = acquired.Value
		return mustEncode(map[string]any{"status": "mutation", "replace": accumulated})
	}}
	p, fixture := toolsHarness(t, []runstate.Definition{{Name: "observations", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{}`), Module: module}}, false)
	fixture.Call = func(_ *http.Request, _ string, args json.RawMessage) (json.RawMessage, error) {
		var query struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(args, &query); err != nil {
			return nil, err
		}
		value := json.RawMessage(`{"equity":"72741.86"}`)
		if query.Key == "quotes" {
			value = json.RawMessage(`{"A":{"bid":10,"ask":11}}`)
		}
		if query.Key == "orders" {
			value = json.RawMessage(`[]`)
		}
		return mustEncode(map[string]any{"content": []any{}, "structuredContent": map[string]any{"kind": query.Key, "value": value}, "isError": false}), nil
	}
	purpose := "Evaluate available opportunities"
	if err := p.Store.RevisePlan(ctx, runstate.Plan{Description: "Inspect available observations", Status: "active", Steps: []runstate.Step{{ID: "inspect", Description: "Inspect observations", Status: "active"}}}); err != nil {
		t.Fatal(err)
	}
	acquisitions := []string{"account", "quotes", "orders", "account"}
	prose := []string{
		"Get account information to evaluate available opportunities.",
		"Get quotes to evaluate available opportunities.",
		"Inspect existing open orders.",
		"",
	}
	actorTurns, reconciliations, evaluations := 0, 0, 0
	firstRejected := false
	p.Client = nativeClient(func(_ context.Context, request toolcall.Request) (toolcall.Response, error) {
		turn := actorTurns
		actorTurns++
		if turn > 0 {
			active, _ := p.Store.Active()
			if active.Plan == nil || active.Plan.UnresolvedFocus != purpose || !strings.Contains(request.Instructions, `"unresolved_focus":"`+purpose+`"`) {
				t.Fatal("purpose did not survive into next actor request")
			}
			if len(active.Plan.Steps) != 1 || active.Plan.CurrentStep != "inspect" || active.Plan.Revision != 2 {
				t.Fatal("successive acquisitions generated next actions or duplicate procedural state")
			}
		}
		if turn == len(acquisitions) {
			return toolcall.Response{Text: "Evidence gathering finished; evaluation remains unresolved."}, nil
		}
		return toolcall.Response{Text: prose[turn], Calls: []toolcall.Call{{ID: fmt.Sprintf("acquire-%d", turn), Name: request.Tools[0].Name, Arguments: mustEncode(map[string]string{"key": acquisitions[turn]})}}}, nil
	})
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		checkPurposeInstructions(t, instructions)
		reconciliations++
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if input.ActorIntent != prose[actorTurns-1] {
			t.Fatal("actor intent was not passed through actual tool boundary")
		}
		if input.Plan == nil || input.Plan.UnresolvedFocus == "" {
			if !firstRejected {
				firstRejected = true
				return reconciliationResult(t, "no_progress", nil, "", input), nil
			}
			if !strings.Contains(instructions, "Acquisition did not evaluate opportunities") {
				t.Fatal("missing-purpose correction absent")
			}
			return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, "", input), &purpose), nil
		}
		return reconciliationResult(t, "no_progress", nil, "", input), nil
	}}
	p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		checkPurposeInstructions(t, instructions)
		evaluations++
		var input purposeEvaluationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if len(input.Updates) != 0 {
			t.Fatal("acquisition chain prescribed a procedural workflow")
		}
		if input.Focus != purpose {
			return continuityResult(false, "Acquisition did not evaluate opportunities; preserve that explicitly actor-authored purpose."), nil
		}
		if actorTurns >= 3 && !strings.Contains(string(mustEncode(input.Context.AcceptedEvidence)), `"orders":[]`) {
			t.Fatal("empty order inspection was lost")
		}
		return continuityResult(true, "Acquired observations do not resolve the actor-authored evaluation purpose."), nil
	}}
	if _, err := p.Handle(ctx, &State{Objective: "Inspect available observations", Prompt: "request"}); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if actorTurns != 5 || reconciliations != 5 || evaluations != 5 || fixture.Calls.Load() != 4 || active.Plan.UnresolvedFocus != purpose {
		t.Fatal("chain failed to preserve purpose through all acquisition boundaries")
	}
	if len(p.Store.Snapshot().Tasks.Records) != 5 {
		t.Fatal("focus correction created extra tasks")
	}
}
