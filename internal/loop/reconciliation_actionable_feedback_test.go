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

func TestReconciliationActionableFeedbackRepairsCompletionToPartial(t *testing.T) {
	p := reconciliationFixture(t)
	seedProcedure(t, p, "inspect", "Inspect configuration")
	calls := 0
	p.Reconciler = structuredClient{call: func(_ context.Context, instructions, _ string, raw json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		calls++
		var input reconciliationInput
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		status := "satisfied"
		if calls == 2 {
			if !strings.Contains(instructions, `Step "inspect"`) || !strings.Contains(instructions, "unresolved_gap_count=1") || !strings.Contains(instructions, "change status to active or partial") {
				t.Fatal("actor did not receive specific correction")
			}
			status = "partial"
		}
		step := runstate.Step{ID: "inspect", Description: "Inspect configuration", Status: status, Outcome: "Configuration acquired; assessment remains unfinished", Evidence: []runstate.EvidenceReference{input.AcceptedEvidence[0].Reference}, InformationGaps: []string{"Assessment remains unfinished"}}
		result := reconciliationResult(t, "progress", []progressUpdate{{Step: step}}, "Assess the configuration")
		var wire map[string]any
		_ = json.Unmarshal(result.JSON, &wire)
		wire["reason"] = "Acquisition establishes partial inspection progress; assessment remains unfinished."
		wire["result_accounting"] = []any{}
		result.JSON = mustEncode(wire)
		return result, nil
	}}
	p.Evaluator = structuredClient{call: func(_ context.Context, instructions, _ string, _ json.RawMessage, _ openai.JSONSpecification) (openai.JSONResult, error) {
		if !strings.Contains(instructions, "purpose_preserved means represented, not completed") {
			t.Fatal("evaluator lacks independent preservation guidance")
		}
		return continuityResult(true, "Partial progress and unfinished assessment are preserved."), nil
	}}
	if err := p.reconcile(context.Background(), p.Store.Snapshot(), "Acquire configuration to assess it", nil); err != nil {
		t.Fatal(err)
	}
	active, _ := p.Store.Active()
	if calls != 2 || active.Plan.Steps[0].Status != "partial" || len(active.Plan.Steps[0].InformationGaps) != 1 || active.Plan.CurrentStep != "inspect" {
		t.Fatal("repair lost progress, gaps, or the unresolved current step")
	}
}

func TestReconciliationSatisfactionFeedbackIdentifiesDefects(t *testing.T) {
	proposal := json.RawMessage(`{"updates":[{"step":{"id":"account","status":"satisfied","outcome":"Account acquired","evidence_refs":[{"partition":"account","version":1,"path":""}],"information_gaps":["Holdings unknown","Analysis unfinished"]}},{"step":{"id":"analysis","status":"satisfied","outcome":"","evidence_refs":[],"information_gaps":[]}}]}`)
	feedback := reconciliationHostCorrection(errors.New("step satisfaction requires outcome, evidence, and no gaps"), proposal)
	for _, expected := range []string{`Step "account"`, "unresolved_gap_count=2", `Step "analysis"`, "outcome_missing=true, evidence_refs_count=0", "change status to active or partial", "clear only information_gaps resolved by accepted evidence", "Do not erase unresolved gaps", reconciliationStatusGuidance} {
		if !strings.Contains(feedback, expected) {
			t.Fatalf("missing corrective guidance %q", expected)
		}
	}
}

func TestReconciliationSatisfactionFeedbackBoundedWithManyDefects(t *testing.T) {
	steps := make([]map[string]any, 32)
	for i := range steps {
		steps[i] = map[string]any{"step": map[string]any{"id": strings.Repeat("\x00", 64), "status": "satisfied", "outcome": "", "information_gaps": []string{"Unresolved"}}}
	}
	proposal, _ := json.Marshal(map[string]any{"updates": steps})
	feedback := reconciliationHostCorrection(errors.New("step satisfaction requires outcome, evidence, and no gaps"), proposal)
	if len(feedback) > maxReconciliationValidationFeedback || !utf8.ValidString(feedback) || !strings.Contains(feedback, "Additional affected steps omitted:") || !strings.Contains(feedback, "Rejected proposal omitted") {
		t.Fatalf("unbounded or misleading feedback: %d bytes", len(feedback))
	}
}

func TestReconciliationNoProgressFeedbackIncludesRequiredFields(t *testing.T) {
	feedback := reconciliationHostCorrection(errors.New("no-progress decision contains procedural changes"), json.RawMessage(`{}`))
	for _, expected := range []string{"Use action progress", "required reason and result_accounting fields", reconciliationStatusGuidance} {
		if !strings.Contains(feedback, expected) {
			t.Fatalf("missing %q", expected)
		}
	}
}
