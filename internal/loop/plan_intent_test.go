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
	"github.com/vectorphresh/0001-grodt/internal/structured"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

func authoredPlan() runstate.Plan {
	return runstate.Plan{Description: "Inspect account, identify opportunities, and execute the actor's selected approach", Status: "active", Steps: []runstate.Step{
		{ID: "account", Description: "Inspect account", CompletionCriteria: "Account information is available", Status: "active"},
		{ID: "assets", Description: "Inspect available assets", Status: "pending"},
		{ID: "assess", Description: "Assess opportunities", CompletionCriteria: "Opportunity assessment is established", Status: "pending"},
		{ID: "execute", Description: "Execute selected approach", Status: "pending"},
	}, CurrentStep: "account", UnresolvedFocus: "Assess opportunities and execute the actor's selected approach"}
}

func captureReply(required bool) openai.JSONResult {
	return openai.JSONResult{JSON: mustEncode(map[string]any{"capture_required": required, "rationale": "Preserve the explicitly actor-authored account, assets, assessment and execution intent."})}
}

func TestCaptureInstructionsDistinguishDeclaredSequenceFromGoalOnlyFocus(t *testing.T) {
	p, _ := toolsHarness(t, nil, false)
	p.Evaluator = structuredClient{capture: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		for _, want := range []string{
			"not whether the plan merely contributes to the goal",
			"require that declared work and its order in represented_plan.steps",
			"A broad goal in unresolved_focus alone does not preserve an explicit sequence",
			"A purpose without an explicit sequence may remain in unresolved_focus",
			"do not require exact wording or infer additional steps",
			"Do not generate steps, strategy, tools, or comprehensive procedural coverage",
		} {
			if !strings.Contains(instructions, want) {
				t.Fatalf("missing capture directive %q", want)
			}
		}
		var input struct {
			ActorIntent string         `json:"actor_intent"`
			Plan        *runstate.Plan `json:"represented_plan"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if input.Plan == nil || len(input.Plan.Steps) != 0 || input.Plan.UnresolvedFocus == "" || !strings.Contains(input.ActorIntent, "then identify") {
			t.Fatal("capture lacks the declared sequence and goal-only plan")
		}
		return captureReply(true), nil
	}}
	plan := runstate.Plan{Description: "Complete the request", Status: "pending", Steps: []runstate.Step{}, UnresolvedFocus: "Achieve the requested goal"}
	required, _, err := p.actorPlanCapture(context.Background(), "First inspect the account, then identify opportunities to achieve the requested goal.", &plan)
	if err != nil || !required {
		t.Fatal("capture rejection not returned", err)
	}
	feedback := (&hostCorrection{ActorIntent: "First inspect the account, then identify opportunities."}).feedback()
	if !strings.Contains(feedback, "record that work in steps in its stated order") {
		t.Fatal("actor correction lacks sequence persistence directive")
	}
}

func TestPurposeJudgmentCannotBeOmitted(t *testing.T) {
	p := purposeFixture(t, json.RawMessage(`{"items":[]}`))
	calls := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		var input reconciliationInput
		_ = json.Unmarshal(raw, &input)
		return reconciliationResult(t, "no_progress", nil, ""), nil
	}}
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":true,"satisfied":true,"rationale":"No new claims."}`)}, nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Get observations to assess opportunities.", nil); err == nil || calls != structured.MaxStructuredAttempts {
		t.Fatal("missing purpose judgment accepted", err, calls)
	}
	if active, _ := p.Store.Active(); active.Plan != nil {
		t.Fatal("malformed judgment persisted state")
	}
}

func TestReadableIntentSurvivesInvalidProposalSchema(t *testing.T) {
	p, _ := toolsHarness(t, nil, false)
	plan := authoredPlan()
	raw := mustEncode(map[string]any{"action": "revise", "plan": plan, "unknown_field": true})
	pending := p.newHostCorrection(context.Background(), toolcall.Call{ID: "invalid", Name: planningTool, Arguments: raw})
	partial := plan
	partial.Steps = partial.Steps[:1]
	if err := p.validateHostCorrection(context.Background(), pending, planCall(t, "dropped", planCommand{Action: "revise", Plan: &partial}).Calls[0]); err == nil || !strings.Contains(err.Error(), "execute") {
		t.Fatal("schema failure erased readable future intent", err)
	}
	if err := p.validateHostCorrection(context.Background(), pending, planCall(t, "full", planCommand{Action: "revise", Plan: &plan}).Calls[0]); err != nil {
		t.Fatal(err)
	}
}

func TestStructurallyValidOversizedPlanIsRejectedBeforeAcceptance(t *testing.T) {
	p, _ := toolsHarness(t, nil, false)
	plan := authoredPlan()
	plan.Steps = []runstate.Step{}
	for i := 0; i < 32; i++ {
		step := runstate.Step{ID: fmt.Sprintf("step%d", i), Description: "Actor intent", Status: "pending"}
		for n := 0; n < 8; n++ {
			step.InformationGaps = append(step.InformationGaps, strings.Repeat("x", 512))
		}
		plan.Steps = append(plan.Steps, step)
	}
	plan.CurrentStep = "step0"
	call := planCall(t, "large", planCommand{Action: "revise", Plan: &plan}).Calls[0]
	if _, err := decodePlanningCommand(context.Background(), call.Arguments); err != nil {
		t.Fatal("fixture must be schema-valid", err)
	}
	if _, _, err := p.managePlan(context.Background(), []toolcall.Call{call}); err == nil || !strings.Contains(err.Error(), "Reconstruct") {
		t.Fatal("oversized valid proposal accepted", err)
	}
	if active, _ := p.Store.Active(); active.Plan != nil {
		t.Fatal("oversized proposal persisted")
	}
}

func TestActorPlanCaptureBeforeAccountAssetsQuotesChain(t *testing.T) {
	observations := map[string]bool{}
	module := &eventModule{fn: func(e runstate.Event) json.RawMessage {
		if e.Source.Kind != "mcp" {
			return json.RawMessage(`{"status":"ignored"}`)
		}
		var event struct {
			Result struct {
				Data struct {
					Kind string `json:"kind"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(e.Payload, &event); err != nil {
			t.Fatal(err)
		}
		observations[event.Result.Data.Kind] = true
		return mustEncode(map[string]any{"status": "mutation", "replace": observations})
	}}
	p, fixture := toolsHarness(t, []runstate.Definition{{Name: "observations", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`{}`), Module: module}}, false)
	fixture.Call = func(_ *http.Request, _ string, args json.RawMessage) (json.RawMessage, error) {
		var query struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(args, &query); err != nil {
			return nil, err
		}
		return mustEncode(map[string]any{"content": []any{}, "structuredContent": map[string]any{"kind": query.Key}, "isError": false}), nil
	}
	prose := "First inspect my account, then inspect available assets, assess opportunities, and execute my selected approach."
	plan := authoredPlan()
	assessments := 0
	p.Evaluator = structuredClient{capture: func(_ context.Context, i, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		assessments++
		if !strings.Contains(i, "Do not generate steps") {
			t.Fatal("capture evaluator allowed planning")
		}
		var input struct {
			Intent string         `json:"actor_intent"`
			Plan   *runstate.Plan `json:"represented_plan"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if input.Intent != prose {
			t.Fatal("actor intent lost")
		}
		return captureReply(input.Plan == nil || len(input.Plan.Steps) != 4), nil
	}, call: func(_ context.Context, _, _ string, raw json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		if spec.Name != "procedural_continuity_evaluation" {
			t.Fatal("unexpected evaluation", spec.Name)
		}
		var input purposeEvaluationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if input.Context.Plan == nil || len(input.Context.Plan.Steps) != 4 || input.Focus != plan.UnresolvedFocus {
			t.Fatal("full plan omitted from reconciliation")
		}
		for _, step := range input.Updates {
			supported := false
			for _, evidence := range input.Context.AcceptedEvidence {
				if evidence.Reference.Partition != "observations" {
					continue
				}
				if evidence.Reference.Path == "/"+step.ID && string(evidence.Value) == "true" {
					supported = true
				}
				var values map[string]bool
				if evidence.Reference.Path == "" && json.Unmarshal(evidence.Value, &values) == nil && values[step.ID] {
					supported = true
				}
			}
			if !supported {
				t.Fatal("unsupported progress claim")
			}
		}
		return continuityResult(true, "Acquisitions establish inspections, while assessment and execution remain unresolved."), nil
	}}
	acquisitions := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		var input reconciliationInput
		_ = json.Unmarshal(raw, &input)
		acquisitions++
		if acquisitions > 2 {
			return reconciliationResult(t, "no_progress", nil, "", input), nil
		}
		active, _ := p.Store.Active()
		step := active.Plan.Steps[acquisitions-1]
		step.Status = "satisfied"
		step.Outcome = "Inspection established"
		step.Evidence = []runstate.EvidenceReference{{Partition: "observations", Version: p.Store.Snapshot().Knowledge["observations"].Metadata.Version, Path: "/" + step.ID}}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, []string{"assets", "assess"}[acquisitions-1], input), nil
	}}
	turns := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		turns++
		switch turns {
		case 1:
			return toolcall.Response{Text: prose, Calls: []toolcall.Call{toolRequest(r, "deferred-account")}}, nil
		case 2:
			assertHostCorrection(t, r)
			if fixture.Calls.Load() != 0 || !strings.Contains(r.Instructions, prose) {
				t.Fatal("call dispatched or declared intent lost")
			}
			incomplete := plan
			incomplete.Steps = append([]runstate.Step{}, plan.Steps[:1]...)
			return planCall(t, "incomplete", planCommand{Action: "revise", Plan: &incomplete}), nil
		case 3:
			assertHostCorrection(t, r)
			if fixture.Calls.Load() != 0 {
				t.Fatal("incomplete capture resumed execution")
			}
			return planCall(t, "capture", planCommand{Action: "revise", Plan: &plan}), nil
		case 4, 5, 6:
			active, _ := p.Store.Active()
			if active.Plan == nil || len(active.Plan.Steps) != 4 || active.Plan.Steps[3].Description != plan.Steps[3].Description {
				t.Fatal("future actor intent lost")
			}
			if strings.Contains(r.Instructions, "resubmit grodt_manage_plan alone") {
				t.Fatal("capture did not resume normal actor choice")
			}
			if fixture.Calls.Load() != int64(turns-4) {
				t.Fatal("deferred call automatically replayed")
			}
			call := toolRequest(r, fmt.Sprintf("actor-acquisition-%d", turns))
			call.Arguments = mustEncode(map[string]string{"key": []string{"account", "assets", "quotes"}[turns-4]})
			return toolcall.Response{Text: prose, Calls: []toolcall.Call{call}}, nil
		default:
			return toolcall.Response{Text: "Acquisitions recorded; assessment remains unresolved."}, nil
		}
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if turns != 7 || fixture.Calls.Load() != 3 || assessments != 5 || active.Plan.Revision != 3 || len(active.Plan.Steps) != 4 || active.Plan.Steps[0].Status != "satisfied" || active.Plan.Steps[1].Status != "satisfied" || active.Plan.CurrentStep != "assess" || active.Plan.Steps[3].Status != "pending" {
		t.Fatal("capture chain changed or lost actor plan", turns, assessments)
	}
}

func TestRejectedStandaloneProposalRetainsFutureSteps(t *testing.T) {
	p, fixture := toolsHarness(t, nil, false)
	proposal := authoredPlan()
	proposal.Steps[0].Status = "satisfied"
	proposal.Steps[0].Outcome = "Account retrieved"
	proposal.Steps[0].Evidence = []runstate.EvidenceReference{{Partition: "missing", Version: 1, Path: "/cash"}}
	proposal.CurrentStep = "assets"
	turns := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		turns++
		switch turns {
		case 1:
			return planCall(t, "rejected", planCommand{Action: "revise", Plan: &proposal}), nil
		case 2, 3:
			assertHostCorrection(t, r)
			if !strings.Contains(r.Instructions, "Execute selected approach") || strings.Contains(fmt.Sprint(r.Messages), "Execute selected approach") {
				t.Fatal("proposal lost or duplicated in correction history")
			}
			corrected := authoredPlan()
			if turns == 2 {
				corrected.Steps = corrected.Steps[:1]
			}
			return planCall(t, "correct", planCommand{Action: "revise", Plan: &corrected}), nil
		default:
			return toolcall.Response{Text: "Full proposal repaired."}, nil
		}
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if turns != 4 || active.Plan == nil || len(active.Plan.Steps) != 4 || active.Plan.Steps[0].Status != "active" || fixture.Calls.Load() != 0 {
		t.Fatal("correction dropped unaffected actor steps")
	}
}

func TestAcceptedPlanContinuityRequiresExplainedScopeChange(t *testing.T) {
	p, _ := toolsHarness(t, nil, false)
	ctx := context.Background()
	plan := authoredPlan()
	if err := p.Store.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	plan = *active.Plan
	// Merely selecting a different step preserves every other intent and criterion.
	plan.CurrentStep = "assess"
	if _, err := p.acceptPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	active, _ = p.Store.Active()
	plan = *active.Plan
	proposal := plan
	proposal.Steps = append([]runstate.Step{}, plan.Steps[:3]...)
	proposal.UnresolvedFocus = "Assess opportunities"
	before := p.Store.Snapshot()
	if _, err := p.acceptPlan(ctx, proposal); err == nil || !strings.Contains(err.Error(), "execute") {
		t.Fatal("silent omission accepted", err)
	}
	if !reflect.DeepEqual(before.Tasks, p.Store.Snapshot().Tasks) {
		t.Fatal("rejected revision mutated accepted state")
	}
	reviews := 0
	p.Evaluator = structuredClient{call: func(_ context.Context, i, _ string, raw json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		reviews++
		if spec.Name != "actor_plan_revision" || !strings.Contains(string(raw), "revision_reason") || !strings.Contains(string(raw), "execute") || !strings.Contains(i, "Do not invent strategy") {
			t.Fatal("revision lacks scope context")
		}
		return openai.JSONResult{JSON: mustEncode(map[string]any{"revision_valid": reviews > 1, "rationale": "Only an explicit actor scope revision can withdraw execution intent."})}, nil
	}}
	if _, err := p.acceptPlan(ctx, proposal, "Restrict my authored scope to observation and assessment; withdraw execution."); err == nil {
		t.Fatal("evaluator rejection ignored")
	}
	if _, err := p.acceptPlan(ctx, proposal, "Restrict my authored scope to observation and assessment; withdraw execution."); err != nil {
		t.Fatal(err)
	}
	active, _ = p.Store.Active()
	if len(active.Plan.Steps) != 3 || reviews != 2 {
		t.Fatal("explained revision not accepted")
	}
	// Criteria are protected alongside step descriptions.
	proposal = *active.Plan
	proposal.Steps = append([]runstate.Step{}, proposal.Steps...)
	proposal.Steps[2].CompletionCriteria = "Different assessment criterion"
	if _, err := p.acceptPlan(ctx, proposal); err == nil || !strings.Contains(err.Error(), "assess") {
		t.Fatal("criteria rewritten without explanation", err)
	}
}

func TestPurposePreservedIsIndependentOfContinuityAndSatisfaction(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(fmt.Sprint(repair), func(t *testing.T) {
			p := purposeFixture(t, json.RawMessage(`{"items":[],"isError":false}`))
			purpose := "Assess opportunities"
			if err := p.Store.RevisePlan(context.Background(), runstate.Plan{Description: purpose, Status: "active", Steps: []runstate.Step{{ID: "assess", Description: purpose, Status: "pending"}}}); err != nil {
				t.Fatal(err)
			}
			attempts := 0
			p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				var input reconciliationInput
				_ = json.Unmarshal(raw, &input)
				attempts++
				if repair && attempts > 1 {
					return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, "", input), &purpose), nil
				}
				return reconciliationResult(t, "no_progress", nil, ""), nil
			}}
			p.Evaluator = structuredClient{call: func(_ context.Context, i, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				var input purposeEvaluationInput
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(i, "An empty set of new satisfaction claims cannot justify purpose_preserved true") {
					t.Fatal("missing independent purpose rule")
				}
				return openai.JSONResult{JSON: mustEncode(map[string]any{"continuity_valid": true, "satisfied": true, "purpose_preserved": input.Focus == purpose, "rationale": "Preserve explicitly actor-authored assessment purpose; empty acquisition did not assess opportunities."})}, nil
			}}
			err := p.reconcile(context.Background(), p.Store.Snapshot(), "Get observations to assess opportunities.", nil)
			active, _ := p.Store.Active()
			if repair {
				if err != nil || attempts != 2 || active.Plan == nil || active.Plan.UnresolvedFocus != purpose {
					t.Fatal("focus-only correction failed", err)
				}
			} else if !errors.Is(err, errContinuityRejected) || attempts != maxReconciliationAttempts || active.Plan.UnresolvedFocus != "" {
				t.Fatal("missing purpose bypassed evaluation", err)
			}
		})
	}
}

func TestCaptureBoundsAndAcquisitionOnly(t *testing.T) {
	p, _ := toolsHarness(t, nil, false)
	p.Evaluator = structuredClient{capture: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		if strings.Contains(string(raw), "Buy") {
			t.Fatal("strategy inferred from objective")
		}
		return captureReply(false), nil
	}}
	if required, _, err := p.actorPlanCapture(context.Background(), "Get observations.", nil); err != nil || required {
		t.Fatal("immediate acquisition required invented plan", err)
	}
	if required, _, err := p.actorPlanCapture(context.Background(), strings.Repeat("x", maxActorPlanIntent+1), nil); err != nil || !required {
		t.Fatal("oversized prose silently truncated", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Evaluator = structuredClient{capture: func(ctx context.Context, _, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		return openai.JSONResult{}, ctx.Err()
	}}
	if _, _, err := p.actorPlanCapture(ctx, "Inspect account, then assess.", nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled assessment continued", err)
	}
}

func TestRejectedStandaloneOversizedProposalRequiresReconstruction(t *testing.T) {
	p, fixture := toolsHarness(t, nil, false)
	turns := 0
	p.Client = nativeClient(func(_ context.Context, r toolcall.Request) (toolcall.Response, error) {
		turns++
		if turns == 1 {
			return toolcall.Response{Calls: []toolcall.Call{{ID: "oversized", Name: planningTool, Arguments: mustEncode(map[string]string{"oversized_marker": strings.Repeat("x", 70000)})}}}, nil
		}
		if turns == 2 {
			assertHostCorrection(t, r)
			if strings.Contains(r.Instructions, "oversized_marker") || strings.Contains(fmt.Sprint(r.Messages), "oversized_marker") || !strings.Contains(r.Instructions, `"reconstruct_arguments":true`) {
				t.Fatal("oversized proposal retained or truncated")
			}
			plan := authoredPlan()
			return planCall(t, "reconstruct", planCommand{Action: "revise", Plan: &plan}), nil
		}
		return toolcall.Response{Text: "Plan reconstructed."}, nil
	})
	if _, err := p.Handle(context.Background(), &State{Objective: "Inspect"}); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if active.Plan == nil || len(active.Plan.Steps) != 4 || fixture.Calls.Load() != 0 {
		t.Fatal("reconstruction lost plan")
	}
}

func TestPlanIntentReviewUsesBoundedFairCanonicalEvidence(t *testing.T) {
	p, _ := toolsHarness(t, []runstate.Definition{
		{Name: "large", Schema: json.RawMessage(`true`), Initial: mustEncode(map[string]string{"large": strings.Repeat("x", 300000)}), Module: &eventModule{}},
		{Name: "small", Schema: json.RawMessage(`true`), Initial: json.RawMessage(`{"items":[]}`), Module: &eventModule{}},
	}, false)
	plan := authoredPlan()
	proposal := plan
	proposal.Steps = append([]runstate.Step{}, plan.Steps[:3]...)
	p.Evaluator = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		if spec.Name != "actor_plan_revision" || len(raw) > maxPlanIntentInput {
			t.Fatal("review not bounded")
		}
		var input struct {
			Evidence runstate.KnowledgeProjection `json:"current_evidence"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		seenSmall := false
		for _, e := range input.Evidence.Evidence {
			if e.Reference.Partition == "small" {
				seenSmall = true
			}
		}
		if !seenSmall || !input.Evidence.Truncated || len(input.Evidence.Sources) != 2 {
			t.Fatal("large source crowded out smaller source or coverage absent")
		}
		return openai.JSONResult{JSON: json.RawMessage(`{"revision_valid":true,"rationale":"Explicitly explained withdrawal accepted within partial evidence coverage."}`)}, nil
	}}
	if err := p.validatePlanIntent(context.Background(), &plan, &proposal, "Withdraw execution from my scope.", "accepted_plan"); err != nil {
		t.Fatal(err)
	}
}

func TestPlanIntentProtectsOrderAndHistoricalProgress(t *testing.T) {
	plan := authoredPlan()
	proposal := plan
	proposal.Steps = append([]runstate.Step{}, plan.Steps...)
	proposal.Steps[1], proposal.Steps[2] = proposal.Steps[2], proposal.Steps[1]
	if !strings.Contains(strings.Join(planIntentChanges(&plan, &proposal, "accepted_plan"), ","), "step_order") {
		t.Fatal("implicit reorder allowed")
	}
	p := purposeFixture(t, json.RawMessage(`{"account":true}`))
	ctx := context.Background()
	plan.Steps[0].Status = "satisfied"
	plan.Steps[0].Outcome = "Account inspected"
	plan.Steps[0].Evidence = []runstate.EvidenceReference{{Partition: "observations", Version: 0, Path: "/account"}}
	plan.CurrentStep = "assets"
	if err := p.Store.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	proposal = *active.Plan
	proposal.Steps = append([]runstate.Step{}, proposal.Steps...)
	proposal.Steps[0].Description = "Rewrite established intent"
	if _, err := p.acceptPlan(ctx, proposal, "Rewrite history."); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatal("scope explanation bypassed completed history protection", err)
	}
}
