package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

func TestReconciliationRepairsEachFieldThenRecordsProgressAndAdvances(t *testing.T) {
	ctx := context.Background()
	p, _ := toolsHarness(t, []runstate.Definition{{Name: "observations", Schema: json.RawMessage(`{"type":"object"}`), Initial: json.RawMessage(`{"account":{"cash":"100"},"positions":[]}`), Module: &eventModule{}}}, false)
	description := "Analyze the current account balance and holdings."
	plan := runstate.Plan{Description: "Inspect account and determine target", Status: "active", Steps: []runstate.Step{{ID: "account", Description: description, Status: "pending"}, {ID: "target", Description: "Determine the target increase.", Status: "pending"}}}
	if err := p.Store.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	calls, evaluations := 0, 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		active, _ := p.Store.Active()
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		step := runstate.Step{ID: "account", Description: description, Status: "active", Outcome: "Account information retrieved: cash 100; no positions.", Evidence: []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}, InformationGaps: []string{"Analysis remains unfinished"}}
		target := plan.Steps[1]
		switch calls {
		case 1:
			step.CompletionCriteria = "Account information retrieved and analyzed."
			target.CompletionCriteria = "Target calculated."
		case 2:
			for _, text := range []string{`"step_id":"account"`, `"step_id":"target"`, `"field":"completion_criteria"`, `"expected":""`, "Retain otherwise valid proposed progress", "Rejected proposal (correction data"} {
				if !strings.Contains(instructions, text) {
					t.Fatalf("missing correction %s", text)
				}
			}
			if active.Plan.Revision != 1 || active.Plan.Steps[0].Status != "pending" {
				t.Fatal("rejected progress applied")
			}
			step.Description = "Inspect account"
		case 3:
			for _, text := range []string{`"field":"description"`, `"expected":"Analyze the current account balance and holdings."`, "Required correction:"} {
				if !strings.Contains(instructions, text) {
					t.Fatalf("second rejection omitted correction %s", text)
				}
			}
			if strings.Contains(instructions, `"received":"Target calculated."`) {
				t.Fatal("obsolete feedback accumulated")
			}
			if active.Plan.Revision != 1 {
				t.Fatal("rejected repair changed plan")
			}
		case 4:
			if active.Plan.Revision != 2 || active.Plan.Steps[0].Status != "active" {
				t.Fatal("supported acquisition progress lost")
			}
			step.Status = "satisfied"
			step.Outcome = "Analyzed cash and holdings: cash 100; no positions."
			step.InformationGaps = nil
		default:
			t.Fatal("unexpected correction count")
		}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}, {Step: target}}, ""), nil
	}}
	p.Evaluator = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, spec openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		if spec.Name != "procedural_continuity_evaluation" || !strings.Contains(string(raw), `"cash":"100"`) {
			t.Fatal("accepted evidence omitted from independent evaluation")
		}
		if evaluations == 2 && !strings.Contains(string(raw), `"new_satisfaction_claim_ids":["account"]`) {
			t.Fatal("completion claim bypassed evaluation")
		}
		return continuityResult(true, "Accepted evidence and the reported analysis support the proposed progress."), nil
	}}
	for i := 0; i < 2; i++ {
		if err := p.reconcile(ctx, p.Store.Snapshot(), "Analyze available account observations.", nil); err != nil {
			t.Fatal(err)
		}
	}
	active, _ := p.Store.Active()
	if calls != 4 || evaluations != 2 || active.Plan.Revision != 3 || active.Plan.CurrentStep != "target" || active.Plan.Steps[0].Status != "satisfied" || active.Plan.Steps[1].Status != "pending" {
		t.Fatal("repair/progress/advancement failed")
	}
}

func TestReconciliationValidationFeedbackBoundsAndCoverage(t *testing.T) {
	plan := runstate.Plan{}
	updates := []progressUpdate{}
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		plan.Steps = append(plan.Steps, runstate.Step{ID: id, Description: strings.Repeat("\x00", 512)})
		updates = append(updates, progressUpdate{Step: runstate.Step{ID: id, Description: strings.Repeat("界", 512), CompletionCriteria: "invented"}})
	}
	err := reconciliationIntentMismatch(plan, reconciliationInput{}, updates)
	if err == nil {
		t.Fatal("missing field mismatches")
	}
	feedback := reconciliationHostCorrection(err, json.RawMessage(strings.Repeat("x", maxReconciliationOutput)))
	if len(feedback) > maxReconciliationValidationFeedback || !utf8.ValidString(feedback) || !strings.Contains(feedback, "additional_errors_omitted") || !strings.Contains(feedback, "received_truncated") || !strings.Contains(feedback, "Rejected proposal omitted") {
		t.Fatalf("feedback bound/coverage failed: %d bytes", len(feedback))
	}
	// Exact expected values remain valid JSON even when control characters expand serialization.
	start := strings.Index(err.Error(), "{")
	var report struct {
		Errors  []reconciliationFieldMismatch `json:"errors"`
		Omitted int                           `json:"additional_errors_omitted"`
	}
	if json.Unmarshal([]byte(err.Error()[start:]), &report) != nil || report.Omitted == 0 || len(report.Errors) == 0 || report.Errors[0].Expected != plan.Steps[0].Description {
		t.Fatal("expected values truncated or omission hidden")
	}
	visible := reconciliationInput{Plan: &runstate.Plan{Steps: plan.Steps[:1]}, OmittedPlanSteps: 7}
	visibleError := reconciliationIntentMismatch(plan, visible, updates)
	if strings.Contains(visibleError.Error(), `"step_id":"b"`) {
		t.Fatal("feedback exposed an omitted step")
	}
}

func TestReconciliationConstraintFeedbackGivesRepairs(t *testing.T) {
	tests := map[string]string{
		"no-progress decision contains procedural changes":                             "Use action progress",
		"progress decision has no updates":                                             "supply a supported step update",
		"too many reconciliation updates":                                              "at most eight",
		"step satisfaction requires outcome, evidence, and no gaps":                    "supply a concise outcome and accepted evidence_refs",
		"current step missing":                                                         "accepted unresolved step ID",
		"completed step history is immutable; invalidate explicitly or add a new step": "Restore the accepted completed step",
	}
	for message, expected := range tests {
		feedback := reconciliationHostCorrection(errors.New(message), json.RawMessage(`{}`))
		if !strings.Contains(feedback, expected) || !strings.Contains(feedback, "not applied") {
			t.Fatalf("nonactionable feedback for %s", message)
		}
	}
}

func TestReconciliationRepeatedCriteriaRejectionPreservesAcceptedPlan(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	before := mustJSON(t, p.Store.Snapshot().Tasks)
	calls := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		if calls > 1 && !strings.Contains(instructions, `"expected":""`) {
			t.Fatal("validation error not returned on every opportunity")
		}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: runstate.Step{ID: "inspect", Description: "Inspect configuration", CompletionCriteria: "Invented criteria", Status: "active"}}}, ""), nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Inspect", nil); err == nil || calls != maxReconciliationAttempts {
		t.Fatal("correction limit changed or invalid result accepted")
	}
	if string(before) != string(mustJSON(t, p.Store.Snapshot().Tasks)) {
		t.Fatal("correction exhaustion changed accepted plan")
	}
}

func TestReconciliationReferenceRejectionIdentifiesStepAndRepair(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	active, _ := p.Store.Active()
	input := reconciliationInput{TaskID: active.ID, Plan: active.Plan}
	proposal := reconciliationResult(t, "progress", []progressUpdate{{Step: runstate.Step{ID: "inspect", Description: "Inspect configuration", Status: "active", Evidence: []runstate.EvidenceReference{{Partition: "configuration", Version: 0, Path: "/missing"}}}}}, "").JSON
	_, err := p.reconciliationPlan(input, proposal)
	if err == nil {
		t.Fatal("unsupported evidence accepted")
	}
	feedback := reconciliationHostCorrection(err, proposal)
	for _, text := range []string{`step "inspect" evidence_refs`, `path="/missing"`, "Use a reference from accepted_evidence", "same version", "otherwise keep the claim unresolved"} {
		if !strings.Contains(feedback, text) {
			t.Fatalf("missing actionable reference detail %s", text)
		}
	}
}
