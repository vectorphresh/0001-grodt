package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

func withReconciliationFocus(t *testing.T, result openai.JSONResult, focus *string) openai.JSONResult {
	t.Helper()
	var wire map[string]any
	if err := json.Unmarshal(result.JSON, &wire); err != nil {
		t.Fatal(err)
	}
	wire["unresolved_focus"] = focus
	result.JSON = mustJSON(t, wire)
	return result
}

func TestStateAwareReconciliationAccumulatedEvidenceAndFocus(t *testing.T) {
	ctx := context.Background()
	module := &eventModule{fn: func(e runstate.Event) json.RawMessage {
		switch e.Source.ID {
		case "first":
			return json.RawMessage(`{"status":"mutation","replace":{"capacity":100}}`)
		case "second":
			return json.RawMessage(`{"status":"mutation","replace":{"capacity":100,"conflicts":[]}}`)
		default:
			return json.RawMessage(`{"status":"mutation","replace":{"capacity":100,"conflicts":[],"observations":{"price":42}}}`)
		}
	}}
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "world", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{}`), Module: module}}, false)
	seedProcedure(t, p, "setup", "Initial observations gathered")
	for _, id := range []string{"first", "second"} {
		if _, err := p.Store.Admit(ctx, runstate.Source{Kind: "mcp", ID: id}, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	before := p.Store.Snapshot()
	if _, err := p.Store.Admit(ctx, runstate.Source{Kind: "mcp", ID: "third"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	focus := "Determine whether the gathered evidence supports an available action"
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		if strings.Contains(instructions, "requires an exact intent_quote") {
			t.Fatal("exact quote restriction retained")
		}
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"capacity":100`) || !strings.Contains(string(raw), `"conflicts":[]`) || !strings.Contains(string(raw), `"price":42`) {
			t.Fatal("co-dependent accumulated facts missing")
		}
		if input.Plan != nil && len(input.Plan.Steps) != 1 {
			t.Fatal("procedural state grew")
		}
		step := runstate.Step{ID: "setup", Description: "Initial observations gathered", Status: "satisfied", Outcome: "Capacity, conflict inspection and observations are available", Evidence: []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}}
		return withReconciliationFocus(t, reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, ""), &focus), nil
	}}
	evaluations := 0
	p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		if spec.Name != "procedural_continuity_evaluation" || !strings.Contains(instructions, "partial or omitted sources") || strings.Count(string(raw), `"capacity":100`) != 1 {
			t.Fatal("scope/completion evaluation missing or payload duplicated")
		}
		return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":true,"purpose_preserved":true,"satisfied":true,"rationale":"Current facts jointly support the retrospective outcome and unresolved intent."}`)}, nil
	}}
	if err := p.reconcile(ctx, before, "Continue investigation", nil); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if active.Plan.UnresolvedFocus != focus || active.Plan.Steps[0].Status != "satisfied" {
		t.Fatal("progress or focus missing")
	}
	projection := string(p.Store.ModelJSON())
	if !strings.Contains(projection, `"established_steps":["setup"]`) || !strings.Contains(projection, focus) {
		t.Fatal("next actor cannot reconstruct continuity")
	}
	if err := p.reconcile(ctx, before, "Continue investigation", nil); err != nil {
		t.Fatal(err)
	}
	active, _ = p.Store.Active()
	if active.Plan.Revision != 2 || evaluations != 2 {
		t.Fatal("unchanged reconciliation revised history or bypassed evaluation")
	}
}

func TestReconciliationScopeRejectionCorrectionAndAtomicity(t *testing.T) {
	for _, mode := range []string{"strategy", "partial_exhaustive", "transport", "malformed_evaluation"} {
		t.Run(mode, func(t *testing.T) {
			p := reconciliationFixture(t)
			seedProcedure(t, p, "evaluate", "Evaluate evidence")
			before := p.Store.Snapshot()
			focus := "Call indicator tools, choose an instrument and execute a strategy"
			if mode == "partial_exhaustive" {
				focus = "All available observations have been exhaustively inspected"
			}
			p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
				return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, ""), &focus), nil
			}}
			calls := 0
			p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, _ json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
				calls++
				if spec.Name != "procedural_continuity_evaluation" || !strings.Contains(instructions, "Reject invented future workflows") {
					t.Fatal("scope guard not requested")
				}
				if mode == "transport" {
					return openai.JSONResult{}, errors.New("evaluator unavailable")
				}
				if mode == "malformed_evaluation" {
					return openai.JSONResult{JSON: json.RawMessage(`{}`)}, nil
				}
				return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":false,"purpose_preserved":true,"satisfied":true,"rationale":"Unsupported procedural inference."}`)}, nil
			}}
			if err := p.reconcile(context.Background(), before, "Evaluate available evidence", nil); err == nil {
				t.Fatal("unsupported proposal accepted")
			}
			after := p.Store.Snapshot()
			if string(mustJSON(t, before.Tasks)) != string(mustJSON(t, after.Tasks)) || string(mustJSON(t, before.Knowledge)) != string(mustJSON(t, after.Knowledge)) {
				t.Fatal("rejected scope mutated accepted state")
			}
			if calls == 0 {
				t.Fatal("scope was not evaluated")
			}
		})
	}
	// A rejected invented focus can be corrected to an honest no-progress result.
	p := reconciliationFixture(t)
	seedProcedure(t, p, "evaluate", "Evaluate evidence")
	calls := 0
	focus := "Call unrequested analytics"
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if calls == 1 {
			return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, ""), &focus), nil
		}
		if !strings.Contains(instructions, "scope validation") {
			t.Fatal("scope correction absent")
		}
		return reconciliationResult(t, "no_progress", nil, ""), nil
	}}
	evaluations := 0
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		if evaluations == 1 {
			return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":false,"purpose_preserved":true,"satisfied":false,"rationale":"Invented future action."}`)}, nil
		}
		return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":true,"purpose_preserved":true,"satisfied":true,"rationale":"Retained state is consistent."}`)}, nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Evaluate", nil); err != nil {
		t.Fatal(err)
	}
	if active, _ := p.Store.Active(); active.Plan.UnresolvedFocus != "" || calls != 2 || evaluations != 2 {
		t.Fatal("rejected focus committed")
	}
}

func TestReconciliationPreservesActorIntentAndFocusUpdates(t *testing.T) {
	ctx := context.Background()
	p := reconciliationFixture(t)
	initial := runstate.Plan{Description: "Actor strategy", CompletionCriteria: "Actor completion rule", Status: "active", UnresolvedFocus: "Inspect the actor-selected observation", Steps: []runstate.Step{{ID: "actor", Description: "Actor-selected investigation", CompletionCriteria: "Actor evidence rule", Status: "active"}}, CurrentStep: "actor"}
	if err := p.Store.RevisePlan(ctx, initial); err != nil {
		t.Fatal(err)
	}
	input := p.reconciliationInput(p.Store.Snapshot(), "Continue", nil)
	changed := initial.Steps[0]
	changed.Description = "Alternative strategy"
	if _, err := p.reconciliationPlan(input, reconciliationResult(t, "progress", []progressUpdate{{Step: changed}}, "").JSON); err == nil {
		t.Fatal("actor intent replaced")
	}
	for _, focus := range []string{"Evaluate available evidence", ""} {
		p.Reconciler = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
			return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, ""), &focus), nil
		}}
		if err := p.reconcile(ctx, p.Store.Snapshot(), "Continue", nil); err != nil {
			t.Fatal(err)
		}
		active, _ := p.Store.Active()
		if active.Plan.UnresolvedFocus != focus || active.Plan.Description != initial.Description || active.Plan.CompletionCriteria != initial.CompletionCriteria || active.Plan.CurrentStep != "actor" {
			t.Fatal("focus update rewrote actor plan")
		}
	}
	if _, err := p.reconciliationPlan(input, withReconciliationFocus(t, reconciliationResult(t, "no_progress", nil, ""), new(string)).JSON); err == nil {
		t.Fatal("contradictory no-progress focus accepted")
	}
	input.TaskID = "different task"
	if _, err := p.reconciliationPlan(input, reconciliationResult(t, "no_progress", nil, "").JSON); err == nil {
		t.Fatal("task mismatch accepted")
	}
}

func TestReconciliationEncodedInputBound(t *testing.T) {
	p := reconciliationFixture(t)
	ctx := context.Background()
	plan := runstate.Plan{Description: "Inspect", Status: "active", Steps: []runstate.Step{}, CurrentStep: "last"}
	for i := 0; i < 32; i++ {
		id := strings.Repeat("x", i+1)
		if i == 31 {
			id = "last"
		}
		plan.Steps = append(plan.Steps, runstate.Step{ID: id, Description: strings.Repeat("\x01", 512), CompletionCriteria: strings.Repeat("\x02", 512), Status: "active", InformationGaps: []string{strings.Repeat("\x03", 512)}})
	}
	if err := p.Store.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	input := p.reconciliationInput(p.Store.Snapshot(), strings.Repeat("\x00", 4096), nil)
	if len(mustJSON(t, input)) > maxReconciliationInput || !input.EvidenceTruncated || len(input.StateCoverage) != 2 {
		t.Fatal("escaped input exceeded bound or crowded out state coverage")
	}
	found := false
	for _, step := range input.Plan.Steps {
		if step.ID == "last" {
			found = true
		}
	}
	if !found {
		t.Fatal("active focus lost during plan projection")
	}
}

func TestReconciliationRejectedSatisfactionReplayAndTaskChange(t *testing.T) {
	ctx := context.Background()
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Characterize observations")
	if _, err := p.Store.Admit(ctx, runstate.Source{Kind: "mcp", ID: "read"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		var input reconciliationInput
		_ = json.Unmarshal(raw, &input)
		step := runstate.Step{ID: "inspect", Description: "Characterize observations", Status: "satisfied", Outcome: "Observations available", Evidence: []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, "inspect"), nil
	}}
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":true,"purpose_preserved":true,"satisfied":false,"rationale":"Evaluation remains incomplete."}`)}, nil
	}}
	for i := 0; i < 2; i++ {
		if err := p.reconcile(ctx, p.Store.Snapshot(), "Continue evaluation", nil); !errors.Is(err, errContinuityRejected) {
			t.Fatal(err)
		}
	}
	active, _ := p.Store.Active()
	if active.Plan.Revision != 1 || active.Plan.Steps[0].Status != "pending" || active.Plan.CurrentStep != "inspect" {
		t.Fatal("repeated rejection grew plan or lost unresolved focus")
	}
	// The evaluator cannot cause a proposal to commit to a different active task.
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		if _, err := p.Store.Push(ctx, "Different task", ""); err != nil {
			t.Fatal(err)
		}
		return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":true,"purpose_preserved":true,"satisfied":true,"rationale":"Supported."}`)}, nil
	}}
	if err := p.reconcile(ctx, p.Store.Snapshot(), "Continue", nil); err == nil {
		t.Fatal("task change during evaluation ignored")
	}
	child, _ := p.Store.Active()
	if child.Plan != nil {
		t.Fatal("proposal committed to new task")
	}
}
