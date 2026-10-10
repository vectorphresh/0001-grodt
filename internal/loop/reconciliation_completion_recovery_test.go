package loop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/mcp"
	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

// Model judgments are scripted: test the acceptance boundary, never interpret
// rationale prose as authority or claim to test real model reasoning.
func TestReconciliationContradictoryCompletion007008(t *testing.T) {
	ctx := context.Background()
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "account", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"equity":"72739.14"}`), Module: &eventModule{}}}, false)
	plan := runstate.Plan{Description: "Calculate target and research", Status: "active", Steps: []runstate.Step{{ID: "step_1", Description: "Analyze the current Alpaca account balance and determine the target increase amount.", Status: "pending"}, {ID: "step_2", Description: "Research cryptocurrencies", Status: "pending"}}}
	if err := p.Store.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	before := p.Store.Snapshot().Tasks
	proposals, evaluations := 0, 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		proposals++
		var input reconciliationInput
		_ = json.Unmarshal(raw, &input)
		if !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
			t.Fatal("rejected evaluation changed accepted state")
		}
		if proposals == 2 {
			for _, s := range []string{"Rejected proposal (correction data", "must be revalidated", "Do not copy evaluator rationale", "3,636.96"} {
				if !strings.Contains(instructions, s) {
					t.Fatalf("missing correction context %q", s)
				}
			}
		}
		step := plan.Steps[0]
		step.Status = "satisfied"
		step.Outcome = "Accepted equity $72,739.14 × 0.05 = $3,636.96"
		step.Evidence = []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, "step_2"), nil
	}}
	p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		var observation struct {
			Updates []runstate.Step `json:"proposed_updates"`
			Claims  []string        `json:"new_satisfaction_claim_ids"`
		}
		_ = json.Unmarshal(raw, &observation)
		if len(observation.Claims) != 1 || observation.Claims[0] != "step_1" || observation.Updates[0].Status != "satisfied" || len(observation.Updates[0].InformationGaps) != 0 {
			t.Fatal("007 rejection downgraded candidate before 008")
		}
		for _, s := range []string{"not completion of pending later steps", "Verify actor-authored derivations", "specific substantive unresolved requirement"} {
			if !strings.Contains(instructions, s) {
				t.Fatalf("missing evaluator rule %q", s)
			}
		}
		if evaluations == 1 {
			return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":true,"purpose_preserved":true,"satisfied":false,"rationale":"The evaluation confirms that the first step is satisfied. The second step remains pending."}`)}, nil
		}
		return continuityResult(true, "Calculation verified from accepted equity; later work remains pending."), nil
	}}
	if err := p.reconcile(ctx, p.Store.Snapshot(), "Calculate the 5% target", nil); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if proposals != 2 || evaluations != 2 || active.Plan.Steps[0].Status != "satisfied" || len(active.Plan.Steps[0].InformationGaps) != 0 || active.Plan.CurrentStep != "step_2" {
		t.Fatal("completion was not revalidated and persisted consistently")
	}
}

func TestReconciliationPartialRequiresSubstantiveGapCorrection(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	calls := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		var input reconciliationInput
		_ = json.Unmarshal(raw, &input)
		step := input.Plan.Steps[0]
		step.Status = "partial"
		step.Outcome = "Configuration acquired"
		step.Evidence = []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}
		if calls == 2 {
			step.InformationGaps = []string{"Assessment of configuration remains unfinished"}
			if !strings.Contains(instructions, "Rejected proposal (correction data") {
				t.Fatal("partial candidate lost")
			}
		}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, ""), nil
	}}
	evaluations := 0
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		return continuityResult(evaluations > 1, "Partial requires a substantive unresolved assessment requirement."), nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Assess configuration", nil); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if active.Plan.Steps[0].Status != "partial" || len(active.Plan.Steps[0].InformationGaps) != 1 || active.Plan.Steps[0].InformationGaps[0] != "Assessment of configuration remains unfinished" {
		t.Fatal("procedural feedback became a gap")
	}
}

func TestReconciliationProceduralRejectionPreservesCompletionCandidate(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	before := p.Store.Snapshot().Tasks
	calls := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
			t.Fatal("procedural rejection committed")
		}
		if calls == 2 && !strings.Contains(instructions, "Rejected proposal (correction data") {
			t.Fatal("completion candidate missing")
		}
		var input reconciliationInput
		_ = json.Unmarshal(raw, &input)
		step := input.Plan.Steps[0]
		step.Status = "satisfied"
		step.Outcome = "Configuration inspection complete"
		step.Evidence = []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, ""), nil
	}}
	evaluations := 0
	p.Evaluator = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		if !strings.Contains(string(raw), `"status":"satisfied"`) {
			t.Fatal("supported completion downgraded")
		}
		return openai.JSONResult{JSON: mustEncode(map[string]any{"continuity_valid": evaluations > 1, "purpose_preserved": true, "satisfied": true, "rationale": "Repair procedural representation; completion supported."})}, nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if calls != 2 || evaluations != 2 || active.Plan.Steps[0].Status != "satisfied" || len(active.Plan.Steps[0].InformationGaps) != 0 {
		t.Fatal("procedural rejection damaged completion")
	}
}

func TestReconciliationRejectedCompletionExhaustionIsAtomic(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	before := p.Store.Snapshot().Tasks
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		var input reconciliationInput
		_ = json.Unmarshal(raw, &input)
		step := input.Plan.Steps[0]
		step.Status = "satisfied"
		step.Outcome = "Inspection complete"
		step.Evidence = []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, ""), nil
	}}
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":true,"purpose_preserved":true,"satisfied":false,"rationale":"The step is satisfied."}`)}, nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); !errors.Is(err, errContinuityRejected) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
		t.Fatal("rationale was committed or completion inferred from prose")
	}
}

func TestResultAccountingDuplicateRepairAndSharedEvidence(t *testing.T) {
	input := reconciliationInput{AcceptedResults: []acceptedExecutionResult{{ResultID: "result", Action: "read"}}, Plan: &runstate.Plan{Steps: []runstate.Step{{ID: "a"}, {ID: "b"}}}}
	evidence := []runstate.EvidenceReference{{Partition: "shared", Version: 1}}
	d := reconciliationDecision{Action: "progress", Reason: "Both steps progressed from one result", Updates: []progressUpdate{{Step: runstate.Step{ID: "a", Status: "satisfied", Outcome: "A complete", Evidence: evidence}}, {Step: runstate.Step{ID: "b", Status: "partial", Outcome: "B progressed", Evidence: evidence, InformationGaps: []string{"B assessment unfinished"}}}}, ResultAccounting: []resultAccounting{{ResultID: "result", StepID: "a", Disposition: "update", Reason: "A acquired"}, {ResultID: "result", StepID: "b", Disposition: "update", Reason: "B acquired"}}}
	err := validateResultAccounting(input, d)
	if err == nil {
		t.Fatal("duplicate accepted")
	}
	feedback := reconciliationHostCorrection(err, mustEncode(d))
	if !strings.Contains(feedback, "Retain other supported step updates") || !strings.Contains(feedback, "multiple steps may cite the same evidence") {
		t.Fatal("duplicate repair loses progress")
	}
	d.ResultAccounting = d.ResultAccounting[:1]
	if err := validateResultAccounting(input, d); err != nil {
		t.Fatal(err)
	}
	if len(d.Updates) != 2 || d.Updates[0].Step.Status != "satisfied" {
		t.Fatal("accounting repair lost independent updates")
	}
}

func TestEmptyAcceptedResultsAfterRejectedTool(t *testing.T) {
	p, fixture := toolsHarness(t, []runstate.Definition{{Name: "observations", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{}`), Module: &eventModule{}}}, false)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	fixture.Call = func(*http.Request, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"content":[{"type":"text","text":"` + strings.Repeat("x", mcp.MaxResultBytes) + `"}]}`), nil
	}
	actors := 0
	p.Client = nativeClient(func(_ context.Context, request toolcall.Request) (toolcall.Response, error) {
		actors++
		if actors == 1 {
			return toolcall.Response{Calls: []toolcall.Call{toolRequest(request, "oversized")}}, nil
		}
		return toolcall.Response{Text: "The failed observation remains unknown."}, nil
	})
	calls := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		var input reconciliationInput
		_ = json.Unmarshal(raw, &input)
		if len(input.AcceptedResults) != 0 || len(input.ActionFailures) != 1 || input.ActionFailures[0].Failure.Category != "result_too_large" {
			t.Fatal("rejected tool result was accepted or failure provenance lost")
		}
		result := reconciliationResult(t, "no_progress", nil, "")
		if calls == 1 {
			var wire map[string]any
			_ = json.Unmarshal(result.JSON, &wire)
			wire["result_accounting"] = []resultAccounting{{ResultID: "inspect", StepID: "inspect", Disposition: "no_progress", Reason: "Tool failed"}}
			result.JSON = mustEncode(wire)
		} else if !strings.Contains(instructions, "accepted_results is empty. Return result_accounting=[]") {
			t.Fatal("empty accounting repair not delivered")
		}
		return result, nil
	}}
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return continuityResult(true, "Failed acquisition leaves inspection unresolved."), nil
	}}
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect configuration"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || fixture.Calls.Load() != 1 {
		t.Fatal("invalid accounting not corrected after rejected tool result")
	}
}

func TestSemanticCandidateFeedbackBounded(t *testing.T) {
	feedback := reconciliationSemanticCorrection(errors.New(strings.Repeat("界", 1000)), json.RawMessage(strings.Repeat("x", maxReconciliationOutput)))
	if len(feedback) > maxReconciliationValidationFeedback || !strings.Contains(feedback, "Rejected proposal omitted") || !strings.Contains(feedback, "must be revalidated") {
		t.Fatal("semantic correction budget or omissions invalid")
	}
}
