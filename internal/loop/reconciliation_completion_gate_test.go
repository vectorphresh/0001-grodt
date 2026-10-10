package loop

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

func TestReconciliationEmptyCompletionClaims(t *testing.T) {
	for _, action := range []string{"no_progress", "partial", "focus"} {
		t.Run(action, func(t *testing.T) {
			p := reconciliationFixture(t)
			seedProcedure(t, p, "inspect", "Inspect configuration")
			before := p.Store.Snapshot().Tasks
			proposals, evaluations := 0, 0
			p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				proposals++
				var input reconciliationInput
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				switch action {
				case "partial":
					step := input.Plan.Steps[0]
					step.Status = "partial"
					step.Outcome = "Configuration acquired"
					step.Evidence = []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}
					step.InformationGaps = []string{"Configuration assessment remains unfinished"}
					return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, ""), nil
				case "focus":
					focus := "Assess the acquired configuration"
					return withReconciliationFocus(t, reconciliationResult(t, "progress", nil, ""), &focus), nil
				default:
					return reconciliationResult(t, "no_progress", nil, ""), nil
				}
			}}
			p.Evaluator = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				evaluations++
				var observation struct {
					Claims []string `json:"new_satisfaction_claim_ids"`
				}
				if err := json.Unmarshal(raw, &observation); err != nil || len(observation.Claims) != 0 {
					t.Fatalf("expected explicit empty completion claims: %s", raw)
				}
				return openai.JSONResult{JSON: json.RawMessage(`{"continuity_valid":true,"purpose_preserved":true,"satisfied":false,"rationale":"No completion claims were proposed; unfinished work remains represented."}`)}, nil
			}}
			if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Assess configuration", nil); err != nil {
				t.Fatal(err)
			}
			if proposals != 1 || evaluations != 1 {
				t.Fatalf("unnecessary correction attempts: proposals=%d evaluations=%d", proposals, evaluations)
			}
			active, _ := p.Store.Active()
			switch action {
			case "no_progress":
				if !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
					t.Fatal("no_progress changed accepted task state")
				}
			case "partial":
				if active.Plan.Steps[0].Status != "partial" || !reflect.DeepEqual(active.Plan.Steps[0].InformationGaps, []string{"Configuration assessment remains unfinished"}) {
					t.Fatal("partial progress was not preserved")
				}
			case "focus":
				if active.Plan.UnresolvedFocus != "Assess the acquired configuration" || active.Plan.Steps[0].Status != "pending" {
					t.Fatal("focus correction changed step completion")
				}
			}
		})
	}
}

func TestReconciliationPendingEvidenceCorrection(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	before := p.Store.Snapshot().Tasks
	calls := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
			t.Fatal("rejected pending evidence modified accepted state")
		}
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		step := input.Plan.Steps[0]
		step.Evidence = []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}
		if calls > 1 {
			if !strings.Contains(instructions, "cannot remain pending") || !strings.Contains(instructions, "active for started work") {
				t.Fatal("correction omitted actionable status guidance")
			}
			step.Status = "active"
		}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, ""), nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect configuration", nil); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if calls != 2 || active.Plan.Steps[0].Status != "active" || len(active.Plan.Steps[0].Evidence) == 0 {
		t.Fatal("collected evidence did not advance pending step through correction")
	}
}

func TestReconciliationCompletionGateRetainsRequiredChecks(t *testing.T) {
	for _, gate := range []string{"continuity", "purpose", "step_completion", "plan_completion"} {
		t.Run(gate, func(t *testing.T) {
			p := reconciliationFixture(t)
			seedProcedure(t, p, "inspect", "Inspect configuration")
			active, _ := p.Store.Active()
			plan := *active.Plan
			ref := runstate.EvidenceReference{Partition: "configuration", Version: p.Store.Snapshot().Knowledge["configuration"].Metadata.Version, Path: ""}
			if gate == "step_completion" || gate == "plan_completion" {
				plan.Steps[0].Status = "satisfied"
				plan.Steps[0].Outcome = "Configuration inspected"
				plan.Steps[0].Evidence = []runstate.EvidenceReference{ref}
			}
			if gate == "plan_completion" {
				if err := p.Store.RevisePlan(context.Background(), plan); err != nil {
					t.Fatal(err)
				}
				active, _ = p.Store.Active()
				plan = *active.Plan
				plan.Status = "satisfied"
				plan.Outcome = "Inspection complete"
				plan.Evidence = []runstate.EvidenceReference{ref}
			}
			before := p.Store.Snapshot().Tasks
			p.Evaluator = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
				var observation struct {
					Claims []string `json:"new_satisfaction_claim_ids"`
				}
				if err := json.Unmarshal(raw, &observation); err != nil {
					t.Fatal(err)
				}
				wantClaims := 0
				if gate == "step_completion" {
					wantClaims = 1
				}
				if len(observation.Claims) != wantClaims {
					t.Fatalf("claims=%v, want count %d", observation.Claims, wantClaims)
				}
				decision := map[string]any{"continuity_valid": gate != "continuity", "purpose_preserved": gate != "purpose", "satisfied": false, "rationale": "The step is satisfied."}
				return openai.JSONResult{JSON: mustEncode(decision)}, nil
			}}
			_, err := p.acceptReconciliationPlan(context.Background(), &plan, reconciliationInput{TaskID: active.ID})
			if !errors.Is(err, errContinuityRejected) {
				t.Fatalf("required %s check bypassed: %v", gate, err)
			}
			if !reflect.DeepEqual(before, p.Store.Snapshot().Tasks) {
				t.Fatal("rejected proposal modified accepted state")
			}
		})
	}
}
