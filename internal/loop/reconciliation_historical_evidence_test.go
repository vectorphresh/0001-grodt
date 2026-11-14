package loop

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	runstate "github.com/vectorphresh/0001-grodt/internal/state"
)

// Reproduce c07eb792's 000020 -> 000021 failure: a current step update
// must not fail because an unchanged partial step references an older version.
func TestReconciliationCurrentProgressWithHistoricalPartialStep(t *testing.T) {
	ctx := context.Background()
	p := reconciliationFixture(t)
	version := p.Store.Snapshot().Knowledge["configuration"].Metadata.Version
	historical := runstate.Step{ID: "step_1", Description: "Acquire configuration", Status: "partial", Outcome: "Configuration acquired", Evidence: []runstate.EvidenceReference{{Partition: "configuration", Version: version, Path: ""}}, InformationGaps: []string{"Assessment remains"}}
	plan := runstate.Plan{Description: "Acquire and assess", Status: "active", Steps: []runstate.Step{historical, {ID: "step_2", Description: "Assess configuration", Status: "pending"}}, CurrentStep: "step_2"}
	if err := p.Store.RevisePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	before := p.Store.Snapshot()
	if _, err := p.Store.Admit(ctx, runstate.Source{Kind: "mcp", ID: "refresh"}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	proposals, evaluations := 0, 0
	p.Reconciler = structuredClient{call: func(_ context.Context, _, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		proposals++
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		step := input.Plan.Steps[1]
		step.Status = "partial"
		step.Outcome = "Current configuration acquired for assessment"
		step.InformationGaps = []string{"Assessment incomplete"}
		for _, evidence := range input.AcceptedEvidence {
			if evidence.Reference.Partition == "configuration" {
				step.Evidence = []runstate.EvidenceReference{evidence.Reference}
				break
			}
		}
		return reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, "step_2"), nil
	}}
	p.Evaluator = structuredClient{call: func(context.Context, string, string, json.RawMessage, openai.JSONSpecification) (openai.JSONResult, error) {
		evaluations++
		return continuityResult(true, "Acquisition recorded; assessment remains unresolved."), nil
	}}
	if err := p.reconcile(ctx, before, "Assess configuration", nil); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if proposals != 1 || evaluations != 1 || !reflect.DeepEqual(active.Plan.Steps[0], historical) || active.Plan.Steps[1].Status != "partial" || active.Plan.Steps[1].Evidence[0].Version != version+1 {
		t.Fatal("cross-step update lost historical provenance or retried unnecessarily")
	}
}
